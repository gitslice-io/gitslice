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

## 5. Single Changeset Submit

For one changeset, submit should run:

```text
1. Load current target-ref head H.
2. Load the current patchset.
3. Recompute changed paths, read set, write set, coverage, and submit requirements.
4. Verify authoring slice containment and permissions.
5. Verify approvals and required checks are fresh for the current patchset.
6. Verify every read-set predicate is satisfied by H.
7. Apply the patchset to H.
8. Create commit C.
9. CAS target_ref from H to C.
10. Emit indexing events.
```

If step 6 fails, the changeset becomes `NeedsRebase`,
`MergeConflict`, or `NeedsRequirementRefresh` depending on the failing
predicate.

If step 9 fails, the submitter reloads the new head and repeats freshness
validation. CAS failure alone is not data loss; it means another writer moved
the target ref first.

## 6. Batched Submit

Batching is an optimization for a hot target ref. It does not change the
changeset model.

A batch can contain independently valid changesets from different authoring
slices, but the batch is not a user-visible cross-slice changeset. Each
changeset keeps its own authoring slice, approvals, checks, commit, and audit
record.

A submit worker can group ready changesets that share a `target_ref` when:

- every candidate already passed review, approval, and required checks
- every candidate's submit requirements are fresh
- candidate write sets are pairwise disjoint under path-prefix comparison
- candidate read sets are compatible with each other
- all path base predicates are satisfied by the current target-ref head

Compatibility means no included candidate's write set invalidates another
included candidate's read predicates. Two sibling file creates can be compatible
when both only require the parent path to remain a directory; a delete of that
parent is not compatible.

The worker then enters the target-ref sequencer once and revalidates the whole
candidate set against the latest head.

Inside the sequencer:

```text
1. Reload current target-ref head H.
2. Revalidate each candidate's read-set predicates against H.
3. Remove candidates that no longer pass freshness validation.
4. Sort the remaining candidates deterministically.
5. Verify that no candidate write invalidates another included candidate's read
   predicates.
6. Apply candidates in order, producing a commit chain:

   H -> C1 -> C2 -> C3

7. Publish the commit chain and CAS target_ref from H to C3 in one metadata
   transaction.
8. Mark each included changeset submitted with its corresponding commit id.
9. Return excluded candidates to an open refresh/retry state.
```

The target ref moves once, but each changeset still gets its own commit and
audit record. This preserves review traceability while reducing target-ref CAS
pressure.

The deterministic order should use a topological sort based on read/write set
intersections:

```text
1. Build a directed graph where an edge exists from Ci to Cj if the read set
   of Ci intersects the write set of Cj.
2. Sort topologically. The edge means Ci must be applied before Cj, so readers
   of a path are ordered before writers of that path within the same batch.
3. If a cycle is detected, partition the batch or exclude conflicting
   candidates.
4. Within each topological level, break ties with submit_ready_at, changeset_id.
```

This prevents false-positive read-set invalidations that occur with naive
chronological ordering. If Changeset A modifies `/lib/utils.go` and Changeset B
reads `/lib/utils.go`, sorting them arbitrarily might cause B's read predicate
to fail if A is applied first.

Fairness should remain outside the correctness model but can be applied as a
tiebreaker within topological levels.

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
patchset id.

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
