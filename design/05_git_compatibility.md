# Gitslice Git Compatibility & Projection Design

This document specifies how Gitslice exposes slices as Git-compatible
repositories over HTTP while keeping the native global source graph,
changesets, submit validation, and storage model as the source of truth. It
consolidates the original Git compatibility specification and the large-slice
lazy projection, pack mirror, and cover-compaction design (formerly
`design/archive/23_large_git_projection.md`).

Related documents:

- [00_product.md](00_product.md): product overview and Git-facing workflows
- [01_gitslice_architecture_design.md](01_gitslice_architecture_design.md): top-level architecture
- [02_storage.md](02_storage.md): commits, refs, trees, blobs, and object storage
- [04_cli_design.md](04_cli_design.md): native CLI behavior
- [07_conflict_resolution.md](07_conflict_resolution.md): path-level conflicts and submit admission
- [35_git_mirrors.md](35_git_mirrors.md): outbound slice mirroring to external Git remotes (e.g. GitHub)

## 1. Core Principle

Git compatibility is a boundary layer.

```text
Native global source graph first.
Git compatibility at the boundary.
```

Gitslice is not implemented internally as a Git server. Native commits, trees,
refs, slices, changesets, and submit state in PostgreSQL and object storage
remain authoritative. The Git HTTP gateway (`internal/gitcompat`) projects a
slice into a deterministic Git history for `git clone` / `git fetch` and
translates `git push` into native changesets.

## 2. Repository Identity & Access Control

Each slice is exposed at:

```text
https://gitslice.io/git/{account}/{slice}.git
```

Examples:

```bash
git clone https://gitslice.io/git/nicholas/identity.git
git clone https://gitslice.io/git/acme/payment.git
```

### 2.1 Anonymous Reads & Authentication

- **Public slices**: requests without credentials may `git clone` or `git fetch` a public slice.
- **Private and missing slices**: anonymous callers receive the same `401` Basic Auth challenge for both private and non-existent slices so slice existence is not leaked.
- **Invalid credentials**: credentials that are present but invalid are always rejected, even on public slices.
- **Pushes**: `git push` always requires valid credentials with write permission on the target slice.

## 3. Architecture & Data Access

Unlike external CLI clients, the Git gateway runs in-process with the server
and reads **directly from storage interfaces** (`ProjectorStores`:
`storage.AuthStore`, `storage.RepositoryStore`, `storage.SliceStore`,
`storage.BlobStore`, plus `ObjectStore`) rather than routing reads through
core gRPC RPCs.

For writes (`git push`), the gateway delegates to `ChangesetService` and
`BlobService` so Git-originated changesets pass through the exact same
validation, containment, path-base, and approval/check pipeline as native CLI
and API writes.

`git-http-backend` handles the smart HTTP wire protocol for fetches, with
responses streamed directly to the HTTP response writer (never buffered whole
in memory).

## 4. Deterministic Projection Model

### 4.1 Path Layout

Gitslice canonical paths are account-rooted (e.g. `/nicholas/services/identity/auth.go`).
Inside a cloned slice repository, the checkout preserves the canonical path
layout minus the leading slash:

```text
identity/
  nicholas/
    services/
      identity/
        auth.go
```

There are no custom mount aliases for `git clone` / `git fetch`. (Outbound
mirrors to external Git remotes such as GitHub can strip a single root folder;
see [35_git_mirrors.md](35_git_mirrors.md).)

### 4.2 Projected Refs & Commits

Native `refs/global/main` projects to `refs/heads/main`.

