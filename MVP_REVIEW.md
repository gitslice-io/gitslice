# Gitslice MVP Review — Design vs. Implementation

Date: 2026-06-09
Scope: review of the current design (`design/`) and Go implementation against the
MVP requirements in `design/00_product.md`, `design/08_mvp_implementation.md`,
and `design/09_execution_plan.md`, with a focus on (a) core-functionality
completeness and (b) how easily the system can scale.

---

## 0. Addendum — Security review and hardening (2026-06-11)

A follow-up security/scalability pass was run against the current tree (several
findings from the 2026-06-09 review were already fixed in code: covering-slice
prefix table, transactional outbox + index worker, streaming blob transfer,
role + visibility authorization). The following fixes were applied in this pass:

- **Single global target ref is now enforced, not just assumed.** Decision:
  keep one global ref (`refs/global/main`); branches stay future work. The
  service layer rejects any non-default `target_ref`
  (`service/errors.go:requireDefaultTargetRef`, wired into `CreateChangeset` and
  `ImportGitRepository`). See §4.2 item 4 for the corrected `path_heads`
  analysis.
- **Fake account service is no longer exposed outside dev mode.**
  `FakeAccountService` mints session tokens with no credential check (dev login
  and self-serve signup) and was registered unconditionally. It is now
  registered only when `DevMode` is set (`server/server.go:NewGRPCServer`); test
  harnesses opt in explicitly.
- **HTTP servers now have deadlines and request-body caps.** The JSON gateway
  sets read-header/read/write/idle timeouts and caps bodies via
  `MaxBytesReader`; the Git smart-HTTP server sets read-header/idle timeouts
  (body deadlines left open for large clone/push) and caps buffered request
  bodies at 128 MiB. This closes the slowloris and memory-exhaustion surface
  from unbounded `io.ReadAll`.

Still open (tracked in `future_work.md`, intentionally deferred as they need
their own design): session revocation / token rotation RPC, rate limiting and
quotas, transport TLS, and unauthenticated `/metrics`. These are real but are
larger than localized fixes and should land as scoped changes.

---

## 1. Verdict

**The core CLI-first MVP journey is real and working end-to-end**, with strong
test discipline (CLI e2e, RPC e2e, restart/persistence, contention load tests)
and an architecture that matches the design's "native source graph first, Git at
the boundary" thesis. The storage model (Postgres metadata + content-addressed
immutable trees/blobs, path-head CAS, pending-publish batching, ref CAS) is
implemented as designed and is the strongest part of the codebase.

**It is not yet a complete MVP against the design's own scope.** Three
MVP-scoped areas are missing or materially incomplete:

1. **Submit settings are not implemented** — no required approvals, required
   checks, review state, or path locks; submit requirements carry only a
   definition hash that is never re-verified at submit time.
2. **Authorization is account-membership-only** — slice visibility
   (`private`/`account`/`public`) is stored but never enforced, and slice roles
   (owner/admin/writer/reader) do not exist.
3. **Git push into changesets (Phase 6) is absent** — push is rejected outright,
   while `00_product.md` lists "Git push into changesets" in MVP scope.

**Scalability**: the conceptual architecture scales well (immutable
content-addressed objects, per-path CAS admission, batched publication, derived
indexes). However, several implementation choices are O(N)-per-request or
single-node-bound and will need rework before the system "scales easily":
covering-slice resolution does a full-table scan per changed path, blob
upload/read are unary whole-payload RPCs, indexes are written synchronously
inside the publish transaction instead of via the designed outbox, and
`path_heads` has no `target_ref` dimension. None of these are wrong for a
prototype — most are called out in `future_work.md` — but they are the concrete
blockers between "MVP" and "scales easily."

---

## 2. Requirement Coverage by Execution Phase

