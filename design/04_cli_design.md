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

Current implemented command groups:

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
gs log ...
gs show ...
gs fs ...
gs cs ...
gs import ...
gs shell
gs version
gs completion ...
gs schema
```

Target canonical command groups:

```text
gs log ...
gs show ...
gs init
gs import ...
gs sync
gs op ...
gs split
gs squash
gs rebase
gs describe
gs resolve
```

The target CLI should prefer a small Git-familiar surface over permanent
parallel command groups. Native noun-heavy commands can remain only when they
represent Gitslice concepts that do not map cleanly to a Git-familiar top-level
verb. Commands that only duplicate a clearer top-level command should be
removed rather than kept as permanent aliases.

Canonical replacements:

```text
gs workspace init <slice>        -> gs init <slice>
gs commit list [path]            -> gs log [-- <path>]
gs commit inspect <commit>       -> gs show <commit>
gs commit diff <commit>          -> gs diff <commit>
gs commit diff <a> <b>           -> gs diff <a> <b>
```

Commands to keep because they represent Gitslice concepts that Git does not
model directly:

```text
gs auth ...
gs context
gs config ...
gs alias ...
gs rpc ...
gs browse
gs help ...
gs slice ...
gs status
gs diff
gs fs ...
gs cs ...
gs shell
gs version
gs completion ...
gs schema
```

Removed compatibility commands:

```text
gs commit list
gs commit inspect
gs commit diff
```

These commands were replaced by `gs log`, `gs show`, and commit-aware `gs diff`.
They should not be retained as hidden aliases because they add a second mental
model for the same workflow.

Help and schema output should advertise the canonical command first. Hidden
compatibility commands should not appear in root help and should be marked as
deprecated in schema output. Root help should include a compact, end-to-end
workflow example so a new user can move from signup to shell, file upload,
workspace initialization, status, changeset creation, diff, and submit:

```bash
gs auth signup --username nic
gs shell
gs fs upload ./notes /nic/notes --recursive
gs init nic/home
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
hosted production web app at `https://gitslice.io`. The default server follows
the same policy and falls back to `api.gitslice.io:443`; staging and local
development use explicit environment overrides.

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
gs init <slice|account/slice>
gs init <username>/home
gs workspace hydrate <path>
gs sync
gs workspace sync
```

The current MVP implements `gs init`, `workspace hydrate`, `gs sync`, and
`gs workspace sync`. The canonical workspace creation command is `gs init`,
matching Git's familiar entry point. `gs workspace init` is hidden compatibility
only. Top-level `gs status`, `gs diff`, and `gs cs ...` commands discover the
workspace by walking up from the current directory.
Workspace-specific `status`, `dehydrate`, and `root` subcommands remain planned
command aliases or helpers, not part of the current implemented CLI surface.
`gs sync` is top-level because it is part of the normal workspace loop, with
`gs workspace sync` as the explicit namespace alias.

`gs init <slice|account/slice>` creates local workspace metadata:

```text
.gs/
  slice.json
  state.json
  base_snapshot.json
  conflicts.json          # only when sync conflicts are unresolved
