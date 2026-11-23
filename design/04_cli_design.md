# Gitslice CLI Design

This document defines the native `gs` command-line experience and the backend
capabilities it depends on.

Related documents:

- [00_product.md](00_product.md): product overview and primary workflows
- [01_gitslice_architecture_design.md](01_gitslice_architecture_design.md): top-level architecture
- [03_core_api.md](03_core_api.md): gRPC APIs used by the CLI
- [05_git_compatibility.md](05_git_compatibility.md): Git gateway and projection
- [07_conflict_resolution.md](07_conflict_resolution.md): path-level conflicts and batched submit
- [13_file_identity_and_move_history.md](13_file_identity_and_move_history.md): entity identity and move-following history
- [15_stacked_changesets.md](15_stacked_changesets.md): dependent changesets workflow
- [25_feedback.md](25_feedback.md): user feedback loop
- [27_blame.md](27_blame.md): line-level blame
- [28_benchmarks.md](28_benchmarks.md): stat cache and batched blob uploads
- [29_slice_lifecycle.md](29_slice_lifecycle.md): slice rename, archive, and delete
- [30_lazy_workspaces.md](30_lazy_workspaces.md): FUSE-backed lazy workspaces

## 1. Positioning

`gs` is the primary Gitslice CLI.

The CLI is Gitslice-native, not a thin wrapper around Git. Gitslice has
first-class concepts that Git does not own:

- account-rooted global paths
- slices (`account:slice`, with `account/slice` accepted as an alias)
- changesets and patchsets
- server-side submit validation, required checks, and approvals
- Git projection as a boundary compatibility layer
- lazy FUSE workspaces with local checkpoints, rewind, and fork
- conflict state as explicit patchset data

## 2. Core UX Rules

1. The user works in a sparse Gitslice workspace (full checkout or lazy FUSE mount), not a full global checkout.
2. Each workspace is bound to exactly one slice.
3. The bound workspace slice is the authoring slice for changesets created from that workspace.
4. If local edits are not fully contained by the bound slice, the CLI stops and asks the user to move that work to another workspace.
5. The CLI does not expose direct user-facing commit creation.
6. The CLI does not expose a Git-style staging area.
7. Submitted work is authoritative only after server validation and target-ref CAS.

## 3. Command Tree

The implemented CLI (`internal/cli/cli.go`, `commands_*.go`) organizes commands into a Git-familiar top-level workflow plus Gitslice-native resource groups:

### 3.1 Workspace, Changeset, and History Workflow (Top-Level)

```text
gs init <slice|account:slice> [dir] [--lazy] [--read-only] [--prefetch <glob>]
gs status [--local] [--relative]                  # alias: st
gs diff [commit] [commit] [--from ps --to ps] [--name-only] [--stat] [-U n]
gs sync [--merge line|manual|ours|theirs]
gs create -m <title> [--description ...] [--base cs] [--root] [--sibling] [--all] [--submit] [--if-changed|--allow-empty] [--yes]
gs modify [--all] [-m <title>] [--no-update-dependents] [--submit] [--watch-timeout 15m]
gs submit [changeset] [--with-dependencies] [--subtree <cs>] [--no-watch] [--watch-timeout 15m]
gs deps [dependency-tree]
gs update-dependents [changeset] [--children|--all]
gs switch <changeset>
gs up [changeset]
gs down [steps]
gs top
gs bottom
gs move <changeset> --onto <base|root> [--order n]
gs insert --base <changeset> --message <title>
gs detach <changeset> [--message <title>]
gs log [-- <path>] [--slice s] [--limit n] [--page-token t] [--follow|--no-follow-moves] [--all] [--full] [--oneline] [--name-only] [--stat] [-p]
gs show <commit-id-or-prefix|changeset> [--full] [--name-only] [--stat] [-p] [-U n]
gs blame <path> [--lines start,end] [--slice s]
gs ci                                              # run workspace checks locally
```

### 3.2 Lazy Workspaces & Local Cache

```text
gs mount <dir> | <slice> <dir> [--workspace id] [--foreground] [--read-only] [--prefetch <glob>]
gs mount list | status [dir|id] | discard <dir|id> --yes
gs unmount <dir|id>
gs fetch [path...] [--workspace id|dir]
gs restore <path>... | --all
gs checkpoint [-m <message>] | list
gs rewind [checkpoint]
gs fork <checkpoint> <dir>
gs cache [status | prune [--max SIZE] [--dry-run]]
```

