# 34. Automations

> Consolidates `34_triggers.md`, `37_automations_and_containers.md`,
> `38_reusable_automations_and_pipelines.md`, and the surviving event/outbound
> primitives from `24_webhooks.md` (all archived in `design/archive/`) into one
> authoritative specification.

## Goal

One folder-scoped system for event-driven workflows, manual operational
runbooks, reusable templates, and multi-step pipelines:

- Any folder can declare `<dir>/.gitslice/automations.yaml`. Changes anywhere
  under that folder cascade up to the account root, just like
  `.gitslice/checks.yaml` ([17_continuous_integration.md](17_continuous_integration.md))
  and folder-scoped secrets ([33_secrets.md](33_secrets.md)).
- Six action types share one engine, run log, and security model: `http`,
  `worker` (Cloudflare Dynamic Workers), `bash` (Cloudflare Containers),
  `check` (slice/account CI daemons), `uses` (reusable `on: [call]` templates),
  and `steps` (multi-step pipelines).
- Standalone slice webhooks (`24_webhooks.md`) were removed in migration `0061`
  and replaced by `action.http`, while the shared event bus (`internal/events`)
  and outbound SSRF/HMAC layer (`internal/outbound`) live on underneath
  automations and Git mirrors.

## Configuration (`.gitslice/automations.yaml`)

```yaml
# services/billing/.gitslice/automations.yaml
version: 1

automations:
  notify-deploy:                      # id: services/billing/notify-deploy
    description: Notify deployment webhook on Go changes
    on: [push]
    refs: [main]
    paths: ["**/*.go", "!**/*_test.go"]
    secrets: [DEPLOY_TOKEN, DEPLOY_HMAC]
    action:
      http:
        url: https://deploy.example.com/hooks/billing
        headers:
          Authorization: "Bearer ${{ secrets.DEPLOY_TOKEN }}"
        sign: DEPLOY_HMAC

  deploy-prod:
    description: Deploy billing service to production
    on: [manual]
    inputs:
      tag:
        type: string
        required: true
        pattern: "^v[0-9]+\\.[0-9]+\\.[0-9]+$"
    manual:
      roles: [admin, writer]
      require_human: true
      confirm: "Deploy billing to production?"
    secrets: [CLOUDFLARE_API_TOKEN]
    network: [api.cloudflare.com]
    concurrency_group: billing-prod
    timeout: 15m
    action:
      bash:
        runtime: node
        size: standard-1
        checkout: folder
        script: ops/deploy.sh
```

| Field | Req | Meaning |
| --- | --- | --- |
| `version` | yes | Format version (`1`). Unknown majors are refused. |
| `automations` | yes | Map of local name (`[a-zA-Z0-9_-]+`) to spec. Qualified ID is `<dir>/<name>` (or `<name>` at account root). May not collide with a check ID in the same folder. |
| `description` | no | Human description shown in CLI and folder Policy drawer. |
| `on` | yes | Events (`push`, `tag.created`, `changeset.*`, `check_run.completed`), `manual`, `call` (reusable template), or `schedule`. |
| `paths` | no | Globs relative to the defining folder or `/`-absolute, with `!` exclusions. Empty ⇒ any change under the folder. |
| `changes` | no | File change kinds (`added`, `modified`, `deleted`, `moved`). Default: all. |
| `refs` | no | Target refs (`main` ⇒ `refs/global/main`). Default: slice default target. |
| `inputs` | no | Typed manual/template inputs (`string`, `choice`, `boolean`, `number`, `description`, `required`, `default`, `options`, `pattern`). |
| `manual` | no | Access policy for `manual` runs: `roles` (`[admin]` or `[admin, writer]`), `require_human: true`, `confirm: "..."`. |
| `secrets` | no | List of secret names at the defining folder (`[FOO]`), or mapping `{ TEMPLATE_SLOT: CALLER_SECRET }` for `action.uses`. |
| `reveal` | no | Explicit opt-in list of secrets exposed as plaintext (for local signing or `egress: direct`); always masked in logs. |
| `network` | no | Outbound HTTPS host allowlist (exact hostname or `*.example.com`). Default: none. |
| `egress` | no | `bash` only: `offline` (default when `network:` omitted), `http-gate` (default when `network:` set), or `direct` (explicit raw TCP/UDP/TLS opt-in). |
| `checks` | no | Local check names (`<dir>/<name>`) the automation may create/update on a changeset. |
| `concurrency` | no | `cancel-superseded` (default for changeset events), `serial`, or `parallel`. |
| `concurrency_group` | no | Account-wide named lock shared across automations (e.g. `prod-server` for deploy + rollback). Runs one at a time, oldest first; implies `serial` (`trigger_runs.concurrency_group`). |
| `timeout` | no | Run deadline. Worker callback wait: default `1h`, max `7d`. Bash: default `5m`, max `15m`. |
| `retry` | no | `worker` only: retry interrupted runs up to `N` times (max 5; default 0 = at-most-once). |
| `action` | yes | Exactly one of `http`, `worker`, `check`, `bash`, `uses`, or `steps`. |

