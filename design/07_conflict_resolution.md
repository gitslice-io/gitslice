# Gitslice Conflict Resolution And Batched Submit Design

This document defines how Gitslice detects conflicts, tracks per-path base
predicates, and batches independent changesets while preserving correctness.

Related documents:

- [01_gitslice_architecture_design.md](01_gitslice_architecture_design.md): system model and submit overview
- [02_storage.md](02_storage.md): commit publication, CAS, and storage transactions
- [03_core_api.md](03_core_api.md): patchset and submit proto shapes
- [06_indexing.md](06_indexing.md): changed-path and freshness indexes

## 1. Goals

The conflict model should:

- allow independent changesets to land without unnecessary full rebases
- keep one accepted target ref linear and easy to audit
- support batching multiple ready changesets into one target-ref update
- make every conflict reason explicit and reproducible
- keep semantic correctness delegated to required checks and dependency indexes

The model must not introduce cross-slice changesets or atomic multi-slice
submission. Each changeset has exactly one authoring slice, and the server
rejects any patchset whose edits are outside that slice.

## 2. Core Idea

A patchset records the base predicate for every path it depends on.

For each relevant path, store a path base predicate:

```text
path
base_commit_id
check_kind
exists
entry_kind
mode
content_hash
tree_id
symlink_target
entry_fingerprint
```

At submit time, the server reads the current target-ref head and verifies:

```text
current path state satisfies patchset base predicate(path)
```

For a modified file, the predicate is usually an exact entry fingerprint match.
For a created file, the predicate is usually "destination is still missing" and
"parent is still a directory." This avoids false conflicts between independent
creates in the same directory while still catching parent deletes or
file/directory replacement.

If every predicate holds, the patchset can be applied without a user-visible
rebase for those paths.

The changeset-level `base_commit` is still useful for review and audit, but it
is not the only conflict predicate. The correctness predicate is path-level
freshness expressed as base predicates.

## 3. Read Sets And Write Sets

Each patchset stores two sets.

```text
write_set
: Paths the patchset creates, modifies, deletes, renames, or changes mode for.

read_set
: Paths whose current state must satisfy the patchset's recorded base
  predicates.
```

For the MVP, the read set should include at least:

- every path in the write set
- old and new paths for renames
- parent directories for creates, deletes, and renames

Submit freshness should also record non-path inputs:

- the authoring slice definition hash
- the active path-lock-set hash for locks intersecting the write set

Later, read sets can expand from build/test dependency analysis:

- generated file inputs
- build manifests
- API/schema files consumed by changed code
- language-specific dependency graph entries

Write sets must be compared with path-prefix semantics. A delete of
`/acme/lib` conflicts with a modify under `/acme/lib/a.go`, even though the
exact strings differ.

## 4. Conflict Classes

File content conflict:

```text
The current fingerprint for a modified file differs from the patchset's base
fingerprint.
```

Path existence conflict:

```text
The patchset expected a path to exist or not exist, but the current head has the
opposite state.
```

Directory conflict:

```text
A parent directory was deleted, replaced, or renamed while the changeset was
open.
```

Rename conflict:

```text
The old path, new path, or either parent directory no longer matches the
patchset's recorded base state.
```

Mode or symlink conflict:

```text
The entry still exists, but its mode, entry kind, or symlink target changed.
```

Requirement refresh:

```text
The authoring slice definition, submit settings, or active path locks changed.
```

Semantic conflict:

```text
The path-level predicates pass, but required checks fail because the combined
accepted state is behaviorally invalid.
```

Semantic conflicts are not solved by the storage layer. They are surfaced
through required checks and, later, dependency-aware read sets.

## 5. Submit Admission With Path-Head CAS

The write-path correctness boundary is submit admission, not final root
publication. Admission uses a durable `path_heads` table that represents the
latest accepted logical state for each touched path. This can be ahead of
`refs/global/main` while accepted changes are waiting for batch publication.

For one changeset, submit admission should run:

```text
1. Load the current target-ref head H for audit and fallback path-head
   initialization.
2. Load the current patchset.
3. Recompute changed paths, read set, write set, coverage, and submit requirements.
4. Verify authoring slice containment and permissions.
5. Verify approvals and required checks are fresh for the current patchset.
6. For every read-set path, lock or initialize the corresponding `path_heads`
   row.
7. Verify every path-head row satisfies the patchset's recorded base predicate.
8. Update `path_heads` to the post-patch fingerprints for the write set.
9. Append a durable `pending_publish` row.
10. Mark the changeset `pending_publish`.
11. Commit the admission transaction.
```

If step 7 fails, the changeset becomes `NeedsRebase`,
`MergeConflict`, or `NeedsRequirementRefresh` depending on the failing
predicate.

`path_heads` stores tombstones for accepted deletes. That distinction matters:
absence of a row means the path has not been initialized in the path-head
index, while an existing tombstone means the accepted logical state is
definitely missing. This prevents a stale update from passing just because the
delete has not yet been published to the root/ref.

After admission, the change is accepted but not yet visible through root-based
reads, Git projection, or `refs/global/main`. Clients that need the existing
synchronous UX may wait until the publisher marks the changeset `submitted`.

## 6. Batched Async Publish

Batching is an optimization for a hot target ref. It does not change the
changeset model or the admission correctness boundary.

A batch can contain independently accepted changesets from different authoring
slices, but the batch is not a user-visible cross-slice changeset. Each
changeset keeps its own authoring slice, approvals, checks, commit, and audit
record.

A publish worker can group pending changesets that share a `target_ref` when:

- every candidate already passed admission, review, approval, and required
  checks
- every candidate still has a durable `pending_publish` row in `pending` state
- candidates can be ordered deterministically from their admission sequence

Normal same-path conflicts have already been rejected by path-head CAS. The
publisher still verifies operational invariants such as pending-row status,
target-ref identity, and target-ref CAS. It does not need to rediscover ordinary
same-path conflicts unless the system intentionally supports multiple admission
sources that bypass `path_heads`.

The worker then enters the target-ref sequencer once for the batch.

Inside the sequencer:

```text
1. Reload and lock current target-ref head H.
2. Select pending rows in admission sequence order.
3. Load each patchset and apply candidates in order, producing a commit chain:

   H -> C1 -> C2 -> C3

4. Publish the commit chain and CAS target_ref from H to C3 in one metadata
   transaction.
5. Mark each included changeset `submitted` with its corresponding commit id.
6. Mark each included `pending_publish` row `published`.
7. Emit indexing and projection invalidation events.
```

The target ref moves once, but each changeset still gets its own commit and
audit record. This preserves review traceability while reducing target-ref CAS
pressure.

Admission sequence is the MVP order. Later dependency-aware read sets can add a
topological ordering layer, but it must preserve the invariant that accepted
path-head state is the source of truth for write conflicts.

## 7. Why Disjoint Writes Are Not Enough

Two changesets can write different files and still conflict semantically.

Examples:

- one changes a public API and another changes a caller
- one changes a build manifest and another changes a generated file
- one changes a schema and another changes a migration

For the MVP, required checks catch these cases. As dependency indexes mature,
Gitslice can add dependency-derived paths to each patchset's read set. That
turns some semantic conflicts into deterministic freshness checks.

## 7.1 Approval Preservation Across Rebases

When a rebase creates a new patchset, existing approvals are not automatically
invalidated. If the new patchset's write-set diff is semantically identical to
the previously approved patchset's diff (same paths, same content hashes, same
operations), the system auto-forwards approvals to the new patchset.

This prevents unnecessary re-review cycles when target refs move frequently but
the changeset's actual changes are unaffected.

Approvals are invalidated only when:

- the file content diff changes (new paths added, content modified)
- submit requirements change (definition hash or path lock set hash)
- the authoring slice definition changes

## 7.2 Workspace Sync And Rebase Patchsets

