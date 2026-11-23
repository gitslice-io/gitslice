# 17. Continuous Integration: Checks, Daemons & Cloudflare Dispatch

> Consolidates the original agent-run checks design, `31_ci_daemon.md`, and
> `32_ci_dispatch_on_cloudflare.md` (archived in `design/archive/`) into one
> current specification.

## Goal

Run automated checks (build, test, lint, e2e, or anything scriptable) against
every new patchset and gate submit on the results — using Gitslice's own agent
daemons ([16_bring_your_own_agent.md](16_bring_your_own_agent.md)) or
on-demand Cloudflare Containers, with zero idle cost on Cloud Run and Neon.

The system builds on three core primitives:

- **Submit gate.** `SubmitRequirements.required_checks`, `check_results`
  (migration `0007`), and `storage.EvaluateSubmitRequirements` block submit
  until every required check has status `pass` (or is `skipped` because no
  watched path changed) for the **current** patchset.
- **Patchset result tree.** Each patchset's `result_tree_id` is a full,
  content-addressed repository tree with the slice's edits overlaid.
- **Agent daemon & hub.** Daemons connect outbound over `AgentService.Connect`
  (`proto/core/v1/agent.proto`) with durable `client_seq` resend/ack, or wait
  in standby behind the Cloudflare doorbell.

## Execution Paths

Checks execute via two paths, split by **materialization access** and
configuration:

1. **Agent-bundled (in-slice fast path).** An authoring agent already has the
   slice materialized and runs in-slice checks during `gs cs capture`,
   uploading results atomically via `UpdateChangesetRequest.bundled_check_runs`
   (provenance `self`). `gs cs capture` skips checks the local machine cannot
   run (e.g. containerized checks without a container runtime) so the CI daemon
   can run them instead.
2. **CI daemon / runner pool (full-tree path).** Handles ancestor and
   out-of-slice `include` checks, any in-slice checks not bundled by an agent
   (e.g. `gs create`, `gs modify`, `git push`), and checks triggered by
   automations (`started_by: automation`, see
   [34_automations.md](34_automations.md)). Runs are recorded with provenance
   `ci`.

`provenance` (`self` | `ci` | `automation`) is recorded on each `check_runs`
row as informational metadata. On terminal status, `check_runs` upserts
`check_results` (`passed` → `pass`, `failed`/`errored` → `fail`). A required
check whose `paths` filter matched no changed files resolves to `skipped` and
is supplied as satisfied during gate evaluation without writing a fake `pass`
to `check_results`.

## Check Definition Files (`.gitslice/checks.yaml`)

### Location & Ancestor-Spine Discovery

Check definitions live in committed, folder-scoped files:
`<dir>/.gitslice/checks.yaml`. On a new patchset, the server discovers the
applicable set by walking the ancestor spine from each directory touched by
`changed_paths` up to the repository root over `result_tree_id`, memoizing
reads by tree-node/blob object ID.

### Format

```yaml
# backend/.gitslice/checks.yaml
version: 1

defaults:                          # optional; applies to checks in THIS file only
  image: "golang:1.25"             # or `build:` (mutually exclusive with `image`)
  timeout: "10m"
  started_by: patchset             # patchset (default) | automation

services:                          # optional; sidecar containers for checks in this file
  postgres:
    image: postgres:16-alpine
    env: { POSTGRES_HOST_AUTH_METHOD: trust }
    ready: "pg_isready -U postgres -h 127.0.0.1"
    ready_timeout: "60s"
    memory: "1g"

checks:
  test:                            # local name — [a-zA-Z0-9_-]+
    description: "Go unit tests"
    run: "go test ./..."
    paths: ["**/*.go"]
    include: ["/go.mod", "/go.sum"]
    working_dir: "."
    env: { CGO_ENABLED: "0" }
    cache: ["/go/pkg/mod"]

  e2e:
    build:
      dockerfile: ci/Dockerfile    # relative to context
      context: "."                 # default: defining folder; leading "/" = repo root
      inputs: [go.mod, go.sum]     # files/folders hashed into the image cache key
      timeout: "30m"
    services: [postgres]
    run: "go test -count=1 ./tests/cli ./tests/rpc"
    network: false
```