```

Authentication state is not written under `.gs/`. The bearer token and saved
subject id live in the user-level config file, currently
`~/.gitslice/config.json`, and functional tests assert that workspace metadata
does not contain the bearer token.

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

### 4.1 Workspace Sync

`gs sync` advances the current workspace to the latest accepted target-ref head
for the bound slice. It is not only a clean hydration command; when the
workspace is associated with a draft changeset, sync is a changeset-aware
rebase operation.

The sync inputs are:

```text
B: the workspace base snapshot before sync
L: the current local workspace contents
R: the latest remote slice projection
```

For clean paths, sync updates the workspace in place:

- remote changed and local unchanged since `B`: write `R`
- local changed and remote unchanged since `B`: keep `L`
- local and remote changed to the same final file: keep that file and mark the
  path clean on the new base
- local and remote changed different non-overlapping text lines: line-merge by
  default and keep the merged file as a local edit on the new base
- paths removed remotely and unchanged locally: remove the tracked local file
- new remote paths with no local blocker: create them locally

`gs sync` defaults to `--merge line`. Supported sync merge strategies are:

- `line`: attempt deterministic line-level three-way merge for text files, then
  fall back to explicit conflicts when edits overlap or the file is not safe to
  line-merge
- `manual`: skip auto-merge and record same-path divergent changes as conflicts
- `ours`: keep the local side for same-path divergent changes
- `theirs`: take the latest remote side for same-path divergent changes

For remaining conflicting paths, sync still updates the workspace and records an
explicit conflict state. The exact presentation can vary by file kind, but the
authoritative state lives under `.gs/` and includes the old base, new base,
conflicted paths, conflict classes, and local/remote fingerprints. Text files
may be materialized with conflict markers. Binary, directory/file,
delete/modify, and untracked-blocker conflicts should preserve local content and
expose side metadata or side variants so the user can inspect both sides without
data loss.

If the workspace has an associated draft changeset, `gs sync` creates a new
patchset on that changeset. The patchset is a sync or rebase snapshot, not a
plain remote snapshot. It records:

- previous base commit
- new synced base commit
- parent patchset
- cleanly rebased local edits
- conflicted paths and conflict metadata

This produces an auditable history:

```text
v1: user patchset on base A
v2: user patchset on base A
v3: sync/rebase patchset onto latest base B, possibly with conflicts
v4: user-resolved patchset on base B
```

The latest patchset is not submittable while unresolved conflicts remain.
`gs status` shows the conflict set, and text conflicts are materialized with
local/base/remote conflict markers. After editing the workspace, the user runs
`gs cs update`; if conflict markers or side metadata are resolved, that command
creates the next normal patchset and clears the conflict state. Future
conflict-specific commands such as `gs diff --conflicts` and `gs resolve` can
build on the same `.gs/conflicts.json` and patchset conflict metadata.

If the workspace has local changes but no associated draft changeset,
interactive `gs sync` should ask whether to create one before syncing. In
non-interactive mode, it should fail with a hint to run `gs cs create` first or
pass an explicit future flag once that policy exists. A clean workspace with no
draft changeset can sync by hydrating `R` and advancing `.gs/state.json` plus
`.gs/base_snapshot.json`.

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
gs init nic/home
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
`gs workspace hydrate`, and `gs init` all write discovered file bytes through
this cache.

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

`gs init <slice|account/slice>` binds the workspace to one slice:

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

When `gs fs ls` is run without a path, it lists the signed-in home slice root
and writes the resolved remote scope to stderr, for example
`remote: listing nic/home at /nic`. This keeps stdout as the directory listing
while making clear that `home` means the remote Gitslice home slice root, not
the local `~/` directory. JSON output omits this human diagnostic and reports
the resolved path in the `path` field.

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

`gs sync` participates in the same snapshot history. When a draft changeset is
associated with the workspace, syncing onto a newer target-ref head creates a
new sync/rebase patchset on that changeset. The patchset may contain unresolved
conflict metadata, in which case it is a durable intermediate snapshot rather
than a submittable patchset. A later `gs cs update` after user resolution creates
the next normal patchset on the synced base.

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
gs cs submit [changeset] [--no-watch] [--watch-timeout <duration>]
gs cs status [changeset] [--watch] [--watch-timeout <duration>]
gs cs show [changeset]
gs cs explain [changeset]
gs cs versions [changeset]
gs cs patchsets [changeset]
gs cs diff [changeset]
gs cs diff [changeset] --patchset <patchset>
gs cs diff [changeset] --from <patchset> --to <patchset>
gs cs diff [changeset] --name-only
gs cs diff [changeset] --stat
gs cs list [--slice <slice|account/slice>] [--status <status>] [--limit <n>]
gs cs abandon [changeset] [--reason <reason>]
```

User-facing changeset selectors use the shareable handle
`<account>/<slice>@<number>`, for example `acme/payment@42`. This handle is the
default text output, the value copied into chat or review links, and the
advertised argument form for commands. Inside a workspace, the CLI may also
accept `@42` as shorthand for the workspace's bound slice; outside a workspace,
commands must require the fully qualified handle or an explicit `--slice`.

Patchsets are normally selected by number within a changeset. `gs cs versions`
should print `1`, `2 current`, and so on, not raw `ps_...` ids. When a patchset
must be shared without surrounding changeset context, the exact selector is
`<changeset-handle>.<patchset-number>`, for example `acme/payment@42.2`.