### 3.3 Native Resource & Platform Commands

```text
gs auth ...            # login, status, token, logout, register-agent, keys, gen-service-key, mint-service-token
gs claims ...          # list, accept (co-ownership of registered agents)
gs account ...         # create-org, members, set-member, remove-member, invite, invitations, accept, decline, cancel-invite, use, current, profile, set-profile, ci-runners, set-ci-daemon
gs group ...           # create, list, show, delete, add, rm
gs slice ...           # alias: slices; create, list, info, paths, history, update, set-ci-daemon, secret, delete, rename, archive, unarchive, member, mirror
gs workspace ...       # alias: ws; hydrate, sync, delete (init is hidden compatibility)
gs cs ...              # alias: changeset; list, show, status, edit, collaborators, handoff, link, explain, versions/patchsets, approve, check, abandon (create/update/submit/diff also exist under cs)
gs fs ...              # alias: file; ls, cat, mkdir, touch, write, upload, mv, rm
gs shell               # interactive remote file shell
gs import <source>     # shallow or deep Git repository import into a mounted path
gs tag ...             # create, list (immutable slice tags)
gs intent ...          # declare, list (ls), clear (rm/delete) path editing intents
gs agent ...           # start, status, expose, unexpose, stop, rename, doctor, install, uninstall
gs check ...           # list, logs, rerun, allow (CI check runs)
gs policy [folder]     # inspect resolved .gitslice/policy.yaml rules
gs secret ...          # ls/list, create, update, rm/delete/remove, env
gs automation ...      # aliases: auto, automations; ls/list, runs, show, logs, run, rerun, cancel, inspect
gs mcp                 # stdio Model Context Protocol server
```

### 3.4 Diagnostics, Config, and System Commands

```text
gs context             # alias: ctx
gs config ...          # alias: cfg; list, get, set
gs alias ...           # list, set, delete/remove/rm
gs rpc ...             # list, call (unary and server-streaming NDJSON)
gs browse [target]     # open or print validated web UI links
gs doctor [slice]      # end-to-end environment, auth, workspace, and slice checks
gs upgrade             # self-update from gitslice.io releases
gs feedback ...        # submit, list, resolve product feedback
gs version             # CLI version and build metadata
gs schema              # machine-readable CLI schema for agents
gs completion <shell>  # bash, zsh, fish, powershell completion scripts
gs help [cmd|topic]    # command and topic help
```

Global persistent flags apply across commands:

- `-C, --directory <dir>`: run as if `gs` was started in `<dir>` (repeatable, like `git -C`)
- `--format text|json|medium`, `--json[=field,field]`, `--jq <expr>`, `--template <tmpl>`
- `--quiet`, `--non-interactive`, `--no-color`, `--verbose`, `--debug`, `--trace`, `--insecure`

End-to-end quickstart workflow:

```bash
gs auth login
gs init nico:home ./home
cd home
gs status
gs create -m "update notes"
gs diff
gs submit
```

## 4. Auth, Accounts, and Context

### 4.1 Auth Commands (`gs auth`)

```bash
gs auth login [--server <addr>] [--clerk | --clerk-token <JWT|->] [--web-url <url>]
gs auth register-agent --username <name> --email <owner-email> [--display-name <name>] [--server <addr>] [--web-url <url>]
gs auth web-login [--expires 24h] [--slice <account>/<slice>]... [--role reader|contributor|writer] [--path /...] [--open]
gs auth keys list | create | revoke <key-id> | use-key
gs auth gen-service-key [--out-dir <dir>]
gs auth mint-service-token --key <priv.pem> --subject <id> [--email <e>] [--issuer <i>] [--ttl 24h]
gs auth status
gs auth token
gs auth logout
```

`gs auth web-login` mints a short-lived, revocable child API key
(`purpose = "web"`, default `--expires 24h`) via `AuthService.CreateApiKey` and
prints a browser sign-in URL (`<web-url>/agent-login[?returnTo=<path>]#key=<secret>`)
on stdout, with session metadata and the `gs auth keys revoke <key-id>` command
on stderr. Because the credential rides in the URL fragment (`#key=...`), it is
never sent in HTTP requests or server logs, and `/agent-login` strips it from
browser history immediately before exchanging it for an `HttpOnly`
`gs_agent_session` cookie. Owners may pass `--agent <username>` to mint a
scoped web link for an agent they own; `--slice` and `--role` further attenuate
the browser session; `--open` launches the URL in the local browser; `--json`
returns `{account, agent_subject_id, key, expires_at, url, revoke_command}`.

