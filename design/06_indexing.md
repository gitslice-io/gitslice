# Gitslice Indexing Design

This document defines Gitslice derived indexes, indexing events, freshness
semantics, and rebuild rules. Product context is in [00_product.md](00_product.md),
the top-level architecture is in
[01_gitslice_architecture_design.md](01_gitslice_architecture_design.md), storage
is in [02_storage.md](02_storage.md), and rollout phases are in
[07_execution_plan.md](07_execution_plan.md).

## 1. Indexing Goals

Indexes are derived data. They should make reads, review, authorization, queue
selection, build selection, and projection fast, but they must not become the
source of truth.

The source of truth remains:

```text
Refs
Commits
Trees
Blobs
Slice definitions
Queue definition files
```

If an index is missing or stale, the system should be able to rebuild it from
those source objects.

The MVP does not include code text search or per-directory policy indexes.

## 2. Indexing Systems

Gitslice should keep the MVP index stack small.

MVP systems:

```text
metadata database
  operational indexes
  freshness/provenance records
  transactional outbox
  worker checkpoints

object storage
  large immutable index artifacts
  build/test graph snapshots
  projection cache artifacts
  rebuild checkpoints

index workers
  consume outbox events
  update metadata indexes
  publish large artifacts
```

The metadata database is the same transactional system used for refs, commits,
changesets, queue state, and slice metadata. For the first implementation this
can be PostgreSQL. The design should not depend on PostgreSQL-only behavior, but
using SQL tables for the MVP keeps correctness-critical indexes easy to inspect,
repair, and transactionally update. If scale requires it, these tables can move
to an ordered distributed KV store with the same key shapes.

### 2.1 System Responsibilities

Metadata database indexes:

- slice coverage
- queue selection
- changed paths
- path history
- projection cache registry
- index freshness and provenance
- outbox events and worker checkpoints

Object storage artifacts:

- build graph snapshots
- test graph snapshots
- projection cache artifacts
- rebuild checkpoints and comparison reports

Not all indexes have the same freshness requirement. Indexes used for submit
validation must be fresh for exact source inputs or recomputed from
source-of-truth objects.

### 2.2 Metadata Index Tables

The metadata index schema should be prefix-oriented because canonical paths are
account-rooted.

Representative tables:

```text
index_outbox(
  event_id,
  event_type,
  target_ref,
  old_commit_id,
  new_commit_id,
  changed_paths[],
  affected_slice_ids[],
  created_at
)

index_worker_checkpoint(
  worker_name,
  shard_key,
  last_event_id,
  source_commit_id,
  indexer_version,
  updated_at
)

slice_prefix_index(
  account,
  slice_id,
  slice_definition_hash,
  included_prefix,
  visibility,
  updated_at
)

queue_rule_prefix_index(
  account,
  queue_id,
  queue_definition_hash,
  matched_prefix,
  target_ref,
  source_commit_id
)

changed_path_index(
  target_ref,
  commit_id,
  path,
  blob_hash,
  change_kind
)

path_history_index(
  path,
  commit_id,
  parent_commit_ids[],
  change_kind,
  committed_at
)

projection_manifest(
  slice_id,
  slice_definition_hash,
  source_commit_id,
  projected_tree_id,
  synthetic_git_commit_id,
  artifact_ids[],
  published_at
)
```

Path-prefix indexes should support:

```text
find prefixes covering a path
find prefixes covered by a changed directory
find slices whose included paths overlap changed paths
find queues that match changed paths
```

The exact physical representation can be SQL btree/range tables for the MVP and
an ordered KV prefix trie later. The logical API should remain prefix lookup and
overlap lookup, not ad hoc string matching in application code.

## 3. Required Indexes

Initial required indexes:

- slice coverage
- queue selection
- changed paths
- path history
- slice projection
- build graph
- test graph
- index freshness/provenance

### 3.1 Slice Coverage

Maps global paths to slices whose latest accepted `included_paths` cover those
paths.

```text
covering_slices(path, definition_epoch) -> []slice_id
```

This index accelerates authorization, review routing, projection invalidation,
and queue selection. It is derived from slice definitions.

### 3.2 Queue Selection

Maps changed paths, covering slices, and authoring account to queue rules from:

```text
/{account}/.gitslice/queues/*.yaml
```