- **One Git commit per qualifying native commit**: the projector reads the native first-parent chain directly from `commits` (`ListCommitChain`), never from asynchronously derived path indexes. A native commit qualifies when its changed paths intersect the slice's `included_paths` and the projected Git tree actually changes. Directory-only changes (such as `mkdir`) produce no Git commit because Git cannot represent empty directories.
- **Deterministic IDs**: Git commit SHA-1s are a pure function of `(slice_id, slice_definition_hash, native history)` with no wall-clock time, randomness, or map iteration order. Any server instance starting from an empty cache computes identical commit IDs, keeping Go pseudo-versions and CI checkouts stable. Changing a slice's `included_paths` starts a new projection epoch.
- **Commit metadata**:
  - Author and committer are the native author's username with `<username>@users.noreply.gitslice.io` (falling back to `Gitslice`), or the imported Git author when preserved by `gs import`.
  - Timestamps come from the native commit time.
  - The message includes a `Gitslice-Commit: <native commit id>` trailer.
  - Non-qualifying commits never contribute metadata.
- **Empty slice**: a slice with no qualifying commits projects to an empty Git repository with no `refs/heads/main`.
- **Fetch by commit ID & partial clone**: projected bare repositories configure `uploadpack.allowReachableSHA1InWant`, `uploadpack.allowAnySHA1InWant`, and `uploadpack.allowFilter`, enabling Go pseudo-version fetches and `--filter=blob:none` partial clones.

### 4.3 Tags & Go Subdirectory Semver Tags

Immutable slice tags (`SliceService.CreateTag` / `ListTags`, `gs tag create | list`, stored in `slice_tags`) are projected as lightweight Git tags (`internal/gitcompat/projection_tags.go`):

- Each tag points `refs/tags/<name>` at the projected Git commit for the newest qualifying native commit at or before the tagged native commit.
- **Go subdirectory semver copy**: for a slice with a single included path (e.g. `/acme/payment`), semver tags (`vX.Y.Z...`) are also published as `refs/tags/<path>/<name>` (e.g. `refs/tags/acme/payment/v1.2.0`), allowing Go toolchains to resolve modules rooted at the slice's canonical subdirectory.
- A tag targeting a commit newer than the projection's processed head is published once the projection advances to that commit.

## 5. Lazy Go Pack Builder & On-Demand Hydration

Building a large slice eagerly by reading every blob and piping into
`git fast-import` is too slow and memory-heavy for ephemeral Cloud Run
instances (1 vCPU, 1 GiB memory, in-memory tmpfs). Gitslice separates building
commit/tree history from serving file contents.

### 5.1 Recording Git Blob IDs (`blobs.git_blob_id`)

Git's object ID for a blob is `SHA-1("blob <size>\0" + bytes)`. Native Gitslice
content IDs remain SHA-256, and each uploaded blob also records its Git SHA-1
in `blobs.git_blob_id`:

- Unary uploads (`UploadBlob`, `UploadBlobs`) hash bytes already in memory.
- Streaming uploads (`UploadBlobStream`) compute the Git SHA-1 on the fly when the client declares the blob size.
- Older or unsized blobs can be backfilled via `GITSLICE_GIT_BLOB_BACKFILL=1` (opt-in background job because Cloud Run throttles CPU between requests; best run via a dedicated job until `ListMissingGitBlobIDs` is empty).
- During a lazy build, any blob without a recorded `git_blob_id` is read from object storage (6 concurrent workers, 8 reads ahead) and its computed ID is recorded so subsequent builds pay zero blob reads.

### 5.2 Default Lazy Projection (`lazy.go`, `pack.go`)

Whenever `ProjectorStores.Blobs != nil` (the default in production), the
projector builds history lazily in Go:

1. Fetches `blobs.git_blob_id` for all changed files in the pending commits.
2. Computes Git tree and commit objects directly in Go (`gittree.go`, `pack.go`) — byte-for-byte identical to `git fast-import` (verified by equivalence tests covering executable modes, symlinks, special filenames, and directory removals).
3. Writes one packfile per **1,000 commits** (`lazyChunkCommits = 1000`), updating `refs/heads/main` and checkpointing `gitslice_projection.json` after each chunk so an interrupted build resumes instead of restarting.
4. Merges small incremental history packs in size tiers (16 at a time) so single-commit landings do not accumulate unbounded packfiles.

