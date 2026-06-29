# 17. Continuous Integration (agent-run checks)

## Goal

Run automated checks (build / test / lint / anything scriptable) against every
new patchset and gate submit on the results — **without standing up separate CI
infrastructure**. The checks are executed by a **Gitslice agent daemon** (the
BYOA daemon from [16_bring_your_own_agent.md](16_bring_your_own_agent.md)) on a
real machine, and the verdict is reported back to the server and surfaced on the
changeset/patchset UI.

The design reuses three things that already exist and adds an executor (the
agent) plus a thin gate-verification path around them:

- the **submit gate** (`SubmitRequirements.required_checks`, `check_results`,
  `EvaluateSubmitRequirements`) — already enforced at submit time today;
- the **agent daemon + hub** (`AgentService.Connect`, the `DaemonMessage` /
  `ServerMessage` multiplex, durable-event resend) — already runs user code in a
  per-conversation workspace and reports back over one outbound stream;
- the **patchset result tree** (`patchsets.result_tree_id`) — already the full
  repository tree with the slice's edits overlaid.

## What already exists (the foundation)

The check *gate* is built; only the *runner* is missing.

- **Slice policy.** `SubmitRequirements.required_checks []string` is part of the
  slice definition (`slices.required_checks`, folded into `definitionHash` and
  versioned in `slice_definition_versions`). It names the checks that must pass
  before a changeset can submit.
- **Per-patchset results.** `check_results(changeset_id, patchset_id,
  check_name, status, reported_by, …)` (migration `0007`) records one row per
  `(changeset_id, patchset_id, check_name)`. Results are **keyed to a specific
  patchset**, so a new patchset automatically invalidates prior passes — exactly
  the semantics CI needs.
- **Reporting RPC.** `ChangesetService.ReportCheckResult(changeset_id,
  check_name, status)` upserts the current patchset's result;
  `storage.NormalizeCheckStatus` constrains status to `pass` | `fail`. CLI today:
  `gs cs check <changeset> <check> --status pass|fail` (manual).
- **Gate evaluation.** `storage.EvaluateSubmitRequirements` blocks submit until
  every required check is `pass` for the **current** patchset, with explicit
  messages ("required check %q has no result for current patchset", etc.).