- `gs auth login` authenticates via the browser-based Clerk device/callback flow by default (`--clerk`), or non-interactively with `--clerk-token` (passing `-` reads the JWT from stdin). Legacy dev-only `--dev-user` and `gs auth signup` commands have been removed.
- `gs auth register-agent` registers an autonomous agent with its own personal account, `<username>:home` slice, and long-lived API key without a browser. The key is saved directly to `~/.gitslice/config.json` and never printed. `--email` designates a human who can later claim co-ownership via `gs claims list` / `gs claims accept` or `<web-url>/claims`. Interrupted registrations resume cleanly on retry.
- `gs auth keys` manages scoped API keys (`list`, `create`, `revoke`, `use-key`).
- `gs auth gen-service-key` and `gs auth mint-service-token` generate Ed25519 keypairs and short-lived service JWTs for automated testing and service accounts without Clerk.
- `gs auth status` validates the saved credential against `AuthService.GetAuthStatus` and prints non-secret status (`signed_in`, `server_addr`, `subject_id`, `reason`). It never prints the token.
- `gs auth token` validates the saved credential with the server and prints the raw bearer token for scripts and Git credential helpers.
- `gs auth logout` clears saved credentials while preserving `server_addr` and local aliases.

Authenticated commands that fail with an unauthenticated or permission-denied error never print the token; they emit structured errors with actionable recovery hints pointing to `gs auth login`, `gs auth register-agent`, `/claims`, and `gs doctor`.

### 4.2 Context (`gs context`)

```bash
gs context
gs context --json
```

`gs context` reports the resolved `cwd`, config path, `server_addr`, auth state, nearest workspace (walking upward to `.gs/`), and active slice.

Slice resolution precedence for slice-aware commands:

```text
1. explicit command flag (--slice <slice|account:slice>)
2. current workspace slice (nearest ancestor containing .gs/slice.json)
3. active/personal default (<account>:home)
```

Slice references use `account:slice` as the canonical form (e.g. `acme:payment`, `nic:home`) and accept `account/slice` as a CLI alias. A bare `<slice>` slug expands to the active or signed-in account (e.g. `tools` -> `nic:tools`).

### 4.3 Config & Environment (`gs config`)

```bash
gs config list
gs config get server_addr
gs config get token_present
gs config set server_addr api.gitslice.io:443
```

`gs config` reads and updates non-secret local CLI configuration in `~/.gitslice/config.json`. `gs config get token` is rejected with a hint to use `gs auth token` or `gs auth status`.

Supported environment variables (`GS_*` preferred, `GITSLICE_*` compatibility aliases):

```text
GS_SERVER_ADDR / GITSLICE_GRPC_ADDR / GITSLICE_SERVER_ADDR   default: api.gitslice.io:443
GS_WEB_URL / GITSLICE_WEB_URL                                default: https://gitslice.io
GS_CLIENT_CACHE_DIR / GITSLICE_CLIENT_CACHE_DIR              global object cache root
GS_CACHE_MAX                                                 cache pruning limit (default 10G; "off" disables)
GS_TOKEN                                                     explicit bearer token override
GS_NO_UPDATE_CHECK=1                                         disable daily release notice
GS_DOWNLOAD_BASE                                             override release download base for gs upgrade
GS_READAHEAD=off                                             disable FUSE tree-walk readahead
GS_TRACE=<file>                                              append NDJSON RPC & MCP tool timing traces
NO_COLOR, TERM=dumb                                          disable color and terminal controls
```

### 4.4 Aliases (`gs alias`)

```bash
gs alias list
gs alias set mine "cs list --status open"
gs alias delete mine
```

User aliases expand the first command token once before Cobra flag parsing (no shell execution) and cannot shadow built-in commands or aliases.

## 5. Workspaces, Object Cache, and Upload Batching

### 5.1 Full Checkouts & Lazy Workspaces

