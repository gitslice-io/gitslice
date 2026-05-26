# File Identity And Move History

This document defines how Gitslice should preserve file and directory history
across moves and renames. Product context is in [00_product.md](00_product.md),
storage is in [02_storage.md](02_storage.md), API shape is in
[03_core_api.md](03_core_api.md), CLI behavior is in
[04_cli_design.md](04_cli_design.md), and derived indexes are in
[06_indexing.md](06_indexing.md).

## 1. Problem Statement

Path-based history is not enough.

If a file starts at:

```text
/nic/app/auth.go
```

and later moves to:

```text
/nic/services/auth/auth.go
```

then a history query for the new path should still find the older commits that
touched the same logical file. A path is only a location. The object whose
history must be preserved is the file or directory entity.

Git handles this mostly through diff-time rename heuristics. Gitslice should
not make heuristics the source of truth. Explicit native moves should be
authoritative, and inference should be a fallback for imports or clients that
can only report delete/add pairs.

## 2. Core Concepts

### 2.1 Entity ID

Every committed file, directory, and symlink under an account-rooted namespace
has a stable entity id.

```text
entity_id = opaque random id
path      = current canonical absolute path
kind      = file | directory | symlink
```

The entity id belongs to the logical filesystem object, not to a path. Moving
or renaming an object keeps the same entity id. Deleting an object and later
creating a new object at the same path creates a new entity id.

The id is scoped by account:

```text
(account_id, entity_id)
```

The account root directory, for example `/nic`, also has a directory entity id.
The global `/` root may remain a synthetic system root.

### 2.2 Path

A path is a mutable location for an entity at a specific commit. The same path
can refer to different entity ids at different commits after delete/recreate
cycles.

History queries must therefore distinguish:

- path history: commits that touched a literal path
- entity history: commits that touched the same logical object
- move-following history: entity history plus ancestor directory moves that
  changed the object's path

### 2.3 Move Event

A move event records that the same entity changed location.

```text
entity_id
old_path
new_path
change_kind = moved
source      = explicit | exact_content_match | similarity | git_import
confidence
```

Explicit moves from native RPC operations are authoritative. Inferred moves are
best-effort metadata and must be marked with their source and confidence.

## 3. Storage Model

### 3.1 Tree Entries

Native tree entries should carry entity ids so lineage is rebuildable from
source-of-truth objects, not only from derived indexes.

```text
TreeEntry:
  name
  entity_id
  kind
  mode
  tree_id
  blob_id
  symlink_target
  size
  content_hash
```

Tree hashes should include `entity_id` in the next tree format version. That
makes the native tree a snapshot of logical source state, not just byte
contents. Two copied files with the same blob bytes can then have different
entity ids while sharing the same blob id.

Directory moves still reuse descendant tree nodes. The moved directory's child
tree is independent of the directory name stored by the parent. The parent
entry changes location and keeps the directory entity id.

### 3.2 Entity Table

PostgreSQL stores the stable entity catalog:

```text
fs_entities(
  account_id,
  entity_id,
  kind,
  created_commit_id,
  deleted_commit_id,
  created_at,
  primary key(account_id, entity_id)
)
```

`deleted_commit_id` is nullable. It marks that the entity no longer exists at
the current head, but the entity remains queryable for history.

### 3.3 Commit Change Index

The path-history index should evolve from path-only rows into entity-aware
change rows:

```text
commit_entity_changes(
  target_ref,
  commit_id,
  account_id,
  entity_id,
  path,
  old_path,
  change_kind,
  content_hash,
  mode,
  committed_at,
  primary key(target_ref, commit_id, entity_id, path)
)
```

Representative `change_kind` values:

```text
added
modified
deleted
moved
copied
mode_changed
```

Path-based indexes can remain as accelerators, but entity-aware change rows are
the durable lineage index used for history queries.

### 3.4 Directory Moves

A directory move should not expand into one change row for every descendant.
Large directories make that too expensive.

Instead, the move commit records one directory entity change:

```text
entity_id = directory entity
old_path  = /nic/old
new_path  = /nic/new
```

A file history query follows the file entity and also considers ancestor
directory move events. This lets the UI show the commit where the containing
directory moved without writing thousands of descendant rows.

## 4. Move Detection

Move detection should be layered.

### 4.1 Explicit Native Moves

Native operations such as `FileEdit{op: "rename", old_path, path}`,
`gs fs mv`, and `gs shell mv` are authoritative. They must keep the old
entity id and write one move event.

Submit validation still checks both paths:

- the old path must exist at the base as the expected entity
- the new path must be allowed by the authoring slice
- the new path must not overwrite an unrelated entity unless the operation
  explicitly supports replacement

### 4.2 Client-Side Operation Logs

Workspace-aware clients should preserve local rename operations when they know
them. A workspace snapshot that only reports final file contents loses intent,
so operation logs are preferred when available.

