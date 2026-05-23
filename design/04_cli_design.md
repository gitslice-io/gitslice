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
gs file ...
gs cs ...
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

## 4. Workspace Commands

```bash
gs workspace init <account>/<slice>
gs workspace status
gs workspace sync
gs workspace hydrate <path>
gs workspace dehydrate <path>
gs workspace root
```

`gs workspace init <account>/<slice>` creates local workspace metadata:

```text
.gs/
  config.yaml
  slice.yaml
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

Files are hydrated on demand. The CLI should preserve canonical account-rooted
paths inside the workspace. Creating another workspace is the supported way to
work on another slice.

## 5. Slice Commands

```bash
gs slice list
gs slice info <account>/<slice>
gs slice paths <account>/<slice>
```

`gs workspace init <account>/<slice>` binds the workspace to one slice:

```text
1. ResolveSlice through the core API.
2. Check read authorization.
3. Write the binding to .gs/slice.yaml.
4. Hydrate only requested or default paths.
5. Record local operation log entry.
```

The MVP does not support adding a second slice to an existing workspace. If the
user needs `acme/payment` and `acme/frontend`, they create two workspaces.

## 6. Status And Diff

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

## 7. Working Copy Snapshot Model

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

## 8. Changeset Commands

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

## 9. Submit Status Commands

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

## 10. Operation Log And Undo

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

## 11. Conflict Handling

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

## 12. Query And Formatting

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

## 13. Backend Requirements

The CLI needs these backend capabilities:

- `SliceService.ResolveSlice`
- `RepositoryService.ResolvePath`
- `RepositoryService.ListDirectory`
- `RepositoryService.ReadFile`
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

## 14. Non-Goals

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
