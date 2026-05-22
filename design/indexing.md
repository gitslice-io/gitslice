# Gitslice Indexing Design

This document defines Gitslice derived indexes, indexing events, freshness
semantics, and rebuild rules. The top-level architecture is in
[gitslice_architecture_design.md](gitslice_architecture_design.md), storage is
in [storage.md](storage.md), and rollout phases are in
[execution_plan.md](execution_plan.md).

## 1. Indexing Goals

Indexes are derived data. They should make reads, search, review, policy
resolution, build selection, and projection fast, but they must not become the
source of truth.

The source of truth remains:

```text
Refs
Commits
Trees
Blobs
Slice definitions
Folder policy files
Queue definition files
```

If an index is missing or stale, the system should be able to rebuild it from
those source objects.

## 2. Required Indexes

Initial required indexes:

- code search
- symbol search
- path history
- slice coverage
- folder policy coverage
- queue selection
- build graph
- test graph
- slice projection
- changed paths

### 2.1 Code Search

Supports text search over visible source files. It must enforce slice visibility
and should return freshness metadata with results.

### 2.2 Symbol Search

Supports language-aware symbol lookup where available. Symbol extraction is
best-effort and can lag behind commit acceptance.

### 2.3 Path History

Maps paths to commits that touched those paths. Rename handling can start as
path-based and later add richer rename detection.

### 2.4 Slice Coverage

Maps global paths to slices whose latest accepted `included_paths` cover those
paths.

```text
covering_slices(path, definition_epoch) -> []slice_id
```

This index accelerates authorization, review routing, projection invalidation,
and queue selection. It is derived from slice definitions.

### 2.5 Folder Policy Coverage

Maps global paths to matching ancestor `.gitslice/policy.yaml` files.

```text
matching_policy_files(path, commit_id) -> []policy_file
```

This index accelerates policy resolution. It is derived from committed tree
state and can be rebuilt by scanning `.gitslice/policy.yaml` files.

### 2.6 Queue Selection

Maps changed paths and authoring account to queue rules from:

```text
/{account}/.gitslice/queues/*.yaml
```

Queue selection must be recomputed before submit against the latest target ref.
The index is an accelerator, not an authority.

### 2.7 Build And Test Graphs

Build and test indexes support affected target calculation and required check
selection.

These indexes may depend on language-specific analyzers. They should degrade to
broader check selection if stale or unavailable.

### 2.8 Slice Projection

Projection indexes cache deterministic slice projections:

```text
(slice_id, slice_definition_hash, global_commit_id) -> projected_tree_id
(slice_id, slice_definition_hash, global_commit_id) -> synthetic_git_commit_id
```

Projection caches can be invalidated or lazily rebuilt when slice definitions or
commits change.

### 2.9 Changed Paths

Changed-path indexes support review summaries, queue routing, CI selection, and
incremental index updates.

## 3. Event Pipeline

Each accepted metadata transition emits events.

Core events:

```text
CommitCreated
RefUpdated
FileChanged
DirectoryChanged
SliceDefinitionChanged
FolderPolicyChanged
QueueDefinitionChanged
SliceProjectionInvalidated
SymbolIndexNeeded
BuildGraphInvalidated
TestGraphInvalidated
```

Submit workers should emit indexing events after a successful ref update. Events
must include enough ids to make handlers idempotent:

```text
event_id
commit_id
parent_commit_ids[]
target_ref
changed_paths[]
affected_slice_ids[]
policy_file_paths[]
queue_ids[]
created_at
```

Async workers consume events and update derived indexes.

## 4. Freshness Model

Index-backed APIs should expose freshness:

```text
fresh
stale_but_usable
unavailable
```

Fresh means the index is known to include the relevant commit, slice definition,
and policy file versions.

Stale-but-usable means results may omit the latest changes, but they are still
safe to display for non-authoritative UX such as search. Stale indexes must not
be used as the final authority for submit validation.

Unavailable means the service should fall back to source-of-truth reads where
possible or return a structured unavailable response.

## 5. Submit-Time Rules

Submit validation must recompute authoritative requirements from source-of-truth
state or from indexes proven fresh for the exact inputs.

Authoritative submit inputs include:

```text
target_ref
base_commit
current_patchset
slice_definition_hashes
folder_policy_file_hashes
queue_definition_hashes
```

If a relevant index is stale, submit should recompute directly or return a
refresh-required state. It should not accept based on stale policy, queue, or
coverage results.

## 6. Rebuild Rules

Every index must have a rebuild path.

Rebuild inputs:

```text
commits
trees
blobs
slice definitions
folder policy files
queue definition files
```

Rebuilds may run per index, per account, per slice, per path prefix, or globally.

Indexes should store provenance:

```text
source_commit_id
slice_definition_hash
policy_file_hashes[]
queue_definition_hash
indexer_version
indexed_at
```

Provenance lets APIs and submit workers decide whether an index result is fresh
for a request.

## 7. Operational Notes

Index workers should be idempotent. Replaying the same event must produce the
same index state.

Index writes should be monotonic when possible. A newer indexed commit should
not be overwritten by an older event without explicit repair mode.

Repair workflows should be able to:

- pause an index worker
- rebuild an index from source objects
- compare indexed provenance against refs and commits
- resume event processing from a known checkpoint

The system should prefer temporarily stale search over blocking submit on a
search index. It should prefer blocking submit over accepting with stale policy,
coverage, or queue-selection data.
