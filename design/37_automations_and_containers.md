# 37. Folder Automations, Manual Runbooks & Bash on Cloudflare Containers

> **Status:** Implemented (2026-11-19). Supersedes the naming and extends the
> runtime architecture of `design/34_triggers.md`.

## Summary

1. **Rename `Trigger` → `Automation`:** User-facing configuration, CLI, web UI,
   and documentation now use **Automations** (`.gitslice/automations.yaml`,
   top-level `automations:` key, `gs automation` / `gs auto`, `/doc/automations`),
   matching the folder Policy drawer's **Automation** tab and covering both
   event-driven workflows and manual runbooks. `.gitslice/checks.yaml` stays
   separate for untrusted draft patchset verification.
2. **Secure manual runbooks:** Any automation with `manual` in `on:` supports
   typed `inputs:` (`string`, `choice`, `boolean`, `number`, defaults, regex
   `pattern`, `required`), explicit `manual:` access controls (`roles: [admin]`
   or `[admin, writer]`, `require_human: true`, `confirm: "..."`), folder-scoped
   `ActionUseSecrets` authorization, actor attribution (`triggered_by`,
   `triggered_by_kind`), interactive prompts and `--follow` in `gs auto run`,
   and the `RunAutomationModal` in the web UI.
3. **Bash execution on Cloudflare Containers:** `action: { bash: ... }` runs
   inline commands or scripts under `.gitslice/` inside single-run Cloudflare
   Containers (`scheduling_policy: "durable_object"`) orchestrated by the
   `BashRunner` Durable Object in `trigger-dispatch/`.

## Secret & Egress Architecture for `action.bash`

Secrets are never passed via `ctx.container.start({ envVars })`. Instead:

1. `BashRunner` claims the run via `TriggerRunService.StartRun` and keeps
   secrets strictly in Worker/DO RAM (never persisted to DO SQLite).
2. Non-revealed secrets are AES-GCM sealed (`gs-secret:v1:...`) with `seal()`
   (`trigger-dispatch/src/crypto.ts`). Secrets listed in `reveal:` remain
   plaintext for local signing or raw TCP tools.
3. `BashRunner` registers `interceptOutboundHttp("gitslice.internal", GsBridge)`
   and starts the container with only a single-use 256-bit `BOOTSTRAP_NONCE`.
4. `cmd/gs-exec` (PID 1) calls `GET http://gitslice.internal/bootstrap` once.
   `GsBridge` verifies and atomically burns the nonce inside `BashRunner` so
   user shell code cannot re-fetch bootstrap data.
5. `gs-exec` writes secrets to `/run/secrets/<NAME>` (`mode 0400`, exported as
   `$GITSLICE_SECRETS_DIR`), injects `$<NAME>`, `$INPUT_<UPPER_SNAKE>`, and
   `$GITSLICE_*` env vars, extracts the landed checkout archive via
   `GET http://gitslice.internal/archive` (server endpoint `/automation-archive`,
   capped at 20,000 files / 256 MiB), drops privileges to UID `10001` (`runner`),
   and runs `bash -euo pipefail`.

### Three Egress Tiers

| Mode | Configuration | Container networking | Secrets |
| --- | --- | --- | --- |
| **Offline** *(default)* | No `network:` or `egress:` | `enableInternet: false`, only `gitslice.internal` | Sealed (or `reveal:`) |
| **HTTP-Gate** *(default when `network:` set)* | `network: [hosts...]` | `enableInternet: false` + `interceptOutboundHttps("*", Gate)` | Sealed placeholders unwrapped by `Gate` only for allowlisted HTTPS hosts |
| **Direct** *(explicit opt-in)* | `egress: direct` | `enableInternet: true` (raw TCP/UDP/TLS/SSH/gRPC) | Must list secrets in `reveal:` (masked in logs) |

In `http-gate` mode, `gs-exec` installs Cloudflare's runtime ephemeral CA cert
(`/etc/cloudflare/certs/cloudflare-containers-ca.crt`) into the system trust
store and exports `SSL_CERT_FILE`, `CURL_CA_BUNDLE`, `REQUESTS_CA_BUNDLE`, and
`NODE_EXTRA_CA_CERTS`.

### Curated Container Runtimes

Because Cloudflare Containers' `durable_object` policy only boots images
declared in `wrangler.jsonc` and hosted in Cloudflare's managed registry,
`trigger-dispatch/containers/` ships 5 digest-pinned runtimes with `gs-exec`
and symlinked `gs-auto` baked in: `base`, `node`, `python`, `go`, and `cloud`
(`gcloud`, `aws`, `kubectl`, `psql`, `gh`, `terraform`).

### Limits & Teardown

- Bash timeout: default `5m`, maximum `15m`.
- Active concurrency: up to 4 concurrent `bash` runs per account (`MaxActiveBashPerAccount = 4`), within the 20 active automations cap.
- Double secret masking in both `GsBridge` (`trigger-dispatch/src/mask.ts`) and `TriggerRunService` (`internal/secretmask`).
- Single-run destruction: `BashRunner` calls `ctx.container.destroy()` immediately on `/finish`, `/cancel`, or its watchdog alarm (`timeout + 15s`).

## Deployment Notes

1. Deploy the Go server (runs migration `0064_automation_run_attribution.sql`).
2. Deploy `trigger-dispatch`: `cd trigger-dispatch && npm run deploy` (runs `npm run build:exec` to cross-compile `cmd/gs-exec` for `linux/amd64`, builds/pushes the 5 curated runtime images, and deploys the Worker + `BashRunner` DO migration).
3. Release updated `gs` CLI with `gs automation` / `gs auto`, then update `.gitslice/checks.yaml` `started_by: trigger` → `started_by: automation` once CI daemons run the new `gs` release.