### Security Boundaries on Definitions

1. **Landed definitions for changeset events.** For `changeset.*` events,
   `automations.yaml` and scripts are always read from the **target ref's head**
   (`refs/global/main`), never from the draft patchset — preventing unreviewed
   edits from exfiltrating folder secrets.
2. **`.gitslice/` admin approval (`service/automation_submit.go`).** Any
   changeset adding, modifying, or deleting a file under `.gitslice/` requires
   the author to be an account owner/admin, or an approval on the current
   patchset from another account owner/admin (enforced on single, merge, and
   stack submits).
3. **Folder-scoped secrets.** Automations only see secrets bound to their
   defining folder (and ancestors via longest-prefix precedence,
   [33_secrets.md](33_secrets.md)) that they explicitly list in `secrets:`.
   Manual runs additionally require `ActionUseSecrets` when secrets are used.

## Event Bus (`internal/events`) & Outbound Guard (`internal/outbound`)

### Event Bus (`internal/events/`)

Producers append immutable event records to `webhook_events` (thin store
wrappers in `internal/events/events.go` plus atomic writes inside the publisher
transaction for `changeset.submitted`):

| Event | Trigger | Matched folders |
| --- | --- | --- |
| `push` | Commit landed touching paths | Ancestor spine of each changed path |
| `tag.created` | New immutable tag created | Tagging slice's included folders and ancestors |
| `changeset.created`, `changeset.updated` | Patchset created/updated | Ancestor spine of patchset's changed paths |
| `changeset.approved`, `changeset.submitted`, `changeset.abandoned` | Changeset lifecycle transition | Ancestor spine of changeset's affected paths |
| `check_run.completed` | Check run reached terminal state | Ancestor spine of changeset's affected paths |

- **Payload envelope (`internal/events/payload.go`):** Carries `id`, `event`,
  `created_at`, `slice`, `sender` (`username`, `kind`: `user|agent|automation`),
  `automation` (`id`, `folder`), `changes[]` (`path`, `kind`, `old_path`),
  `definition_commit`, and event-specific `changeset`, `commit`, `tag`, or
  `check_run`.
- **Loop prevention:** Automations ignore events emitted by themselves, and
  dispatch halts past event `depth > 3`.
- **Throttled-host drain (`internal/events/dispatcher.go`):** Requests that
  record events drain inline for up to 2 s when automations exist (checked via
  a 30 s cache reset on `.gitslice/automations.yaml` landings). A background
  sweep handles retries and timeouts while the server is active, pausing under
  `internal/activity` so Cloud Run and Neon scale to zero.
- **Deduplication & claims:** `trigger_runs` enforces uniqueness on
  `(trigger_id, event_id)` (manual reruns synthesize `<event_id>.rerun.<n>`).
  `StartRun` claims queued runs with a conditional update + 2-minute lease;
  expired leases mark `worker` runs `errored` unless `retry > 0`.

### Outbound Guard & Signing (`internal/outbound/`)

Used by `action.http` (and shared with external delivery paths):