### 4.3 Exact Delete/Add Inference

When a patchset contains a delete and an add with the same content hash and
compatible mode, the server may infer a move if the pairing is unambiguous.

Ambiguous examples must not be auto-linked:

- one deleted file and multiple new files with the same content hash
- multiple deleted files and one new file with the same content hash
- generated empty files with identical content

When ambiguous, Gitslice records delete/add as separate entities unless the
client supplies an explicit rename.

### 4.4 Similarity Inference

Similarity detection can support rename-plus-edit, especially for Git imports.
It must be treated as heuristic metadata:

- run only within a bounded patchset or Git commit diff
- require a conservative similarity threshold
- record `source = similarity` and a confidence score
- never override an explicit native move

The MVP can defer similarity inference and start with explicit moves plus exact
content matches.

### 4.5 Git Import

Git import may use Git rename detection as an import-time hint, but the native
result should still be stored as entity moves. After import, Gitslice history
queries should not need to rerun Git diff heuristics.

## 5. History Query Semantics

`ListCommits` should support path-filtered history with an explicit
move-following option.

```text
ListCommits(ref_name, path, slice, follow_moves, limit, page_token)
```

When `path` is present:

1. Resolve the path at the requested ref to an entity id.
2. Enforce slice visibility for the resolved path and commits being returned.
3. If `follow_moves` is false, query literal path history.
4. If `follow_moves` is true, query entity history and ancestor directory move
   events.
5. Return an opaque page token based on the chosen ordering and lineage cursor.

The recommended default is:

- `follow_moves = true` for file and directory path history
- an explicit `--no-follow-moves` CLI flag for literal path history

If the path no longer exists at the requested ref, the API should allow callers
to request literal path history. Following entity history from a missing path
requires either a commit selector where the path did exist or an explicit
entity id.

## 6. Slice Visibility

Slices are projections over the global account-rooted tree. Entity history must
not leak paths or commits outside the caller's visible slice.

For a path history query scoped to a slice:

- resolve the path in that slice's projected view
- include only commits visible through the slice definition at the relevant ref
- hide old paths outside the slice unless the caller has access to a slice that
  includes them
- preserve continuity when the same entity moves within the visible projection

If an entity moves out of a slice, the history query may show a terminal move
event but should not reveal the private destination path unless authorized.

## 7. Sharding And ID Shape

Entity ids should be opaque random ids, not path-derived ids.

Random ids are better for hash-based sharding because they avoid hot ranges
from popular path prefixes such as `/nic/...` or `/acme/services/...`. They
also remain stable when the path changes.

The primary ownership and authorization boundary is still the account:

```text
shard key candidates:
  account_id
  hash(account_id, entity_id)
```

For PostgreSQL, random UUID primary keys can cause btree page churn, so indexes
should be designed around the actual query shapes:

```text
(account_id, entity_id)
(target_ref, account_id, entity_id, committed_at desc)
(target_ref, path, committed_at desc)
```

At larger scale, physical sharding can use `hash(account_id, entity_id)` while
logical APIs continue to use account-scoped entity ids.

## 8. Migration Plan

1. Add nullable entity ids to tree entry encoding under a new tree format
   version.
2. Backfill entity ids by walking current heads and recent history.
3. Create `fs_entities` and `commit_entity_changes`.
4. Populate entity-aware rows for new commits from explicit native operations.
5. Add exact delete/add inference for snapshot-style patchsets.
6. Teach `ListCommits` to follow entity lineage behind an opt-in flag.
7. Make follow-moves the default once RPC and CLI tests cover visibility,
   pagination, directory moves, and ambiguity cases.
8. Keep path-only history indexes as compatibility accelerators until all
   callers move to entity-aware queries.

Backfill cannot perfectly reconstruct all old moves. Historical links inferred
during backfill should be marked as inferred, not authoritative.

## 9. Test Plan

Required RPC tests:

- explicit file move preserves history across old and new paths
- explicit directory move preserves child file history without descendant row
  expansion
- delete and add with the same content hash infers a move only when
  unambiguous
- ambiguous exact matches do not create false lineage
- delete and recreate at the same path creates a new entity id
- path history with `follow_moves = false` remains literal
- custom slice history follows moves only through visible paths
- pagination remains stable while following move lineage

Required CLI tests:

- `gs fs mv` followed by `gs commit list <new-path>` shows older file commits
- `gs shell mv` has the same history behavior
- `gs commit list --no-follow-moves <path>` shows literal path history
- unauthorized old or new paths are redacted in human and JSON output

Required import tests:

- Git rename imports as one entity move
- Git copy imports as a new entity linked with `copied_from_entity_id`
- rename-plus-edit similarity inference is either disabled or marked
  non-authoritative with confidence metadata