```bash
gs init <slice|account:slice> [dir]
gs init --lazy <slice|account:slice> [dir] [--read-only] [--prefetch <glob>]
gs workspace hydrate <path> [path...]
gs workspace delete [dir] [--force]
gs sync [--merge line|manual|ours|theirs]
```

- **Full checkout (`gs init`)**: requires an empty target directory (creating `[dir]` if provided), binds the workspace to one slice, hydrates included paths through the global object cache, and preserves canonical account-rooted paths below the workspace root (e.g. `/nic/hello/readme.md` -> `nic/hello/readme.md`).
- **Lazy workspace (`gs init --lazy`)**: mounts the slice via FUSE on Linux and macOS (see [30_lazy_workspaces.md](30_lazy_workspaces.md)). Only the directory listing is fetched upfront; file contents are fetched on first read with tree-walk readahead and stored in the shared object cache. Local edits stay in the workspace's local overlay until `gs create` or `gs modify`. A single background supervisor (`gs mountd`) serves all lazy workspaces for the user and exits when the last is unmounted. `gs mount`, `gs unmount`, `gs fetch`, `gs restore`, `gs checkpoint`, `gs rewind`, and `gs fork` manage lazy mounts and local edit checkpoints.

Workspace metadata under `.gs/`:

```text
.gs/
  slice.json          # bound slice ref, slice_id, definition hash
  state.json          # base commit, active changeset/stack state
  base_snapshot.json  # hydrated file manifest for the base commit
  stat_cache.json     # path -> (size, mtime, mode, content_hash) to skip rehashing unchanged files
  conflicts.json      # present only while sync conflicts are unresolved
```

Auth credentials are never written under `.gs/`.

### 5.2 Global Object Cache & `UploadBlobs` Batching

All workspaces and lazy mounts for a local user share a content-addressed object cache (`~/.cache/gitslice/objects/sha256/<2>/<2>/<digest>`, overridable via `GS_CLIENT_CACHE_DIR`). `gs cache status` and `gs cache prune` inspect and prune LRU entries above `GS_CACHE_MAX` (default 10G), never evicting objects pinned by local checkpoints or unresolved sync conflicts or used within the last 6 hours.

When scanning and uploading workspace edits (`gs create`, `gs modify`, `gs fs upload`):

1. **Stat cache**: `.gs/stat_cache.json` records each path's size, mtime, mode, and SHA-256 content hash so unchanged files are not re-read on every scan (see [28_benchmarks.md](28_benchmarks.md)).
2. **Bounded hashing**: changed files are hashed with a bounded worker pool into the global object cache.
3. **Batch availability check**: `BlobService.GetBlobStatus` checks which content hashes the server already has for the slice.
4. **Batched & streaming upload** (`internal/cli/blob_upload.go`): missing small blobs are grouped into gzip-compressed `BlobService.UploadBlobs` batches (up to 256 blobs or 4 MiB per call) and uploaded concurrently (default `min(max(4, 2*NumCPU), 16)` workers). Blobs larger than the stream threshold use `BlobService.UploadBlobStream`. If a server returns `Unimplemented` for `UploadBlobs`, the CLI falls back transparently to individual `UploadBlob` calls.

### 5.3 Workspace Sync (`gs sync`)