- **SSRF guard (`internal/outbound/guard.go`):** Requires `https://`, rejects
  URL userinfo and lengths > 2048 bytes, and blocks loopback, private,
  link-local (cloud metadata), CGNAT, and reserved IP ranges both at validation
  time and **after DNS resolution** at dial time. Redirects are not followed;
  timeouts are 10 s. (`GITSLICE_WEBHOOK_ALLOW_PRIVATE_TARGETS=1` lifts this for
  local/test environments only.)
- **Headers & HMAC (`internal/outbound/post.go`):** Sends `Content-Type:
  application/json`, `X-Gitslice-Event`, `X-Gitslice-Event-ID`,
  `X-Gitslice-Delivery`, `X-Gitslice-Automation`, and (when `sign:` is set)
  `X-Gitslice-Signature-256: sha256=<hex HMAC-SHA256(secret, body)>`.
- **Retries:** `action.http` retries non-2xx / network errors at `1m`, `5m`,
  `30m`, `2h`, `6h`, `12h` (7 attempts total; refused targets fail
  immediately). `${{ secrets.NAME }}` is permitted in `url` and `headers` only.

## Actions & Execution Architecture

### 1. `http`
Executed directly by the Go server (`internal/automations/engine.go` +
`internal/outbound`). No Cloudflare sandbox needed.

### 2. `check`
Queues named checks from the folder's `.gitslice/checks.yaml` (typically marked
`started_by: automation`) onto the slice's or account's CI daemon pool via
`checkDispatcher.sendChecks`. Each check run carries `automation_run_id` and
provenance `ci`. The automation run waits until its checks complete
(`Engine.FinishIfDone` on `check_run.completed`).

### 3. `worker` (Cloudflare Dynamic Workers — `trigger-dispatch/`)

```
Go server (Cloud Run)                         Cloudflare (triggers.gitslice.io)
  fan-out → insert trigger_runs (queued)
     │ POST /run {run_id, ts} (HMAC) ────────►  Worker "trigger-dispatch"
     │                                          1. AutomationRunService.StartRun(run_id)
     │                                          2. env.LOADER.get("trigger-" + code_hash)
     │                                             → Dynamic Worker + globalOutbound Gate
     │                                          3. entrypoint.onEvent(event, gs)
     │                                          4. AutomationRunService.FinishRun
  external CI ── POST /cb/<run>/<sig> ────────► StartCallback → onCallback(req, gs)
```

- **Isolate reuse & limits:** Loaded via Cloudflare's Worker Loader binding
  (`env.LOADER`) keyed by `sha256(modules, network)` (max 50 relative ES
  modules under `.gitslice/`, max 1 MiB). One automation run per Worker
  invocation (respecting Cloudflare's 4-concurrent-Dynamic-Workers-per-request
  limit). `console.*` output is captured per call via `AsyncLocalStorage`
  (`nodejs_als`) and masked before `AppendRunLog`. Limits per run: 25 s wall
  time per `onEvent`/`onCallback` call, 50 subrequests, 64 KiB JSON `gs.state`,
  100 callbacks.
- **`gs` RPC stub:** Passed per call (`WorkerEntrypoint`), backed by a
  short-lived HMAC run token (`gstr_<run>.<exp>.<hmac>`):
  - `gs.checks.create` / `.update` (status, Markdown summary, `detailsUrl`,
    line `annotations` up to 200; upserts `check_results` for submit gates)
  - `gs.files.read(path, {at})` / `.list(dir, {at})` (`"event"` or `"base"`)
  - `gs.changeset.get()`
  - `gs.state.get()` / `.set(obj)` (64 KiB)
  - `gs.secret(name)` (sealed placeholder) / `gs.revealSecret(name)` (requires `reveal:`)
  - `gs.callbackUrl(data)` (`https://triggers.gitslice.io/cb/<run>/<hmac>`)
  - `gs.waitForCallback()`, `gs.finish()`, `gs.fail(message)` (ends run as
    `failed` without stack trace), `gs.setDetailsUrl(url)` (`https://`, ≤2048 B),
    `gs.setOutput(key, value)` (for pipelines/inspection), `gs.idempotencyKey(label)`
