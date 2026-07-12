# Gitslice Security & Performance Review

**Date:** 2026-07-11  
**Scope:** Full implementation review (server, service, auth, CI/exec, storage, Git compat, CLI agent paths) — not limited to a single branch.  
**Method:** Code-level audit of the current tree. Not a live penetration test.

---

## Summary

Authz is consistently applied on slice-scoped RPCs, container CI has solid isolation defaults (capability drop, no-new-privileges, pids/memory/cpu limits), and there are body/message caps (128 MiB). The highest risks are **server-side git import SSRF / local clone**, **global content-addressed blob access**, **plaintext CI secrets**, **host-mode check execution**, and **resource-exhaustion** paths (unary ReadFile, import, git projection rebuild, GC).

---

## Security

### Critical / high

| # | Severity | Issue | Where | Impact |
|---|----------|--------|--------|--------|
| S1 | **Critical** | **Git import accepts arbitrary sources** (local paths, any `://` URL, `git@…`) | `service/repository.go` — `normalizeGitHubSource`, `cloneForImport` | Authenticated writers can make the **server** `git clone` internal hosts (SSRF), cloud metadata endpoints, or **local filesystem paths** (`/…`). Server-side data exfil / internal network access. |
| S2 | **High** | **Global blob store is only gated by “any slice read/write”** | `service/blob.go`, `service/blob_stream.go` | `ReadBlobStream` / `GetBlobStatus` authorize a slice, then load by **global content hash** with no check that the hash is reachable from that slice’s trees. Anyone with write on a slice can upload; anyone with read (including public) who learns a hash can fetch private content. Classic multi-tenant CAS hole. |
| S3 | **High** | **CI secrets stored and shipped in plaintext** | `internal/postgres/migrations/0020_slice_secrets.sql` (`value text not null`); `service/check_dispatch.go` — `mergeCheckEnv` → `RunChecks.Env` | DB dumps, backups, and daemon streams expose secrets. List API correctly returns names only, but storage + dispatch are plaintext. |
| S4 | **High** | **Host-mode checks run `sh -c` with full host env** | `internal/checkexec/exec.go` — `runHost`, `hostEnv` | No image/setup → process runs on the daemon host, inherits `os.Environ()`, can use host tools/network. Container path is much safer. |

### Medium

| # | Severity | Issue | Where | Impact |
|---|----------|--------|--------|--------|
| S5 | **Medium** | **Container network defaults on** | `internal/checks` YAML `network` defaults true; `checkexec` only sets `--network=none` when false | Checks (and secret env) can exfiltrate over the network unless authors opt out. |
| S6 | **Medium** | **Workspace volume is RW; image/setup fully attacker-controlled** | `checkexec` `containerRunArgs` (`-v root:/workspace`); `setupDockerfile` `FROM`/`RUN` | Malicious `.gitslice/checks.yaml` can pull arbitrary images and run setup with build network; RW mount can poison workspace/cache volumes. Caps drop helps but does not block image supply-chain abuse. |
| S7 | **Medium** | **Rate limit key spoofable via `X-Forwarded-For`** | `server/ratelimit.go` — `httpClientIP` | If the edge does not strip/rewrite XFF, clients pick their own bucket → bypass or DoS another key. |
| S8 | **Medium** | **`/metrics` open unless `GITSLICE_METRICS_TOKEN`** | `server/gateway.go` | Process/API shape and traffic patterns leak; warned in logs but easy to misconfigure in prod. |
| S9 | **Medium** | **CLI login session token stored in DB for poll** | `internal/postgres/auth_store.go` — `session_token` on `cli_login_sessions` until poll | One-shot poll is good (delete on redeem); still a window where DB read steals a 30-day session. Codes are hashed (`tokenHash`) — good. |
| S10 | **Medium** | **CORS allows `*` if configured** | `server/gateway.go` — `corsOrigin` | Credentialed browser misuse if someone sets `GITSLICE_HTTP_ALLOWED_ORIGIN=*`. |

### Lower / design debt

| # | Severity | Issue | Notes |
|---|----------|--------|--------|
| S11 | **Low** | No per-tenant object quotas on upload | Only message size + RPS. Storage DoS with many blobs. |
| S12 | **Low** | Session revocation / token rotation incomplete | Still called out as future work. |
| S13 | **Info** | Auth middleware is optional-token | Methods enforce `requireSubject` / public slice reads — intentional, but every new RPC must not forget authz. |
| S14 | **Info** | Public slice read is intentional | `authz` ActionRead + visibility `public`. |

### Positive security controls observed