Canonical `cs_...` and `ps_...` ids remain API/storage identifiers and may be
accepted for backward compatibility or debugging, but they should not appear in
normal text output, help examples, hints, web labels, or Git gateway messages.
Structured output should expose the handle as the primary field and may include
canonical ids for scripts and diagnostics.

Default text output should look like:

```text
created changeset acme/payment@42 patchset 1

changeset: acme/payment@42
patchsets:
  1 changed=1
  2 current changed=1
```

Create flow:

```text
1. Load the workspace's bound slice.
2. If the workspace already has an active draft or pending changeset, stop and
   tell the user to run `gs cs update` to create a new patchset instead.
3. Snapshot local changes into file edits.
4. Reject the command if any file edit is outside the bound slice.
5. Upload missing blobs.
6. CreateChangeset.
7. UpdateChangeset to create patchset 1.
8. Store canonical changeset id and shareable handle in local workspace state.
9. Record local operation log entry.
```

Update flow:

```text
1. Snapshot local changes.
2. Verify every file edit is still inside the workspace's bound slice and the
   changeset's authoring slice.
3. Upload missing blobs.
4. UpdateChangeset with the expected-current-patchset concurrency token.
5. Store returned canonical patchset id and display the patchset number.
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
   and tell the user to run `gs cs status --watch <changeset-handle>`.
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
1. If no changeset selector is supplied, read the current workspace changeset.
2. Resolve the selector to a full shareable handle or canonical id, then fetch
   the changeset through ChangesetService.GetChangeset.
3. `show` prints metadata, affected paths, and patchsets.
4. `versions`/`patchsets` prints patchset numbers and changed paths.
5. `explain` prints the current patchset's submit requirements, read set,
   write set, and path-base fingerprints.
```

Server-side diff flow:

```text
1. Resolve the current or supplied changeset selector.
2. Call ChangesetService.DiffChangeset.
3. With --patchset, compare that patchset against its own base commit.
   This nearest-base diff is the canonical patchset diff.
4. With --from/--to, compare two patchsets by number or exact patchset handle.
   This is a review convenience for patchset-to-patchset deltas, primarily when
   both patchsets share the same base. It is not the product contract for
   arbitrary snapshot-to-snapshot diffs across sync base transitions.
5. With --name-only or --stat, use the server-returned changed path list
   instead of rendering unified diff text.
```

Nearest-base diff is intentionally the MVP boundary. For example, if a user
starts from `base1`, creates patchsets `v1` and `v2`, runs sync onto `base2`,
and then creates resolved patchset `v4`, each patchset's primary diff is:

```text
v1: base1 -> v1
v2: base1 -> v2
v3 sync: base2 -> v3 sync overlay, including conflict marker files if any
v4 resolved: base2 -> v4
```

The CLI and web UI should present that as the default review surface. They
should not imply that Gitslice supports a complete arbitrary diff such as
`v2 -> v3 sync` across `base1 -> base2`; remote-only changes introduced by the
base transition belong to the base history, not to the sync patchset's local
overlay.

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

- `gs init` and slice binding creation
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

`gs sync` is the primary workspace rebase entry point. It should update the
workspace to the latest remote base even when conflicts exist, create a
sync/rebase patchset on the associated changeset, and leave the user with
explicit local conflicts to resolve. The conflict state is not hidden process
state; it is recorded in `.gs/` and in the latest patchset. Until that conflict
state is cleared by a follow-up `gs cs update`, `gs cs submit` must reject the
changeset with a conflict-specific hint.

When the server reports a stale path base, the CLI should show the path and the
expected/current fingerprints when available. The detailed conflict model is in
[07_conflict_resolution.md](07_conflict_resolution.md).

## 13. Query And Formatting

The CLI should eventually support structured changeset and file selectors:

```bash
gs cs list -r 'touches(/acme/payment/**)'
gs cs list -r 'mine() & open()'
gs diff -f '/acme/payment/**/*.go'
```

Initial implementation can keep selectors simple:

- changeset handle, for example `acme/payment@42`
- workspace-relative changeset shorthand, for example `@42`
- patchset number within a changeset
- exact patchset handle, for example `acme/payment@42.2`
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

The MVP CLI supports importing a Git repository into a mounted Gitslice path:

