# Gitslice Storage Design

This document specifies the native storage model, content-addressed hashing,
object/cache hierarchy, PostgreSQL schema groups, Go storage interfaces, and
3-stage asynchronous publish pipeline for Gitslice.

Companion documents:
[00_product.md](00_product.md),
[01_gitslice_architecture_design.md](01_gitslice_architecture_design.md),
[03_core_api.md](03_core_api.md),
[06_indexing.md](06_indexing.md),
[07_conflict_resolution.md](07_conflict_resolution.md),
[13_file_identity_and_move_history.md](13_file_identity_and_move_history.md).

---

## 1. Storage Architecture Overview

Gitslice separates transactional metadata from immutable content-addressed
payloads:

```text
Ref -> Commit -> RootTree -> TreeEntries -> Blobs
```

Everything except mutable `refs` (and operational/workflow rows) is immutable.

- **Metadata & operational indexes**: **PostgreSQL (Neon)** via
  `internal/postgres` (migrations `0001_init.sql` through
  `0066_reusable_automations_and_pipelines.sql`). PostgreSQL stores `refs`,
  `commits` (including `root_tree_id`), `blobs` metadata, slices, changesets,
  patchsets, path heads, `pending_publish`, `outbox`, derived indexes, and all
  control-plane state. It never stores a full per-commit file listing.
- **Object storage**: **Cloudflare R2** (`internal/objectstore/r2`) stores
  immutable blob bytes and canonical JSON tree nodes. Production enforces
  `GITSLICE_REQUIRE_R2=1` so `gitslice-server` refuses to start with the local
  filesystem object store (`internal/objectstore/filesystem`), which is used
  only in local development and tests.
- **In-process caches**:
  - **Object cache** (`internal/objectstore/cache`): wraps the backing
    `ObjectStore` with a 256 MiB default budget (`GITSLICE_OBJECT_CACHE_BYTES`,
    default `256<<20`) and a 4 MiB maximum single-entry cap (`4<<20`).
  - **Decoded tree-node cache** (`internal/treestore/node_cache.go`): an LRU
    cache (`nodeCacheEntries = 200_000` total entries across cached nodes) of
    immutable decoded `treestore.Node` structs keyed by `tree_id`. Because tree
    nodes are content-addressed, cached nodes never go stale; writers copy on
    mutation.

---

## 2. Content-Addressed Object Identity (`internal/objectid`)

All native object ids use SHA-256 formatted as `sha256:<64-hex>` with explicit
domain separation (`<domain>\x00<payload>`) in `internal/objectid/objectid.go`:

### 2.1 Blobs

- **`RawContentHash(data)`**: `sha256(raw_bytes)` — used as `content_hash` for
  deduplication, patchset predicates, and client upload checks.
- **`BlobID(data)`**: `sha256("gitslice.blob.v1\x00" || raw_bytes)` — native
  domain-separated blob identifier. `NewBlobHasher()` computes both hashes and
  byte length in a single streaming pass.
- Git blob SHA-1 ids are stored separately (`blobs.git_blob_id`, migration
  `0029_blob_git_ids.sql`) solely for Git projection speed and are never native
  ids.

### 2.2 Trees (`gitslice.tree.v1`)

Tree nodes are stored as JSON in object storage under:

```text
trees/sha256/{hash[0:2]}/{hash[2:4]}/{hash}.json
```

(see `treestore.Key(treeID)` in `internal/treestore/treestore.go`).

Each node has `version: "gitslice.tree.v1"` and an ordered `entries` slice
sorted lexicographically by child `name`. `objectid.TreeID(entries)` hashes:

```text
sha256("gitslice.tree.v1\x00" || json({"entries": entries}))
```

Each `objectid.TreeEntry` / `treestore.Entry` carries:

```json
{
  "name": "handler.go",
  "kind": "file",
  "mode": 420,
  "tree_id": "",
  "blob_id": "sha256:...",
  "symlink_target": "",
  "size": 1234,
  "content_hash": "sha256:..."
}
```