- **Whole-repo tree per patchset.** In `changeset_store.go`
  (`baseTreeForPatchsetTx`, `ApplyEdits`), a patchset's `result_tree_id =
  ApplyEdits(base_tree_id, edits)`, where `base_tree_id` is a full **root** tree —
  derived from the base commit's root tree, or, for a patchset stacked on another,
  from that base patchset's `result_tree_id`. Either way `result_tree_id` is the
  entire repository as of that patchset, not just the slice subtree.

What's missing: nothing *runs* the checks. CI fills that gap by making the agent
the runner. `check_runs` (new) is the execution record; on completion it upserts
`check_results` (existing), so the submit gate is untouched.

## Execution paths

**Trust model: all registrants are trusted.** A subject who can register a daemon
or author a slice is trusted to run and report checks honestly. We therefore do
**not** treat self-reported results as an attack surface, and the gate does not
distinguish "who ran it" for correctness — any `pass` for the current patchset
satisfies a required check, exactly as today. This collapses what would otherwise
be an elaborate provenance/verification layer. (If an untrusted-author model is
ever needed, see Future work; it requires strong daemon identity and protected
check definitions that the current code does not yet provide.)

Given that, there are two execution paths, split purely by **materialization
access** — what files the runner physically has — not by trust.

### Default: agent-bundled (the fast path)

The authoring agent already has the slice materialized and already runs `gs cs
capture` after each turn. So it also runs the **in-slice** applicable checks right
there (in their declared container) and **uploads the results together with the
patchset** (`bundled_check_runs` on `UpdateChangeset`, recorded atomically with
the new patchset). The server upserts `check_results`. No server plan, no
`RunChecks` round trip, no re-materialization. The patchset lands green/red.

### Full-tree runner: for checks the agent can't materialize

Ancestor and out-of-slice `include` checks need files beyond the slice that the
authoring agent does not have checked out. Those route to a **full-tree runner**
— a daemon designated on the slice (`slices.ci_daemon_id`) that materializes the
wider tree from `result_tree_id`. This is a **capability** role (it has the
files), not a trust boundary. A slice only needs one if its plan contains checks
that escape the slice; a slice whose checks are all in-slice never uses it.

| check kind | who runs it | gates submit if required |
|---|---|---|
| **in-slice** | authoring agent, bundled at capture | yes |
| **ancestor / out-of-slice `include`** | the slice's full-tree runner (`ci_daemon_id`) | yes (requires a runner to exist) |
| **advisory (non-required)** | whichever runner has the files | shown, never blocks |

`provenance` (`self` | `ci`) is still **recorded** on each run as informational
metadata — useful in the UI and for debugging "which machine produced this" — but
it does not change gate evaluation.

## Decisions (v1)

- **Definitions live in committed, folder-scoped files**, not in server config
  and not as tree-node metadata (tree nodes stay pure content). A check is
  defined in `<dir>/.gitslice/checks.yaml`. This makes check definitions
  content-addressed, versioned with the code, and naturally scoped to the
  folders they guard.
- **The cascade ceiling is the repository root.** Ancestor checks apply to
  sub-slice changes: for each path a patchset touches, every `checks.yaml` from
  that path up to the repo root contributes. It is the repository owner's
  responsibility to decide what belongs in higher-level (e.g. root) check files,
  knowing they run for descendant changes.
- **Trusted registrants.** Anyone who can register a daemon or author a slice is
  trusted. The gate does not distinguish who produced a result; a `pass` for the
  current patchset satisfies a required check, unchanged from today. No provenance
  verification, no independent re-run for trust reasons.
- **Agent-bundled by default; full-tree runner only for files the agent lacks.**
  The authoring agent runs **in-slice** checks at capture and **uploads results
  with the patchset** (`bundled_check_runs` on `UpdateChangeset`). A slice
  designates a **full-tree runner** (`slices.ci_daemon_id`) only to run ancestor /
  out-of-slice `include` checks, which need files beyond the slice. That split is
  about **materialization access**, not trust.
- **Trigger: every new patchset.** Patchsets are created through
  `ChangesetService.UpdateChangeset` (agent auto-capture and human `gs cs update`
  alike). The agent attaches its in-slice results to that call; the server
  dispatches `RunChecks` to the full-tree runner only for checks the agent
  couldn't materialize.
- **Containerized execution for reproducibility.** A check may declare an `image`
  so its result doesn't depend on the runner's local toolchain, and so a full-tree
  runner can serve many languages without installing each. Host/native execution
  (no image) is allowed — registrants are trusted — but containers are recommended
  for reproducibility and get basic hygiene (rootless, resource limits, no daemon
  socket).
- **Surfacing: a dedicated check-run model with live logs**, not reused
  conversation events. `check_runs` + `check_run_logs`, streamed to the web UI on
  the patchset/changeset page.

## Check definition files

### Location & discovery

One file per folder: `<dir>/.gitslice/checks.yaml`. The server discovers the
applicable set by walking, for each directory the patchset touched, the ancestor
spine up to the repo root over `result_tree_id`, reading any `checks.yaml` found.

This is an ancestor-spine read over an immutable, content-addressed tree, not a
full-repo traversal: cost ≈ `#touched_dirs × depth`, and a cache keyed by
tree-node / blob object id hits across every patchset that didn't edit that file
(identical subtrees share an id). Plan fragments can be memoized by node id.

### Format

```yaml
# backend/.gitslice/checks.yaml
version: 1

defaults:                          # optional; applies to checks in THIS file only
  image: "golang:1.22@sha256:..."  # pin by digest for hermeticity
  timeout: "10m"

checks:
  test:                            # local name (key) — [a-zA-Z0-9_-]+
    description: "Go unit tests"   # optional, UI only
    run: "go test ./..."           # required; executed via `sh -c`
    paths: ["**/*.go"]             # optional; globs, folder-relative or "/"-absolute
    include:                       # optional; extra paths to materialize (see below)
      - "/go.mod"                  #   leading "/" = repo-root-relative
      - "/go.sum"
    working_dir: "."               # optional; folder-relative or "/"-absolute, default "."
    env:                           # optional; layered onto the run environment
      CGO_ENABLED: "0"

  lint:
    image: "golangci/golangci-lint:v1.59"  # per-check override of defaults.image
    run: "golangci-lint run"

  smoke:
    run: "./scripts/smoke.sh"
    network: true                  # opt out of default-deny networking
```

