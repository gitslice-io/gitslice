# Gitslice CLI Design

This document defines the native `gs` command-line experience and the backend
capabilities it depends on.

Related documents:

- [00_product.md](00_product.md): product overview and primary workflows
- [01_gitslice_architecture_design.md](01_gitslice_architecture_design.md): top-level architecture
- [03_core_api.md](03_core_api.md): gRPC APIs used by the CLI
- [05_git_compatibility.md](05_git_compatibility.md): Git gateway and compatibility workflows
- [07_conflict_resolution.md](07_conflict_resolution.md): path-level conflicts and batched submit
- [08_mvp_implementation.md](08_mvp_implementation.md): Go MVP implementation shape and test harness
- [09_execution_plan.md](09_execution_plan.md): rollout phases

## 1. Positioning

`gs` is the primary Gitslice CLI.

The CLI should be Gitslice-native, not a thin wrapper around Git. Gitslice has
first-class concepts that Git does not own:

- account-rooted global paths
- slices
- changesets and patchsets
- server-side submit validation
- Git projection as a compatibility layer
- local operation log and undo
- draft patchset snapshots
- conflict state as explicit patchset data

## 2. Core UX Rules

1. The user works in a sparse Gitslice workspace, not a full global checkout.
2. Each workspace is bound to exactly one slice.
3. The bound workspace slice is the authoring slice for changesets created from
   that workspace.
4. If local edits are not fully contained by the bound slice, the CLI must stop
   and ask the user to move that work to another workspace.
5. The CLI does not expose direct user-facing commit creation.
6. The CLI does not expose a Git-style staging area.
7. Local workspace actions are recorded in a local operation log.
8. Submitted work is authoritative only after server validation and ref CAS.

## 3. Command Groups

Initial command groups:

```text
gs auth ...
gs context
gs config ...
gs alias ...
gs rpc ...
gs browse
gs help ...
gs workspace ...
gs slice ...
gs status
gs diff
gs fs ...
gs cs ...
gs repo ...
gs commit ...
gs shell
gs version
gs op ...
gs log ...
gs config ...
```

Later command groups:

```text
gs split
gs squash
gs rebase
gs describe
gs resolve
```

The later commands should be added when the underlying changeset and patchset
model can represent the operation cleanly.

Command names should stay stable, explicit, and discoverable. The CLI may also
offer short aliases for commands that are frequently typed or whose noun has a
natural singular/plural variant:

```text
gs workspace ...  alias: gs ws ...
gs status         alias: gs st
gs context        alias: gs ctx
gs config ...     alias: gs cfg ...
gs slice ...      alias: gs slices ...
gs repo ...       alias: gs repository ...
gs commit ...     alias: gs commits ...
gs cs ...         alias: gs changeset ...
gs fs ...         compatibility alias: gs file ...
```

Help and schema output should advertise the canonical command first and list
aliases as secondary metadata. Root help should include a compact, end-to-end
workflow example so a new user can move from signup to shell, file upload,
workspace initialization, status, changeset creation, diff, and submit:

```bash
gs auth signup --username nic
gs shell
gs fs upload ./notes /nic/notes --recursive
gs workspace init nic/home
gs status
gs cs create --title "update notes"
gs cs diff
gs cs submit
```

## 3.1 Auth Commands

```bash
gs auth login --server <grpc-addr> --dev-user <name>
gs auth signup --username <name>
gs auth status
gs auth token
gs auth logout
```

`gs auth signup --username <name>` starts a fake browser-approved device flow
for local development. The CLI starts a temporary localhost callback listener,
opens or prints the static web approval URL, waits for the browser callback, and
stores the returned bearer token in the user config. The approval page calls the
generated HTTP JSON grpc-gateway endpoint for
`FakeAccountService.ApproveSignup`; there is no custom signup HTTP handler in
the Go server. Approval creates the personal account and its default
`<name>/home` slice. The web page is a prototype approval screen, not a real
identity-provider login.

`gs auth status` reads the locally configured server and bearer token, validates
that token with the server's authenticated auth-status RPC, and never prints the
saved bearer token. JSON output should expose only stable, non-secret fields:

```json
{
  "signed_in": true,
  "server_addr": "127.0.0.1:50051",
  "subject_id": "user_alice"
}
```