| Field | Req | Meaning |
| --- | --- | --- |
| `version` | yes | Format version (`1`). Unknown majors are rejected. |
| `defaults` | no | File-local defaults (`image`, `build`, `timeout`, `env`, `network`, `started_by`, `cache`, `setup`, `memory`, `cpus`). Does **not** cascade to descendant folders. |
| `services` | no | Map of service container specs (`image`, `env`, `ready`, `ready_timeout`, `memory`). |
| `checks` | yes | Map of **local name → spec**. |
| `run` | yes | Shell command executed via `sh -c`. |
| `image` | no | Container image. Present ⇒ containerized; absent (and no `build`) ⇒ host exec (requires daemon `allow_host_exec`). |
| `build` | no | Mutually exclusive with `image`. Builds an image from the tree (`dockerfile`, `context`, `inputs`, `timeout`) and reuses it until the Dockerfile or any input changes. |
| `services` | no | Names from the file's top-level `services` map. Requires a containerized check (`image` or `build`). |
| `paths` | no | Trigger globs, folder-relative or `/`-absolute. Check runs only if the diff touches ≥1 match; empty ⇒ any change under the folder. |
| `include` | no | Extra paths materialized alongside the defining folder (folder-relative or `/`-absolute). |
| `working_dir` | no | Command cwd, folder-relative or `/`-absolute (default `.`). |
| `timeout` | no | Per-check wall-clock cap (Go duration); clamped by server max. |
| `env` | no | Extra environment variables for the command. |
| `network` | no | Default `false` (deny). `true` enables outbound network access. |
| `started_by` | no | `patchset` (default): runs automatically on every new patchset. `automation` (legacy alias `trigger`): only an automation's `check` action starts it ([34_automations.md](34_automations.md)). |

### Qualified Check IDs & Scopes

A check's stable ID — stored in `check_results.check_name` and listed in
`slice.required_checks` — is **`<defining_dir>/<local_name>`** relative to the
account root (e.g. `test` at account root, `backend/test` in `backend/`).
Local names contain no slashes, so the union along the ancestor spine is
collision-free with no shadowing semantics.

Three scopes are separated cleanly:

- **Trigger scope:** the defining folder, narrowed by `paths` (supports
  `/`-absolute globs to watch dependencies).
- **Materialization scope:** the defining folder subtree ∪ every resolved
  `include` prefix ∪ `build` Dockerfile/inputs, materialized as a sparse view
  at true repo-relative paths.
- **Working dir:** `working_dir` resolved inside the materialized tree.

`..` path ascent is rejected; all paths are validated through `internal/paths`
and mapped between logical account-relative paths and canonical `/<account>/...`
storage paths.

### Plan Computation (`service/check_plan.go`, `internal/checks/`)

1. Load the patchset's `result_tree_id` and `changed_paths`.
2. Walk the ancestor spine of each changed path to the account root, reading
   `checks.yaml` (memoized by node ID) and taking the union by qualified ID.
3. Drop checks whose `paths` filter matches none of `changed_paths` (`skipped`).
4. Separate `started_by: patchset` (`plan.Runnable`) from
   `started_by: automation` (`plan.Triggered`).