| Field | Req | Meaning |
|---|---|---|
| `version` | yes | Format version; `1`. Parser rejects unknown majors. |
| `defaults` | no | File-local defaults (`image`, `timeout`, `env`, `network`). Does **not** cascade to descendant folders. |
| `checks` | yes | Map of **local name → spec**. |
| `run` | yes | Command, executed via the daemon's shell. |
| `image` | no | Container image. Present ⇒ containerized; absent ⇒ host exec (subject to daemon policy). |
| `paths` | no | Trigger globs, folder-relative or `/`-absolute. Check runs only if the patchset diff touches ≥1 match; empty ⇒ any change under the folder. |
| `include` | no | Extra paths added to the materialization beyond the defining folder (folder-relative or `/`-absolute). See **Materialization scope** below. |
| `working_dir` | no | cwd for `run`, folder-relative or `/`-absolute. Default `.` (the defining folder). |
| `timeout` | no | Per-check wall-clock cap (Go duration); server clamps to a max. |
| `env` | no | Extra env vars for the command. |
| `network` | no | Default `false` (deny). `true` allows network (e.g. dependency fetch). |

### Identity (what makes the cascade collision-free)

A check's stable id — the string stored in `check_results.check_name` and
referenced by `slice.required_checks` — is **`<defining_dir>/<local_name>`**,
`defining_dir` relative to the repo root:

- root `.gitslice/checks.yaml` → `test`, `lint`
- `backend/.gitslice/checks.yaml` → `backend/test`, `backend/lint`

Local names match `[a-zA-Z0-9_-]+` (no slashes), so the segment after the final
slash is unambiguously the name. A `test` at root and a `test` in `backend/` are
`test` and `backend/test`: distinct ids, both run when in scope, neither shadows
the other. The **effective plan is the union** along the spine — there is no
override semantics to reason about. A backend slice might set
`required_checks: ["backend/test", "test"]` to gate on both suites.

### What lives outside the file (intentionally)

- **Required vs advisory** is `slice.required_checks` (server policy), never the
  file. Defining a check makes it *available*; it gates submit only if listed.
- **Path matching** is computed server-side: globs match the diff's changed
  paths after making them relative to the defining folder.
- **Execution root** (the subtree to materialize) defaults to the defining
  folder's subtree of `result_tree_id`; `working_dir` resolves inside it. See
  Materialization scope for how `include` widens it.

### Materialization scope (three scopes, not one)

The design separates three things that the defining folder collapses by default:

- **Trigger scope** — what changes run the check: the defining folder, narrowed
  by `paths`.
- **Materialization scope** — what must be on disk to run it: the defining folder
  subtree **∪ every `include` prefix**.
- **Working dir** — the command's cwd: `working_dir`, default the defining folder.

The default (defining folder for all three) handles the common case: put a check
at the altitude that encloses its dependencies — a Go module's CI belongs in the
folder holding `go.mod`, so the whole module materializes and imports resolve.

When raising the altitude would over-broaden (e.g. you only need the root
`go.mod` plus a sibling package, not every sibling), `include` is the escape
hatch. Materialization becomes a **sparse view of `result_tree_id`**: the union
of the defining folder and each `include` prefix, **laid out at their true
repo-relative paths** so imports and relative references resolve (`/go.mod` lands
above `/backend/shared`, not flattened). A whole-repo check is just the
degenerate `include: ["/"]`.

`include` only affects *materialization*, never the *trigger*. To re-run a check
when a dependency folder changes, add that path to `paths` (which accepts
`/`-absolute globs). Auto-deriving the trigger from dependencies is out of scope
for v1 (too language-specific to infer reliably).

