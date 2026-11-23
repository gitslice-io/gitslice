# File Identity And Move History

This document defines how Gitslice preserves file and directory history across
moves and renames.

Related documents:

- [00_product.md](00_product.md): product overview
- [02_storage.md](02_storage.md): commits, trees, and blobs
- [03_core_api.md](03_core_api.md): `ListCommits` and `ResolveCommit` APIs
- [04_cli_design.md](04_cli_design.md): `gs log`, `gs show`, and `gs blame`
- [06_indexing.md](06_indexing.md): outbox processing and `RebuildDerivedIndexes`

## 1. Problem Statement

Path-based history is not enough.

If a file starts at `/nic/app/auth.go` and later moves to
`/nic/services/auth/auth.go`, a history query for the new path should still
find the older commits that touched the same logical file. A path is only a
location; the object whose history must be preserved is the file or directory
entity.

Explicit native moves are authoritative, while exact delete/add matching serves
as a fallback for snapshot-style clients and imports.

## 2. Core Concepts

### 2.1 Entity ID

Every committed file, directory, and symlink under an account-rooted namespace
is assigned a stable account-scoped entity ID:

```text
(account_id, entity_id)
kind = file | directory | symlink
```

Moving or renaming an object keeps the same `entity_id`. Deleting an object and
later creating a new object at the same path allocates a new `entity_id`.

### 2.2 Path vs. Entity History

A path is a mutable location for an entity at a given commit. History queries
distinguish:

- **path history** (`follow_moves = false`): commits that touched a literal path
- **entity / move-following history** (`follow_moves = true`, default for single-path `gs log`): commits that touched the same logical entity, plus ancestor directory move events that changed the object's path

### 2.3 Move Events

A move event records that an entity changed location:

```text
entity_id
old_path
path
change_kind = moved
source      = explicit | exact_content_match | git_import
confidence  = 0..100 (100 for explicit native moves)
```

Explicit moves from native RPC operations (`FileEdit{op: "rename"}`, `MovePath`,
`gs fs mv`, `gs shell mv`) are authoritative (`source = 'explicit'`,
`confidence = 100`).

## 3. Current Storage Model (PostgreSQL Derived Tables)

Entity identity currently lives in **PostgreSQL derived tables** populated by
the `commit_published` outbox worker (`internal/postgres/outbox.go`,
`internal/postgres/entity_rows.go`) and rebuildable via
`RebuildDerivedIndexes` (see [06_indexing.md](06_indexing.md)).

The object-store tree format in `internal/treestore` remains
**`gitslice.tree.v1`**: tree entries do not yet embed `entity_id` (see §9
Future Work).

### 3.1 `fs_entities`

Stores the account-scoped entity catalog (`0003_entity_move_history.sql`):

```sql
fs_entities(
  account_id        text not null references accounts(id),
  entity_id         text not null,
  kind              text not null,
  created_commit_id text references commits(id),
  deleted_commit_id text references commits(id),
  created_at        timestamptz not null default now(),
  primary key(account_id, entity_id)
)
```

`deleted_commit_id` is nullable; it marks entities no longer present at the
current head while keeping them queryable for history.

### 3.2 `current_path_entities`

Maps each live path on a target ref to its current entity, content hash, and
mode:

```sql
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
```

When `ref_materialized_heads` matches the requested head commit, this table
also powers head-read fast paths in `RepositoryStore` and `path_bases`.

### 3.3 `commit_entity_changes`

Stores entity-aware change lineage per commit:

```sql
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
```

Supporting indexes:

- `idx_commit_entity_changes_ref_entity_time` on `(target_ref, account_id, entity_id, committed_at desc, commit_id)`
- `idx_commit_entity_changes_ref_path_time` on `(target_ref, path, committed_at desc, commit_id)`

Representative `change_kind` values: `added`, `modified`, `deleted`, `moved`,
`copied`, `mode_changed`.

### 3.4 Directory Moves

A directory move records a single directory entity move row
(`old_path = /nic/old`, `path = /nic/new`) rather than expanding into one
`commit_entity_changes` row per descendant. File history queries follow the
file entity and also include ancestor directory move events.

## 4. Move Detection

Move detection is layered:

1. **Explicit native moves**: `FileEdit{op: "rename", old_path, path}`, `MovePath`, `gs fs mv`, and `gs shell mv` preserve the existing `entity_id` and record `source = 'explicit'`, `confidence = 100`.
2. **Exact delete/add inference**: when a snapshot-style patchset deletes one path and adds another with the exact same `content_hash` and compatible mode, the outbox processor infers a move (`source = 'exact_content_match'`) only when the pairing is unambiguous (1-to-1 within the commit). Ambiguous pairings (multiple deletes or multiple adds with the same hash, or empty files) are recorded as separate delete/add entities.
3. **Git import**: `gs import` translates Git rename hints into native `rename` edits so imported history preserves entity lineage without re-running Git diff heuristics at query time.

## 5. History Query Semantics

`ListCommits` and `ResolveCommit` support path-filtered history with
`follow_moves`:

```text
ListCommits(ref_name, path, slice, follow_moves, limit, page_token)
ResolveCommit(commit_id_or_prefix, ref_name, path, slice, follow_moves)
```

When `path` is supplied:

1. Resolve the path at `ref_name` in `current_path_entities` (enforcing slice visibility).
2. If `follow_moves` is `false` (`gs log --no-follow-moves`), query literal path history from `commit_changed_paths`.
3. If `follow_moves` is `true` (default for single-path `gs log`, and used by `gs blame`), query `commit_entity_changes` for the entity plus ancestor directory moves.
4. `ResolveCommit` uses the same scope so short commit prefixes shown by `gs log` resolve consistently in `gs show` and `gs diff`.

## 6. Slice Visibility

Entity history never leaks paths or commits outside the caller's authorized
slice projection:

- Path resolution and returned commits are filtered through the slice's `included_paths`.
- Old or destination paths outside the caller's visible projection are not disclosed.
- Hidden commits are excluded before commit-prefix ambiguity checks in `ResolveCommit`.

## 7. Sharding And ID Shape

Entity IDs are opaque account-scoped identifiers (`(account_id, entity_id)`),
avoiding hot path-prefix ranges and remaining stable across renames. Indexes
are shaped around `(target_ref, account_id, entity_id, committed_at desc)` and
`(target_ref, path, committed_at desc)`.

## 8. Rebuild & Repair

Because `fs_entities`, `current_path_entities`, and `commit_entity_changes` are
derived tables, `ChangesetStore.RebuildDerivedIndexes` replays published
`pending_publish` patchsets in commit order to reconstruct entity tables from
scratch (see [06_indexing.md](06_indexing.md#5-rebuild-rules-rebuildderivedindexes)).

## 9. Future Work

The following items are **not** yet implemented:

- **Entity IDs in tree objects (`gitslice.tree.v2`)**: embedding `entity_id` directly inside immutable `treestore` tree entries (currently `treestore` uses `gitslice.tree.v1`, so entity lineage is derived in PostgreSQL from patchset edits rather than stored inside tree hashes).
- **Similarity-based rename-plus-edit inference**: heuristic content-similarity matching (`source = 'similarity'` with a graded confidence score) for modified renames in snapshot patchsets or Git imports.
- **Copy lineage (`copied_from_entity_id`)**: recording a back-pointer on copied entities to the source `entity_id` for cross-copy history traversal.
- **Client-side rename tracking in full checkouts**: capturing explicit local rename operations from a client operation log rather than relying on exact delete/add matching.