5. Resolve per-check settings over `defaults`, collapse nested `include`
   prefixes, and compute `OutOfSlice` (whether materialization escapes the
   slice's included paths).
6. For any ID in `slice.required_checks` not reached by discovery, inspect the
   `checks.yaml` of the directory named by the ID. If the change is outside its
   trigger scope, treat it as `skipped` (satisfied); if the definition is
   missing or unparseable, emit a synthetic `errored` run so the gate fails
   loudly.

## Folder Secret Injection & Untrusted-Author Gating

Checks receive the folder-scoped secrets that apply at the defining folder of
their `checks.yaml` (`SecretValuesAt`, see [33_secrets.md](33_secrets.md)).
Both the daemon (`CheckRunSpec.secret_env`) and the server (`runSecretMasker`
in `service/check_secrets.go`) mask secret values with `***` in logs and
summaries (`internal/secretmask`).

Because draft patchsets can edit `.gitslice/checks.yaml` or run untrusted code,
secret injection is gated per patchset (`service/check_secrets.go`, migration
`0055_patchset_secret_allowances.sql`):

- **Patchsets touching `.gitslice/`:** Secrets are withheld unless the patchset
  author is an account admin, an account admin approved that specific patchset,
  or an account admin explicitly allowed secrets for that patchset.
- **All other patchsets:** Secrets are injected when the patchset author has
  `ActionUseSecrets` on the slice (writer+), or a member with `ActionUseSecrets`
  has approved or explicitly allowed secrets for that patchset. Outside
  contributors therefore run secretless CI until allowed.
- **Allowing secrets:** `CheckService.AllowPatchsetSecrets` (CLI:
  `gs check allow [changeset] [--patchset <id>]`) records an allowance row in
  `patchset_secret_allowances(changeset_id, patchset_id, allowed_by_subject_id,
  created_at)` and immediately reruns prior CI checks on that patchset with
  secrets enabled (`dispatchAllowedPatchsetChecks`).

## Data Model & Daemon Pools

| Table | Migration | Purpose |
| --- | --- | --- |
| `check_runs` | `0018`, `0048`, `0050` | Execution record per attempt (`attempt`, `superseded_by_run_id`, `provenance`, `status`, `exit_code`, `summary`, `claimed_by`, `lease_expires_at`, `automation_run_id`, `details_url`, `annotations`). |
| `check_run_logs` | `0018` | Append-only stdout/stderr chunks keyed by `(run_id, seq)`, replayable via `after_seq`. |
| `slice_ci_daemons` | `0050` | Ordered per-slice CI daemon pool `(slice_id, daemon_id, position)`. `slices.ci_daemon_id` mirrors the first member for compatibility. |
| `patchset_secret_allowances` | `0055` | Explicit per-patchset approval to inject folder secrets into CI checks. |
| `account_ci_daemons` | `0063` | Account-level CI daemon pool `(account_id, daemon_id, position)`; used by any slice in the account that does not specify its own `slice_ci_daemons` override. |

### Pool Routing, Claims & Leases

- **Daemon selection (`pickDaemon`):** Picks the first daemon in the effective
  pool (slice pool if configured, otherwise account pool) that is exposed to the
  slice and currently online (connected locally, online via `StandbyPoll` /
  doorbell `standby_until`, or configured as an on-demand runner in
  `GITSLICE_CI_RUNNER_DAEMONS`), falling back to the first exposed member.
- **Multi-process claims:** Each daemon process sends a random
  `RegisterDaemon.instance_id` and `check_concurrency`
  (`GITSLICE_CHECK_CONCURRENCY`). `dispatchQueued` claims queued runs up to each
  process's free slots using `FOR UPDATE SKIP LOCKED`, writing
  `check_runs.claimed_by` and a 90 s `lease_expires_at`. Leases renew on every
  15 s heartbeat and every `CheckRunUpdate`.
- **Recovery & failover (`service/check_recovery.go`):**
  - On process disconnect, queued claims are released immediately; running
    claims stay until their 90 s lease lapses.
  - The recovery sweep (every 30 s while active, gated on `internal/activity`)
    reclaims lapsed leases: queued runs are handed out again; running runs are
    canceled and superseded by a new attempt on the pool (up to 3 attempts, then
    `errored`).
  - Unclaimed queued runs pinned to a stale daemon (`> DaemonStaleAfter` and
    older than 2 minutes) are reassigned to the next reachable pool member
    (`ListStrandedCheckRuns` / `ReassignCheckRun`).
  - Newer patchsets cancel in-flight runs on older patchsets via
    `CancelCheckRun`.

## Proto / RPC Surface

### `CheckService` (`proto/core/v1/check.proto`)

- `ListCheckRuns(changeset_id, patchset_id)` — returns runs with
  `daemon_online`, `daemon_last_seen_at`, `automation_run_id`, `details_url`,
  and `annotations`.
- `GetCheckRun(run_id)` — returns a single `CheckRun`.
- `RerunCheck(run_id)` — supersedes a run with `attempt + 1` and queues it.
- `StreamCheckRun(run_id, after_seq)` — replays persisted `check_run_logs` and
  tails live chunks via `service/check_log_hub.go`.
- `AllowPatchsetSecrets(changeset_id, patchset_id?)` — records secret allowance
  and reruns CI checks with secrets.

### `AgentService` (`proto/core/v1/agent.proto`)

- `Connect` stream multiplex:
  - Server → daemon: `RunChecks` (carrying `repeated CheckRunSpec` with
    `command`, `image`, `CheckImageBuild build`, `repeated CheckServiceContainer
    services`, `working_dir`, `materialize_paths`, `env`, `secret_env`,
    `network`, `timeout_ms`, `setup`, `cache`, `memory`, `cpus`),
    `CancelCheckRun`, `CheckRunAck`.
  - Daemon → server: `RegisterDaemon` (advertising `instance_id`,
    `container_runtimes`, `allow_host_exec`, `check_concurrency`),
    `CheckRunUpdate` (with monotonic `client_seq` deduped on
    `(run_id, client_seq)`), `Detach`.
- `StandbyPoll(StandbyPollRequest) → StandbyPollResponse` — used by detached
  CI-only daemons; returns pending work flag, `doorbell_url`, `doorbell_token`,
  and `safety_poll_after_ms`.

### `SliceService` (`proto/core/v1/slice.proto`)

- `SetSliceCIDaemon(slice, daemon_id, add?, remove?)` — replaces, adds to, or
  removes from `slice_ci_daemons`.
- `GetAccountCIDaemons(account)` / `SetAccountCIDaemon(account, daemon_id,
  add?, remove?)` — manages the account-wide default CI daemon pool
  (`account_ci_daemons`).

## Server Code Layout (`service/check*.go`)

- `service/check.go` — `CheckService` RPC handlers (`ListCheckRuns`,
  `GetCheckRun`, `RerunCheck`, `StreamCheckRun`, `AllowPatchsetSecrets`).
- `service/check_plan.go` — ancestor-spine plan resolution and required-check
  evaluation.
- `service/check_dispatch.go` — `checkDispatcher`, pool selection
  (`pickDaemon`), claiming/hand-out (`handOut`, `dispatchQueued`), and doorbell
  ringing.
- `service/check_secrets.go` — folder secret lookup, `runSecretMasker`, and
  untrusted-author/draft `.gitslice/` gating (`patchsetApprovedForGitsliceSecrets`).
- `service/check_log_hub.go` — live `StreamCheckRun` subscriber fan-out.
- `service/check_recovery.go` — lease expiration sweep, stranded-run
  reassignment, and on-demand runner re-ringing.

## Execution Engine (`internal/checkexec/`) & Daemon (`internal/agentd/`)

### `internal/checkexec/`

- **Tree materialization (`workspace_stage.go`, `internal/agentd/checks_materialize.go`):**
  Incremental, content-addressed sparse checkout of `materialize_paths` from
  `result_tree_id`, shared across checks with the same `(result_tree_id,
  materialize_paths)`.
- **Image builds (`build.go`, `image_build.go`):** Hashes the Dockerfile and
  all declared `inputs` from the materialized tree into
  `gitslice-ci/build-<family>:<key>` (single-flight per daemon). Cache hits skip
  `docker build` completely; after a build, all but the 2 newest images in the
  family are pruned.
- **Services (`services.go`):** Starts declared service containers on a private
  run network (`--internal` unless `network: true`), polls each `ready` command
  until healthy, runs the check on the same network, and tears down services and
  network on exit.
- **User mapping & caches (`runtime.go`, `isolation.go`):** Detects rootful
  Docker (`--user uid:gid`), rootless Docker, or rootless Podman
  (`--userns=keep-id`) via `docker info`/`podman info`. `cache` paths bind-mount
  `$XDG_CACHE_HOME/gitslice/check-cache/<hash(slice, path)>` so checks in the
  same slice share build caches safely.
- **Host-network mode (`hostnet.go`):** When `GITSLICE_CHECK_HOST_NETWORK=1`
  (used in Cloudflare DinD containers without iptables), builds, networked
  checks, and services run on the host network with `--add-host <svc>:127.0.0.1`,
  while non-networked checks keep `--network=none`.

### `internal/agentd/` & CLI

- **`gs agent start --ci`:** Runs a checks-only daemon (runtime `ci`; server
  refuses conversations on it, no agent runtime or empty-dir requirement). Runs
  preflight checks on startup and only advertises a container runtime when
  `docker info`/`podman info` succeeds.
- **`gs agent doctor` (`internal/agentd/doctor.go`):** Checks auth status,
  container runtime reachability, rootful/rootless mode, free disk for check
  staging/caches, and host toolchains.
- **`gs agent install --ci --name <name>` (`internal/agentd/install.go`):**
  Installs a persistent user service (systemd user unit
  `gs-agent-<name>.service` on Linux, LaunchAgent
  `io.gitslice.agent.<name>.plist` on macOS; `--print` to preview;
  `gs agent uninstall <name>` to remove).
- **Offline visibility:** `gs create --submit` and `gs submit --watch` surface
  `daemon_online` and `daemon_last_seen_at` when waiting on a queued check whose
  daemon is offline.

## Idle Cost Control: Detach-and-Poll & Cloudflare Doorbell

Holding an open `Connect` stream on Cloud Run bills an instance continuously.
To let Cloud Run and Neon scale to zero when idle:

### 1. Detach-and-Poll (`internal/agentd/daemon_standby.go`)

A `--ci` daemon with no active runs or unacked updates for `--detach-after`
(default `2m`, `GITSLICE_AGENT_DETACH_AFTER`, `0` disables) probes
`StandbyPoll`, sends `Detach`, and closes `Connect` without going offline. If
no doorbell is configured (or `GITSLICE_AGENT_DOORBELL=off`), it polls
`StandbyPoll` every 30 s (`GITSLICE_AGENT_POLL_INTERVAL`) and reconnects when
runs are queued. A daemon counts as online if heard from within
`storage.DaemonStaleAfter` (90 s) or while its `standby_until` doorbell window
is active.

### 2. Cloudflare Doorbell (`doorbell/`, `internal/agentd/doorbell.go`)

- **Worker & `Bell` Durable Object:** Deployed at `doorbell.gitslice.io`. One
  `Bell` DO per daemon holds hibernating WebSockets (no duration charge while
  hibernating; 30 s `"ping"` frames are answered by the runtime's auto-response
  without waking the DO).
- **Direct `/ring` & HMAC auth:** When `handOut` leaves queued runs unclaimed,
  the Go server POSTs `/ring` directly to the Worker (up to 3 retries), signed
  with `HMAC-SHA256(GITSLICE_DOORBELL_SECRET, "ring", body)` and a timestamp
  (5 min skew window). `StandbyPollResponse` mints a 2-hour HMAC doorbell token
  (`gsdb1.<daemon>.<exp>.<hmac>`) and sets `safety_poll_after_ms` (1 hour) and
  `agent_daemons.standby_until` (+3 min grace). If a ring arrives while no
  socket is open, `Bell` retains a pending wake for 1 hour and the server clears
  `standby_until` so the daemon appears offline for failover.
- **Fallback:** If the WebSocket drops or Cloudflare is unreachable, the daemon
  automatically falls back to 30 s `StandbyPoll` intervals so no check is lost.

### 3. Cloudflare On-Demand Runners (`doorbell/runner/`)

The same Worker exposes a `Runner` Container class (`docker:28-dind` + `gs`,
instance type `standard-4`, capped at `MAX_RUNNERS = 4`). A `/ring` for
`RUNNER_DAEMON_ID` boots one ephemeral container per queued run up to free
slots. Each container starts `dockerd` without iptables, sets
`GITSLICE_CHECK_HOST_NETWORK=1`, and runs:

```sh
gs agent start --ci --name cf-ci --detach-after 0 --idle-timeout 90s
```

with `GITSLICE_CHECK_CONCURRENCY=1`. It claims and runs queued checks, exits
after 90 s idle (hard-stopped at 2 hours by the Worker), and leaves zero
running infrastructure when idle. The server treats daemons listed in
`GITSLICE_CI_RUNNER_DAEMONS` as always reachable and re-rings them from the
recovery sweep while queued runs remain.

### 4. Server Activity Gating (`internal/activity`)

Cloud Run throttles CPU between requests without stopping goroutines. All
background server loops (publisher, index worker, event/automation dispatcher,
check recovery sweep, blame warmer) wait on `internal/activity` and pause after
`activity.IdleAfter` (5 minutes) with no active HTTP requests, resuming on the
next request so Neon scales to zero. Agent `Connect` streams are exempt from
the activity gate (`activity.DaemonConnectPath`).

## Future Work

- **Container snapshots & image registries:** Restoring pre-warmed Cloudflare
  Container snapshots (or pushing `build:` images to a shared registry) to
  eliminate cold build-cache starts on ephemeral `Runner` containers.
- **Queue-backed doorbell buffering / asymmetric tokens:** Adding Cloudflare
  Queues or Ed25519 doorbell tokens if multi-tenant or burst scale ever requires
  decoupling `/ring` from direct Worker calls.
- **Container image policy:** Account-level allow-lists for `image:` / `build:`
  base images.
- **DAGs & matrices:** Check matrices, `needs:` ordering, and affected-only
  selection driven by `read_set` / `write_set`.
