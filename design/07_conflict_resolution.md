# Gitslice Conflict Resolution And Batched Submit Design

This document defines how Gitslice detects conflicts, tracks per-path base
predicates, batches independent changesets, restacks dependent changesets,
and applies merge-on-submit and `.gitslice/merge.yaml` rules.

Related documents:

- [01_gitslice_architecture_design.md](01_gitslice_architecture_design.md): system model and submit overview
- [02_storage.md](02_storage.md): commit publication, CAS, and storage transactions
- [03_core_api.md](03_core_api.md): patchset and submit proto shapes
- [06_indexing.md](06_indexing.md): changed-path and materialized-head indexes
- [15_stacked_changesets.md](15_stacked_changesets.md): dependent changesets workflow

## 1. Goals

The conflict model should:

- allow independent changesets to land without unnecessary full rebases
- keep one accepted target ref linear and easy to audit
- support batching multiple ready changesets into one target-ref update
- make every conflict reason explicit and reproducible
- keep semantic correctness delegated to required checks

The model does not introduce cross-slice changesets or atomic multi-slice
submission. Each changeset has exactly one authoring slice, and the server
rejects any patchset whose edits are outside that slice.

## 2. Core Idea

A patchset records the base predicate for every path it depends on.

For each relevant path, store a `PathBase` predicate:

```text
path
base_commit_id
check (check_kind)
exists
entry_kind
mode
blob_id
content_hash
tree_id
symlink_target
entry_fingerprint
```

At submit time, the server reads the current accepted path state and verifies:

```text
current path state satisfies patchset base predicate(path)
```

For a modified file, the predicate is an exact entry fingerprint match. For a
created file, the predicate is "destination is still missing" and "parent is
still a directory." This avoids false conflicts between independent creates in
the same directory while still catching parent deletes or file/directory
replacement.

If every predicate holds, the patchset can be applied without a user-visible
rebase for those paths.

The changeset-level `base_commit_id` is still useful for review and audit, but
the correctness predicate is path-level freshness expressed as base predicates.

## 3. Read Sets And Write Sets

Each patchset stores two sets:

```text
write_set
: Paths the patchset creates, modifies, deletes, renames, or changes mode for.

read_set
: Paths whose current state must satisfy the patchset's recorded base
  predicates.
```

Currently, the read set includes:

- every path in the write set
- old and new paths for renames
- parent directories for creates, deletes, and renames

Submit freshness also checks non-path inputs:

- the authoring slice definition hash (`source_slice_definition_hash`)

Write sets are compared with path-prefix semantics. A delete of `/acme/lib`
conflicts with a modify under `/acme/lib/a.go`, even though the exact strings
differ.

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
The authoring slice definition or submit settings changed.
```

Semantic conflict:

```text
The path-level predicates pass, but required checks fail because the combined
accepted state is behaviorally invalid.
```

Semantic conflicts are not solved by the storage layer. They are surfaced
through required checks.

## 5. Submit Admission With Path-Head CAS

The write-path correctness boundary is submit admission, not final root
publication. Admission uses a durable `path_heads` table that represents the
latest accepted logical state for each touched path. This can be ahead of
`refs/global/main` while accepted changes are waiting for batch publication.

For one changeset, submit admission runs:

```text
1. Load the current target-ref head H for audit and fallback path-head
   initialization.
2. Load the current patchset (rejecting patchsets with unresolved conflicts).
3. Recompute changed paths, read set, write set, coverage, and submit requirements.
4. Verify authoring slice containment and permissions.
5. Verify approvals and required checks are fresh for the current patchset.
6. For every read-set path, lock or initialize the corresponding `path_heads`
   row.
7. Verify every path-head row satisfies the patchset's recorded base predicate.
8. Update `path_heads` to the post-patch fingerprints for the write set.
9. Append a durable `pending_publish` row.
10. Mark the changeset `pending_publish` and clear `submit_blocked_reason` /
    `submit_blocked_code`.
