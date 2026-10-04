# 23. Git Projection For Very Large Slices

Status (2026-10-04): the first steps are in; the rest needs a decision on where
the Git tier runs.

## Goal

A slice of 10 GB or more, with tens of thousands of commits, can be cloned
(whole, partially or shallowly), fetched and extended as changes land, and a
new server instance starts serving it in minutes, not hours.

## How it works today

A slice's Git repository is a projection: one Git commit per native commit
that touches the slice, written into a bare repository on the serving
instance's disk with `git fast-import`, and served by `git http-backend`.
The projection is deterministic, so any instance can rebuild it, and a state
file (`gitslice_projection.json`) lets an instance extend its cache instead of
replaying the history.

Production runs this inside Cloud Run: 1 vCPU, 1 GiB of memory, no volumes.
The cache is therefore on the instance's in-memory filesystem and is lost with
the instance, including at every deploy.

## What a cold build costs

Measured on the Gitslice source slice (456 commits, 3,059 blobs, 115 MB of
file contents, 29 MB packed), from production logs:

| Stage | Time | Rate |
|---|---|---|
| Listing native commits and their changes | 18 s | 40 ms per commit |
| Fetching blobs from the object store | 20 s | 5.7 MB/s |
| Importing into Git | 20 s | 5.7 MB/s |

Stages overlap, and the total was 39 to 43 s. Extrapolated linearly (not
measured at that size) a 10 GB slice with 50,000 commits needs about 35 minutes
to list, 30 minutes to fetch and 30 minutes to import: over an hour and a half,
paid by whichever request arrives first after every deploy.

## What breaks at 10 GB and more

1. **The response was buffered in memory.** `serveBackend` read all of git's
   output before sending any of it, so one clone of a large slice would
   exhaust the instance. *Fixed in this change: the response is streamed.*
2. **The cache cannot fit.** A 10 GB repository does not fit a 1 GiB instance
   whose disk is memory.
3. **The cold build is one request.** All commits go through a single
   `git fast-import` run while the first request waits, with no checkpoints. A
   crash or timeout at minute 80 starts over.
4. **Blobs are held whole in memory,** up to 48 at once. A few large files can
   exhaust the instance.
5. **The state file is parsed on every request.** Its per-file map is about
   200 bytes per file, so 300,000 files is 60 MB of JSON read per Git request.
6. **Nothing repacks.** Incremental updates add packs over time, and there is
   no commit-graph or bitmap index, so large fetches slow down.
7. **A push is read into memory,** capped at 128 MB.
8. **A Git remote cannot mirror it.** Cloudflare Artifacts caps a repository at
   1 GB. Any Git-remote mirror also restores by fetching the whole history.

## Design

### The cache lives on a disk that survives restarts

Git serving needs a real, persistent disk. Cloud Run cannot give it one at
this size, so the Git tier moves to a stateful service (a VM or StatefulSet
with an SSD), behind the existing gateway. The API tier stays on Cloud Run. A
deploy then does not lose the cache.

### A durable copy in the object store, as packs

For a new disk, a failed disk and a second replica, the projection is also kept
in the object store (R2) as immutable files:

- every pack and index that `fast-import` or a repack writes, uploaded once,
  never modified;
- a small manifest naming the current packs, the refs, and the state file.

Restore downloads the packs in parallel and writes the manifest's refs. There is
no replay: at 100 to 200 MB/s a 10 GB restore is one to two minutes (assumed,
to be measured). This replaces the Git-remote mirror for large slices; that one
stays for slices that fit a single repository.

### Building is resumable and bounded

- Commits are imported in batches. Each batch ends with a checkpoint (state
  file and pack upload), so a restart continues from the last one.
- The build runs in the background. Until the head is reached, a request gets
  `503` with `Retry-After` and the build's progress, not a hung connection.
- Blobs are streamed into `fast-import` (their sizes are known) with a budget
  on bytes in flight, instead of held whole.
- Listing runs ahead of fetching, on its own concurrency.

### Serving

- The state is kept in memory per repository and stored compactly.
- A background job repacks idle repositories and writes the commit-graph and a
  bitmap index.
- Partial clone (`--filter=blob:none`) is enabled. With sparse checkout it lets
  an agent work on part of a large slice without downloading the rest.
- Large pushes stream to disk rather than into memory.

### Later: serve Git straight from the native store

The best answer is not to materialize the repository at all: answer fetches
by building packs from native objects on demand. That needs each file's Git
blob id stored when it is uploaded, so trees can be built without reading
contents. It is a larger project and is only worth it if the work above leaves
cold starts or disk costs too high.

### Git blob ids, recorded at upload

Git's id for a file is the SHA-1 of `blob <size>` and a NUL byte followed by the
bytes, so it cannot be known without reading the file. Gitslice's own ids stay
SHA-256, and are still the identity of the data. Next to them, each upload now
records the Git id in `blobs.git_blob_id`:

- the unary upload hashes the bytes it already holds;
- the streaming upload hashes on the way through when the client declared the
  size (Git's id starts with it); otherwise the backfill does it;
- `GITSLICE_GIT_BLOB_BACKFILL=1` turns on a background job that reads each older
  file once and records its id. It is off by default because it reads every
  file, and Cloud Run only gives a background job CPU while a request is being
  served (CPU throttling), so in production it needs that setting changed or a
  separate runner.

This separates the two jobs of the projection. Building commits and trees needs
ids and paths, not contents. Serving file contents is needed only when a client
asks for them. A test checks that the id stored for an upload equals both what
`git hash-object` prints and the id of the file in a cloned projection.

The lazy projection (below) builds trees and commits from these ids and serves
file contents on demand.

## Done

Each piece is off by default and tested end to end against Postgres.

- **Streaming responses** (item 1): git's output is sent as it is produced.
- **Partial clone**: `--filter=blob:none` works, as do single-file fetches by id
  (`uploadpack.allowFilter`, `allowAnySHA1InWant`).
- **Git blob ids** (`blobs.git_blob_id`): recorded at upload;
  `GITSLICE_GIT_BLOB_BACKFILL=1` fills in older files.
- **Lazy projection** (`GITSLICE_GIT_LAZY_BLOBS=1`):
  - History is built from paths, modes and the recorded blob ids. Trees and
    commits are computed in Go and written as one pack per 1,000 commits, each
    a checkpoint, so an interrupted build resumes.
  - The objects are byte for byte what `git fast-import` writes: a test builds
    the same history both ways, with executable files, symlinks, awkward names
    and directory removal, and requires identical commit ids. Another switches
    a server with an eager cache to lazy and requires the same ids as a fresh
    eager build. Existing ids, Go pseudo-versions included, stay valid.
  - A file with no recorded id is still read and written, as before.
- **Hydration**: before git answers a `git-upload-pack` request, the server reads
  the request, asks git which file contents it would need
  (`rev-list --objects --missing=print`), and writes only those into the
  repository, streamed from the object store in parallel packs. Each file is
  checked against its recorded id as it is written; a mismatch refuses the
  file. A blobless fetch needs none, a lazy single-file fetch needs one, and a
  full clone needs everything, once (a complete head is remembered).
- **Pack mirror** (`GITSLICE_GIT_MIRROR_PACKS=1` with
  `GITSLICE_GIT_MIRROR_SLICES`, `*` for all): the history packs, the state and a
  manifest are kept in the object store (R2), in parts of 32 MiB because R2
  holds a whole object in memory to upload it. A new instance downloads the
  packs in parallel and points the branch at the head; nothing is replayed.
  Packs the server no longer uses are deleted. On a lazy projection these packs
  hold only commits and trees, since the files are already in R2.
- **Pack merging**: a landing adds a handful of objects, so history packs are
  merged in size tiers (16 at a time) rather than collecting one per commit.
- A mirror to a **Git remote** (`GITSLICE_GIT_MIRROR_DIR`) for slices that fit a
  repository.

### Measured

On a 2-CPU test host, with 5 files changed per commit in a monorepo-shaped tree:

| | Result |
|---|---|
| Lazy history build | 2.3 to 2.8 ms per commit, at 2,000 and 10,000 commits |
| Before pooling zlib writers | 11 to 13 ms per commit (the garbage collector was the largest cost) |
| Repository size | 61 MB of objects and 4 MB of state for 10,000 commits and 16,700 files |
| Hydration, large files | 37 MB/s, memory flat at about 35 MB |
| Hydration, small files | about 2,000 files per second |

At those rates, computing the history of a 50,000-commit slice takes a few
minutes. Reading the native history to list each commit's files, about 40 ms per
commit, is now the largest cost of a build from scratch, and the pack mirror
avoids it on a cold start. These are unit-level measurements; nothing has been
run against a slice of tens of gigabytes.

## Not done

- **Where the hydrated files live.** A full clone writes every file into the
  repository. On Cloud Run that is memory, so a 10 GB slice still needs a larger
  instance or a disk, and nothing evicts hydrated packs when space runs short.
  The Git tier on a persistent disk (see Design) is still the plan.
- **Background work on Cloud Run.** The mirror upload and the blob id backfill
  run in the background, and Cloud Run only gives CPU to a container while it
  serves a request (CPU throttling is on). They need that setting off, or a
  separate runner.
- **The state file is still parsed on every request,** and pushes are still read
  into memory (up to 128 MB).
- **Files uploaded without a size** get their Git id from the backfill, not at
  upload, and the eager build path still holds whole files in memory.
- **No repack or commit-graph** for fast negotiation on very large histories.
- **Serving without a repository at all,** the "Later" section above.
- Not run on a very large slice. Validation needs a machine with at least 50 GB
  of free disk and a real repository imported into a slice.

## Rolling it out

1. Deploy the code with everything off. Nothing changes.
2. Turn on `GITSLICE_GIT_BLOB_BACKFILL=1` and let it reach every file. Watch the
   "git id backfill progress" log; uploads record their own ids from now on.
3. Turn on `GITSLICE_GIT_MIRROR_PACKS=1` with `GITSLICE_GIT_MIRROR_SLICES=*`. The
   next request for each slice builds as before and publishes its packs.
4. Turn on `GITSLICE_GIT_LAZY_BLOBS=1`. Existing caches are extended, not
   rebuilt, and new builds skip the file reads.

## Decisions needed

1. Where the Git tier runs, and the budget for a disk that holds the hydrated
   files. The pack mirror makes that disk a cache, so it need not be fast or
   durable; its size and read speed decide what a full clone costs.
2. Whether the pack mirror may use the production R2 bucket (it writes under
   `git-mirror/`).
3. A test environment with the disk and bandwidth to import a 10 GB repository.
