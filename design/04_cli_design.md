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
gs workspace ...
gs slice ...
gs status
gs diff
gs fs ...
gs cs ...
gs repo ...
gs commit ...
gs shell
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

## 3.1 Auth Commands

```bash
gs auth login --server <grpc-addr> --dev-user <name>
gs auth signup --username <name>
gs auth status
```

`gs auth signup --username <name>` starts a fake browser-approved device flow
for local development. The CLI starts a temporary localhost callback listener,
opens or prints the server web approval URL, waits for the browser callback, and
stores the returned bearer token in the user config. Approval creates the
personal account and its default `<name>/home` slice. The server web page is a
prototype approval screen, not a real identity-provider login.

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

## 4. Workspace Commands

```bash
gs workspace init <account>/<slice>
gs workspace init <username>/home
gs workspace status
gs workspace sync
gs workspace hydrate <path>
gs workspace dehydrate <path>
gs workspace root
```

`gs workspace init <account>/<slice>` creates local workspace metadata:

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

The workspace stores:

- one slice binding
- hydrated file cache
- local overlay changes
- draft patchset snapshots
- local operation log
- server metadata cache

Files for the bound slice are hydrated during workspace initialization. The CLI
can initialize the default personal home slice after signup:

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
gs slice create <account>/<slice> [--include /account/path] [--visibility account|public]
gs slice list [account]
gs slice info <account>/<slice>
gs slice paths <account>/<slice>
gs slice update <account>/<slice> [--include /account/path] [--visibility account|public]
gs slice delete <account>/<slice> --yes
```

`gs slice create` creates a native slice under an account where the signed-in
subject is a member. If no `--include` flag is provided, the CLI defaults to
`/<account>/<slice>`, except the reserved `home` slice default is
`/<account>`. Repeating `--include` replaces the full included path list for
create and update. Included paths must be canonical account-rooted paths inside
the slice account; only `home` slices may include the account root itself.

`gs slice list` lists slices for the given account. If no account is supplied,
the CLI derives the personal account slug from a signed-up subject such as
`user_nic`.

`gs slice delete` is metadata-only and requires `--yes`. Deleting a slice with
existing changesets is rejected by the server.

`gs workspace init <account>/<slice>` binds the workspace to one slice:

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
gs shell --slice <account>/<slice>
gs shell --commit <commit-id>
```

`gs shell` is a local interactive shell for inspecting server-side files. It can
run from any local directory after `gs auth login`; it does not require a
Gitslice workspace and does not browse or mutate the local filesystem.

`--slice <account>/<slice>` explicitly attaches the shell to a slice and shows a
projection of that slice from its account root. For a custom slice that includes
`/nic/tools`, `ls /` shows `tools/`, and paths outside the included roots are
rejected even when they are under the same account. When the flag is omitted
inside a workspace, the shell keeps the existing slice-rooted view for the
workspace's bound slice. When the flag is omitted outside a workspace, the shell
first tries to resolve the signed-in user's default personal home slice,
`<username>/home`. If that slice exists, the shell labels the session with that
slice and shows the user's account-root folder from `/`, for example `ls` shows
`nic/` for `nic/home`. Empty home folders are visible from slice metadata even
before the user has created files. Legacy development accounts without a
personal home slice fall back to the global repository root, where paths such as
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
terminal-control output are disabled by `--no-color`, `--quiet`, JSON mode,
non-terminal writers, and test/piped usage so scripted output stays stable.

## 6.1 Absolute Filesystem Commands

```bash
gs fs ls /nic
gs fs cat /nic/notes/readme.md
gs fs mkdir /nic/notes
gs fs write /nic/notes/readme.md --text "hello"
gs fs write /nic/notes/readme.md --stdin
gs fs touch /nic/notes/empty.txt
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

## 7. Status And Diff

```bash
gs status
gs diff
gs diff --from <patchset>
gs diff --to <patchset>
```

`gs status` should:

- snapshot local filesystem metadata
- detect changed files
- use the bound slice as the authoring slice
- show whether changes are inside the bound slice
- show required approvals and checks when available
- show current draft changeset and patchset state

`gs diff` should show the diff between local overlay changes and the current
base commit for the bound slice.

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
gs cs create
gs cs update
gs cs status
gs cs show <id>
gs cs abandon <id>
gs cs submit <id>
gs cs list
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
4. If submit succeeds, update local base commit and clear overlay state.
5. If submit fails, show submit requirement, check, authorization, or conflict
   reason.
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
```

## 14. Repository Import And Commit Inspection

The MVP CLI supports importing a GitHub repository into a mounted Gitslice path:

```bash
gs repo import github <owner/repo-or-url> \
  --mount /acme/payment/vendor/lib \
  --slice acme/payment \
  --mode shallow

gs repo import github <owner/repo-or-url> \
  --mount /acme/payment/vendor/lib \
  --slice acme/payment \
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
gs commit list --limit 20
gs commit inspect <native-commit-id>
```

These commands are intentionally native. They list and inspect Gitslice commits,
not Git object ids. The import response maps imported Git commit ids to native
commit ids for follow-up inspection.

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