If the local config is missing, incomplete, or the server rejects the token, the
command reports `"signed_in": false` and may include a non-secret `reason`.

`gs auth token` is the explicit secret-bearing auth command for scripts and
Git-compatible flows that need a bearer token. It reads the saved token, first
validates it with `AuthService.GetAuthStatus`, and prints the raw token only
when the server accepts it. JSON/template output is supported for automation,
but users should prefer `gs auth status` when they only need non-secret auth
state.

`gs auth logout` clears the saved bearer token and subject id without contacting
the server. It preserves non-secret local configuration such as `server_addr`
and user-defined aliases, so the next login can reuse the same server and local
shortcuts.

Authenticated commands that receive an unauthenticated RPC error should not leak
or print the saved bearer token. They should turn the raw RPC failure into a
stable CLI error with an actionable recovery hint:

```text
authentication failed: invalid token
hint: Run gs auth status to inspect the saved token. If it is invalid, run gs auth signup --username nic.
```

The username-specific hint is derived from local non-secret subject metadata
when available, for example `user_nic` -> `nic`.

## 3.2 Context Command

```bash
gs context
gs context --json
```

`gs context` explains what local and server context the CLI resolved before a
workflow runs. It is modeled after `gh`'s repository-context behavior: the CLI
uses the current directory and saved configuration by default, while explicit
flags on individual commands can override those defaults.

Context resolution precedence for slice-aware commands should be:

```text
1. command flag such as --slice <slice|account/slice>
2. current workspace slice found by walking up to .gs/slice.json
3. future GS_SLICE environment override
4. signed-in user's personal <account>/home slice
```

Commands that accept a slice reference should accept both the canonical
`<account>/<slice>` form and a bare `<slice>` slug. The bare form is CLI-only
sugar and expands to the signed-in account, for example `--slice tools` becomes
`nic/tools` for user `nic`. If the CLI cannot determine a signed-in account,
bare slice refs are rejected with a hint to pass `account/slice`.

The command should be diagnostic and scriptable. It validates the saved auth
token when possible, reports invalid-token and unavailable-server state without
printing secrets, and reports the nearest workspace even when invoked from a
subdirectory:

```json
{
  "cwd": "/work/checkout/src",
  "config_path": "/home/alice/.gitslice/config.json",
  "server_addr": "127.0.0.1:50051",
  "signed_in": true,
  "subject_id": "user_alice",
  "workspace": {
    "root": "/work/checkout",
    "ref": "alice/home",
    "slice_id": "slice_alice_home",
    "base_commit_id": "sha256:..."
  },
  "active_slice": "alice/home",
  "active_slice_source": "workspace"
}
```

## 3.3 Config Commands And Environment

```bash
gs config list
gs config get server_addr
gs config get token_present
gs config set server_addr 127.0.0.1:50051
```

`gs config` exposes local CLI configuration without exposing secrets. It can
print the config path, saved server address, saved subject id, and whether a
token is present. It must never print the bearer token; `gs config get token`
is rejected with a hint to use `gs auth token` when a script truly needs the
secret, or `gs auth status` for non-secret auth state.

The initial mutable key is `server_addr`. Authentication-owned fields such as
`subject_id` and token state are read-only and are updated by `gs auth login` or
`gs auth signup`.

The CLI supports short `GS_*` environment aliases while preserving existing
`GITSLICE_*` names for compatibility:

```text
GS_SERVER_ADDR          preferred server default
GITSLICE_GRPC_ADDR      compatibility server default
GITSLICE_SERVER_ADDR    compatibility server default
GS_WEB_URL              preferred web UI URL default
GITSLICE_WEB_URL        compatibility web UI URL default
GS_GATEWAY_URL          preferred signup gateway URL default
GITSLICE_GATEWAY_URL    compatibility signup gateway URL default
GS_HTTP_ADDR            compatibility gateway address source
GITSLICE_HTTP_ADDR      compatibility gateway address source
GS_CLIENT_CACHE_DIR     preferred client object cache root override
GITSLICE_CLIENT_CACHE_DIR compatibility cache root override
NO_COLOR                disable color
TERM=dumb               disable color and sticky shell headers
```

## 3.4 User Aliases

```bash
gs alias list
gs alias set mine "cs list --status draft"
gs alias delete mine
```