- **Sealed secrets & `globalOutbound` Gate (`trigger-dispatch/src/gate.ts`, `crypto.ts`):**
  - `gs.secret("NAME")` returns an AES-GCM sealed placeholder
    `gs-secret:v1:<sealed>` whose AAD binds it to `code_hash` and the run's
    deadline (`exp`). Plaintext secrets never enter the V8 isolate unless listed
    in `reveal:`.
  - Every `fetch` passes through `globalOutbound` (`prepareGateRequest`), which
    enforces `https://` only, no URL credentials, no IP literals or reserved
    hostnames (`.localhost`, `.internal`, `.local`, `.home.arpa`), and strict
    `network:` allowlist matching.
  - **Whole-URL secrets are rejected:** `prepareGateRequest` validates the URL
    *both before and after* unwrapping placeholders and requires
    `finalUrl.origin === url.origin`. A secret placeholder cannot supply or
    alter the request's scheme, credentials, or origin/host — it may only
    expand inside the path, query, headers (rejecting CR/LF), or textual body
    of an already-allowlisted `https://` origin.

### 4. `bash` (Cloudflare Containers — `BashRunner` DO + `cmd/gs-exec`)

`action: { bash: ... }` runs inline `run:` commands or `.gitslice/` scripts
inside single-run Cloudflare Containers (`scheduling_policy: "durable_object"`)
managed by the `BashRunner` Durable Object in `trigger-dispatch/src/bash_runner.ts`.

- **Curated container runtimes (`trigger-dispatch/containers/`):** Five
  digest-pinned images declared in `wrangler.jsonc`, each bundling `gs-exec`
  and symlinked `gs-auto`:
  - `base`, `node`, `python`, `go`, and `cloud` (`gcloud`, `aws`, `kubectl`,
    `psql`, `gh`, `terraform`).
  - Container sizes: `lite`, `basic`, `standard-1`, `standard-2`.
- **Bootstrap & privilege drop (`cmd/gs-exec/`):**
  1. `BashRunner` calls `StartRun` and keeps secrets in Worker/DO RAM only
     (never persisted to DO SQLite). Non-revealed secrets are sealed with
     `gs-secret:v1:`.
  2. `BashRunner` registers `interceptOutboundHttp("gitslice.internal",
     GsBridge)` and boots the container with only a single-use 256-bit
     `BOOTSTRAP_NONCE` (no secrets in `envVars`).
  3. `cmd/gs-exec` (PID 1) calls `GET http://gitslice.internal/bootstrap` once;
     `GsBridge` atomically burns the nonce so user shell code cannot re-fetch it.
  4. `gs-exec` writes secrets to `/run/secrets/<NAME>` (`mode 0400`, exported
     as `$GITSLICE_SECRETS_DIR`), injects `$<NAME>`, `$INPUT_<UPPER_SNAKE>`,
     and `$GITSLICE_*` env vars.
  5. When `checkout: folder|account` is set, `gs-exec` fetches the landed tree
     archive from `GET http://gitslice.internal/archive` → Go server
     `/automation-archive` (`service/automation_archive.go`, capped at 20,000
     files / 256 MiB).
  6. `gs-exec` drops privileges to UID `10001` (`runner`) and executes
     `bash -euo pipefail`.
  7. Helper CLI inside the container: `gs-auto set-output <key> <value>` and
     `gs-auto set-details-url <url>` post to `http://gitslice.internal/gs/...`.
- **Three egress tiers:**

| Mode | Configuration | Container networking | Secrets |
| --- | --- | --- | --- |
| **Offline** *(default)* | No `network:` or `egress:` | `enableInternet: false`, only `gitslice.internal` | Sealed (or `reveal:`) |
| **HTTP-Gate** *(default when `network:` set)* | `network: [hosts...]` | `enableInternet: false` + `interceptOutboundHttps("*", Gate)` | Sealed placeholders unwrapped by `Gate` only for allowlisted HTTPS hosts; `gs-exec` installs Cloudflare's ephemeral CA cert (`/etc/cloudflare/certs/cloudflare-containers-ca.crt`) and exports `SSL_CERT_FILE`, `CURL_CA_BUNDLE`, `REQUESTS_CA_BUNDLE`, `NODE_EXTRA_CA_CERTS` |
| **Direct** *(explicit opt-in)* | `egress: direct` | `enableInternet: true` (raw TCP/UDP/TLS/SSH/gRPC) | Must list secrets in `reveal:` (masked in logs) |