Queue selection must be recomputed before submit against the latest target ref.
The index is an accelerator, not an authority.

### 3.3 Changed Paths

Changed-path indexes support review summaries, queue routing, CI selection, and
incremental index updates.

### 3.4 Path History

Maps paths to commits that touched those paths. Rename handling can start as
path-based and later add richer rename detection.

### 3.5 Slice Projection

Projection indexes cache deterministic slice projections:

```text
(slice_id, slice_definition_hash, global_commit_id) -> projected_tree_id
(slice_id, slice_definition_hash, global_commit_id) -> synthetic_git_commit_id
```

Projection caches can be invalidated or lazily rebuilt when slice definitions or
commits change.

### 3.6 Build And Test Graphs

Build and test indexes support affected target calculation and required check
selection.

These indexes may depend on language-specific analyzers. They should degrade to
broader check selection if stale or unavailable.

## 4. Incremental Update Flow

Incremental indexing starts from a successful accepted ref update. The submit
path writes the source-of-truth mutation first, then publishes durable outbox
events for index workers.

```text
submit transaction
  -> create commit
  -> move target ref with CAS
  -> write index_outbox events
  -> commit metadata transaction
```

Workers consume the outbox independently:

```text
index dispatcher
  -> metadata index worker
  -> projection worker
  -> build/test graph worker
```

Normal code edit:

```text
CommitCreated(new_commit, changed_paths)
  -> append changed_path_index rows
  -> append path_history_index rows
  -> identify affected slices by prefix overlap
  -> identify affected queues by prefix overlap
  -> invalidate affected projection cache entries
  -> invalidate affected build/test graph entries
```

Queue definition edit:

```text
QueueDefinitionChanged(queue_file_path)
  -> parse queue rules
  -> update queue_rule_prefix_index
  -> mark affected queued changesets NeedsQueueRefresh
```

Slice definition edit:

```text
SliceDefinitionChanged(slice_id)
  -> update slice_prefix_index
  -> invalidate projection cache entries for that slice
  -> publish new slice definition epoch
```

Workers must be idempotent. Replaying the same event must either produce the
same rows and artifact manifests or recognize that the event has already been
applied.

## 5. Event Pipeline

Each accepted metadata transition emits events.

Core events:

```text
CommitCreated
RefUpdated
FileChanged
DirectoryChanged
SliceDefinitionChanged
QueueDefinitionChanged
SliceProjectionInvalidated
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
queue_ids[]
created_at
```

Async workers consume events and update derived indexes.

## 6. Freshness Model

Index-backed APIs should expose freshness:

```text
fresh
stale_but_usable
unavailable
```

Fresh means the index is known to include the relevant commit, slice definition,
and queue definition versions.

Stale-but-usable means results may omit the latest changes, but they are still
safe to display for non-authoritative UX such as review summaries or history
views. Stale indexes must not be used as the final authority for submit
validation.

Unavailable means the service should fall back to source-of-truth reads where
possible or return a structured unavailable response.

## 7. Submit-Time Rules

Submit validation must recompute authoritative requirements from source-of-truth
state or from indexes proven fresh for the exact inputs.

Authoritative submit inputs include:

```text
target_ref
base_commit
current_patchset
slice_definition_hashes
queue_definition_hashes
```

If a relevant index is stale, submit should recompute directly or return a
refresh-required state. It should not accept based on stale coverage or
queue-selection data.

## 8. Rebuild Rules

Every index must have a rebuild path.

Rebuild inputs:

```text
commits
trees
blobs
slice definitions
queue definition files
```

Rebuilds may run per index, per account, per slice, per path prefix, or globally.

Indexes should store provenance:

```text
source_commit_id
slice_definition_hash
queue_definition_hash
indexer_version
indexed_at
```

Provenance lets APIs and submit workers decide whether an index result is fresh
for a request.

## 9. Operational Notes

Index workers should be idempotent. Replaying the same event must produce the
same index state.

Index writes should be monotonic when possible. A newer indexed commit should
not be overwritten by an older event without explicit repair mode.

Repair workflows should be able to:

- pause an index worker
- rebuild an index from source objects
- compare indexed provenance against refs and commits
- resume event processing from a known checkpoint

The system should prefer blocking submit over accepting with stale coverage or
queue-selection data.
