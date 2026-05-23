# Gitslice Execution Plan

This document tracks implementation phases and workflow validation for Gitslice.
Product context is in [00_product.md](00_product.md), architecture is in
[01_gitslice_architecture_design.md](01_gitslice_architecture_design.md), the
storage design is in [02_storage.md](02_storage.md), and the gRPC API boundary is
in [03_core_api.md](03_core_api.md). CLI behavior is in
[04_cli_design.md](04_cli_design.md). Git compatibility is in
[05_git_compatibility.md](05_git_compatibility.md). Indexing details are in
[06_indexing.md](06_indexing.md). Conflict resolution and batched submit are in
[07_conflict_resolution.md](07_conflict_resolution.md).

## 1. MVP Phases

### Phase 1: Native Object Model

- PostgreSQL schema and migrations for source-of-truth metadata
- Cloudflare R2 bucket layout and credentials
- Content-addressed blob store
- Immutable tree metadata
- Canonical path rules
- Global commit graph
- Atomic refs
- Staged blob upload protocol
- Reachability roots for GC

Exit criteria:

- PostgreSQL tables, constraints, and critical indexes exist for refs, commits,
  trees, blobs, and outbox events.
- R2 blob keys are content-addressed and uploads are verified by hash before
  metadata can reference them.
- Blobs can be uploaded, verified, and referenced by metadata.
- Trees and commits are immutable and content-addressed where required.
- Refs can be updated only through CAS.
- Canonical path validation is shared by storage and API layers.
- Storage can enumerate accepted refs, patchsets, staged blob leases, and cache
  leases as GC roots.

### Phase 2: Slice Definitions And Projection

- User and organization accounts with globally unique slugs
- Slice identity
- Slice definitions as versioned metadata
- Absolute included paths
- Slice-level visibility and roles
- Overlapping slice coverage
- Deterministic projection by latest definition

Exit criteria:

- A slice can project a deterministic tree from the global commit graph.
- Slice definition changes create new auditable definition versions.
- Overlapping slices resolve covering slices for changed paths.
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
- Changeset creation rejects local diffs that are not contained by one
  authoring slice.
- Workspace state does not require cloning the full global source graph.
- Local operations can be inspected through `gs op log`.
- `gs cs create` and `gs cs update` produce server patchsets from local snapshots.

### Phase 4: Changesets And Direct Submit

- Changeset creation
- Patchsets
- Review state
- Conflict detection
- Per-path base predicates
- Patchset read/write sets
- Covering-slice refresh
- Submit requirement resolution
- Required approvals and checks
- Per-target-ref landing sequencer
- Batched submit for compatible read/write sets
- Atomic ref update

Exit criteria:

- A changeset is scoped to one authoring slice.
- Each update creates an immutable patchset.
- Patchsets record path base predicates, read sets, and write sets.
- Submit recomputes coverage, submit requirements, and approvals.
- Submit finalization is serialized per target ref after validation.
- Multiple compatible changesets can publish as one commit chain and one ref CAS.
- Submit publishes through CAS or fails without moving the target ref.

### Phase 5: Git Read Compatibility

- Git smart HTTP endpoint
- Clone from slice URL
- Synthetic Git history
- Fetch
- Partial clone support
- Packfile/projection cache
- Native large-blob projection rules

Exit criteria:

- A slice can be cloned from its Git URL.
- Projected Git commits are stable for the same projection inputs.
- Fetch and partial clone operate through projected refs and trees.
- Cached projection artifacts are keyed by all Git-visible projection inputs.

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
- Slice coverage index
- Submit requirement provenance index
- Patchset read/write set index
- Build/test integration
- Regional reads
- Projection cache
- Distributed GC
- Advanced replication

Exit criteria:

- Indexes can be rebuilt from committed source-of-truth objects.
- CI requirements can be selected from slice submit settings and build/test indexes.
- Batched submit candidate selection can use indexes, but final submit
  revalidates path predicates from source-of-truth state.
- GC can delete unreachable staged blobs and projection artifacts only after
  reachability recheck and grace periods.
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
2. Verify all changed paths are contained by the authoring slice.
3. Resolve covering slices for overlap metadata.
4. Record path base predicates, read set, and write set.
5. Resolve submit requirements from the authoring slice definition and path locks.
6. Refresh overlap and submit requirements.
7. Check authoring-slice roles and approvals.
8. Run required checks.
9. Hand off to the target-ref landing sequencer.
10. Revalidate path predicates, submit requirements, checks, and conflicts.
11. Rebase or apply onto latest target ref.
12. Create commit or batched commit chain.
13. Update ref with CAS.
14. Emit indexing events.
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
4. Verify every changed path is inside the URL's authoring slice.
5. Resolve covering slices for overlap metadata.
6. Resolve submit requirements.
7. Create changeset.
8. Create patchset.
9. Run validation.
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
- cross-slice changesets
- distributed atomic commits across slices or target refs

These can be revisited only if a concrete product requirement justifies the
additional complexity.