```bash
gs import <source> \
  --mount /acme/payment/vendor/lib \
  --slice payment \
  --mode shallow

gs import <source> \
  --mount /acme/payment/vendor/lib \
  --slice payment \
  --mode deep \
  --max-commits 100
```

`gs import` is the canonical command. The import source determines the protocol:
URLs with schemes, SSH sources such as `git@host:owner/repo.git`, and local
absolute or relative paths are passed through as provided. `owner/repo` shorthand
is resolved to `https://github.com/owner/repo.git`. Tests may pass a local Git
repository path through the same command so functional coverage does not depend
on external network access.

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

Commit inspection commands should use Git-familiar top-level verbs:

```bash
gs log --limit 20
gs log -- /acme/payment/api
gs log --path /acme/payment/api --limit 20
gs log --slice backend --limit 20
gs log --slice acme/backend --path /acme/payment/shared
gs log --path /acme/payment/api --limit 20 --page-token <token>
gs log --follow -- /acme/payment/api/file.go
gs log --no-follow-moves -- /acme/payment/api/file.go
gs log --full -- /acme/payment/api
gs show <native-commit-id-or-prefix>
gs show --stat <native-commit-id-or-prefix>
gs show --name-only <native-commit-id-or-prefix>
gs show -p <native-commit-id-or-prefix>
gs diff <native-commit-id-or-prefix>
gs diff <old-native-commit-id-or-prefix> <new-native-commit-id-or-prefix>
gs diff <native-commit-id-or-prefix> -- /acme/payment/api/file.go
```

These commands are intentionally native even though their names are Git-like.
They list, show, and diff Gitslice commits, not Git object ids. The import
response maps imported Git commit ids to native commit ids for follow-up
inspection. The old `gs commit list`, `gs commit inspect`, and `gs commit diff`
compatibility commands are removed; examples and tests should use `gs log`,
`gs show`, and `gs diff`.

Native commit ids are content-addressed and canonical in the API, for example
`sha256:14e085c8afbf800239e8b6e064e4f8488ca85ca311c72d1c562a14e90f2aad76`.
That full form is too noisy for normal list output, so human `gs log` output
should show short ids by default:

```text
14e085c8afbf  file write /test-user/folder1/folder1-1/hello.txt
87ecd8bb3549  file mkdir /test-user/folder1/folder1-2
a3c11219b759  file mkdir /test-user/folder1/folder1-1
```

The default display length is 12 hex characters. `--full` prints canonical ids.
`--json` always includes the full `id` and may include `short_id`; scripts
should not have to reconstruct canonical ids from display text.

Commands that take commit ids should accept a full id, a `sha256:`-prefixed
short id, or a bare hex prefix:

```bash
gs show sha256:14e085c8afbf800239e8b6e064e4f8488ca85ca311c72d1c562a14e90f2aad76
gs show sha256:14e085c8afbf
gs show 14e085c8afbf
gs diff 14e085c8afbf
```

The CLI may normalize bare prefixes before calling the server, but the server
resolver is authoritative. Prefixes shorter than 8 hex characters should be
rejected with a clear error. If a prefix matches multiple commits in the current
visible scope, the CLI should show the ambiguous candidates with short ids and a
hint to use a longer prefix. Candidate lists must not include commits outside
the caller's authorized account, path, or slice scope.

`gs log` reads server-side commit history from the native target ref. The
default target ref is `refs/global/main`; future ref selection should use
`--ref <ref>`, not positional revision syntax.

Scope resolution should make `gs log` feel like running `git log` inside a
repository while preserving Gitslice's account and slice model:

```text
1. --slice <slice|account/slice>
2. nearest workspace slice
3. signed-in user's personal home slice
4. --all to list all readable account history
```

Inside a workspace, plain `gs log` therefore shows history visible through the
workspace's slice. Outside a workspace, plain `gs log` shows the signed-in
user's home-slice history. `--all` is the explicit escape hatch for account-wide
or multi-account history the user can read.

Path filters should support both Gitslice-native flags and Git's `--` path
separator:

```bash
gs log -- /acme/payment/api
gs log -- ./internal/api
gs log --path /acme/payment/api
gs log --slice backend -- /acme/payment/shared
```

Inside a workspace, relative paths are resolved against the workspace root and
then converted to canonical account-rooted paths. Outside a workspace, relative
paths are rejected with a hint to pass an absolute account-rooted path. Help and
examples should prefer `gs log -- <path>` because it is familiar to Git users
and avoids ambiguity with future revision selectors.

