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

Nothing uses the ids yet; they are the prerequisite for building a projection
from metadata and for serving blobs on demand.

## Done

- The Git response is streamed (item 1).
- An optional mirror to a Git remote for slices that fit one repository
  (`GITSLICE_GIT_MIRROR_SLICES` and `GITSLICE_GIT_MIRROR_DIR`): a new instance
  restores the projection and extends it instead of replaying the history. Its
  test starts a second server with an empty cache and checks that it restored.
- Partial clone is enabled (`uploadpack.allowFilter`).
- Git blob ids are recorded at upload, with a backfill for older files.

## Not done, and not measured

Everything in Design except the above. Nothing has been run on a slice larger
than 29 MB; the figures for 10 GB are linear extrapolations. Validating needs a
machine with at least 50 GB of free disk and a large real repository imported
into a Gitslice slice.

## Decisions needed

1. Where the Git tier runs, and the budget for a persistent SSD.
2. Whether the pack mirror may use the production R2 bucket.
3. A test environment with the disk and bandwidth to import a 10 GB repository.
