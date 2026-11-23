# Gitslice Indexing Design

This document defines Gitslice's derived indexes, transactional outbox,
background index worker, synchronous prefix tables, and rebuild rules.

Related documents:

- [00_product.md](00_product.md): product overview
- [01_gitslice_architecture_design.md](01_gitslice_architecture_design.md): top-level architecture
- [02_storage.md](02_storage.md): authoritative storage schema and object store
- [07_conflict_resolution.md](07_conflict_resolution.md): path-head CAS and batched submit
- [13_file_identity_and_move_history.md](13_file_identity_and_move_history.md): entity-aware move history
- [27_blame.md](27_blame.md): line-level blame snapshots

## 1. Indexing Principles

Indexes are derived data. They accelerate reads, history queries, slice
coverage lookups, move-following log/blame, and commit prefix resolution, but
they are never the source of truth.

The authoritative source of truth remains:

```text
refs
commits
tree nodes (gitslice.tree.v1 in object storage)
blobs (object storage + blobs table)
slices (included_paths, submit settings)
patchsets / pending_publish
```

Every derived index can be rebuilt from those source objects. Correctness-critical
paths (submit admission, authorization, Git projection history) either read
authoritative tables directly or gate fast paths on explicit head-equality
markers (`ref_materialized_heads`).

## 2. Implemented PostgreSQL Derived Tables

All currently implemented derived indexes live in PostgreSQL alongside the
authoritative metadata tables:

```sql
-- Transactional outbox for asynchronous derived-index updates (0009_outbox.sql)
outbox(
  id            bigserial primary key,
  kind          text not null,          -- currently: 'commit_published'
  payload       jsonb not null,
  created_at    timestamptz not null default now(),
  processed_at  timestamptz,
  attempts      integer not null default 0
)

-- Per-ref changed-path history index (0002_commit_changed_paths.sql)
commit_changed_paths(
  target_ref    text not null references refs(name),
  commit_id     text not null references commits(id),
  path          text not null,
  change_kind   text not null default 'modified',
  committed_at  timestamptz not null,
  primary key(target_ref, commit_id, path)
)

-- Stable file/directory entity catalog (0003_entity_move_history.sql)
fs_entities(
  account_id        text not null references accounts(id),
  entity_id         text not null,
  kind              text not null,
  created_commit_id text references commits(id),
  deleted_commit_id text references commits(id),
  created_at        timestamptz not null default now(),
  primary key(account_id, entity_id)
)

-- Materialized head path -> entity/content mapping (0003_entity_move_history.sql)
current_path_entities(
  target_ref    text not null references refs(name),
  path          text not null,
  account_id    text not null,
  entity_id     text not null,
  kind          text not null,
  content_hash  text,
  mode          integer,
  updated_at    timestamptz not null default now(),
  primary key(target_ref, path),
  foreign key(account_id, entity_id) references fs_entities(account_id, entity_id)
)

-- Entity-aware commit change lineage (0003_entity_move_history.sql)
commit_entity_changes(
  target_ref    text not null references refs(name),
  commit_id     text not null references commits(id),
  account_id    text not null,
  entity_id     text not null,
  kind          text not null,
  path          text not null,
  old_path      text,
  change_kind   text not null,
  source        text not null default 'explicit',
  confidence    integer not null default 100,
  content_hash  text,
  mode          integer,
  committed_at  timestamptz not null,
  primary key(target_ref, commit_id, account_id, entity_id, path, change_kind),
  foreign key(account_id, entity_id) references fs_entities(account_id, entity_id)
)

-- Synchronous slice prefix lookup table (0008_slice_included_paths.sql)
slice_included_paths(
  slice_id  text not null references slices(id) on delete cascade,
  prefix    text not null,
  primary key(slice_id, prefix)
)

-- Head-equality marker gating flat-index read fast paths (0022_ref_materialized_heads.sql)
ref_materialized_heads(
  target_ref  text primary key references refs(name),
  commit_id   text not null,
  updated_at  timestamptz not null default now()
)

-- Cached line-level blame snapshots (0035_blame_snapshots.sql)
blame_snapshots(
  commit_id   text not null,
  path        text not null,
  data        bytea not null,
  created_at  timestamptz not null default now(),
  primary key(commit_id, path)
)
```