- Slice admin required for set/delete secrets; list API returns names only.
- Path canonicalization (`internal/paths`); workspace escape checks in checkexec.
- Container: `--cap-drop=ALL`, `no-new-privileges`, pids/memory/cpu limits, optional network off.
- gRPC/HTTP body caps; Git smart-HTTP body cap; metrics token constant-time compare.
- Clerk + optional service JWT path; CLI login codes stored hashed.
- Prod fail-fast when object store is not durable R2 (`GITSLICE_REQUIRE_R2`) — durability/ops safety.

---

## Performance / resource exhaustion

### High

| # | Severity | Issue | Where | Impact under load |
|---|----------|--------|--------|-------------------|
| P1 | **High** | **`ReadFile` / check tree reader `io.ReadAll` whole object** | `service/repository.go` — `readFile`; `service/check_dispatch.go` — `checkTreeReader.ReadFile` | Large files → memory spike per concurrent request (up to ~128 MiB unary). Prefer ranged stream or hard max when length unset. |
| P2 | **High** | **Object cache `Put` always `ReadAll`s then writes** | `internal/objectstore/cache/cache.go` | Every Put buffers full object in memory (even if > maxObjectBytes, still fully read). Concurrent large uploads thrash RAM. |
| P3 | **High** | **Git projection rebuild is full materialize + commit + force-push** | `internal/gitcompat/projector.go` — `rebuild`, `projectedFiles` | One lock per repo; cold/miss clones entire slice into worktree. Large slices × concurrent git clients → disk + CPU + object-store read storm. |
| P4 | **High** | **Import clones full remote then walks commits** | `service/repository.go` — `ImportGitRepository` + deep mode | CPU/disk/network heavy; only subject RPS limits (default 500/s). Easy self-DoS or multi-tenant impact. |
| P5 | **High** | **GC report loads all blob metadata into memory** | `internal/postgres/gc.go` | Advisory-only today, but O(blob table) RAM when run on large deployments. |

### Medium

| # | Severity | Issue | Where | Notes |
|---|----------|--------|--------|--------|
| P6 | **Medium** | **CI materialize concurrency 32+32** | `internal/cli/agent_checks.go` | Good latency; can stampede API/object store without client-side backpressure. |
| P7 | **Medium** | **Agent hub buffers** (send 64, sub 256) | `service/agent_hub.go` | Drop-on-full is correct for live events; many daemons/subs still grow maps until disconnect. |
| P8 | **Medium** | **Projector lock map never shrinks** | `gitcompat/projector.go` — `locks` | Slow leak of mutex entries per projected repo path. |
| P9 | **Medium** | **Unary message limit 128 MiB** | `internal/rpclimits` | Protects against unbounded messages but allows large single-request cost; streaming exists for blobs but not all paths use it. |
| P10 | **Medium** | **In-process rate limits only** | `server/ratelimit.go` | Multi-instance Cloud Run: limits are per process, not global; generous subject defaults. |

### Positive performance patterns observed

- Blob stream with 1 MiB chunks + hash/size verify.
- Write-through object cache with LRU byte budget (default 256 MiB).
- CI materialize parallel walk + content-addressed client cache.
- Non-blocking hub publish (slow subscribers drop).
- Directory list pagination; HTTP/gRPC timeouts on gateway.

---

## Recommended priority order

1. **Lock down git import source** — allowlist trusted hosts (e.g. `https://github.com/...`), reject `file://`, absolute paths, and private/link-local IPs after DNS resolution.
2. **Blob tenancy** — require content hash to appear in an authorized tree/slice coverage, or use tenant-scoped object keys.
3. **Encrypt secrets at rest**; avoid putting full secret values on long-lived dispatch messages if possible (short-lived fetch by daemon).
4. **Default CI network off**; require explicit `network: true`. Prefer **forbid host-mode** on shared daemons (image required).
5. **Cap ReadFile default length**; stream large files; don’t `ReadAll` unbounded.
6. **Stream Put through cache** without always buffering; or bypass cache for large objects entirely (already not cached when over max object size, but still fully read today).
7. **Import / projection quotas** (concurrency, max clone size, max commits, disk budget).
8. **Require metrics token in prod**; fix XFF trust model (use rightmost trusted hop or platform client IP).

---

## Related docs

- `MVP_REVIEW.md` — earlier design vs. implementation review (includes a 2026-06-11 security addendum).
- `future_work.md` — production security/scalability backlog (encryption, tenant isolation tests, quotas, GC, etc.).
- `design/12_account_auth.md` — account/auth model.
- `design/17_continuous_integration.md` — CI / checks design.
- `design/02_storage.md` — storage model.

---

## Scope note

Findings are from static review of the paths listed above. A live pentest, dependency audit, and production config review would be complementary next steps.
