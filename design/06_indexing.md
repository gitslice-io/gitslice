# Gitslice Indexing Design

This document defines Gitslice derived indexes, indexing events, freshness
semantics, and rebuild rules. Product context is in [00_product.md](00_product.md),
the top-level architecture is in
[01_gitslice_architecture_design.md](01_gitslice_architecture_design.md), storage
is in [02_storage.md](02_storage.md), and rollout phases are in
[07_execution_plan.md](07_execution_plan.md).

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

## 2. Indexing Systems

Gitslice should use different index systems for different correctness needs.
There should not be one generic indexing engine responsible for every query.

MVP systems:

```text
metadata database
  authoritative operational indexes
  freshness/provenance records
  transactional outbox
  worker checkpoints

Zoekt
  code text search shards
  best-effort symbol search
  file-name search

object storage
  large immutable index artifacts
  search shard snapshots
  rebuild checkpoints

index workers
  consume outbox events
  update metadata indexes
  build and publish search shards
```

The metadata database is the same transactional system used for refs, commits,
changesets, queue state, and policy metadata. For the first implementation this
can be PostgreSQL. The design should not depend on PostgreSQL-only behavior, but
using SQL tables for the MVP keeps correctness-critical indexes easy to inspect,
repair, and transactionally update. If scale requires it, these tables can move
to an ordered distributed KV store with the same key shapes.

Zoekt is the preferred code-search backend for the MVP. It is purpose-built for
source-code text search and file-name search. Gitslice should not use
OpenSearch or Elasticsearch as the first code search backend unless there is a
separate product requirement for general document search, analytics, or semantic
search. Those systems can be added later as secondary indexes.

### 2.1 System Responsibilities

Metadata database indexes:

- slice coverage
- folder policy coverage
- queue selection
- changed paths
- path history
- projection cache registry
- index freshness and provenance
- search shard manifests
- outbox events and worker checkpoints

Zoekt indexes:

- file content text
- file names
- best-effort symbols when available
- language tags and searchable document metadata

Object storage artifacts:

- immutable Zoekt shard files
- large symbol graph artifacts
- build/test graph snapshots
- rebuild checkpoints and comparison reports

Not all indexes have the same freshness requirement. Metadata indexes used for
submit validation must be fresh for exact source inputs or recomputed from
source-of-truth objects. Zoekt search may be stale-but-usable, but slice
visibility filtering must fail closed if the slice definition or authorization
state cannot be proven fresh.

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