`gs sync` advances the workspace to the latest accepted `refs/global/main` head (or restacks the workspace's dependency tree via `ChangesetStackService.Restack` when an active draft changeset is part of a dependency tree).

Sync compares base `B`, local `L`, and remote `R`, defaulting to `--merge line` (with `manual`, `ours`, and `theirs` also supported) and applying `.gitslice/merge.yaml` rules where configured (see [07_conflict_resolution.md](07_conflict_resolution.md#75-merge-rules-for-lockfiles-and-generated-files)):

- Non-conflicting remote updates are applied in place; non-conflicting local edits are preserved; non-overlapping text edits are three-way merged.
- Remaining conflicts are materialized (conflict markers for text files, preserved local content plus side metadata in `.gs/conflicts.json` for binary/delete-modify/directory conflicts) and `gs sync` exits with code `3`.
- When an active draft changeset exists, `gs sync` records a new sync/rebase patchset on the server (marked as a `conflict` patchset if unresolved conflicts remain). After resolving conflicts locally, `gs modify` creates a clean patchset and clears `.gs/conflicts.json`.

## 6. Slice Commands (`gs slice`)

```bash
gs slice create <slice|account:slice> [--include /account/path] [--visibility private|public] \
  [--required-approvals n] [--required-human-approvals n] [--required-check name] \
  [--approvers @account/group] [--description text]
gs slice list [account] [--shared] [--archived]
gs slice info <slice|account:slice>
gs slice paths <slice|account:slice>
gs slice history <slice|account:slice> [--page-size n]
gs slice update <slice|account:slice> [--include ...] [--visibility private|public] \
  [--required-approvals n] [--required-human-approvals n] [--required-check name] \
  [--clear-required-checks] [--approvers @account/group] [--description text] \
  [--merge-on-submit on|off]
gs slice set-ci-daemon <slice|account:slice> <daemon-id> [--add|--remove]
gs slice secret set|rm|ls <slice|account:slice> ...
gs slice member ...
gs slice mirror ...
gs slice rename <slice|account:slice> <new-name>
gs slice archive <slice|account:slice>
gs slice unarchive <slice|account:slice>
gs slice delete <slice|account:slice> --yes
```

- Visibility is `private` (default) or `public`.
- If `--include` is omitted on create, it defaults to `/<account>/<slice>` (or `/<account>` for the reserved `home` slice).
- `gs slice lifecycle` operations (`rename`, `archive`, `unarchive`, `delete`) are specified in [29_slice_lifecycle.md](29_slice_lifecycle.md); outbound Git mirrors (`gs slice mirror`) are in [35_git_mirrors.md](35_git_mirrors.md).
- `gs webhook` was removed on 2026-11-17; outbound HTTP notifications are handled by `.gitslice/automations.yaml` (`gs automation`).

## 7. Changeset & Dependency Commands

Top-level verbs are the primary day-to-day workflow (`gs create`, `gs modify`, `gs submit`, `gs deps`, `gs update-dependents`, `gs switch`, `gs up`, `gs down`, `gs top`, `gs bottom`, `gs move`, `gs insert`, `gs detach`), complemented by `gs cs` subcommands for inspection, editing metadata, collaboration, approval, and administration:

```bash
gs cs list [--slice <slice>] [--status <status>] [--limit n]
gs cs show [changeset]
gs cs status [changeset] [--watch] [--watch-timeout 5m]
gs cs link [changeset]
gs cs explain [changeset]
gs cs versions [changeset]           # alias: patchsets
gs cs edit [changeset] [--title <title>] [--description <text> | --description-file <path>]
gs cs approve <changeset>
gs cs check <changeset> <check-name> --status pass|fail
gs cs collaborators ...
gs cs handoff ...
gs cs abandon [changeset] [--reason <reason>]
```

(`gs cs create`, `gs cs update`, `gs cs submit`, and `gs cs diff` also remain available under `gs cs`.)

### 7.1 Changeset Display IDs (`ShortChangesetID`)

User-facing text output, web links, and Git push messages identify changesets by their **10-character hex prefix** (`model.ShortChangesetID`, i.e. the first 10 hex characters after `cs_`, such as `97048df407`). Patchsets are displayed as `<short-id>.<number>` (e.g. `97048df407.2`) or by patchset number within a changeset (`1`, `2 current`).

- Canonical `cs_...` and `ps_...` IDs remain API/storage identifiers and are accepted anywhere a changeset or patchset selector is expected.
- Every command that creates or lands a changeset prints the short changeset ID and its web link (`view: https://gitslice.io/cs/<short-id>`). When a commit is created, the commit ID is printed separately and labelled as a commit, not a changeset ID.
- `gs cs link` and server changeset lookups also resolve a landed commit's ID (full or 12-char prefix) to the changeset that landed it.

### 7.2 Create, Modify, and Submit Behavior

- **No-op protection**: with no workspace edits, `gs create` (and `gs cs create`) fails with `nothing_to_change`. Pass `--if-changed` to exit 0 as a no-op (useful in scheduled jobs) or `--allow-empty` to create an empty changeset explicitly.
- **One-step `--submit`**: `gs create --submit` and `gs modify --submit` wait for required checks that have not yet reported (polling every 5s up to `--watch-timeout`, default `15m`), submit, and wait for publish. Non-transient blockers (failing checks, missing approvals) return immediately with exit code `1` (`submit_blocked`) while leaving the changeset open.
- **Waiting for publish**: `gs submit` waits until the changeset reaches `submitted` status by default; `--no-watch` returns immediately after submit admission (`pending_publish`), and `gs cs status --watch` resumes waiting.
- **Dependent changesets**: see [15_stacked_changesets.md](15_stacked_changesets.md) for the full base/dependent workflow (`--base`, `--root`, `--sibling`, `gs deps`, `gs update-dependents`, `gs move`, `gs insert`, `gs detach`).

## 8. Remote Files (`gs fs`, `gs shell`) & Import (`gs import`)

### 8.1 `gs fs`

```bash
gs fs ls [remote-path]
gs fs cat <absolute-path>
gs fs mkdir <absolute-path>
gs fs touch <absolute-path>
gs fs write <absolute-path> (--text <text> | --stdin)
gs fs upload <local-path> <absolute-remote-path> [--recursive] [--concurrency n]
gs fs mv <absolute-old-path> <absolute-new-path>
gs fs rm <absolute-path>
```

`gs fs` operates on absolute canonical paths in the signed-in user's personal home slice. Each mutation creates and submits a changeset immediately; `--no-submit` leaves the changeset open for review. Deleting the last file under a directory also removes the now-empty directory and any emptied ancestors.

### 8.2 `gs shell`

```bash
gs shell [--slice <slice|account:slice>] [--commit <commit-id>]
```

Interactive shell for inspecting and making single-file edits against server-side files (`pwd`, `ls`, `cd`, `cat`, `stat`, `mkdir`, `write`, `touch`, `mv`, `rm`, `ref`, `help`, `exit`/`quit`). Runs without a local workspace, defaults to the workspace slice or personal home slice, and is read-only when pinned with `--commit`.

### 8.3 `gs import`

```bash
gs import <source> --mount /account/path [--slice <slice>] [--mode shallow|deep] [--deep] [--max-commits n] [--resume] [--retries n]
```

Server-side Git repository import (`shallow` imports `HEAD` as one commit; `deep` replays commits chronologically using tree diffs). Accepts URLs, SSH sources, local paths, or GitHub `owner/repo` shorthand. Streams progress to stderr in text mode and supports automatic reconnect/resume (`--retries`).

## 9. History, Blame, and Diff (`gs log`, `gs show`, `gs diff`, `gs blame`)

```bash
gs log [-- <path>] [--slice <slice>] [--limit 20] [--page-token <t>] [--follow|--no-follow-moves] [--all] [--full] [--oneline] [--name-only] [--stat] [-p]
gs show <commit-id-or-prefix|changeset> [--full] [--name-only] [--stat] [-p] [-U n]
gs diff [commit] [commit] [--from <ps> --to <ps>] [--name-only] [--stat] [-U n]
gs blame <path> [--lines start,end] [--slice <slice>]
```

- Native commit IDs (`sha256:<hex>`) are displayed as 12-char hex prefixes by default (`--full` prints canonical IDs). Prefixes of at least 8 hex characters are resolved server-side within the caller's authorized scope.
- `gs log` defaults to the workspace slice (or personal home slice outside a workspace; `--all` lists all readable history). For a single path, move-following is on by default (`--no-follow-moves` requests literal path history). Each commit entry names the changeset (`ShortChangesetID`) that landed it.
- `gs show` accepts either a commit ID/prefix or a changeset ID/handle (falling back to changeset lookup if no commit matches or the ID starts with `cs_`).
- `gs diff` with no arguments diffs local workspace edits against the base snapshot; with `--from`/`--to` diffs server-side patchsets; with 1 or 2 commit args diffs a commit against its parent or two commits against each other.
- `gs blame` returns line-level commit and changeset attribution following moves (see [27_blame.md](27_blame.md)).

## 10. Diagnostic, Integration, and System Commands

### 10.1 RPC Escape Hatch (`gs rpc`)

```bash
gs rpc list
gs rpc call <service>/<method> ['{...}' | --request '{...}'] [--server <addr>] [--unauthenticated]
```

`gs rpc` uses protobuf descriptors linked into the CLI to list and invoke generated core gRPC methods with protojson request bodies. `gs rpc call` supports both **unary** and **server-streaming** RPCs: a server-streaming RPC prints one JSON object per line (NDJSON) as messages arrive. Client-streaming and bidirectional-streaming RPCs remain out of scope for the generic escape hatch.

### 10.2 Browser Handoff (`gs browse`)

```bash
gs browse [account:slice | /account/path | workspace/path | web-page] [--path <path>] [--web-url <url>] [--print]
```

`gs browse` resolves a target (current workspace slice, `account:slice`, canonical `/account/path`, workspace-relative path, or web page such as `cs/<id>`, `slices/...`, `doc/...`, `claims`, `changesets`) and opens it in the browser (or prints the URL with `--print`). `--path` opens a specific file or directory inside a slice.

When signed in, `gs browse` validates targets against the server before opening or printing:

- Slice and `--path` targets are checked for existence; private slices emit a warning on stderr noting that only signed-in members can open the link.
- `cs/<id>` targets verify the changeset exists and resolve landed commit IDs to the corresponding changeset URL.

### 10.3 MCP Server (`gs mcp`)

```bash
gs mcp [--slice account:slice]
```

Stdio Model Context Protocol server (JSON-RPC 2.0, protocol versions `2025-06-18`, `2025-03-26`, `2024-11-05`) exposing `read_file`, `list_directory`, `search`, `history`, `blame`, `create_changeset`, `get_changeset`, and `submit_changeset` so MCP-capable agents can read, search, and edit slices without a local workspace. `search` uses batched `ReadFiles` through the local object cache.

### 10.4 Doctor (`gs doctor`), Upgrade (`gs upgrade`), Version, Feedback, Completion

- `gs doctor [account:slice] [--json]`: checks version, config, server gRPC health & latency, auth token kind/validity, workspace conflict/staleness state, FUSE availability, and slice visibility/access. Exits `1` if any check fails.
- `gs upgrade [--check] [--version <tag>] [--force] [--path <gs>]`: downloads release archives from `https://gitslice.io/releases/`, verifies SHA-256 checksums, smoke-tests `gs version --json`, and atomically swaps the binary. Daily background release checks print a one-line notice on stderr when a newer version is available (skipped for `--json`, `--quiet`, source builds, and `GS_NO_UPDATE_CHECK=1`).
- `gs version [--json]`: prints version, commit, build date, Go version, and dirty state.
- `gs feedback [message] | list | resolve`: submits and tracks product feedback (see [25_feedback.md](25_feedback.md)).
- `gs completion bash|zsh|fish|powershell`: Cobra shell completion with dynamic server-backed completion (3s timeout) for changeset and slice arguments.
- `gs schema`: emits the full machine-readable command and output schema for agents.

## 11. Output Formatting, Help Topics, and Exit Codes

- Text output is human-oriented; `--json` / `--format json` emits stable JSON on stdout while progress, warnings, and errors go to stderr.
- `--json=field,field` projects top-level fields; `--jq <expr>` filters JSON; `--template <tmpl>` formats with Go `text/template` (including a `json` helper).
- Long operations show live progress on stderr (rewriting a line on TTYs, printing every 5s on non-TTYs; suppressed by `--json` and `--quiet`).
- `gs help` provides topic pages: `environment`, `formatting`, `exit-codes`, `paths`, `slices`.

Stable exit codes:

```text
0  success
1  general command failure
2  command canceled
3  gs sync left files with unresolved conflict markers
4  authentication missing, invalid, or rejected by the server
```

## 12. Future Work

The following commands and capabilities are planned or deferred and are **not** part of the currently implemented CLI:

- **Local operation log & undo (`gs op log|show|undo|restore`)**: durable local operation log for full checkouts (lazy workspaces already provide local `gs checkpoint` / `gs rewind` / `gs fork`).
- **History-rewriting & conflict helpers (`gs split`, `gs squash`, `gs rebase`, `gs describe`, `gs resolve`, `gs diff --conflicts`)**: dedicated commands beyond `gs sync`, `gs modify`, `gs cs edit`, and dependency-tree operations.
- **Structured query language**: revset/fileset filter expressions such as `gs cs list -r 'touches(/acme/payment/**)'` or `gs diff -f '/acme/payment/**/*.go'`.
- **Workspace helpers**: `gs workspace status`, `gs workspace dehydrate`, `gs workspace root`.
- **Filesystem watcher & background pre-upload**: continuous daemon/CLI watching for pre-uploading blobs during edits (see `design/archive/18_agent_workspace_preupload.md`).
