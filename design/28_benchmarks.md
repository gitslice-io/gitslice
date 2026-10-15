# Benchmarks Against Git

How long do the operations people and agents wait for take with `gs`, next to
`git` on the same files? `tests/cli/bench_git_test.go` times four workloads,
the ones the OpenScion and Scion projects publish for their own clients, so the
numbers can be read side by side.

## Workloads

The fixture is 10 directories of 4 KiB files of seeded pseudorandom bytes:
100 files each (1,000 files) or 1,000 each (10,000 files).

| Scenario | git | gs |
| --- | --- | --- |
| Import directory | `init`, `add -A`, `commit` | `gs create --all --submit` in a fresh workspace, until the change is published |
| Commit one changed file | `commit -a` | `gs create --all` (the changeset is recorded on the server) |
| Full clone / checkout | `clone --no-local` | `gs init <account>/home` |
| One-directory clone / checkout | `clone --no-local --filter=blob:none --sparse`, then `sparse-checkout set dir0000` | `gs init` of a slice of `dir0000` |

Setup is outside the clock: writing the fixture, the import a checkout starts
from, creating the slice. Every checkout is compared with the fixture byte
for byte. Before the commit scenario both checkouts sit for a second, so
neither tool re-hashes files written in the same second as its own index.

Each run starts its own server (a fresh database schema and object store) and
uses fresh client caches, so nothing is deduplicated against an earlier run.
Before every timed step the runner lets the server finish indexing in the
background (path history, moves), flushes the disk (`sync`) and waits for the
machine to go idle, so no step pays for the one before it. Without this, git
was timed while the kernel was still writing back gs's files, and its
pack-writing clone waited behind that writeback.
The server runs in the test process on Postgres and a filesystem object store,
over loopback gRPC; `gs` commands run in-process too, signed in as a
registered agent with an API key. `git` runs as a process per command, with
the user's configuration and hooks isolated.

```bash
GITSLICE_TEST_DATABASE_URL=... GITSLICE_BENCH=1 \
  GITSLICE_BENCH_FILES_PER_DIR=100 GITSLICE_BENCH_RUNS=7 \
  go test ./tests/cli -run TestBenchmarkAgainstGit -v -timeout 60m
```

Without `GITSLICE_BENCH` the test runs a 20-file fixture once, as part of the
e2e suite, to keep every scenario working.

## Results

Measured 2026-10-15 on a 2-vCPU VM (Xeon E5-2680 v2, 3.3 GiB), Linux 5.15,
Go 1.24, Postgres 16, git 2.34. Medians in milliseconds; 7 runs at 1,000
files, 5 at 10,000.

| Scenario | git, 1k | gs, 1k | git, 10k | gs, 10k |
| --- | ---: | ---: | ---: | ---: |
| Import directory | 446 | 1,325 | 4,175 | 11,329 |
| Commit one changed file | 70 | 149 | 259 | 485 |
| Full clone / checkout | 734 | 413 | 2,069 | 3,888 |
| One-directory clone / checkout | 180 | 83 | 459 | 343 |

A one-directory checkout costs what the directory costs, not the
repository: gs fetches that directory's files and nothing else, so it beats
git's partial clone at both sizes. Where gs is still slower:

- **Import** uploads 41 batches at 10,000 files, each blob written to the
  object store (about a millisecond each on this disk), then validates and
  publishes 10,000 paths into the global graph.
- **Full checkout at 10,000 files** writes each file twice, into the client
  cache and into the workspace, where git writes one pack and the files.
- **Commit** spends about 70 ms in the server calls that record the
  changeset; the scan itself reads only the changed file.

Read them with the differences in mind:

- **gs does more.** Import lands the change on a server (uploaded, validated,
  published to the global graph) where git writes a local repository; a gs
  commit records the changeset on the server, git's stays local.
- **No network.** Client and server share a machine. Batching (below) is what
  keeps a real network's round trips from multiplying by the file count.
- **No process start for gs.** gs runs in the test process; git starts a
  process per command (a few milliseconds each).
- **Storage.** The object store is the local filesystem, not R2.

## Over a real network