Key supporting indexes include:

- `idx_outbox_processed_id` on `outbox(processed_at, id)`
- `idx_commit_changed_paths_ref_path_time` and `idx_commit_changed_paths_path_time`
- `idx_current_path_entities_ref_prefix`, `idx_current_path_entities_ref_entity`, `idx_current_path_entities_ref_path_pattern` (`text_pattern_ops`), and partial indexes for `.gitslice/` config discovery
- `idx_commit_entity_changes_ref_entity_time` and `idx_commit_entity_changes_ref_path_time`
- `idx_slice_included_paths_prefix` on `slice_included_paths(prefix)`
- `idx_commits_id_prefix` on `commits(id text_pattern_ops)` for short commit ID prefix resolution

## 3. Update Flows

### 3.1 Asynchronous Outbox (`commit_published`)

When the submit publisher lands a commit (or batch of commits) and moves
`refs/global/main` via CAS, it writes a `commit_published` row to `outbox` in
the same PostgreSQL transaction. The payload contains `target_ref`,
`base_commit_id`, `commit_id`, `patchset_id` (or `file_edits`), `changed_paths`,
and `committed_at`.

`ChangesetStore.ProcessOutbox` (`internal/postgres/outbox.go`) drains pending
rows in ID order:

1. Acquires a transaction-scoped advisory lock (`pg_advisory_xact_lock(724943, 1)`) so only one outbox step applies entity transitions at a time.
2. Selects the next unprocessed row (`where processed_at is null and id > $1 order by id limit 1 for update skip locked`).
3. Opens a `savepoint outbox_apply` and runs `applyCommitPublishedOutboxTx`:
   - inserts rows into `commit_changed_paths`
   - loads `file_edits` (from the patchset when `patchset_id` is set, else inline) and updates `fs_entities`, `current_path_entities`, and `commit_entity_changes` (`applyEntityHistoryTx`)
   - advances `ref_materialized_heads(target_ref, commit_id)` in the **same transaction** so the flat-index read fast path (`RepositoryStore.GetFile` / `GetEntry` and `path_bases` head lookups) only engages when `current_path_entities` matches the requested head commit
4. On success, releases the savepoint and sets `processed_at = now()`. On failure, rolls back to the savepoint and increments `attempts`.

`commit_published` is currently the only outbox event kind.

### 3.2 Index Worker & Activity Idle Gating

`internal/indexworker/worker.go` runs a background loop that calls
`store.ProcessOutbox(ctx, batchSize)` (default `batchSize = 128`):

- **Immediate wake via `Nudge()`**: commit publication nudges the worker so newly landed commits are indexed with minimal latency (resets delay to `defaultInterval = 25ms`).
- **Exponential backoff**: empty polls double the poll delay from `25ms` up to `defaultMaxInterval = 60s`; errors retry after `errorRetryInterval = 1s`.
- **Queue depth metrics**: samples `OutboxDepth` every `30s` (`depthSampleInterval`) and immediately after a batch processes rows, exporting queue depth and processed/failed counters.
- **Idle gating (`internal/activity`)**: before each timer-driven poll, the worker calls `activity.Default.Wait(ctx)`. Once no request has been active for `activity.IdleAfter` (`5m`), the worker sleeps without querying PostgreSQL so Neon can scale to zero. Any incoming request or `Nudge()` wakes it immediately.

### 3.3 Synchronous `slice_included_paths` Updates