- **Limits & teardown:** Default timeout `5m`, max `15m`; up to
  `MaxActiveBashPerAccount = 4` concurrent bash runs per account (within the
  20 active automations/account cap). Double secret masking in both `GsBridge`
  (`trigger-dispatch/src/mask.ts`) and `AutomationRunService`
  (`internal/secretmask`). `BashRunner` calls `ctx.container.destroy()`
  immediately on `/finish`, `/cancel`, or its watchdog alarm (`timeout + 15s`).

## Reusable Templates (`uses:`) & Multi-Step Pipelines (`steps:`)

### Reusable Templates (`on: [call]` + `action.uses`)

Any folder's `.gitslice/automations.yaml` can publish a reusable template with
`on: [call]` (no separate registry; versioned via immutable slice tags or
commit SHAs). Templates cannot recursively use `uses:` or `steps:`.

```yaml
secrets:
  GCP_SA_KEY: BILLING_PROD_GCP_KEY   # template slot -> caller folder secret
reveal: [BILLING_PROD_GCP_KEY]       # explicit caller opt-in
egress: direct                       # explicit caller opt-in
action:
  uses: /acme/platform/deploy-cloudrun@v1.2.0
  with:
    service: billing-api
```

- **Reference grammar (`internal/automations/templates.go:ParseUsesRef`):**
  `/<account>[/<folder>...]/<name>[@<tag>|@sha256:<commit>]`. Same-account
  unpinned refs resolve live from the caller's target tree; `@<tag>` resolves
  via immutable slice tags covering the template folder; `@sha256:<commit>`
  resolves directly from that commit's tree. Cross-account refs must be pinned
  and target a public slice/commit.
- **Caller-bounded security (`BindTemplate`):**
  1. Every template secret slot must be bound by the caller; values are always
     loaded from the **caller's folder** (`run.Folder`), never the template's,
     and remapped to template slot names at `StartRun`.
  2. Template `reveal:` and `egress: direct` require explicit caller opt-in.
  3. Cross-account callers must explicitly cover every template `network:` host
     (same-account callers inherit `network:` if omitted, or bound it if set).
  4. Caller `timeout:` caps template timeout (`min(caller, template)`).
  5. Scripts (`bash.script`, `worker` modules) load from `(TemplateTreeID,
     TemplateFolder)` while `bash.checkout` inspects the caller's folder and
     commit. Runs record `template_id` and `template_commit_id`.
  6. Unbound template inputs inherit into the caller's effective `inputs:`
     schema (`EffectiveInputsForUses`).
- **Discovery:** `AutomationService.InspectAutomationTemplate` (CLI:
  `gs auto inspect <uses-ref> [--account <account>]`) returns the template
  definition, resolved commit, canonical ref, and available immutable tags.

### Multi-Step Pipelines (`action.steps`) & Named Outputs

```yaml
action:
  steps:
    - id: build
      bash: { runtime: go, checkout: folder, run: "gs-auto set-output image gcr.io/acme/app:v1" }
    - id: deploy
      uses: /acme/platform/deploy-cloudrun@v1.2.0
      with: { service: app, image: "${{ steps.build.outputs.image }}" }
    - id: notify
      if: always
      http: { url: "https://hooks.slack.com/services/${{ secrets.SLACK_WEBHOOK }}" }
```

- Up to **`MaxSteps = 20`** sequential steps (`id`, `name`, `if: success |
  failure | always`, `timeout`, and one of `uses`/`bash`/`worker`/`http`/`check`).
- **Parent/child runs (migration `0066_reusable_automations_and_pipelines.sql`):**
  Adds `template_id`, `template_commit_id`, `parent_run_id`, `step_index`,
  `step_id`, and `outputs` (`jsonb`) plus `trigger_runs_parent_idx` to
  `trigger_runs`. Top-level `ListAutomationRuns` excludes child runs; passing
  `parent_run_id` returns children ordered by `step_index ASC`.