`gs alias` is modeled after `gh alias`: it stores local, user-controlled command
shortcuts in the CLI config file. Alias expansion is intentionally command-only,
not shell execution. An alias replaces the top-level command token once and then
the normal Cobra command parser handles flags, validation, output format, and
errors. For example, `gs mine --json` can expand to
`gs cs list --status draft --json`.

Alias names must use letters, numbers, dash, or underscore, cannot start with a
dash, and cannot shadow built-in commands or built-in command aliases. Auth
commands preserve configured aliases when they update the saved server/token
state.

## 3.5 RPC Escape Hatch

```bash
gs rpc list
gs rpc call AuthService/GetAuthStatus --request '{}'
gs rpc call gitslice.core.v1.AuthService/GetAuthStatus --request '{}' --json=subject_id
gs rpc call FakeAccountService/Login --request '{"dev_user":"alice"}' --unauthenticated
```

`gs rpc` is a diagnostic escape hatch, similar in spirit to `gh api`. It should
not replace dedicated product commands, but it gives developers a way to inspect
and exercise generated core RPCs while iterating on the CLI and server.

The first version uses generated protobuf descriptors linked into the CLI rather
than server reflection. `gs rpc list` lists generated core RPC methods.
`gs rpc call` supports unary RPCs only, accepts protojson request bodies, and
prints protojson responses. By default it uses the saved server and bearer
token; `--server` overrides the server address and `--unauthenticated` omits the
saved bearer token for development-only unauthenticated methods such as fake
login/signup.

Streaming RPCs remain out of scope for the generic escape hatch and should use
dedicated commands where progress and cancellation semantics are explicit.

## 3.6 Browser Handoff

```bash
gs browse
gs browse signup
gs browse source/nic/notes?ref=main --print
```

`gs browse` is a small browser handoff modeled after `gh browse`. It builds a
URL under the configured web UI base and opens it in the local browser, or
prints it with `--print` for scripts and terminals where browser launch is not
wanted. The command does not add server APIs and does not imply every designed
web route is already implemented; it is only a stable way for the CLI to hand
the user to the web surface.

The default base URL comes from `GS_WEB_URL`, then `GITSLICE_WEB_URL`, then the
local development default.

## 3.7 Version Command

```bash
gs version
gs version --json=version,commit,go_version,dirty
```

`gs version` prints CLI version and build metadata for bug reports, CI logs, and
local environment inspection. Text output is for humans; JSON output exposes the
stable fields `version`, `commit`, `build_date`, `go_version`, and `dirty`.
Release builds can inject version metadata with Go linker flags, while local
builds fall back to Go build-info VCS settings when available.

## 3.8 Shell Completion

```bash
gs completion bash
gs completion zsh
gs completion fish
gs completion powershell
```

`gs completion <shell>` emits shell completion scripts generated by Cobra. The
command is part of the documented CLI surface and appears in `gs schema` so
automation and help renderers can discover it without scraping root help text.

## 4. Workspace Commands

```bash
gs workspace init <slice|account/slice>
gs workspace init <username>/home
gs workspace status
gs workspace sync
gs workspace hydrate <path>
gs workspace dehydrate <path>
gs workspace root
```

`gs workspace init <slice|account/slice>` creates local workspace metadata:

```text
.gs/
  config.json
  slice.json
  state.json
  cache/
  overlay/
  op_log/
  draft_patchsets/
```

The command must run in an empty directory. If the current directory already
contains files, directories, Git metadata, or another `.gs` workspace, the CLI
rejects initialization and asks the user to create a new empty directory. This
prevents hydration from mixing server state with unrelated local files.

After initialization, workspace-aware commands discover the workspace by walking
up from the current directory to the nearest parent containing `.gs`. Commands
such as `gs status`, `gs cs create`, `gs cs submit`, `gs cs status`,
`gs workspace hydrate`, and workspace-default `gs shell` can run from any
subdirectory inside the workspace. Local scans and metadata writes still operate
against the workspace root.

The workspace stores:

- one slice binding
- hydrated file cache
- local overlay changes
- draft patchset snapshots
- local operation log
- server metadata cache

Files and directories for the bound slice are hydrated during workspace
initialization using canonical account-rooted local paths. For example, a file
at `/nic/hello/readme.md` in slice `nic/hello` is materialized as
`nic/hello/readme.md` below the workspace root, and empty directories are
created as directories. The CLI can initialize the default personal home slice
after signup:

```bash
gs auth signup --username nic
gs workspace init nic/home
```

The `home` slice slug is reserved for the default personal slice. Its included
path is the user's account root, for example `nic/home` covers `/nic`. Custom
personal slice slugs such as `tools` or `dotfiles` may cover narrower paths
inside `/nic`, but not paths under another account.

The CLI should preserve canonical account-rooted paths inside the workspace.
Creating another workspace is the supported way to work on another slice.

The client also has a global object cache outside any workspace:

```text
user cache dir:
  gitslice/
    objects/
      sha256/<2>/<2>/<digest>
    tmp/
```

The cache is content-addressed by file content hash and shared by every
workspace for the same local user. The default root is the user's local cache
area; `GITSLICE_CLIENT_CACHE_DIR` can override it for tests or isolated agent
runs. Workspace metadata may reference content hashes, but cached bytes are not
workspace-private. `gs status`, `gs cs create`, `gs cs update`,
`gs workspace hydrate`, and `gs workspace init` all write discovered file bytes
through this cache.

Hydration is cache-first:

1. Resolve the file entry and content hash from the server.
2. If the content hash exists in the client cache, write workspace bytes from
   the cache without reading the blob from the server.
3. If the content hash is absent, read bytes from the server, verify the hash,
   store them in the global cache, then write the workspace file.
4. Record hydrated files in `.gs/base_snapshot.json` for that workspace only.

Submit is also cache-aware. Before uploading changed blobs, the CLI calls
`BlobService.GetBlobStatus` for the changed content hashes. Hashes already
available on the server are reused by blob id. Missing hashes are uploaded from
the global client cache, not reread from the workspace path.

## 5. Slice Commands

```bash
gs slice create <slice|account/slice> [--include /account/path] [--visibility account|public]
gs slice list [account]
gs slice info <slice|account/slice>
gs slice paths <slice|account/slice>
gs slice update <slice|account/slice> [--include /account/path] [--visibility account|public]
gs slice delete <slice|account/slice> --yes
```

`gs slice create` creates a native slice under an account where the signed-in
subject is a member. A bare slice slug creates the slice under the signed-in
account. If no `--include` flag is provided, the CLI defaults to
`/<account>/<slice>`, except the reserved `home` slice default is `/<account>`.
Repeating `--include` replaces the full included path list for create and
update. The CLI also accepts comma-separated values in one `--include` flag and
expands them before calling the API. Included paths must be
canonical account-rooted paths inside the slice account and must not contain
commas. Included paths for custom slices must already exist at the current
global target ref; only `home` slices may include the account root itself.

`gs slice list` lists slices for the given account. If no account is supplied,
the CLI derives the personal account slug from a signed-up subject such as
`user_nic`.

`gs slice delete` is metadata-only and requires `--yes`. Deleting a slice with
existing changesets is rejected by the server.

`gs workspace init <slice|account/slice>` binds the workspace to one slice:

```text
1. ResolveSlice through the core API.
2. Check read authorization.
3. Write the binding to `.gs/slice.json`.
4. Initialize the global client object cache.
5. Hydrate included paths through the global object cache.
6. Record local operation log entry.
```

The MVP stores workspace metadata as JSON rather than YAML. JSON keeps the first
Go CLI dependency-light and gives functional tests stable local-state fixtures.
The files are local cache and coordination state only; server-side validation
remains authoritative.

The MVP does not support adding a second slice to an existing workspace. If the
user needs `acme/payment` and `acme/frontend`, they create two workspaces.

## 6. Server File Shell

```bash
gs shell
gs shell --slice <slice|account/slice>
gs shell --commit <commit-id>
```

`gs shell` is a local interactive shell for inspecting server-side files. It can
run from any local directory after `gs auth login`; it does not require a
Gitslice workspace and does not browse or mutate the local filesystem.