Path syntax: an entry with a leading `/` is repo-root-relative; otherwise it is
relative to the defining folder. `..` ascent is rejected. The server resolves and
validates every `paths` / `include` / `working_dir` (reusing `internal/paths`
canonicalization), requiring each to resolve to an existing node within the tree
and rejecting anything escaping it.

**Logical vs canonical paths.** Inside `checks.yaml` paths are written
**relative to the slice's repository root** (`/`, `backend/test`). Gitslice's
stored paths are **account-rooted** (`/<account>/...`), and `result_tree_id` is a
root tree in that namespace. The resolver maps a logical repo-root path to its
canonical account-rooted path before walking the tree; qualified check ids and the
cascade ceiling ("repo root") are all expressed in the logical, account-relative
space. This mapping must be defined precisely in the parser (it is the one place
the doc's `/`-rooted examples meet the account-rooted store).

## Plan computation

The same resolution runs in two places, but they see different inputs. The
**authoring agent** computes a plan from its **slice workspace** — necessarily a
*local subset*, since it can only see in-slice `checks.yaml` — to decide what to
run and bundle. The **server** computes the **complete** plan from the full
`result_tree_id` + `slice.required_checks`; this is the authoritative set, and it
is the only one that sees ancestor / out-of-slice checks. So the agent's plan is
advisory and partial by construction (not because it's untrusted — registrants are
trusted — but because it lacks the files); the server's plan determines what still
needs a full-tree run and what the gate expects.

On a new patchset the (server) plan is computed as:

1. Load the patchset's `result_tree_id` and `changed_paths`.
2. For each changed path, walk its ancestor spine to repo root, reading
   `checks.yaml` (memoized by node id). Union the discovered checks by their
   qualified ids.
3. Drop any check whose `paths` filter matches none of `changed_paths`
   (status `skipped`).
4. Resolve each remaining check's `image`/`env`/`timeout`/`working_dir` (per-check
   over `defaults`) and its **materialization set** — the defining folder ∪ the
   resolved `include` prefixes (deduped; nested prefixes collapsed).