- **Advancement (`internal/automations/pipeline.go`):** `Engine.runPipeline`
  starts step 0 as a normal child `AutomationRun`. Whenever a child finishes,
  `Engine.OnRunFinished` idempotently evaluates the next step's `if:` condition,
  interpolates `${{ inputs.<name> }}` and `${{ steps.<id>.outputs.<key> }}`
  into step `with:`, `bash.env`, `bash.run`, and `http` URL/headers, and
  enqueues the next child — or settles the parent as `succeeded`/`failed`.
  Canceling or timing out a parent cascades to all non-terminal children.
- **Named outputs (`SetRunOutput`):** Emitted via `await gs.setOutput(key, val)`
  in workers or `gs-auto set-output <key> <val>` in bash containers. Keys match
  `^[A-Za-z_][A-Za-z0-9_-]{0,63}$`; max 50 outputs/run, max 16 KiB/value;
  masked for all folder secrets in both `GsBridge` and the server.

## Trying Automations Before Landing

Two workflows allow testing draft automations without landing to `main`:

- **Draft cloud run (`--from-changeset`):**
  `gs auto run <id> --from-changeset <cs>` sets
  `RunAutomationRequest.definition_from_changeset = true`. The server reads the
  definition and scripts from the open changeset's current patchset
  (`RunSpec.definition_tree`). If the draft lists `secrets:` or `reveal:`, the
  caller must be an account admin.
- **Local worker run (`--local`):**
  `gs auto run <id> --local` runs a `worker` script from the local workspace
  under Node using `internal/cli/trigger_local_harness.mjs`, enforcing Gate's
  `network:` rules on `fetch`, with local `--secret NAME=value` and
  `--callback <file>` inputs.

## API, CLI & Code Layout

### Proto (`proto/core/v1/automation.proto`)

- **`AutomationService`** (user-facing): `GetFolderPolicy`, `ListAutomations`,
  `ListAutomationRuns`, `GetAutomationRun`, `ListAutomationRunLogs`,
  `RunAutomation`, `RerunAutomationRun`, `CancelAutomationRun`,
  `InspectAutomationTemplate`.
- **`AutomationRunService`** (dispatcher key or per-run token): `StartRun`,
  `StartCallback`, `FinishRun`, `AppendRunLog`, `GetRunState`, `SetRunState`,
  `SetRunDetailsUrl`, `SetRunOutput`, `CreateCheck`, `UpdateCheck`, `ReadFile`,
  `ListDirectory`, `GetChangeset`.

### CLI (`gs automation` / `gs auto`)

- `gs auto ls [folder]` — effective automations at a folder
- `gs auto runs [--changeset | --automation | --account]`
- `gs auto show <run>` / `gs auto logs <run>`
- `gs auto run <id> [--input k=v] [--follow] [--from-changeset <cs> | --local]`
- `gs auto rerun <run>` / `gs auto cancel <run>`
- `gs auto inspect <uses-ref> [--account <account>]`

### Code Layout

| Component | Location |
| --- | --- |
| Parser, resolver, inputs, templates, pipeline, engine, tokens | `internal/automations/` (`parse.go`, `resolve.go`, `changes.go`, `actions.go`, `inputs.go`, `modules.go`, `templates.go`, `pipeline.go`, `engine.go`, `tokens.go`) |
| Service handlers & submit gate | `service/automation.go`, `automation_run.go`, `automation_run_checks.go`, `automation_archive.go`, `automation_submit.go`, `automation_templates.go` |
| Event bus & outbound SSRF/HMAC | `internal/events/`, `internal/outbound/` |
| Storage & migrations | `internal/postgres/migrations/0048_triggers.sql`, `0064_automation_run_attribution.sql`, `0066_reusable_automations_and_pipelines.sql` |
| Cloudflare dispatcher & containers | `trigger-dispatch/src/` (`index.ts`, `entry.ts`, `gate.ts`, `crypto.ts`, `mask.ts`, `api.ts`, `bash_runner.ts`, `bash_config.ts`), `trigger-dispatch/containers/` |
| Container init & helper CLI | `cmd/gs-exec/` |
| Local worker test harness | `internal/cli/trigger_local_harness.mjs` |
