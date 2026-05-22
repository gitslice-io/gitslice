# Gitslice CLI Design

This document defines the native `gs` command-line experience and the backend
capabilities it depends on.

Related documents:

- [gitslice_architecture_design.md](gitslice_architecture_design.md): top-level architecture
- [core_api.md](core_api.md): gRPC APIs used by the CLI
- [git_compatibility.md](git_compatibility.md): Git gateway and optional Git/Jujutsu interop
- [execution_plan.md](execution_plan.md): rollout phases

## 1. Positioning

`gs` is the primary Gitslice CLI.

The CLI should be Gitslice-native, not a thin wrapper around Git or Jujutsu.
Gitslice has first-class concepts that those tools do not own:

- account-rooted global paths
- slices
- folder policy files
- changesets and patchsets
- account queues
- server-side submit validation
- Git projection as a compatibility layer

Jujutsu is still a strong UX reference. The useful ideas to borrow are:

- no user-facing staging area
- working-copy changes are easy to snapshot
- local operation log and undo
- expressive revision/file selection
- conflict state as data instead of as an interrupted command mode
- simple commands for splitting, squashing, rebasing, and describing work

The CLI should expose those ideas through Gitslice objects.

## 2. Core UX Rules

1. The user works in a sparse Gitslice workspace, not a full global checkout.
2. The workspace can contain multiple slices.
3. A changeset is scoped to exactly one authoring slice.
4. If a command cannot infer the authoring slice, it must ask for `--slice`.
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
gs queue ...
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
gs jj ...
```

The later commands should be added when the underlying changeset and patchset
model can represent the operation cleanly.

## 4. Workspace Commands

```bash
gs workspace init
gs workspace status
gs workspace sync
gs workspace hydrate <path>
gs workspace dehydrate <path>
gs workspace root
```

`gs workspace init` creates local workspace metadata:

```text
.gs/
  config.yaml
  slices.yaml
  cache/
  overlay/
  op_log/
  draft_patchsets/
```

The workspace stores:

- slice bindings
- hydrated file cache
- local overlay changes
- draft patchset snapshots
- local operation log
- server metadata cache

Files are hydrated on demand. The CLI should preserve canonical account-rooted
paths inside the workspace.

## 5. Slice Commands

```bash
gs slice add <account>/<slice>
gs slice remove <account>/<slice>
gs slice list
gs slice info <account>/<slice>
gs slice paths <account>/<slice>
```

Adding a slice:

```text
1. ResolveSlice through the core API.
2. Check read authorization.
3. Add slice binding to .gs/slices.yaml.
4. Hydrate only requested or default paths.
5. Record local operation log entry.
```

Removing a slice should not delete unsubmitted local edits unless the user
explicitly confirms or moves them into another still-bound slice.

## 6. Status And Diff

```bash
gs status
gs diff
gs diff --slice acme/payment
gs diff --from <patchset>
gs diff --to <patchset>
```

`gs status` should:

- snapshot local filesystem metadata
- detect changed files
- resolve authoring slice candidates
- show whether changes are inside one slice or ambiguous
- show matching folder policy files when available
- show current draft changeset and patchset state

`gs diff` should show the diff between local overlay changes and the current
base commit for the authoring slice.

## 7. Working Copy Snapshot Model

The CLI should use a working-copy-as-draft-patchset model.

On most mutating `gs` commands:

```text
1. Scan changed workspace paths.
2. Normalize to canonical global paths.
3. Verify authoring slice containment.
4. Stage changed blob content through BlobService.
5. Create or refresh a local draft patchset snapshot.
6. Record a local operation log entry.
```

This is inspired by Jujutsu's automatic working-copy snapshots, but the Gitslice
unit is a draft patchset, not a local commit.

The CLI should still make submit explicit:

```bash
gs cs create
gs cs update
gs cs submit
```

## 8. Changeset Commands

```bash
gs cs create --slice <account>/<slice>
gs cs update
gs cs status
gs cs show <id>
gs cs abandon <id>
gs cs submit <id>
gs cs list
```

Create flow:

```text
1. Resolve authoring slice.
2. Snapshot local changes into file edits.
3. Upload missing blobs.
4. CreateChangeset.
5. UpdateChangeset to create patchset 1.
6. Store changeset id in local workspace state.
7. Record local operation log entry.
```

Update flow:

```text
1. Snapshot local changes.
2. Upload missing blobs.
3. UpdateChangeset with expected current patchset id.
4. Store returned patchset id.
5. Record local operation log entry.
```

Submit flow:

```text
1. Refresh workspace metadata.
2. Confirm current patchset is uploaded.
3. SubmitChangeset.
4. If submit succeeds, update local base commit and clear overlay state.
5. If submit fails, show policy, queue, check, or conflict reason.
```

## 9. Queue Commands

```bash
gs queue status
gs queue status <changeset>
gs queue explain <changeset>
```

Queue commands should use `QueueService` to show:

- required queues
- queue positions
- runnable state
- required checks
- policy refresh requirements
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

- slice add/remove
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

## 12. Query And Formatting

The CLI should eventually support revset-like and fileset-like selectors:

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

## 13. Optional Jujutsu Interop

Jujutsu should be supported as optional interop, not as the primary CLI.

Supported later:

```bash
jj git clone https://gitslice.io/git/acme/payment.git
gs cs create --from-jj '@'
gs cs update --from-jj 'stack(@)'
```

Interop path:

```text
jj local commits
  -> Git-compatible projected slice repository
  -> gs converts selected jj/Git commits to file edits
  -> Gitslice changeset and patchset
  -> normal policy, queue, and submit validation
```

The interop layer must not bypass Gitslice changesets, queues, folder policy
files, or submit validation.

## 14. Backend Requirements

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
- `QueueService.ResolveRequiredQueues`
- `QueueService.GetQueueItem`
- `WorkspaceService.GetWorkspaceState`
- `WorkspaceService.HydratePaths`
- `WorkspaceService.ValidateWorkspaceDiff`
- `WorkspaceService.RecordWorkspaceOperation`

The `WorkspaceService` calls are backend helpers. The CLI still owns local
workspace files, local cache, and local operation undo.

## 15. Non-Goals

The initial CLI should not:

- use Jujutsu as the only supported frontend
- expose direct native commit creation
- expose a Git-style staging area
- allow cross-slice changesets
- make Git sparse checkout a core workflow
- bypass server-side folder policy, queue, or submit validation

## 16. External References

- Jujutsu repository: <https://github.com/jj-vcs/jj>
- Jujutsu working copy model: <https://docs.jj-vcs.dev/latest/working-copy/>
- Jujutsu operation log: <https://docs.jj-vcs.dev/latest/operation-log/>
- Jujutsu architecture: <https://docs.jj-vcs.dev/latest/technical/architecture/>
- Jujutsu Git compatibility: <https://docs.jj-vcs.dev/latest/git-compatibility/>