With a path filter, the server uses the changed-path index to return only
commits that touched that file or directory. With `--slice`, the server resolves
the slice's current included paths and returns commits that touched any included
prefix. Combining `--slice` and `--path` returns the intersection, so a path
outside the slice produces an empty log rather than falling back to broader
history.

The default human output should be compact and scannable:

```text
14e085c8afbf  file write /test-user/folder1/folder1-1/hello.txt
87ecd8bb3549  file mkdir /test-user/folder1/folder1-2
a3c11219b759  file mkdir /test-user/folder1/folder1-1
```

This is equivalent to Git's `--oneline` style and is the default because
Gitslice commit messages often encode file operations. `--oneline` should be
accepted as an explicit no-op for Git familiarity. `--full` changes only the id
display, not the layout:

```text
sha256:14e085c8afbf800239e8b6e064e4f8488ca85ca311c72d1c562a14e90f2aad76  file write /test-user/folder1/folder1-1/hello.txt
```

`gs log --format medium` can provide a more Git-like block layout:

```text
commit 14e085c8afbf
Author: user_test
Date:   2026-05-30 10:12:03 -0700

    file write /test-user/folder1/folder1-1/hello.txt
```

Additional display flags should map to familiar Git behavior:

```bash
gs log --name-only -- /acme/payment/api
gs log --stat -- /acme/payment/api
gs log -p -- /acme/payment/api
```

`--name-only` can be implemented from commit `changed_paths`. `--stat` and `-p`
depend on server-side commit diff support and may be added after
`gs diff <commit>` is implemented. Until then, the CLI should return a stable
unsupported-option error instead of silently ignoring those flags.

If more commits are available, human output prints an actionable pagination
hint after the commit lines:

```text
next_page_token: eyJjb21taXRfaWQiOi...
hint: Run gs log --page-token eyJjb21taXRfaWQiOi... with the same filters.
```

JSON output includes both the commits and the resolved query scope:

```json
{
  "scope": {
    "ref_name": "refs/global/main",
    "slice": {"account": "test-user", "slice": "home"},
    "path": "/test-user/folder1",
    "follow_moves": true
  },
  "commits": [
    {
      "id": "sha256:14e085c8afbf800239e8b6e064e4f8488ca85ca311c72d1c562a14e90f2aad76",
      "short_id": "14e085c8afbf",
      "author": "user_test",
      "created_at": "2026-05-30T17:12:03Z",
      "message": "file write /test-user/folder1/folder1-1/hello.txt",
      "changed_paths": ["/test-user/folder1/folder1-1/hello.txt"]
    }
  ],
  "next_page_token": ""
}
```

When a single path is supplied, `gs log` follows stable file and directory
identity across moves by default. `--follow` is accepted for Git familiarity and
is the default for a single path. `--no-follow-moves` requests literal
path-index history. `--follow` without exactly one path should return an
invalid-argument error with a hint to pass `-- <path>`. Slice-scoped history
must respect the attached or specified slice and must not reveal old or new
paths outside the user's visible projection. See
[13_file_identity_and_move_history.md](13_file_identity_and_move_history.md).

Intentional differences from Git:

- `gs log` does not create or inspect local commits; it reads published native
  Gitslice commits.
- positional revision ranges such as `main..topic` are not part of the MVP;
  commit comparison belongs to `gs diff <a> <b>`.
- path output should remain canonical account-rooted in JSON. Human text may
  add a future `--relative` mode, but canonical paths are the stable default.

`gs show <commit>` prints commit metadata and defaults to a Git-like commit
detail view. `--stat`, `--name-only`, and `-p` select summary, path-only, and
patch output. `gs diff <commit>` diffs the resolved commit against its first
parent by default. `gs diff <a> <b>` diffs two resolved commits. `--name-only`
and `--stat` should mirror the formatting used by changeset diffs.

## 15. Backend Requirements

The CLI needs these backend capabilities:

- `SliceService.ResolveSlice`
- `RepositoryService.ResolvePath`
- `RepositoryService.ListDirectory`
- `RepositoryService.ReadFile`
- `RepositoryService.GetCommit`
- `RepositoryService.ResolveCommit`
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