`slice_included_paths` is **not** updated via the outbox. `SliceStore`
(`internal/postgres/slice_store.go`, `slice_lifecycle.go`) updates
`slice_included_paths` synchronously inside the same transaction whenever a
slice is created, updated, or deleted. This guarantees that covering-slice
queries and authorization checks immediately observe the latest slice
definition with zero async lag.

### 3.4 Blame Snapshots (`blame_snapshots`)

Line-level blame snapshots are populated lazily on `Blame` queries and by the
activity-gated blame warmer; see [27_blame.md](27_blame.md).

## 4. Query Patterns

### 4.1 Path History & Move-Following History

- Literal path history (`follow_moves = false`) queries `commit_changed_paths` by `(target_ref, path)` or directory prefix with `(committed_at, commit_id)` cursor pagination.
- Move-following history (`follow_moves = true`, default for single-path `gs log`) resolves the path in `current_path_entities` and queries `commit_entity_changes` by `(target_ref, account_id, entity_id)` plus ancestor directory moves. See [13_file_identity_and_move_history.md](13_file_identity_and_move_history.md).

### 4.2 Commit ID Prefix Resolution

Canonical commit IDs are `sha256:<hex>`. Short prefixes (8+ hex chars) are
resolved server-side using `idx_commits_id_prefix` (`id text_pattern_ops`)
scoped to the caller's authorized path/slice prefixes via `commit_changed_paths`
or `commit_entity_changes`, fetching `limit 2` candidates to prove uniqueness
without leaking commits outside the caller's visible scope.

### 4.3 Materialized-Head Fast Path

When a read targets `refs/global/main`'s current head and `ref_materialized_heads`
matches that exact `commit_id`, PostgreSQL can resolve file metadata and
path-base fingerprints directly from `current_path_entities` instead of walking
tree nodes in object storage. If the outbox is lagging (so
`ref_materialized_heads.commit_id != requested_commit_id`), reads fall back
automatically to walking the authoritative tree in `treestore`.

## 5. Rebuild Rules (`RebuildDerivedIndexes`)

`ChangesetStore.RebuildDerivedIndexes(ctx, targetRef)` (`internal/postgres/outbox.go`)
reconstructs all outbox-derived tables for a target ref inside a single
transactional repair pass:

1. Acquires the outbox advisory lock (`pg_advisory_xact_lock(724943, 1)`).
2. Locks the `refs` row for `targetRef` (`for update`) and all unprocessed `commit_published` rows in `outbox` targeting `targetRef`.
3. Deletes existing `commit_changed_paths`, `commit_entity_changes`, and `current_path_entities` rows for `targetRef`, then deletes orphaned `fs_entities` rows.
4. Rebuilds `commit_changed_paths` by walking the commit graph recursively from `refs.commit_id` via `commits.parent_ids` and `commits.changed_paths`.
5. Rebuilds `fs_entities`, `current_path_entities`, and `commit_entity_changes` by replaying published `pending_publish` patchsets in commit order (`rebuildEntityHistoryTx`).
6. Marks all pending `commit_published` outbox rows for `targetRef` as processed and updates `ref_materialized_heads` to `currentCommitID`.

## 6. Future Work

The following indexing capabilities from earlier architecture sketches are **not**
implemented yet:

- **Build and test graph indexers**: language-aware dependency graph snapshots in object storage for affected-target check selection and dependency-derived read sets.
- **Explicit API freshness enums**: returning structured `fresh` / `stale_but_usable` / `unavailable` freshness metadata on index-backed RPC responses (today, head fast paths check `ref_materialized_heads` internally and fall back to trees transparently).
- **Additional outbox event kinds & sharded worker checkpoints**: splitting indexing across multiple worker types/shards with `index_worker_checkpoint` tables and separate `SliceDefinitionChanged` / `PatchsetUpdated` outbox events.
- **Path-lock & submit-requirement provenance index tables**: dedicated `path_locks` and `submit_requirement_index` tables (today submit requirements are evaluated directly from the authoritative slice definition and `.gitslice/` config).