5. For every id in `slice.required_checks` that the plan did **not** produce a
   runnable definition for, emit a synthetic `errored` run ("required check %q
   has no definition in this revision") so the gate fails loudly rather than
   silently passing.

The plan is a list of `{ qualified_name, command, image, working_dir, env,
network, timeout, materialize_paths }` to execute, each backed by a `check_runs`
row.

## Data model

New migration `0018_check_runs.sql`:

- `check_runs(id pk, changeset_id, patchset_id, check_name, daemon_id,
  provenance, attempt, superseded_by_run_id, status, exit_code, summary,
  started_at, finished_at, duration_ms, created_at, updated_at)`
  - `status ∈ queued | running | passed | failed | errored | skipped | canceled`.
  - `provenance ∈ self | ci` — informational only (which runner produced it); does
    **not** affect the gate.
  - **Attempts are explicit.** A rerun inserts a new row with `attempt+1` and sets
    the prior row's `superseded_by_run_id`. The "current" run for a
    `(patchset_id, check_name)` is the non-superseded one; there is no uniqueness on
    `(patchset, check)` itself, so the row-per-attempt and the "latest wins" lookup
    don't conflict.
- `check_run_logs(id pk, run_id fk, seq, stream, chunk, created_at,
  unique(run_id, seq))`
  - `stream ∈ stdout | stderr`. Append-only; replayable by `after_seq` — the same
    shape as `agent_conversation_events`.

`check_runs` is the execution record. On terminal status it **upserts
`check_results`** (`passed`→`pass`, `failed`/`errored`→`fail`). The existing gate
(`EvaluateSubmitRequirements`, `check_results`) is **unchanged** — trusted
registrants mean any `pass` for the current patchset satisfies a required check.

**`skipped` is handled outside `check_results`.** The existing
`NormalizeCheckStatus` accepts only `pass`/`fail`, and a missing row blocks. A
required check that resolved to `skipped` (its `paths` matched nothing) is *not*
written as a fake `pass`; instead the gate-evaluation step, which already loads the
patchset's plan, supplies skipped-required checks as satisfied when building the
`checkStatuses` map passed to `EvaluateSubmitRequirements`. So the on-disk status
vocabulary stays `pass`/`fail`, and "skipped = nothing to check" is computed from
the plan, not persisted as a result.

Slice gains the full-tree-runner pointer (extend `0018` or the slice schema):

- `slices.ci_daemon_id` (nullable fk to `agent_daemons`) — the daemon used to run
  ancestor / out-of-slice checks that the authoring agent can't materialize. A
  capability pointer, not a trust switch. Set via a new RPC / CLI / web control.

## Proto / RPC surface

Extend `AgentService` (`proto/core/v1/agent.proto`). New oneof arms on the
existing `Connect` multiplex, plus web-facing read RPCs.

**Bundled path (default).** The agent uploads self-run results *with* the
patchset by extending the capture call — no `Connect` message needed.
`UpdateChangesetRequest` gains a repeated `bundled_check_runs`, recorded
atomically with the new patchset (provenance `self`):

```proto
message BundledCheckRun {
  string name      = 1;   // qualified id, e.g. "backend/test"
  string status    = 2;   // passed | failed | errored | skipped
  int32  exit_code = 3;
  string summary   = 4;
  string log       = 5;   // captured output (or a blob ref for large logs)
}
// UpdateChangesetRequest { … existing …; repeated BundledCheckRun bundled_check_runs = N; }
```

Self-run logs ride along with capture; live tailing of a self-run during the turn
(optional) is surfaced through the existing conversation event stream, since it is
the agent's own turn. The trusted-re-run messages below are only for the CI-daemon
path.

Server → daemon (`ServerMessage`):

```proto
message ServerMessage {
  oneof payload {
    // … existing arms …
    RunChecks run_checks = 9;     // ancestor / out-of-slice checks only
    CancelCheckRun cancel_check = 10;
    CheckRunAck check_ack = 11;
  }
}

message RunChecks {
  string run_batch_id = 1;
  string changeset_id = 2;
  string patchset_id  = 3;
  SliceRef slice      = 4;
  string slice_id     = 5;
  string server_addr  = 6;     // where to materialize trees from
  string result_tree_id = 7;   // the patchset's full-repo tree
  repeated CheckSpec checks = 8;
}

message CheckSpec {
  string run_id       = 1;     // pre-created check_runs row id
  string name         = 2;     // qualified id, e.g. "backend/test"
  string command      = 3;
  string image        = 4;     // empty ⇒ host exec (subject to daemon policy)
  string working_dir  = 5;     // repo-relative path; cwd inside the materialized view
  repeated string materialize_paths = 6; // repo-relative prefixes of result_tree_id
                                          // to materialize (defining folder ∪ include)
  map<string,string> env = 7;
  bool   network      = 8;
  int64  timeout_ms   = 9;
}

message CancelCheckRun { string run_id = 1; }
```

Daemon → server (`DaemonMessage`):

```proto
message DaemonMessage {
  oneof payload {
    // … existing arms …
    CheckRunUpdate check_update = 5;
  }
}

message CheckRunUpdate {
  string run_id     = 1;
  string status     = 2;       // running | passed | failed | errored | canceled
  int32  exit_code  = 3;
  string log_chunk  = 4;       // optional incremental log
  string stream     = 5;       // stdout | stderr
  int64  client_seq = 6;       // per-run monotonic; dedup on (run_id, client_seq)
  bool   final      = 7;
  string summary    = 8;       // short human summary on terminal
}

// Server → daemon ack. EventAck is conversation-shaped (it carries
// conversation_id and dedups on (conversation_id, client_seq)), so check runs need
// their own ack rather than reusing it.
message CheckRunAck {
  string run_id           = 1;
  int64  acked_client_seq = 2;
}
// (CheckRunAck rides in ServerMessage alongside RunChecks / CancelCheckRun.)
```

`CheckRunUpdate` follows the **same `client_seq` resend pattern** PR #224 built for
`AgentEvent` — buffer unacked updates, resend on reconnect — but with a dedicated
`CheckRunAck` and dedup on `(run_id, client_seq)`, so check logs survive
Cloudflare's ~125s `Connect` resets without loss or duplication.

`RegisterDaemon` gains capability advertisement so the server routes correctly:

```proto
message RegisterDaemon {
  string name = 1; string runtime = 2; string version = 3;
  repeated string container_runtimes = 4;  // e.g. ["docker"], ["podman"], []
  bool allow_host_exec = 5;                // willing to run non-imaged checks natively
}
```

Web (ConnectRPC, unary + one server-stream):

- `ListCheckRuns(ListCheckRunsRequest{changeset_id|patchset_id}) → ListCheckRunsResponse`
- `GetCheckRun(GetCheckRunRequest{run_id}) → CheckRun`
- `StreamCheckRun(StreamCheckRunRequest{run_id, after_seq}) → stream CheckRunLog`
- `RerunCheck(RerunCheckRequest{run_id}) → CheckRun`
- `SetSliceCIDaemon(SetSliceCIDaemonRequest{slice, daemon_id}) → Slice` (or fold
  into the slice update RPC)

## Server hub (`service/agent.go`, `service/agent_hub.go`)

The hub already maps `daemon_id → daemonConn` and fans out durable events. CI
adds a parallel topic:

- `run_id → set of StreamCheckRun subscriber channels`.

Flow:

1. **Patchset created with bundled results** (`UpdateChangeset` carrying
   `bundled_check_runs`) → record `check_runs` (provenance `self`, terminal) +
   their logs → upsert `check_results`. The patchset is green/red on arrival.
2. **Out-of-slice dispatch** → the server computes the complete plan from
   `result_tree_id` + `slice.required_checks` and finds the checks the agent
   couldn't materialize (ancestor / out-of-slice `include`). If the slice has a
   full-tree runner (`ci_daemon_id`), it inserts `ci`-provenance `check_runs`
   (`queued`) for those and pushes `RunChecks` to it. If there are none, nothing is
   dispatched — the bundled results already cover the plan.
3. **`CheckRunUpdate`** from the runner → assign seq, persist log chunk to
   `check_run_logs`, update the `check_runs` row, send `CheckRunAck`, fan out to
   live `StreamCheckRun` subscribers. The server validates the update's `run_id`
   belongs to the connected daemon and is non-terminal. On terminal, upsert
   `check_results`.
4. **`StreamCheckRun{run_id, after_seq}`** → replay persisted logs `seq >
   after_seq` then tail live — identical pattern to `StreamConversation`.
5. **Full-tree runner (re)register** → after the existing
   `replayDaemonConversations`, replay **queued/running `check_runs`** routable to
   this daemon via a new `replayDaemonCheckRuns`, so runs dispatched while offline
   are picked up. Mirror the `ReconcileWorkspaces` idea if stale materializations
   need reaping.

The hub stays a stateless relay plus the Postgres log; correctness comes from the
persisted `check_runs` / `check_run_logs` and `after_seq` replay.

## Runner selection

The authoring agent runs everything it *can* (in-slice checks) and bundles them.
The server routes only what the agent couldn't materialize:

```
per check on a new patchset:
  materialize set outside the slice (ancestor / out-of-slice include)?
      ─▶ run on slice.ci_daemon_id (only runner with the files)
          · no ci_daemon_id set ─▶ errored: "needs a full-tree runner"
          · runner offline       ─▶ queued; replay on (re)register
  else (in-slice)
      ─▶ already covered by the agent's bundled result
```

Routing is purely about **which runner has the files**, not trust. A check whose
materialization stays in-slice never leaves the agent; one that escapes the slice
can only run on the full-tree runner. Gating on out-of-slice checks therefore
requires designating one — otherwise those checks `error` and the gate stays
unsatisfied (a misconfiguration, surfaced clearly).

The server will not route a containerized check to a daemon whose
`container_runtimes` is empty, nor a host-exec (no-image) check to a daemon with
`allow_host_exec = false`; such runs go `errored` with a clear message.

## Execution model (daemon)

Both runners execute the same way; they differ only in where the working tree
comes from. The **authoring agent** (bundled path) already has the slice
materialized in its conversation workspace and runs in place. The **CI daemon**
(re-run path) materializes per `CheckSpec` in a `RunChecks`:

1. **Materialize** `materialize_paths` of `result_tree_id` into an ephemeral
   directory (read from `server_addr`) as a **sparse view**: the union of the
   listed prefixes, each placed at its true repo-relative path so imports and
   relative refs resolve. This is **not** a slice workspace — it is a read-context
   checkout keyed by tree id, with the slice's edits already baked into
   `result_tree_id`.

   `materialize_paths` is the check's **defining folder plus its `include`
   prefixes**, not the repo root: a `backend/` check with no `include`
   materializes only `backend/`. The whole tree is materialized **only** for a
   check defined at the root (e.g. a root `go test ./...`, which needs the whole
   module to compile `./...`) or one that explicitly lists `include: ["/"]` — and
   only when its `paths` filter matched.

   Materialization is **incremental and content-addressed**, never a fresh clone
   per run:
   - The daemon keeps a persistent checkout from the previous patchset (or base).
     A new `result_tree_id` differs only in the patchset's `changed_paths`, so
     materializing it is "reuse the cached tree, fetch only changed objects,
     overlay them." Unchanged subtree node-ids are cache hits (the same R2
     object-cache win the import path relies on). Cold daemon = full fetch once;
     steady state = a small delta.
   - Materialize **once per distinct `(result_tree_id, materialize set)`** and
     share across every check with that set (and mount the same dir into each
     container), rather than copying per check.

   So a whole-repo root test needs all files *present*, but "present" is satisfied
   from cache + a changed-objects delta, not a repo re-download.