**Fallback**: when `ProjectorStores.Blobs == nil` (e.g. library callers without a blob store), the projector falls back to `git fast-import` (`fast_import.go`) with `fastimport.unpackLimit=0` so imported objects are always written as packs rather than loose objects.

### 5.3 On-Demand Blob Hydration (`lazy_hydrate.go`)

Because lazy history packs contain only commits and trees, file blobs are
hydrated on demand before `git-upload-pack` answers a fetch:

1. Parse the incoming `git-upload-pack` request (`uploadreq.go`) to inspect requested wants and filters.
2. Ask Git which missing objects are needed (`git rev-list --objects --missing=print`).
   - A `--filter=blob:none` partial clone needs **zero** blobs.
   - A lazy single-file fetch needs **one** blob.
   - A full clone hydrates all missing blobs once and marks the head complete in memory.
3. Stream missing blobs from the object store in parallel packs into the bare repo, verifying each blob's SHA-1 against its recorded `git_blob_id` before accepting it.

## 6. Durable Pack Mirror (R2)

To avoid rebuilding histories from scratch after every Cloud Run deploy or
scale-to-zero cold start, every slice's projection is mirrored to object
storage under `git-mirror/` (`internal/gitcompat/packmirror.go`):

- **16 MiB multipart chunks**: packs and indexes are uploaded in 16 MiB parts (bounding per-upload memory to at most two 16 MiB buffers on 1 GiB instances). Part size is recorded per object in the manifest so it can change without breaking restores.
- **Gzipped state**: `gitslice_projection.json` is stored gzipped (~10× smaller) and streamed during restore.
- **Cold-start restore**: a new instance downloads the manifest's packs in parallel and updates refs with zero history replay. If restore fails, the instance falls back to building from scratch.
- **Cloud Run CPU-throttling accommodation**: a request that advances the projected head waits up to **20 seconds** for the mirror publish to complete while the container still has request CPU. Failed uploads retry up to 3 times with backoff unless superseded by a newer snapshot.
- **Safe concurrent GC**: before publishing, the manifest is re-read so packs uploaded by another instance are reused. Dropped packs are deleted two publishes later so concurrent restores from the previous manifest do not race with deletion.
- **Observability**: `gitslice_git_mirror_operations_total{operation,result}` tracks publish and restore outcomes.

The earlier `GITSLICE_GIT_MIRROR_DIR` Git-remote mirror has been removed; the R2 pack mirror replaces it for all slice sizes.

## 7. Covers & Compaction (`cover.go`, `compact_http.go`)

A lazy projection restores commits and trees quickly (~4s on the Gitslice
source slice), but a cold full clone still had to hydrate all 5,364 blobs
(162 MB) and have `git-upload-pack` delta-compress them on the fly, exceeding
Cloudflare's 100-second request timeout.