`--slice <slice|account/slice>` explicitly attaches the shell to a slice and keeps
the canonical global path layout visible. For a custom slice that includes
`/nic/tools`, `ls /` shows `nic/`, `ls /nic` shows `tools/`, and `pwd` inside
that folder prints `/nic/tools` rather than remapping it to `/tools`. The shell
may synthesize ancestor directories needed to navigate to included roots, but
file reads and mutations outside the included roots are rejected even when they
are under the same account. When the flag is omitted inside a workspace, the
shell keeps the existing slice-rooted view for the workspace's bound slice. When
the flag is omitted outside a workspace, the shell first tries to resolve the
signed-in user's default personal home slice,
`<username>/home`. If that slice exists, the shell labels the session with that
slice and shows the user's account-root folder from `/`, for example `ls` shows
`nic/` for `nic/home`. The home shell is still a slice projection: other
account roots are filtered from listings and rejected on navigation or file
operations. Empty home folders are visible from slice metadata even before the
user has created files. Legacy development accounts without a personal home
slice fall back to the global repository root, where paths such as
`/acme/payment/app.go` are interpreted directly.

By default, the shell reads the latest `refs/global/main` commit from the
server. `--commit` pins inspection to a specific native commit. A pinned shell
remains read-only even when `--slice` or a workspace provides an authoring
slice.

Initial commands:

```text
pwd              print current server path
ls [path]        list a server directory
cd [path]        change server directory
cat <file>       print a server file
stat <path>      inspect server file or directory metadata
mkdir <path>     create a server directory
write <path> ... create or replace a server file
touch <path>     create an empty server file
mv <old> <new>   rename or move a server file or directory
rm <path>        delete a server file or directory
ref              print inspected commit id
help             show shell commands
exit, quit       leave the shell
```

Paths inside the shell are slice-rooted only when the shell is launched from a
workspace. In that mode, a full canonical path such as `/acme/payment/app.go` is
also accepted when it is inside the bound slice, and attempts to leave the bound
slice are rejected locally before issuing server reads. Outside a workspace,
paths are global repository paths, with the personal home folder synthesized
from the resolved home slice when present.

The shell supports small server-side mutations against the current slice scope.
Each mutation creates a single changeset, updates it with one file edit, submits
it, waits for publish, and advances the shell to the new commit. Mutations are
disabled when the shell has fallen back to the legacy global root because there
is no authoring slice to validate against.

Shell prompts should show the current scope and current path. On interactive
terminals, the shell may pin a compact status header at the top of the screen
with the attached slice, current commit, mode, root, and current path. Color and
sticky-header terminal-control output are disabled by `--no-color`, `--quiet`,
JSON mode, non-terminal writers, and test/piped usage so scripted output stays
stable.
When stdin and stdout are attached to a real terminal, `gs shell` uses a line
editor with command history and Tab completion for shell commands and visible
server paths. Completion must use the same slice projection and synthesized
directory rules as `ls` and `cd`, so custom slices complete account-rooted
ancestor directories before included roots. Piped input, test runners, and
`--quiet` continue to use the simple scanner path without terminal control.

## 6.1 Absolute Filesystem Commands

```bash
gs fs ls /nic
gs fs cat /nic/notes/readme.md
gs fs mkdir /nic/notes
gs fs write /nic/notes/readme.md --text "hello"
gs fs write /nic/notes/readme.md --stdin
gs fs touch /nic/notes/empty.txt
gs fs upload ./notes /nic/notes --recursive
gs fs mv /nic/notes/readme.md /nic/notes/today.md
gs fs rm /nic/notes/today.md
```

`gs fs` commands use absolute global paths when a path is provided. They are
intended for small remote reads and edits in the signed-in user's personal home
slice. For username `nic`, every `gs fs` operation must stay under `/nic`;
attempts to read or mutate `/alice`, `/acme`, or any other account root are
rejected before issuing server calls that need the path, and mutations are also
rejected by server-side changeset validation.

Empty directories are first-class tree entries for these commands. Creating a
directory does not create a placeholder file.

`gs fs upload <local-path> <absolute-remote-path>` copies a local regular file
or directory into the signed-in user's home slice. File uploads target the exact
remote file path. Directory uploads require `--recursive`, map the directory's
contents under the remote path, preserve empty leaf directories, upload blobs
with bounded concurrency, and submit the full upload as one changeset instead of
creating one changeset per file. The default concurrency is chosen from the
local CPU count and can be overridden with `--concurrency`.

## 7. Status And Diff

```bash
gs status
gs diff
gs diff --name-only
gs diff --stat
gs diff --from <patchset> --to <patchset>
```

`gs status` should:

- snapshot local filesystem metadata
- detect changed files
- use the bound slice as the authoring slice
- show whether changes are inside the bound slice
- show required approvals and checks when available
- show current draft changeset and patchset state

`gs diff` shows the diff between local overlay changes and the current base
commit for the bound slice. `--name-only` prints canonical global paths only,
and `--stat` prints a compact changed-path summary. When `--from` or `--to` is
provided, `gs diff` delegates to the server-side current changeset diff and
uses the same patchset selector semantics as `gs cs diff`.

## 8. Working Copy Snapshot Model

The CLI should use a working-copy-as-draft-patchset model.

On most mutating `gs` commands:

```text
1. Scan changed workspace paths.
2. Normalize to canonical global paths.
3. Verify all changed paths are contained by the bound slice.
4. Stage changed blob content through BlobService.
5. Create or refresh a local draft patchset snapshot.
6. Record a local operation log entry.
```

The Gitslice unit is a draft patchset, not a local commit.

Correctness must not depend on a file watcher. The CLI may keep a local changed
path index for speed, but every mutating command must be able to reconcile that
index against the filesystem and server state before creating or updating a
patchset.

Watcher-backed status flow:

```text
filesystem events
  -> local changed path index
  -> bounded reconciliation scan on gs status / gs cs update
  -> WorkspaceService.ValidateWorkspaceDiff
  -> draft patchset snapshot
```

If a watcher misses an event, `gs status` may be slower because it falls back to
a scan, but it must not report a clean workspace incorrectly. File watchers are
a performance feature; server-side patchset validation and submit validation
remain authoritative.

The CLI should still make submit explicit:

```bash
gs cs create
gs cs update
gs cs submit
```

## 9. Changeset Commands

```bash
gs cs create [--title <title>]
gs cs update
gs cs submit [changeset-id] [--no-watch] [--watch-timeout <duration>]
gs cs status [changeset-id] [--watch] [--watch-timeout <duration>]
gs cs show [changeset-id]
gs cs explain [changeset-id]
gs cs versions [changeset-id]
gs cs patchsets [changeset-id]
gs cs diff [changeset-id]
gs cs diff [changeset-id] --patchset <patchset>
gs cs diff [changeset-id] --from <patchset> --to <patchset>
gs cs diff [changeset-id] --name-only
gs cs diff [changeset-id] --stat
gs cs list [--slice <slice|account/slice>] [--status <status>] [--limit <n>]
gs cs abandon [changeset-id] [--reason <reason>]
```

Create flow:

```text
1. Load the workspace's bound slice.
2. Snapshot local changes into file edits.
3. Reject the command if any file edit is outside the bound slice.
4. Upload missing blobs.
5. CreateChangeset.
6. UpdateChangeset to create patchset 1.
7. Store changeset id in local workspace state.
8. Record local operation log entry.
```

Update flow:

```text
1. Snapshot local changes.
2. Verify every file edit is still inside the workspace's bound slice and the
   changeset's authoring slice.
3. Upload missing blobs.
4. UpdateChangeset with expected current patchset id.
5. Store returned patchset id.
6. Record local operation log entry.
```

Submit flow:

```text
1. Refresh workspace metadata.
2. Confirm current patchset is uploaded.
3. SubmitChangeset.
4. By default, wait for async publish until the changeset reaches submitted
   status, printing progress to stderr for text output.
5. If --no-watch is set, return after submit admission with the accepted status
   and tell the user to run gs cs status --watch <changeset-id>.
6. If submit succeeds and publish is visible, update local base commit and clear
   overlay state.
7. If submit fails, show submit requirement, check, authorization, or conflict
   reason.
```

`gs cs status --watch` polls `GetChangeset` until the changeset reaches the
terminal `submitted` state or the watch timeout expires. This gives scripts and
humans an explicit way to resume waiting after `gs cs submit --no-watch` or
after a submit timeout.

Show, versions, and explain flow:

```text
1. If no changeset id is supplied, read the current workspace changeset id.
2. Fetch the changeset through ChangesetService.GetChangeset.
3. `show` prints metadata, affected paths, and patchsets.
4. `versions`/`patchsets` prints patchset numbers, ids, and changed paths.
5. `explain` prints the current patchset's submit requirements, read set,
   write set, and path-base fingerprints.
```

Server-side diff flow:

```text
1. Resolve the current or supplied changeset id.
2. Call ChangesetService.DiffChangeset.
3. With --patchset, compare that patchset against its base commit.
4. With --from/--to, compare two patchsets by id or patchset number.
5. With --name-only or --stat, use the server-returned changed path list
   instead of rendering unified diff text.
```

## 10. Submit Status Commands

```bash
gs cs status
gs cs status <changeset>
gs cs explain <changeset>
```

Submit status commands should use `ChangesetService` to show:

- required checks
- required approvals
- submit requirement refresh state
- CAS/rebase retry state

`gs cs list` is slice-scoped. Inside a workspace it defaults to that
workspace's slice; outside a workspace the caller must pass `--slice`.

## 11. Operation Log And Undo

```bash
gs op log
gs op show <op>
gs op undo
gs op restore <op>
```

The operation log is local workspace metadata. It records CLI operations that
change workspace state:

- workspace init and slice binding creation
- hydration/dehydration
- snapshot creation
- changeset create/update
- local restore
- conflict resolution

`gs op undo` should undo local workspace state where possible. It must not
rewrite already-submitted server history. If a local operation has a server-side
effect, undo should either create a compensating action or clearly explain why
manual action is required.

The backend may accept optional workspace operation records for audit and agent
debugging, but local undo must not depend on a server round trip.

## 12. Conflict Handling

Conflicts should be first-class patchset state.

```bash
gs resolve
gs resolve --tool
gs diff --conflicts
```

The CLI should avoid Git's interrupted-operation model. A rebase or submit can
produce a patchset with conflict metadata. The user can inspect and resolve it,
then run `gs cs update`.

When the server reports a stale path base, the CLI should show the path and the
expected/current fingerprints when available. The detailed conflict model is in
[07_conflict_resolution.md](07_conflict_resolution.md).

## 13. Query And Formatting

The CLI should eventually support structured changeset and file selectors:

```bash
gs log -r 'mine() & open()'
gs cs list -r 'touches(/acme/payment/**)'
gs diff -f '/acme/payment/**/*.go'
```

Initial implementation can keep selectors simple:

- changeset id
- patchset id
- slice id
- path prefix
- current workspace

Output should support stable machine-readable formats:

```bash
gs cs status --format json
gs status --format json
gs auth status --json=signed_in,server_addr
gs auth status --jq .reason
gs auth status --template '{{.signed_in}} {{.reason}}'
```

`gs help formatting` documents current output rules. Text output is optimized
for humans. `--json` and `--format json` produce stable command-specific JSON
objects, while diagnostics, progress, and errors stay on stderr. When JSON mode
is active, errors use the stable `error.code`, `error.message`, optional
`error.hint`, and `error.retriable` shape from `gs schema`. Field selection,
using `--json=field,field` projects top-level JSON fields after the command
builds its normal output. `--jq <expr>` filters the same JSON-shaped data with
jq syntax and can be combined with field selection. String, boolean, number,
and null results are printed as raw scalar lines; object and array results are
printed as JSON. `--template <template>` formats structured output with Go
`text/template` over JSON-shaped field names and can also be combined with
field selection. A `json` template helper serializes nested values for commands
that expose arrays or objects.

## 13.1 Help Topics And Exit Codes

`gs help` should support both command help and topic help:

```bash
gs help auth status
gs help environment
gs help formatting
gs help exit-codes
gs help paths
gs help slices
```

The first topic set is intentionally small and should mirror the concepts users
need while moving between local workspaces, remote slices, and automation:

- `environment`: CLI environment variables such as `GS_SERVER_ADDR`,
  `GS_WEB_URL`, `GS_GATEWAY_URL`, `GS_CLIENT_CACHE_DIR`, compatibility
  `GITSLICE_*` aliases, `NO_COLOR`, and `TERM=dumb`
- `formatting`: text vs JSON output, top-level JSON field selection, jq
  filtering, template output, stderr diagnostics, JSON error shape, and color
  controls
- `exit-codes`: stable process exit codes
- `paths`: canonical account-rooted server paths and workspace materialization
- `slices`: home slice semantics, custom slice slugs, and one-slice workspace
  binding

Stable exit codes:

```text
0  success
1  general command failure
2  command canceled
4  authentication missing, invalid, or rejected by the server
```

The CLI may add command-specific exit codes later, but generic automation should
be able to rely on these baseline codes.

