# Gitslice Execution Plan

This document tracks implementation phases and workflow validation for Gitslice.
The architecture is in [gitslice_architecture_design.md](gitslice_architecture_design.md),
the storage design is in [storage.md](storage.md), and the gRPC API boundary is
in [core_api.md](core_api.md). CLI behavior is in [cli_design.md](cli_design.md).
Git compatibility is in [git_compatibility.md](git_compatibility.md). Indexing
details are in [indexing.md](indexing.md).

## 1. MVP Phases

### Phase 1: Native Object Model

- Content-addressed blob store
- Immutable tree metadata
- Canonical path rules
- Global commit graph
- Atomic refs
- Staged blob upload protocol

Exit criteria:

- Blobs can be uploaded, verified, and referenced by metadata.
- Trees and commits are immutable and content-addressed where required.
- Refs can be updated only through CAS.
- Canonical path validation is shared by storage and API layers.

### Phase 2: Slice Definitions And Projection

- User and organization accounts with globally unique slugs
- Slice identity
- Slice definitions as versioned metadata
- Absolute included paths
- Folder policy metadata files
- Slice-level visibility and roles
- Overlapping slice coverage
- Folder-level overlap policy union
- Deterministic projection by latest definition

Exit criteria:

- A slice can project a deterministic tree from the global commit graph.
- Slice definition changes create new auditable definition versions.
- Overlapping slices resolve covering slices and matching policy files.
- Projection cache keys include slice id, slice definition hash, and global commit.

### Phase 3: Workspace And Native CLI

- Sparse workspace metadata
- On-demand hydration
- Slice add/remove in workspace
- Local status/diff
- Local operation log and undo
- Draft patchset snapshots
- Native `gs` workflows

Exit criteria:

- A user can initialize a workspace and add one or more slices.
- File hydration preserves canonical account-rooted paths.
- Local edits can be converted into canonical global path diffs.
- Workspace state does not require cloning the full global source graph.
- Local operations can be inspected through `gs op log`.
- `gs cs create` and `gs cs update` produce server patchsets from local snapshots.

### Phase 4: Changesets And Versioned Queues

- Changeset creation
- Patchsets
- Review state
- Conflict detection
- Covering-slice and folder policy file refresh
- Versioned queue definition files
- Queue selection
- Queue leases
- Multi-queue submit coordination
- Atomic ref update

Exit criteria:

- A changeset is scoped to one authoring slice.
- Each update creates an immutable patchset.
- Submit recomputes coverage, policy files, queue selection, and approvals.
- Submit publishes through CAS or fails without moving the target ref.

### Phase 5: Git Read Compatibility

- Git smart HTTP endpoint
- Clone from slice URL
- Synthetic Git history
- Fetch
- Partial clone support

Exit criteria:

- A slice can be cloned from its Git URL.
- Projected Git commits are stable for the same projection inputs.
- Fetch and partial clone operate through projected refs and trees.

### Phase 6: Git Push Into Changesets

- Convert Git diff to global path patchset
- Push to changeset refs
- Changeset creation/update from Git
- Protected branch push policy

Exit criteria:

- `refs/changes/new` creates a changeset.
- Pushing to an existing changeset ref creates a new patchset.
- Protected ref pushes are rejected or translated into changesets.
- Git-originated changes go through the same validation as native changes.

### Phase 7: Indexing, CI, And Scale

- Changed path index
- Code search
- Slice coverage index
- Build/test integration
- Regional reads
- Projection cache
- Advanced replication

Exit criteria:

- Indexes can be rebuilt from committed source-of-truth objects.
- CI requirements can be selected from matching folder policy files and queues.
- Regional read replicas can serve reads while preserving linearizable ref writes.

## 2. Example Native Workflow

### 2.1 Create Workspace

```bash
gs workspace init
gs slice add nicholas/identity
```

### 2.2 Edit Code

```bash
vim nicholas/services/identity/auth.go
```

### 2.3 Create Changeset

```bash
gs cs create
```

### 2.4 Update Changeset

```bash
gs cs update
```

### 2.5 Submit

```bash
gs cs submit
```

Server behavior:

```text
1. Resolve changed absolute paths.
2. Resolve covering slices.
3. Select required account queues.
4. Refresh overlap and queue policy requirements.
5. Acquire required queue leases.
6. Check slice roles and approvals.
7. Rebase onto latest target ref.
8. Run required checks.
9. Create commit or commits.
10. Update ref with CAS.
11. Emit indexing events.
```

## 3. Example Git Workflow

```bash
git clone https://gitslice.io/git/nicholas/identity.git
cd identity
git checkout -b my-change
# edit files
git commit -am "Update auth flow"
git push origin HEAD:refs/changes/new
```

Server behavior:

```text
1. Resolve slice from URL.
2. Authenticate and authorize user.
3. Convert Git diff to global absolute paths.
4. Resolve covering slices.
5. Select required account queues.
6. Create changeset.
7. Create patchset.
8. Run validation.
```

## 4. Initial Non-Goals

The initial implementation should not include:

- special `/shared` or `/system` namespaces
- custom mount aliases inside slices
- direct user-facing commit creation
- single-owner path model
- object-store participation in metadata transactions
- path-level ACLs as the primary access model
- Git-native storage internals
- distributed atomic commits across slices or target refs

These can be revisited only if a concrete product requirement justifies the
additional complexity.