11. Commit the admission transaction.
```

If admission fails (steps 2, 4, 5, or 7), the changeset remains `open` and the
server records the structured blocker in `changesets.submit_blocked_reason` and
`changesets.submit_blocked_code` (see §11).

`path_heads` stores tombstones for accepted deletes. That distinction matters:
absence of a row means the path has not been initialized in the path-head
index, while an existing tombstone means the accepted logical state is
definitely missing. This prevents a stale update from passing just because the
delete has not yet been published to the root/ref.

After admission, the change is accepted but not yet visible through root-based
reads, Git projection, or `refs/global/main`. Clients that need synchronous UX
(`gs submit` default) wait until the publisher marks the changeset `submitted`.

## 6. Batched Async Publish

Batching is an optimization for a hot target ref. It does not change the
changeset model or the admission correctness boundary.

A batch can contain independently accepted changesets from different authoring
slices, but the batch is not a user-visible cross-slice changeset. Each
changeset keeps its own authoring slice, approvals, checks, commit, and audit
record.

A publish worker groups pending changesets that share a `target_ref` when:

- every candidate already passed admission, review, approval, and required checks
- every candidate still has a durable `pending_publish` row in `pending` state
- candidates are ordered deterministically from their admission sequence

Normal same-path conflicts have already been rejected by path-head CAS. The
publisher still verifies operational invariants such as pending-row status,
target-ref identity, and target-ref CAS.

Inside the target-ref sequencer:

```text
1. Reload and lock current target-ref head H.
2. Select pending rows in admission sequence order.
3. Load each patchset and apply candidates in order, producing a commit chain:

   H -> C1 -> C2 -> C3

4. Publish the commit chain and CAS target_ref from H to C3 in one metadata
   transaction.