## 14. Repository Import And Commit Inspection

The MVP CLI supports importing a GitHub repository into a mounted Gitslice path:

```bash
gs repo import github <owner/repo-or-url> \
  --mount /acme/payment/vendor/lib \
  --slice payment \
  --mode shallow

gs repo import github <owner/repo-or-url> \
  --mount /acme/payment/vendor/lib \
  --slice payment \
  --mode deep \
  --max-commits 100
```

`owner/repo` is resolved to `https://github.com/owner/repo.git`. Tests may pass
a local Git repository path through the same command so functional coverage does
not depend on external network access.

Import modes:

- `shallow`: import only the Git `HEAD` tree as one native changeset and commit.
- `deep`: walk Git commits in topological chronological order and preserve one
  native commit per Git commit. The first imported commit materializes the mounted
  tree; subsequent commits use Git tree diffs and read only changed blobs rather
  than re-reading every file in every snapshot.

`--max-commits N` limits deep import to the most recent N Git commits and uses a
depth-limited clone. It is intended for bounded validation, staged backfills, and
large-repo smoke tests before a full history import. `--resume` is enabled by
default and skips Git commits already recorded for the same source, mount path,
slice, target ref, and mode.

The mounted path must be inside the authoring slice. Import writes still go
through server-side validation, path-base checks, pending publish, and ref CAS.
The CLI does not clone repositories or create native commits locally.

Interactive text imports use the streaming import RPC and print progress to
stderr:

```text
cloning repository...
listing commits...
found 42 commit(s)
reading 1/42 1a2b3c4 first commit
uploading 1/42 1a2b3c4 (12 changed path(s))
published 1/42 1a2b3c4 -> sha256:... (12 changed path(s))
import complete
```

`--json` keeps stdout stable and returns only the final import response.

Commit inspection commands:

```bash
gs commit list [absolute-path] --limit 20
gs commit list --path /acme/payment/api --limit 20
gs commit list --slice backend --limit 20
gs commit list --slice acme/backend --path /acme/payment/shared
gs commit list --path /acme/payment/api --limit 20 --page-token <token>
gs commit inspect <native-commit-id>
```

These commands are intentionally native. They list and inspect Gitslice commits,
not Git object ids. The import response maps imported Git commit ids to native
commit ids for follow-up inspection.

`gs commit list` reads server-side commit history. Without filters, it walks the
main native ref. With `--path` or a positional path, the server uses the
changed-path index to return only commits that touched that file or directory.
With `--slice`, the server resolves the slice's current included paths and
returns commits that touched any included prefix. Signed-in users may pass a
bare slice slug for slices under their signed-in account; org slices can be
passed as `account/slice`. Combining `--slice` and `--path` returns the
intersection, so a path outside the slice produces no commits.

If more commits are available, the human output prints `next_page_token:
<token>` after the commit lines, and JSON output includes `next_page_token`.
Pass that value back with `--page-token` while keeping the same path/slice
filters to fetch the next page.

## 15. Backend Requirements

The CLI needs these backend capabilities:

- `SliceService.ResolveSlice`
- `RepositoryService.ResolvePath`
- `RepositoryService.ListDirectory`
- `RepositoryService.ReadFile`
- `RepositoryService.GetCommit`
- `RepositoryService.ListCommits`
- `RepositoryService.ImportGitRepository`
- `RepositoryService.ImportGitRepositoryStream`
- `BlobService.UploadBlob`
- `ChangesetService.CreateChangeset`
- `ChangesetService.UpdateChangeset`
- `ChangesetService.SubmitChangeset`
- `ChangesetService.AbandonChangeset`
- `WorkspaceService.GetWorkspaceState`
- `WorkspaceService.HydratePaths`
- `WorkspaceService.ValidateWorkspaceDiff`
- `WorkspaceService.RecordWorkspaceOperation`

The `WorkspaceService` calls are backend helpers. The CLI still owns local
workspace files, local cache, and local operation undo.

## 16. Non-Goals

The initial CLI should not:

- depend on an external VCS frontend
- support external VCS-specific interop commands in the MVP
- expose direct native commit creation
- expose a Git-style staging area
- allow cross-slice changesets
- auto-link multiple changesets into one submission
- bind multiple slices into one workspace
- make Git sparse checkout a core workflow
- bypass server-side submit validation