- `EmptyTreeID()` / `treestore.EmptyRootID()` is `TreeID([]TreeEntry{})`.
- **`gitslice.tree.v1` does NOT embed `entity_id` in tree entries.** Move and
  rename identity is tracked in PostgreSQL (`fs_entities`,
  `current_path_entities`, `commit_entity_changes`). Embedding `entity_id` in
  tree nodes (`gitslice.tree.v2`) is an unimplemented future option.

### 2.3 Commits (`gitslice.commit.v1`)

`objectid.CommitID(obj)` normalizes `ParentIDs` and `ChangedPaths` to non-nil
slices, converts `CreatedAt` to UTC, marshals `objectid.CommitObject` to JSON,
and hashes:

```text
sha256("gitslice.commit.v1\x00" || json({
  "parent_ids":    [...],
  "root_tree_id":  "sha256:...",
  "author":        "<subject_id>",
  "message":       "<commit_message>",
  "created_at":    "<RFC3339Nano UTC>",
  "changed_paths": [...]
}))
```

Human-facing tools may abbreviate commit ids (minimum 8 hex chars, default 12
in CLI) via `RepositoryService.ResolveCommit`, which resolves prefixes within
the caller's visible commit set before reading objects.

---

## 3. Canonical Path Rules (`internal/paths/paths.go`)

All paths stored in trees, commits, patchsets, and indexes are canonical
account-rooted POSIX paths:

- **`Canonical(p)`**: normalizes `\` to `/`, prepends `/` if missing, runs
  `path.Clean`, rejects empty strings, `.` / `..` segments, root escapes, and
  requires **at least 2 segments** (`/{account}/{segment}...`).
- **`CanonicalPrefix(p)`**: applies the same normalization and safety checks
  but permits a **single segment** (`/{account}`), used for personal home
  slices (`nic/home` → `/nic`) and prefix-scoped queries.
- **`Contains(prefix, p)`**: strict path-boundary prefix check (`p == prefix`
  or `strings.HasPrefix(p, prefix+"/")`), preventing `/acme/pay` from matching
  `/acme/payment`.
- **`AncestorPrefixes(p)`**: expands `/a/b/c` into `["/a", "/a/b", "/a/b/c"]`
  for covering-slice and `.gitslice/` config lookups.
- **`FromWorkspacePath(includedPrefix, workspacePath)`**: maps a local
  workspace-relative path back to its canonical global path.

---

## 4. Ref Model

A ref (`refs` table) is a mutable named pointer `(name, commit_id, version,
updated_at, updated_by)` advanced only by conditional compare-and-swap (CAS) on
`commit_id`:

- **Default target ref**: `refs/global/main`.
- **Changeset refs (Git gateway)**:
  `refs/changes/{account}/{slice}/{changeset_number}/{patchset_number}` (and
  `refs/changes/new` as a Git push alias).
- **Projected Git refs**: `refs/global/main` projects to `refs/heads/main` per
  slice.

---

## 5. 3-Stage Asynchronous Publish Pipeline (`internal/postgres/changeset_publish.go`)

Submit admission and commit publication are decoupled so concurrent submits on
disjoint paths do not serialize on R2 tree writes:

1. **Admission (`SubmitChangeset`)**: validates requirements, approvals, and
   checks; performs durable **path-head CAS** on `path_heads` (comparing each
   changed path's base fingerprint and advancing it to the accepted post-patch
   fingerprint); sets `changesets.status = 'pending_publish'`; and appends a
   `pending_publish` row in one PostgreSQL transaction.
2. **Stage 1 — Claim (`claimPendingPublishBatch`)**: after
   `reapStalePendingPublishClaims` resets any `publishing` rows older than
   `pendingPublishClaimTimeout` (60s), opens a short transaction that selects
   up to `limit` rows (`status = 'pending'`, ordered by `sequence`,
   `FOR UPDATE SKIP LOCKED`), filters to the first row's `target_ref`, reads
   the current `(refs.commit_id, commits.root_tree_id)`, marks the claimed rows
   `status = 'publishing'`, and commits. **No DB locks are held during tree
   building.**
3. **Stage 2 — Build (`buildPendingPublishBatch`)**: outside any DB
   transaction, loads each claimed changeset and patchset, converts file edits
   to `[]treestore.FileEdit`, and calls **`treestore.ApplyEditChain(ctx,
   claim.RootTreeID, editSets)`**. `ApplyEditChain` applies every patchset in
   sequence against a single in-memory `bufferingObjectStore` (so intermediate
   tree nodes are served from memory), prunes unreachable intermediate nodes,
   performs **one batched flush to R2**, and populates `treestore.nodeCache`.
   It then computes deterministic `CommitID`s, `path_heads` mutations, outbox
   payloads (`commit_published`), and event records. If any step fails,
   `releasePendingPublishClaims` returns claimed rows to `pending`.
4. **Stage 3 — Finalize (`finalizePendingPublishBatch`)**: opens a short
   transaction that locks the target ref with **`SELECT commit_id FROM refs
   WHERE name = $1 FOR NO KEY UPDATE`** (not `FOR UPDATE`, preventing deadlocks
   with `FOR KEY SHARE` FK locks taken by concurrent `pending_publish`
   inserts), verifies `lockedCommitID == claim.OriginalCommitID` (resetting
   claims on CAS mismatch), locks the changesets, inserts `commits`, applies
   `path_heads` mutations, marks `pending_publish` rows published and
   changesets `submitted`, clears `path_intents`, refreshes stack statuses,
   inserts `outbox` and event rows, and updates `refs` with CAS (`WHERE name =
   $3 AND commit_id = $4`).

---

## 6. PostgreSQL Schema Overview

The schema is managed by sequential SQL migrations in
`internal/postgres/migrations/` (`0001_init.sql`–`0066_reusable_automations_and_pipelines.sql`;
retired tables `slice_secrets`, `secret_migrations`, `webhooks`, and
`webhook_deliveries` were dropped in migrations `0046` and `0061`).

### 6.1 Identity, Accounts, Groups & Auth
- `accounts`, `subjects`, `subject_emails`, `account_memberships`,
  `account_invitations`, `sessions`, `cli_login_sessions`
- `agent_registrations`, `api_keys` (scoped API keys, `0056`)
- `account_groups`, `account_group_members`, `slice_group_members` (`0058`)

### 6.2 Core Graph, Blobs & Derived Indexes
- `refs`, `commits`, `blobs`, `blob_slices` (slice-scoped blob visibility),
  `ref_materialized_heads` (`0022`)
- `commit_changed_paths` (path-prefix history index, `0002`)
- `fs_entities`, `current_path_entities`, `commit_entity_changes` (stable
  entity move history, `0003`)
- `outbox` (transactional outbox drained by `indexworker.Worker`, `0009`)
- `blame_snapshots` (cached per-file blame snapshots, `0035`)
- `git_imports`, `git_import_commits` (resumable Git repository imports)

### 6.3 Slices, Lifecycle & Git Mirrors
- `slices`, `slice_included_paths`, `slice_definition_versions` (`0010`),
  `slice_members` (`0034`), `slice_tags` (`0028`), `slice_aliases` (`0036`)
- `slice_ci_daemons` (`0050`), `account_ci_daemons` (`0063`)
- `slice_git_mirrors` (`0051`)

### 6.4 Changesets, Stacks, Path Heads & Collaboration
- `changesets` (including `submit_blocked_code`, `0062`), `patchsets`,
  `path_heads`, `pending_publish`
- `approvals`, `check_results` (`0007`)
- `changeset_stacks`, `changeset_stack_entries` (`0012`)
- `changeset_collaborators` (`0054`), `path_intents` (`0059`)

### 6.5 Agents, CI Checks, Secrets, Automations & Feedback
- `agent_daemons`, `daemon_slice_exposures`, `agent_conversations`,
  `agent_conversation_events`
- `check_runs`, `check_run_logs`, `patchset_secret_allowances` (`0055`)
- `secrets`, `secret_slices` (`0045`), `secret_paths` (`0047`)
- `webhook_events` (internal event log), `trigger_runs`, `trigger_run_logs`
  (`0048`, `0064`–`0066`)
- `feedback` (`0033`)

---

## 7. Go Storage Interfaces (`internal/storage/`)

Runtime services depend on narrow Go interfaces in `internal/storage/` rather
than concrete SQL structs. `internal/postgres` provides the production
implementation and `internal/storage/memory` provides a parity-tested in-memory
implementation for fast service tests:

- **Core interfaces (`internal/storage/interfaces.go`)**:
  - `AuthStore` (embeds `RoleLookupStore`, `APIKeyStore`, `AccountGroupStore`):
    CLI login, external identities, emails, usernames, agent signup/claim,
    delegated agents, orgs/members/invitations, profiles/avatars, API keys, and
    groups.
  - `BlobStore`: blob upsert/lookup, slice associations, content-hash path
    lookups, and Git blob SHA-1 mappings.
  - `ChangesetStore` (embeds `StackStore`, `PathIntentStore`): changeset CRUD,
    patchsets, approvals, check results, submit admission, `PublishPending`,
    collaborators, handoff, stacks, and path intents.
  - `DerivedIndexStore`: `ProcessOutbox`, `OutboxDepth`, `WaitForOutboxDrain`,
    and `RebuildDerivedIndexes`.
  - `RepositoryStore`: refs, commit/tree reads (`GetEntryAtTree`,
    `ListDirectoryAtTree`, `ListTreeAtTree`, `DiffTreeAtTree`), paged path and
    entity history, commit prefix resolution, and Git import tracking.
  - `SliceStore` (embeds `SliceSecretStore`, `SliceMemberStore`,
    `SliceTagStore`): slice definitions/versions, visibility listings, CI
    daemon pools, rename/archive/retire, `merge_on_submit`, and covering-slice
    lookups.
  - `CheckStore`: check run lifecycle, streaming logs, lease-based claiming and
    reassignment (`ClaimCheckRuns`, `RenewCheckRunClaims`,
    `TakeLapsedCheckRuns`), and `patchset_secret_allowances`.
  - `AgentStore`: BYO daemon registration/standby/exposure and conversation
    event streams.
- **Additional store interfaces**:
  - `AutomationStore` (`internal/storage/automations.go`): automation run and
    log persistence, run state/output, and pipeline/callback coordination.
  - `BlameStore` (`internal/storage/blame.go`): blame snapshot read/write.
  - `EventStore` (`internal/storage/events.go`): durable domain event queue for
    automations and Git mirrors.
  - `GitMirrorStore` (`internal/storage/git_mirrors.go`): outbound Git mirror
    config and push queue.
  - `FeedbackStore` (`internal/storage/feedback.go`): feedback submissions and
    resolution.

---

## 8. Blob Upload And Transaction Semantics

Metadata and object storage never participate in a distributed two-phase commit:

1. Client uploads blob bytes (`UploadBlob`, `UploadBlobs`, or
   `UploadBlobStream`).
2. Server hashes bytes in a single pass (`NewBlobHasher`), writes the immutable
   object to R2, and upserts the verified `blobs` row and `blob_slices`
   association in PostgreSQL.
3. Patchset creation rejects any edit referencing a missing or unverified blob.
4. Orphaned unreferenced blobs can be swept asynchronously once past their
   retention window; R2 listing is never authoritative over PostgreSQL
   reachability.

---

## 9. Storage Invariants And Future Work

Invariants:
- Blobs, trees, and commits are immutable and content-addressed (`sha256:<hex>`).
- PostgreSQL `refs.commit_id` moves only via conditional CAS.
- Every referenced blob is verified before any patchset or commit references it.
- Derived index tables (`commit_changed_paths`, entity move tables,
  `blame_snapshots`) can be rebuilt from commits, trees, and blobs.

Unimplemented future extensions:
- **`gitslice.tree.v2`**: embedding `entity_id` directly in tree entries.
- **Multi-region replication**: active-passive or active-active regional
  metadata/object replication.