The benchmark has no network, so it cannot show round trips. Checking out
`gitslice/gitslice` (779 files) from gitslice.io, from a cold client cache,
took 455 s with v0.4.6 and 61.5 s once file reads and uploads were batched:
the rest was listing, 110 `ListDirectory` calls one after another, each a
round trip plus a cold tree read. Recursive listing removes those (61.5 s to
8 s). What remained was the network: from that machine (about 200 ms from
the API) each call costs a round trip, and with gRPC's default 64 KiB
HTTP/2 window a large response (the listing, the files) arrived 64 KiB per
round trip. So the client opens 4 MiB stream and 16 MiB connection windows
(the server's upload buffers match), compresses `ReadFiles` and
`UploadBlobs` with gzip (source shrinks about 3.5 times; 9.8 MB of
`gitslice/gitslice` is 2.8 MB), asks for the slice and the ref head at once,
and lists each included path without resolving it first. For comparison,
`git clone --depth 1` of the GitHub mirror takes 1.5 s from the same
machine.

## What the first run found

The first measurement (before the runner waited between steps) had gs at
5.5 s to import 1,000 files and 4.9 s for a full checkout, and 54 s and 53 s
at 10,000 files. Nothing was slow by itself; everything was per file:

- **Checkout read one file per call.** `gs init` called `ReadFile` for each
  file, one after another, and each call paid authentication, an
  authorization lookup and database reads.
- **Uploads were one blob per call,** each with a slice authorization, an
  object write and four database statements.
- **Validation looked up each edit** (its blob, and its path's base entry)
  with its own queries.
- **Path lookups decoded the directory again for each file.** Tree nodes were
  cached as bytes, so reading every file of a 1,000-entry directory decoded
  that directory 1,000 times.
- **Every commit re-read every file.** A scan hashed (and copied into the
  client cache) the whole workspace to find one changed file.

## Changes

- `RepositoryService.ReadFiles`: up to 1,000 files (32 MiB) per call, read
  at one commit or tree. It authorizes once per account (or once through a
  slice), resolves the root tree once and walks it, and reads objects
  concurrently. `gs init` collects the files first, then fetches those not in
  the client cache in batches of up to 256 files or 4 MiB, four batches at a
  time; files over 4 MiB still stream one by one. Duplicate content is
  fetched once.
- `BlobService.UploadBlobs`: up to 1,000 blobs (64 MiB) per call, one slice
  authorization, concurrent object writes, and one statement each to record
  the blobs, their Git ids and their slice. Captures and `gs fs upload` send
  small blobs in batches of up to 256 blobs or 4 MiB.
- Both clients fall back to the per-file calls when the server does not
  implement the batch ones (`Unimplemented`), so a new CLI works with an older
  server.
- `ListDirectory` takes `recursive`: everything under a path inside an
  account (or inside the slice), paged by path, up to 10,000 entries a page.
  The tree store reads it a level at a time, each level's directories
  concurrently. Checkout lists each included path once instead of once per
  directory; a server that answers one level (`recursive` unset in the
  response) is walked a directory at a time, as before.
- Changeset validation reads all edits' blobs in one query and resolves the
  base tree once, walking it for each path.
- The tree store keeps decoded nodes (`internal/treestore/node_cache.go`): a
  least-recently-used cache bounded at 200,000 entries in total. Nodes are
  named by their content, so they never go stale; readers share them, and a
  writer gets a copy to edit.
- The server remembers resolved signed tokens (JWTs: Clerk session tokens and
  service tokens) for 30 seconds, never past their expiry
  (`server/subject_cache.go`). They are valid by signature and expiry alone,
  so this extends nothing. API keys and sessions are still checked against the
  database on every call, so revoking one takes effect at once.
- Workspaces keep a stat cache (`.gs/stat_cache.json`), as Git's index does:
  each file's size, modification time and mode with its content hash. A scan
  reuses a hash only while all three match, the file was last modified before
  the cache was written, and the client cache still holds the content; files
  modified within 10 ms of a scan's start are not recorded. Checkout seeds it
  with the files it writes, so the first commit after a checkout reads only
  what changed.