2. **Execute `run`:**
   - **Containerized** (`image` set): run inside `image`, mounting the materialized
     view (writable overlay) as the working tree; `working_dir` resolves inside it.
     Pin by digest for reproducibility; network default-deny (`network: true` opts
     in). Basic hygiene (rootless, resource limits, no daemon socket) even though
     registrants are trusted. An optional persistent cache volume keyed per
     `(slice, check)` survives between runs (module/dependency caches).
   - **Host/native** (no `image`): run directly with `cwd` in the materialized
     dir, when the daemon set `allow_host_exec`. Fine in the trusted model; the
     tradeoff is reproducibility (depends on the runner's toolchain), not safety.
3. **Stream** stdout/stderr as `CheckRunUpdate{log_chunk, stream, client_seq}`;
   enforce `timeout_ms`.
4. **Report terminal:** exit 0 → `passed`, non-zero → `failed`, timeout/setup
   failure → `errored`; final `CheckRunUpdate{status, exit_code, summary,
   final}`. The server upserts `check_results`.

Containerization is invisible upstream: the server's plan, routing, persistence,
and the submit gate do not change with execution mode.

## Submit integration

**The existing gate is unchanged.** `check_runs` terminal status upserts
`check_results`; `SubmitChangeset` → `EvaluateSubmitRequirements` blocks until
every `required_check` is `pass` for the current patchset, with no provenance
filtering (trusted registrants). The only adjustment is feeding skipped-required
checks in as satisfied when assembling the `checkStatuses` map (see Data model),
since `skipped` is not a `check_results` status. Because results are per-patchset,
a new patchset re-opens the gate until its own runs complete.

## Lifecycle & edge cases

- **Superseding:** a newer patchset cancels in-flight runs for older patchsets
  (`CancelCheckRun`, status `canceled`). Old `check_results` already don't gate
  the current patchset — cancellation just saves work.
- **Daemon dies mid-run:** the run is marked `errored`/stale and re-dispatched on
  reconnect via `replayDaemonCheckRuns`.
- **Skipped + required:** a *required* check whose `paths` matched nothing is
  `skipped` and treated as satisfied (nothing it guards changed). A required
  check with **no definition** in the revision is `errored` and blocks (see Plan
  step 5).
- **Missing/invalid `checks.yaml`:** a parse error makes every check that file
  would define `errored` with the parse message; unrelated checks are unaffected.
- **Concurrency:** the daemon bounds parallel check execution; excess runs wait
  `queued`.

## Trust & security model

The operating assumption is **trusted registrants**: a subject who can register a
daemon or author a slice is trusted to run and report checks honestly. That
deliberately removes the hardest problems (forged results, weakened definitions,
daemon-identity spoofing) from v1 scope. The remaining items are about access and
hygiene, not defending against the author:

- **Self-reported results gate.** The agent bundles its in-slice results and they
  satisfy the gate directly; no independent re-run for trust. `provenance` is kept
  only as informational metadata.
- **Materialization access, not authz, is the real boundary.** A sub-slice agent
  can't run ancestor/out-of-slice checks simply because its workspace doesn't
  contain those files; the full-tree runner can. Note this is *not* currently
  enforced by repository authz — repo reads authorize at the **account** level when
  no `SliceRef` is supplied — so before relying on it, materialization needs a
  **scoped endpoint/token** that serves only the requested subtrees of a given
  `result_tree_id`. (Tracked as a prerequisite for the full-tree runner.)
- **Containers are for reproducibility + multi-language convenience**, not an
  isolation boundary here. Still apply basic hygiene (rootless, resource limits, no
  daemon socket, network default-deny). Host exec is allowed.
- **Log visibility is a product decision.** Out-of-slice check logs can surface
  file contents the sub-slice author can't otherwise read; default such logs to
  owner-visible and make per-check disclosure a setting, rather than leaning on
  "the owner's responsibility."
- **Update authenticity (cheap and worth keeping):** the server accepts a
  `CheckRunUpdate` only from the daemon that owns that `run_id`, and only while the
  run is non-terminal and its patchset is current — even trusted daemons shouldn't
  cross-write each other's runs.

## Phasing

Ordered so the default (agent-bundled) path ships first and delivers value before
the full-tree runner exists.

1. **Definitions + plan computation.** `checks.yaml` parser, ancestor-spine walk
   over `result_tree_id`, scope/path-filter/`include` resolver, qualified-id model,
   logical→canonical path mapping. Pure, unit-testable.
2. **Data model + web read.** `0018_check_runs.sql` (with `attempt` /
   `superseded_by_run_id`, informational `provenance`), `check_runs` upsert into the
   **unchanged** `check_results`, `ListCheckRuns` / `GetCheckRun` / `StreamCheckRun`.
3. **Agent-bundled execution (the default path).** Agent runs in-slice checks at
   capture (host first, then container), `bundled_check_runs` on `UpdateChangeset`.
   End-to-end CI for the common case with no orchestration; gate unchanged. Plus
   the skipped-required-as-satisfied wiring in gate evaluation.
4. **Full-tree runner (out-of-slice checks).** `RunChecks` / `CheckRunUpdate` /
   `CheckRunAck` proto, scoped subtree materialization from `result_tree_id`,
   `slices.ci_daemon_id`, `replayDaemonCheckRuns`, superseding / cancel,
   `run_id`↔daemon update authenticity. Needs the scoped materialization endpoint.
5. **Web UI.** Checks panel on the patchset/changeset page: per-check status
   (with `self` vs `ci` provenance), live log tail, rerun, and the slice CI-daemon
   control.

Phases 1–3 are a self-contained, useful product (agent-run CI, in-slice checks,
gate unchanged). Phase 4 adds out-of-slice/ancestor checks via the full-tree
runner; phase 5 is UI.

## Future work

- **Untrusted-author model.** If slices ever accept code from authors who are not
  trusted, the gate needs what trusted-registrants lets us skip: strong daemon
  identity (dedicated credentials, no `account+name` takeover), server-assigned
  provenance, required-check definitions read from owner-protected paths or the
  base tree rather than author-edited result content, and a real `self`-vs-verified
  distinction. Deliberately out of v1.
- Container image allow-listing / registry policy per account.
- A managed CI daemon pool (Gitslice-hosted) as an alternative to BYOA runners.
- Check matrices, `needs`/DAG ordering, and fan-out parallelism across daemons.
- Secrets injection for checks that need credentials (scoped, server-held); until
  then v1 checks are hermetic / no-secret.
- Incremental/affected-only test selection driven by `read_set` / `write_set`.
- Lazily-projected working tree (FUSE over the tree) so a check faults in only
  the files it opens — meaningful for targeted tests, marginal for whole-module
  compiles that read everything.