| Phase (design/09) | Status | Notes |
|---|---|---|
| 0 — Runtime, server shell, test harness | **Complete** | Single server binary, wiring-only `server/`, fake account service, migration runner, harness with isolated HOME/DB/object roots. Metrics/pprof endpoints required by 08 §3 are missing. |
| 1 — Native object model | **Complete** | Content-addressed blobs/trees/commits (`internal/objectid`, `internal/treestore`), ref CAS, canonical paths, hash-verified uploads. GC and staged-blob leases are not implemented. |
| 2 — Slice definitions & projection | **Mostly complete** | Slices, versioned definitions with `definition_hash` optimistic concurrency, overlap coverage, slice-projected listing. No definition *history* table (only current row, so the "auditable definition versions" exit criterion is weak), visibility/roles unenforced. |
| 3 — Workspace & native CLI | **Mostly complete** | Sparse hydrate, global client object cache, status/diff, create/update/submit, shell, `gs fs`, bare slice refs. `gs op log` / local undo (an explicit Phase 3 exit criterion) not implemented. |
| 4 — Changesets & direct submit | **Core complete, requirements subset missing** | Patchsets immutable, path-base predicates + read/write sets recorded, path-head CAS admission, batched publisher, ref CAS, stale-base and outside-slice rejection covered by tests. Missing: approvals, required checks, review state, path locks, submit-time coverage/requirement refresh. |
| 5 — Git read compatibility | **Partial** | Smart-HTTP clone/fetch with projection cache and auth matrix tests. No partial-clone filter support found; anonymous read of public slices not possible (auth always required). |
| 6 — Git push into changesets | **Not implemented** | `internal/gitcompat/http.go:45` rejects receive-pack with "push is not supported". This is MVP scope in `00_product.md` §8. |
| 7 — Indexing, CI, scale | **Partial** | `commit_changed_paths` and entity move-history indexes exist but are written synchronously in the publish transaction; no transactional outbox, no index workers, no rebuild tooling, no GC, no read replicas. Load tests exist and are good, but they assert correctness only — latency/throughput thresholds required by 08 §10 are logged (`t.Logf`), not enforced. |

The minimal CLI journey required by 08 §12 (`auth login → workspace init →
status → cs create → cs submit → cs status`) **passes end-to-end against a real
server** (`tests/cli/cli_smoke_test.go: TestMinimalCLIJourney`), including
restart persistence (`TestRestartPreservesSubmittedState`) and concurrent-submit
correctness (`TestSameNewPathConcurrentOnlyOneSubmitSucceeds`,
`TestLoadSamePathSubmitContention`). That gate is genuinely met.

---

## 3. Core-Functionality Findings

### 3.1 Submit requirements are a stub (High)

`design/00_product.md` §8 puts "slice-level submit settings" in MVP scope, and
the architecture doc (§11–12) makes approvals/checks/locks the centerpiece of
submit validation. In the implementation:

- `SubmitRequirements` is populated with only `SourceSliceDefinitionHash`
  (`service/changeset.go:429-431`). `RequiredApprovals`, `RequiredChecks`, and
  `PathLockIds` exist in the proto and CLI output (`internal/cli/cli.go:4671`)
  but are never set or evaluated.
- There is no review state, no approval recording, no check integration, and no
  path-lock table.