To make cold full clones fast without client-side changes, Gitslice builds
pre-compressed **covers** (inspired by OpenAI's Pando):

- **What compaction does**: acquires the repo's write serve lock, hydrates every missing blob in the history, runs `git repack -a -d -b` to produce one delta-compressed pack with a `.bitmap` index, records `cover_head` and `complete_at` in the projection state, and mirrors the cover pack + `.bitmap` to R2.
- **Fast cold clones**: a new instance restoring a covered slice marks the repository complete immediately; `git-upload-pack` serves the pre-compressed pack nearly verbatim with zero blob hydration.
- **Trigger thresholds & guardrails**:
  - `coverMaxPacks = 8`: after a full fetch, if the repo has no cover or more than 8 packs (each landing adds a small pack), compaction is requested after the response finishes.
  - `coverCooldown = 10m`: an instance requests compaction for a given slice at most once every 10 minutes.
  - `coverMaxBytes = 512 MiB`: slices whose on-disk projection exceeds 512 MiB are skipped so an in-memory Cloud Run container does not OOM.
- **Out-of-band execution (`POST /git/_internal/compact`)**: because Cloud Run only allocates CPU while serving a request and the triggering clone must not wait, the server POSTs `/git/_internal/compact` to its own Cloud Run URL (`GITSLICE_SELF_URL`, bypassing Cloudflare's 100s timeout), authenticated with a token derived from `GITSLICE_SECRETS_KEY`. When `GITSLICE_SELF_URL` is unset (tests, local dev), compaction runs in-process.
- Blobs hydrated for incremental fetches after a cover are not re-mirrored until the next cover compaction.

## 8. Push Into Changesets (`push.go`, `push_pack.go`, `push_protocol.go`)

Protected accepted refs (`refs/heads/main`) cannot be updated directly via
`git push`. Pushes must target a changeset ref:

```bash
git push origin HEAD:refs/changes/new
git push origin HEAD:refs/changes/<changeset-id>
```

### 8.1 Push Rules & Validation

1. **Single ref update**: each push must contain exactly one ref command; ref deletions (`0000...`) and direct pushes to `refs/heads/main` are rejected.
2. **Base resolution**:
   - The base is the nearest first-parent ancestor of the pushed commit that belongs to the projected history, and pushed commits above it must form a single linear chain.
   - A push on top of the projected head uses the current native head as `base_commit_id`.
   - A push on an older projected commit keeps that commit's native `base_commit_id`, so submit validation catches overlapping concurrent edits (`stale_path_base`) instead of silently overwriting them, while disjoint edits land cleanly.
   - A slice with no history accepts a root commit diffed against the empty tree, allowing `git push <url> HEAD:refs/changes/new` to seed an empty slice.
   - Pushes based on an unrelated history or obsolete projection epoch fail with `NEEDS_REBASE`.
3. **Containment & native changeset creation**:
   - The diff is converted to canonical account-rooted paths and verified to lie entirely within the URL's authoring slice (cross-slice pushes are rejected).
   - Empty diffs are rejected.
   - Blobs are uploaded via `BlobService`, and `ChangesetService.CreateChangeset` / `UpdateChangeset` creates or updates the changeset and patchset.
   - Push status messages report the 10-character `ShortChangesetID` and `<short-id>.<patchset-number>` (e.g. `Created changeset 97048df407 patchset 97048df407.1`).

## 9. Large Blob Handling

Git LFS is a non-goal. Native Gitslice storage holds blobs in object storage
with PostgreSQL metadata, and the Git gateway projects them as ordinary Git
blobs (served lazily on demand or via partial clone `--filter=blob:none`).

## 10. Outbound Git Mirrors

For continuous backup and read-only mirroring of a slice to an external Git
remote (such as GitHub), Gitslice provides per-slice outbound mirrors
configured via `gs slice mirror`. Outbound mirrors reuse the slice's
projection, optionally strip a single root folder to mirror at repository root,
push commits and tags without force-pushing, and omit Go's `<folder>/vX.Y.Z`
tag copies. See [35_git_mirrors.md](35_git_mirrors.md).

## 11. Known Limits & Future Work

- **Persistent disk tier for >10 GB slices**: on Cloud Run, the bare-repo cache lives in container memory (`tmpfs`), and nothing evicts hydrated packs when space runs short. Full clones of slices larger than `coverMaxBytes` (512 MiB) or multi-GB slices require moving the Git tier to a stateful host with an SSD (keeping the R2 pack mirror as the durable source for fast rebuilds).
- **In-memory push cap**: incoming `git-receive-pack` payloads are currently buffered in memory up to 128 MiB rather than streamed to disk.
- **State file parsing**: `gitslice_projection.json` is parsed per request rather than kept in an in-memory cache or compact binary index.
- **Blob ID backfill runner**: `GITSLICE_GIT_BLOB_BACKFILL=1` only advances while Cloud Run has request CPU; a dedicated Cloud Run Job should run it to completion on legacy datasets.
- **Commit-graph & direct pack serving**: writing Git `commit-graph` files for fast negotiation on 50k+ commit slices, or serving packs directly from native storage without a bare repository, remain future optimizations.
- **Fetchable changeset refs**: exposing `refs/changes/<id>/<patchset>` for direct `git fetch` of open patchsets by CI tools.