5. Mark each included changeset `submitted` with its corresponding commit id.
6. Mark each included `pending_publish` row `published`.
7. Write `commit_published` outbox rows (see design/06_indexing.md).
```

The target ref moves once, but each changeset still gets its own commit and
audit record. This preserves review traceability while reducing target-ref CAS
pressure.

## 7. Why Disjoint Writes Are Not Enough

Two changesets can write different files and still conflict semantically:

- one changes a public API and another changes a caller
- one changes a build manifest and another changes a generated file
- one changes a schema and another changes a migration

Currently, required checks catch these cases.

### 7.1 Approval Preservation Across Rebases (Unbuilt / Future Work)

Approvals are currently bound to a specific `patchset_id`. Creating a new
patchset via `gs sync`, `gs modify`, restack, or merge-on-submit does **not**
auto-forward existing approvals, so slices that require approvals need fresh
approval on the new patchset (matching §7.4).

Automatic approval forwarding for semantically identical rebase diffs (same
paths, content hashes, operations, and unchanged submit requirements/slice
definition) remains unbuilt future work.

### 7.2 Workspace Sync And Rebase Patchsets

`gs sync` rebases the current workspace and its associated draft changeset onto
the latest accepted target-ref head. It updates the workspace even when
conflicts exist, so the user has the latest remote context to resolve them.

Sync compares three states:

```text
B: the workspace base snapshot before sync
L: the current local workspace contents
R: the latest remote slice projection
```

The merge result is written back to the workspace. Non-conflicting remote
updates are applied, non-conflicting local edits are preserved, and conflicting
paths are materialized as explicit local conflicts. The workspace conflict
record (`.gs/conflicts.json`) records:

- old base commit `B` and new base commit `R`
- path and conflict class
- local, remote, and base fingerprints/content hashes

If a draft changeset is associated with the workspace, sync creates a new
patchset on that changeset recording the cleanly rebased edits plus conflict
metadata:

```text
v1: user patchset on base A
v2: user patchset on base A
v3: sync/rebase patchset onto latest base B with conflicts
v4: user-resolved patchset on base B
```

Patchset review diffs use each patchset's nearest base as the canonical old
side (`v1`/`v2` diff against `A`; `v3`/`v4` diff against `B`).

A patchset with unresolved sync conflicts is not submittable. Submit admission
rejects it before path-head CAS and reports the unresolved paths. After the
user edits the workspace and runs `gs modify`, the CLI validates that conflict
markers or side metadata have been resolved, creates the next patchset, and
clears `.gs/conflicts.json`.

### 7.3 Restacking A Dependency Tree

`gs sync` of a workspace whose draft lives in a dependency tree (`gs create`)
restacks the tree on the server (`ChangesetStackService.Restack`): each
changeset gets a patchset on the new head (a root) or on its parent's new
patchset. Because a patchset's edits are whole files, restack compares each
edited path's recorded base (`PathBase.entry_fingerprint`) with the new base:

- unchanged upstream: the edit is replayed;
- a text file both sides changed: a three-way line merge (`diffutil.Merge3`,
  base, the patchset's version, the new base's). A clean merge becomes the
  edit; overlapping changes fall back to `.gitslice/merge.yaml` (§7.5) if a
  rule matches, otherwise are written with conflict markers
  (`<<<<<<< changeset`, `=======`, `>>>>>>> upstream`) and recorded as a conflict;
- anything else both sides changed (binary or over 4 MiB, deleted on one side,
  added on both, a move): a conflict that keeps the patchset's version (unless
  a merge rule resolves it).

Conflicts make the new patchset a `conflict` patchset: it keeps every edit so
the workspace shows the markers, and it cannot be submitted (`unresolved
patchset conflicts`) until a new patchset (`gs modify`) replaces it. Unresolved
conflicts are carried through later restacks, and `gs sync` lists the
conflicted paths and exits with code `3`.

### 7.4 Merge On Submit

A changeset whose base went stale is refused at submit (`stale_path_base`:
`path base conflict, refresh or rebase the changeset`): its author syncs, which
merges upstream in, and submits again. On a busy slice, agents bounce on this.
A slice with `merge_on_submit` (a slice setting outside the definition, so no
new definition version: `gs slice update --merge-on-submit on|off`, or Settings
in the web app; owners and admins) lets the server do that merge at submit
instead:

- The submit is tried as always. Only the stale-base refusal triggers a merge;
  a missing approval or check does not.
- The current patchset is taken onto the latest head of the target ref by the
  restack merge (§7.3, `rebaseEdits`, honoring `.gitslice/merge.yaml` rules in
  §7.5): unchanged-upstream edits are replayed, and text files both sides
  changed are merged line by line.
- Any conflict refuses the submit with the paths (up to five, then a count) and
  makes no patchset: overlapping changes need the author, in a workspace where
  they can be tested (`gs sync`).
- A clean merge becomes a new patchset of kind `submit_merge`, created via
  `UpdateChangeset`, so the slice's CI daemon runs the required checks on it.
  Because approvals belong to patchsets and §7.1 approval forwarding is not
  built, a slice that requires approvals needs them again — the merged code is
  not what was approved.
- The merge is then submitted. Its checks have usually not reported yet, so the
  usual answer is a pending check, reported after `"the server merged the
  changeset onto the latest base as patchset N, which runs CI again"`.
  `gs submit` waits for checks and submits again as usual; a submit that still
  names the patchset it started with is taken to mean the server's merge of it.
- Only a changeset that depends on no other (a plain changeset or the root of
  a dependency tree) is merged; dependents follow their root by restack. A
  patchset with unresolved conflicts is never auto-merged.

What lands includes upstream's changes, so the workspace's files are not the
new base: `gs submit` notes that the server merged the changeset and leaves the
workspace's base as it was, for `gs sync` to bring the merge in cleanly.

### 7.5 Merge Rules For Lockfiles And Generated Files

Some files merge badly line by line but have an obvious resolution: `go.sum`
is a set of lines, and a lockfile or generated file should take the newest
version. A folder can configure this in `.gitslice/merge.yaml`:

```yaml
version: 1
rules:
  - paths: ["go.sum", "**/go.sum"]
    merge: union      # the new base's lines, minus those the change removed, plus those it added
  - paths: ["**/package-lock.json", "**/*.pb.go"]
    merge: newest     # the new base's version
  - paths: ["docs/generated/**"]
    merge: ours       # the change's version
```

- Patterns are doublestar globs relative to the folder holding `.gitslice`.
  The nearest `merge.yaml` above a path decides, and in it the first matching
  rule wins.
- Rules apply only where a line merge would leave conflict markers, both on
  the server (restacking a dependency tree, merge on submit) and in `gs sync`.
- They are read from the new base, which has landed: a change cannot set the
  rules of its own merge.
- `regenerate:<check>` (take the newest, then rebuild the file with a check)
  is not supported: nothing writes a check's output back into a change yet.
  Use `newest` with a check that fails when the file is stale.

Code: `internal/mergerules`; `service/restack_merge.go` (server);
`internal/cli/sync_merge_rules.go` (`gs sync`).

## 8. Interaction With Overlapping Slices

Overlapping slices do not duplicate files. They project the same global path.

If slice X and slice Y both include `/acme/lib/a.go`, a submit from slice X
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

Indexes accelerate lookups but do not decide submit correctness. Submit
admission always verifies `path_heads` rows and the latest authoritative slice
definition. `current_path_entities` (gated by `ref_materialized_heads`) can
accelerate initial path-base construction when fresh, falling back to tree
reads otherwise.

## 10. Storage Requirements

Storage supports publishing either one commit or a commit chain with a single
target-ref CAS update. For a batched submit, the metadata transaction is
all-or-nothing:

```text
verify ref still points at H
write C1, C2, C3 metadata
move target_ref from H to C3
write commit_published outbox rows
commit transaction
```

If the CAS compare fails, none of the commits in the batch become reachable
through the target ref.

## 11. User-Facing Blocked States (`submit_blocked_reason` & `submit_blocked_code`)

A changeset's lifecycle `status` stays one of `open`, `pending_publish`,
`submitted`, or `abandoned`. When submit admission is refused, the changeset
remains `open` and records a human-readable `submit_blocked_reason` (exposed on
`Changeset.submit_blocked_reason` in `proto/core/v1/changeset.proto`) alongside
a structured `submit_blocked_code` in PostgreSQL (`0062_submit_blocked_code.sql`):

| `submit_blocked_code` | Meaning |
|---|---|
| `stale_path_base` | One or more touched paths changed on the target ref since the patchset's base (`path base conflict`). User runs `gs sync` (or slice uses `merge_on_submit`). |
| `requirements_changed` | Authoring slice definition or submit settings changed since the patchset was created, or paths are outside the latest slice definition. |
| `approvals_missing` | Required approvals, required human approvals, path owners, or `.gitslice` approval rules are not yet satisfied on the current patchset. |
| `checks_missing` | One or more required checks have no passing result on the current patchset. |
| `blocked_on_parent` | Dependent changeset cannot submit before its base changeset (`BlockedOnBaseChangeset`). |

When a patchset has unresolved sync/restack conflicts, submit admission also
refuses before path-head CAS (`unresolved patchset conflicts`) until `gs modify`
replaces the patchset.

## 12. Future Work

- **Path locks (`path_locks`)**: explicit administrative path-lock records and `source_path_lock_set_hash` validation during submit admission (the proto fields exist as placeholders, but path-lock enforcement is not yet built).
- **Dependency-derived read sets**: expanding patchset read sets from build/test dependency graphs (generated file inputs, build manifests, API/schema consumers) to turn semantic conflicts into deterministic freshness checks.
- **Approval forwarding across clean rebases**: §7.1 auto-forwarding when a rebase patchset has an identical diff and unchanged submit requirements.