`gs sync` rebases the current workspace and its associated draft changeset onto
the latest accepted target-ref head. It must be allowed to update the workspace
even when conflicts exist, because the user needs the latest remote context in
order to resolve those conflicts.

The sync operation compares three states:

```text
B: the workspace base snapshot before sync
L: the current local workspace contents
R: the latest remote slice projection
```

The merge result is written back to the workspace. Non-conflicting remote
updates are applied, non-conflicting local edits are preserved, and conflicting
paths are materialized as explicit local conflicts. The workspace conflict
record must include enough information to reproduce the decision:

- old base commit `B`
- new base commit `R`
- path
- conflict class
- local fingerprint or tombstone
- remote fingerprint or tombstone
- base fingerprint or tombstone when available
- any side-variant blob ids needed for inspection or recovery

If a draft changeset is associated with the workspace, sync creates a new
patchset on that changeset. This patchset represents a rebase attempt onto the
new base, not a plain snapshot of the remote tree. It records the cleanly
rebased edits plus conflict metadata. For example:

```text
v1: user patchset on base A
v2: user patchset on base A
v3: sync/rebase patchset onto latest base B with conflicts
v4: user-resolved patchset on base B
```

Patchset review diffs use each patchset's nearest base as the canonical old
side. In the example above, `v1` and `v2` diff against base `A`, while the sync
patchset `v3` and resolved patchset `v4` diff against base `B`. This keeps the
sync patchset focused on the local overlay after rebasing. It does not need to
carry all remote-only changes from `A` to `B`, and the MVP does not require a
complete arbitrary diff between any two materialized snapshots across a base
transition.

A patchset with unresolved sync conflicts is not submittable. Submit admission
must reject it before path-head CAS and report the unresolved paths. After the
user edits the workspace and runs `gs cs update`, the CLI validates that the
conflict markers or side metadata have been resolved, creates the next patchset,
and clears the conflict state.

This keeps conflict resolution auditable without introducing Git's hidden
interrupted-operation state. The durable changeset history shows both the rebase
attempt and the user-authored resolution.

## 8. Interaction With Overlapping Slices

Overlapping slices do not duplicate files. They project the same global path.

If slice X and slice Y both include `/acme/lib/a.go`, then a submit from slice X
that changes `/acme/lib/a.go` updates the accepted global target ref. Slice Y
sees the new content the next time it syncs or fetches its projection.

Conflict checks are path-based, not slice-based:

```text
same global path + stale path predicate -> conflict
different global paths + compatible read/write sets -> can batch
```

The authoring slice controls write authorization, reviewer selection, approvals,
and submit requirements for its changeset. Covering slices affect visibility,
projection invalidation, and conflict reporting only.

## 9. Index Support

Indexes accelerate candidate discovery but do not decide correctness.

Useful indexes:

- changed-path index
- path history index
- open changeset write-set index
- patchset read-set predicate index
- submit requirement freshness index
- dependency-derived read-set index later

Submit must always revalidate from source-of-truth state or from indexes proven
fresh for the exact target-ref head, slice definition hash, path lock state, and
canonical patchset identity.

## 10. Storage Requirements

Storage must support publishing either:

- one commit and one target-ref CAS update
- a commit chain and one target-ref CAS update

For a batched submit, the metadata transaction must be all-or-nothing:

```text
verify ref still points at H
write C1, C2, C3 metadata
move target_ref from H to C3
write outbox events
commit transaction
```

If the CAS compare fails, none of the commits in the batch become reachable
through the target ref.

## 11. User-Facing States

Recommended submit failure states:

```text
NeedsRebase
: The patch can likely be replayed against the new head.

MergeConflict
: A touched path changed in a way that requires user resolution.

NeedsRequirementRefresh
: Slice submit settings, active path locks, or authorization inputs changed.

Failed
: Required checks failed or an invariant was violated.
```

The CLI should show the path-level reason where possible:

```text
/acme/lib/a.go changed since patchset 3
expected content hash: h1
current content hash:  h2
```