folder_policy_prefix_index(
  account,
  policy_file_path,
  governed_prefix,
  policy_file_hash,
  source_commit_id
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

search_manifest(
  account,
  target_ref,
  indexed_commit_id,
  indexer_version,
  shard_generation,
  shard_ids[],
  tombstone_generation,
  published_at
)
```

Path-prefix indexes should support:

```text
find prefixes covering a path
find prefixes covered by a changed directory
find slices whose included paths overlap changed paths
find policies and queues that match changed paths
```

The exact physical representation can be SQL btree/range tables for the MVP and
an ordered KV prefix trie later. The logical API should remain prefix lookup and
overlap lookup, not ad hoc string search.

### 2.3 Code Search Shards

Code search should use immutable shard generations plus a manifest.

```text
account/ref search manifest
  -> base shard ids
  -> delta shard ids
  -> tombstone generation
  -> indexed_commit_id
```

Search documents are keyed by canonical global path and content identity:

```text
account
target_ref
global_path
blob_hash
source_commit_id
language
is_binary
indexer_version
```

The search worker should reuse extraction by `blob_hash` when the same content
appears at a new path. Deleted paths are represented as tombstones in the next
published generation. A background compactor can merge base shards, delta
shards, and tombstones into a new base shard without changing search semantics.

Publishing a search update is a manifest swap:

```text
1. Build new delta shards from changed paths.
2. Write shard files to object storage or local shard storage.
3. Verify shard checksums.
4. Write search_manifest for the new indexed_commit_id.
5. Make readers use the newest complete manifest.
```

Readers must never observe a partially published shard generation.

## 3. Required Indexes

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

### 3.1 Code Search

Supports text search over visible source files. It must enforce slice visibility
and should return freshness metadata with results.

### 3.2 Symbol Search

Supports language-aware symbol lookup where available. Symbol extraction is
best-effort and can lag behind commit acceptance.

### 3.3 Path History

Maps paths to commits that touched those paths. Rename handling can start as
path-based and later add richer rename detection.

### 3.4 Slice Coverage

Maps global paths to slices whose latest accepted `included_paths` cover those
paths.

```text
covering_slices(path, definition_epoch) -> []slice_id
```

This index accelerates authorization, review routing, projection invalidation,
and queue selection. It is derived from slice definitions.

### 3.5 Folder Policy Coverage

Maps global paths to matching ancestor `.gitslice/policy.yaml` files.

```text
matching_policy_files(path, commit_id) -> []policy_file
```

This index accelerates policy resolution. It is derived from committed tree
state and can be rebuilt by scanning `.gitslice/policy.yaml` files.

### 3.6 Queue Selection

Maps changed paths and authoring account to queue rules from:

```text
/{account}/.gitslice/queues/*.yaml
```

Queue selection must be recomputed before submit against the latest target ref.
The index is an accelerator, not an authority.

### 3.7 Build And Test Graphs

Build and test indexes support affected target calculation and required check
selection.

These indexes may depend on language-specific analyzers. They should degrade to
broader check selection if stale or unavailable.

### 3.8 Slice Projection

Projection indexes cache deterministic slice projections:

```text
(slice_id, slice_definition_hash, global_commit_id) -> projected_tree_id
(slice_id, slice_definition_hash, global_commit_id) -> synthetic_git_commit_id
```

Projection caches can be invalidated or lazily rebuilt when slice definitions or
commits change.

### 3.9 Changed Paths

Changed-path indexes support review summaries, queue routing, CI selection, and
incremental index updates.

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
  -> search index worker
  -> symbol worker
  -> build/test graph worker
```

Normal code edit:

```text
CommitCreated(new_commit, changed_paths)
  -> append changed_path_index rows
  -> append path_history_index rows
  -> identify affected slices by prefix overlap
  -> invalidate affected projection cache entries
  -> build Zoekt delta shard for changed text files
  -> publish new search manifest
```

Folder policy edit:

```text
FolderPolicyChanged(policy_file_path)
  -> recompute governed prefix
  -> update folder_policy_prefix_index
  -> mark open changesets under that prefix NeedsPolicyRefresh
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
same rows and shard manifest or recognize that the event has already been
applied.

## 5. Slice-Scoped Search

Slice-scoped search is implemented by combining a global/account-level code
search index with fresh slice membership filtering. Gitslice should not create a
separate full text index for every slice by default because slices can overlap
heavily and would duplicate the same file content many times.

Query flow:

```text
Search(slice, query)
  -> resolve slice and slice_definition_hash
  -> authorize caller for slice read access
  -> load included path prefixes from slice_prefix_index
  -> verify slice_prefix_index freshness or recompute from slice definition
  -> load search_manifest for target_ref
  -> query only shards overlapping the slice prefixes
  -> apply path-prefix filter to matches
  -> revalidate each result path against the slice definition
  -> return snippets and freshness metadata
```

The search service, not Zoekt itself, owns authorization. Zoekt is a retrieval
engine. The search service must not return snippets until it has verified that
the caller can read the slice and that the result path is included in the slice.

Freshness is split into two dimensions:

```text
scope_freshness: slice definition and authorization state
content_freshness: indexed commit in the search manifest
```

Rules:

- If scope freshness is stale or unavailable, fail closed.
- If content freshness is stale, return `stale_but_usable` only for normal UX
  search.
- If the caller requests `require_fresh`, wait for the search manifest to reach
  the requested commit or return `unavailable`.
- Revalidate result paths before producing snippets.

Large slices can query by shard overlap first and path-prefix filter second.
Small slices can push exact prefix filters into the Zoekt query. Popular slices
may later get materialized search scopes or precomputed shard bitsets, but those
are cache optimizations and must not become authorization authorities.

## 6. Event Pipeline

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

## 7. Freshness Model

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

## 8. Submit-Time Rules

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

## 9. Rebuild Rules

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

## 10. Operational Notes

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