- `ChangesetStore.Submit` (`internal/postgres/changeset_store.go:360`) validates
  path-base freshness against the current head, but does **not** re-verify the
  recorded slice-definition hash, recompute coverage, or re-check authoring-slice
  containment against the *latest* slice definition — design §11.2 steps 10–12
  and invariant 12 ("a changeset must satisfy the submit requirements of its
  authoring slice") are not enforced at submit time. A slice definition can be
  narrowed between patchset creation and submit, and the submit still lands.

**Suggested improvement**: implement a minimal-but-real version before calling
the MVP complete: (1) add `submit` settings (required approval count or team +
required check names) to the slice definition and its hash; (2) add an
`approvals` table keyed by `(changeset_id, patchset_id, subject_id)` and a
`gs cs approve` / `SetReview` RPC; (3) add a check-state RPC that CI can call;
(4) in `Submit`, recompute containment + coverage against the latest definition,
compare definition hashes, and block with an explicit reason
(`NeedsRequirementRefresh`) on mismatch. The schema and service seams already
exist, so this is incremental work, not a redesign.

### 3.2 Authorization model is flat (High)

Every service method authorizes with exactly one primitive:
`AuthStore.EnsureAccountMember(subject, account)` (used throughout
`service/slice.go`, `service/changeset.go`, `service/workspace.go`,
`service/repository.go`).

Consequences, relative to design §4.3–4.4 and §5:

- **Visibility is stored but never read for access decisions.** A `private`
  slice is readable by every account member; a `public` slice is *not* readable
  by non-members or anonymously (including over Git HTTP, which always requires
  a token). Both directions diverge from the design.
- **No slice roles.** Any account member (the membership `role` column is never
  consulted) can update any slice definition, delete slices, submit, and abandon
  other members' changesets. Design distinguishes owner/admin/writer/reader and
  requires writer access to create changesets.
- **Blob endpoints check only authentication** (`service/blob.go:21,44`).
  `GetBlobStatus` lets any authenticated user probe for blob existence by
  content hash across all accounts — a cross-tenant information leak; the
  tenant-isolation tests in `future_work.md` would catch this.

**Suggested improvement**: introduce a single `authorize(subject, slice, action)`
helper used by all services, backed by membership role + slice visibility, even
with a coarse role mapping at first (account admin ⇒ slice admin, member ⇒
writer). This keeps the fake account service but makes the authorization shape
real, so production identity can later swap in beneath an already-correct
enforcement layer. Scope blob-status checks to content hashes referenced by
slices the caller can read, or simply require the caller to name a slice.

### 3.3 Git compatibility is read-only (Medium)

Clone/fetch with projection caching, auth, and stable synthetic commits is
implemented and tested (`TestGitCloneProjection`, the auth matrix test). But
push-to-changeset (`refs/changes/new`) — MVP scope and the entire Phase 6 — is
not started; receive-pack returns a 403 with guidance. Git users can read but
cannot contribute. Decide explicitly: either implement Phase 6's minimal form
(single-commit push converted to one patchset through the existing
`ValidateWorkspaceDiff`/`UpdateChangeset` path) or amend `00_product.md` to move
Git push out of MVP scope so docs and code agree.

### 3.4 Workspace op log / undo missing (Low)

Phase 3 exit criteria include "local operations can be inspected through
`gs op log`"; `08` §8 lists `op_log/` and `draft_patchsets/` in `.gs/`. Neither
exists in `internal/cli`. `future_work.md` acknowledges this. Either implement a
minimal append-only op log or strike it from the Phase 3 exit criteria.

### 3.5 No GC / storage lifecycle (Medium)

MVP scope (`00_product.md` §8) includes "correctness-first storage lifecycle and
GC". There is no GC for staged/orphaned blobs, unreachable tree nodes, abandoned
patchsets, or projection cache entries, and no staged-upload lease protocol —
`UploadBlob` immediately marks bytes available. The integrity verifier
(`internal/postgres/integrity.go`) is a good start on the read side. For MVP, a
conservative offline `gs admin gc --dry-run` that enumerates reachability roots
(refs, live patchsets) and reports unreachable objects would satisfy the
"correctness-first" bar without risky deletion.

### 3.6 Observability below the design's own floor (Medium)

`08` §3 requires structured logs (present, `slog`), a basic metrics endpoint
(absent), and pprof in dev mode (absent). There are no request IDs, no counters
for submit acceptance/conflict/CAS-retry — the exact metrics the load tests are
supposed to gate on. Adding a `/metrics` endpoint plus counters around
`Submit`/`PublishPending` is cheap and directly feeds §4's load-test thresholds.

### 3.7 Smaller design/implementation mismatches

- **Slice definition versions are not auditable**: `slices` keeps only the
  current row (version + hash). Design §4.2 requires each accepted change to
  create a new recorded definition version. Add a `slice_definition_versions`
  append-only table.
- **DESIGN.md at the repo root is a web style guide** ("Alexandria — High-End
  Editorial") that has nothing to do with the system design under `design/`.
  Rename it (e.g. `design/14_web_style_guide.md`) to avoid misleading readers
  and tools that treat root `DESIGN.md` as the architecture entry point.
- **Load tests don't enforce budgets**: `08` §10 says thresholds "must be
  checked automatically". Today they `t.Logf` p50/p95/p99. Add even generous
  assertions so regressions fail CI's manual load job.

---

## 4. Scalability Findings

### 4.1 What already scales well (architecture)

- **Immutable, content-addressed storage** (`objectid`, `treestore`): commits
  store only `root_tree_id`; publish path-copies only changed tree nodes. This
  is the right asymptotic shape for large trees and histories.
- **Submit admission via per-path CAS + durable `pending_publish` + batched
  publisher + single ref CAS** matches the design and gives a clean path to high
  submit throughput on hot refs. Crash recovery resumes from durable rows;
  `for update skip locked` (`changeset_store.go:488`) makes multiple publisher
  instances safe.
- **Derived history indexes** (`commit_changed_paths`, entity move history) keyed
  by `(target_ref, path)` support slice-history projection without walking the
  graph; commit listing is paginated (`ListCommitPage*`).
- **Clean seams for scaling out**: object store behind an interface
  (swap filesystem → S3/GCS), `internal/storage` interfaces with both Postgres
  and in-memory implementations, `server/` wiring-only, services stateless over
  the stores — horizontal scaling of the gRPC tier is mostly a deployment
  problem, not a refactor.
- **Load/contention coverage already exists** (disjoint vs same-path submit
  contention, hot-file publish latency, multi-user, large directories, 8 MB
  files, 180-commit history pagination) — rare for an MVP and very valuable as
  a regression harness when the scaling work starts.

### 4.2 Concrete bottlenecks to fix before "scales easily"

1. **Covering-slice resolution is O(all slices) per changed path.**
   `SliceStore.CoveringIDs` (`internal/postgres/slice_store.go:213`) selects
   *every* slice row and prefix-matches in Go; `validateFileEdits` calls it once
   per affected path (`service/changeset.go:413`). A 1,000-file patchset against
   10,000 slices is 10M comparisons plus 1,000 full-table scans per
   create/update. **Fix**: a `slice_included_paths(prefix, slice_id)` table with
   a prefix-match index (or Postgres `ltree`), queried once per patchset with
   the full path set.

2. **Blob transfer is unary and memory-bound.** `UploadBlob` takes the whole
   payload in one message (`service/blob.go:43`), bounded by
   `MaxUnaryMessageBytes = 128 MiB` (`internal/rpclimits`); `ReadFile` returns
   bytes in a unary response (offset/length exist, but the CLI path and
   `DiffChangeset`'s `io.ReadAll` (`service/changeset.go:321-332`) buffer whole
   files). Large files cost full-file RSS per concurrent request on both client
   and server. **Fix**: streaming Upload/Read RPCs (chunked, hash-verified on
   finalize) — the design's staged-upload protocol (02 §) already describes
   this; add diff size cutoffs ("binary/large file changed" instead of full
   unified diff).

3. **Index writes are synchronous inside the publish transaction, with no
   outbox.** `PublishPending` inserts `commit_changed_paths` one row per path
   per commit (`changeset_store.go:664-675`) and walks entity history in the
   same transaction. This couples publish latency (and the ref lock hold time)
   to index fan-out — directly against the architecture's "indexes are derived,
   event-driven, rebuildable" rule, and there is no rebuild tooling. **Fix**:
   add the designed transactional `outbox` table written in the publish tx, move
   changed-path/entity-history/projection-invalidation work to a worker, and
   batch row inserts (single multi-row `INSERT` or `COPY`). Keep the publish tx
   to: commit row, path_heads refresh, ref CAS, outbox append.

4. **`path_heads` is keyed by `path` alone — resolved as intended, not a
   defect.** Primary key is `path` (`migrations/0001_init.sql:95`), which is
   correct only while there is exactly one target ref. The earlier draft of this
   review called that a design divergence; that was wrong — the design's own
   `path_heads` schema (`design/02_storage.md:524`) is also `path primary key`,
   so implementation and design agree. The project has now committed to a
   **single global target ref** (`refs/global/main`); multiple target refs
   (branches) remain explicitly future work (`design/02_storage.md` §3.1). To
   keep that invariant enforced rather than merely assumed, the service layer now
   rejects any non-default `target_ref` at `CreateChangeset` and
   `ImportGitRepository` (`service/errors.go:requireDefaultTargetRef`). Adding a
   `target_ref` column to `path_heads` is deferred until branches are actually on
   the roadmap; it is a cheap change while the model stays single-ref and an
   online hot-table migration afterward, so the trade is acknowledged and
   accepted.

5. **Publisher processes one target ref per tick.** `PublishPending` filters the
   locked batch to the first ref's rows (`changeset_store.go:511-517`) and
   returns; other refs wait for the next 25 ms poll
   (`server/publisher.go:11`). Fine today (single ref), but with many refs this
   is head-of-line blocking; group pending rows by ref and publish each group
   (or run per-ref workers). Also consider `LISTEN/NOTIFY` instead of polling to
   cut idle DB load and tail latency.

6. **Single-process assumptions are documented but worth restating as the
   scaling boundary**: filesystem object store requires single-writer
   discipline (multi-host gRPC tier is *not* safe until a durable object store
   adapter lands — this is the first prerequisite for any horizontal scaling);
   the publisher runs as a goroutine in every server process (safe via
   skip-locked + ref CAS, but uncoordinated).

7. **No rate limits or quotas** on uploads, changeset creation, or projection
   requests (acknowledged in `future_work.md`); a single misbehaving client can
   saturate the publish pipeline or object store.

### 4.3 Scaling priority order

If the goal is "scale easily" with the least rework:

1. Durable object-store adapter (S3-compatible) behind the existing interface —
   unlocks multi-host deployment of the stateless gRPC tier.
2. Covering-slice prefix index (fixes the only per-request full-table scan).
3. Streaming blob upload/read.
4. Outbox + async index workers + batched inserts (decouples publish latency
   from index fan-out; gives rebuildability).
5. `(target_ref, path)` path_heads key + per-ref publisher batching.
6. Metrics + enforced load-test budgets so 1–5 are measured, not guessed.

---

## 5. Strengths Worth Keeping

- **Design/code coherence**: package layout, service boundaries, and even doc
  cross-references match `08_mvp_implementation.md` almost exactly; AGENTS.md
  architecture rules are followed (wiring-only `server/`, migrations as SQL
  files, path rules centralized in `internal/paths`, ids in `internal/objectid`).
- **Test pyramid is genuinely end-to-end**: 37 CLI e2e tests against real
  server + real Postgres + real binaries, separate RPC-contract suite,
  restart/persistence, race-prone concurrency tests, opt-in load suite — this is
  the MVP delivery gate the design asked for.
- **Execution log discipline** (`design/10_execution_log.md`): every change
  records request, decisions, learnings, and verification commands — excellent
  for auditability and onboarding.
- **Storage correctness primitives**: hash-verified uploads, deterministic
  commit/tree ids, integrity verifier, idempotent submit (re-submit of a
  submitted changeset returns the prior commit).
- **User-facing identity polish**: shareable changeset handles
  (`acme/payment@42.2`) implemented down to CLI output and selectors.

---

## 6. Recommended Path to "Complete MVP"

In order:

1. **Authorization pass** (§3.2): visibility enforcement + role checks via one
   shared helper; scope blob-status probing. (Closes the largest design gap and
   most of the multi-tenancy risk while keeping fake auth.)
2. **Minimal submit settings** (§3.1): approvals + required checks in the slice
   definition, an approvals table, submit-time freshness recheck of definition
   hash/containment, and explicit blocked-state reasons.
3. **Decide Git push** (§3.3): implement minimal `refs/changes/new` → patchset,
   or formally descope it from MVP in `00_product.md`.
4. **Observability floor** (§3.6): metrics endpoint, submit/publish/conflict
   counters, request IDs; turn load-test logs into enforced budgets.
5. **Scaling items 1–3 from §4.3** as the first post-MVP milestone, since they
   require no design changes — the interfaces are already in place.
6. **Doc hygiene**: relocate root `DESIGN.md`, add a definition-versions table
   or amend §4.2, and strike or implement `gs op log`.

Items already correctly tracked in `future_work.md` (production auth, GC,
replication, sharding, chaos testing, SLOs) are not repeated here, but §4.2's
covering-slice scan and the missing submit-time freshness recheck deserve to be
promoted from "future work" to "MVP completeness," since the design documents
treat both as MVP behavior.
