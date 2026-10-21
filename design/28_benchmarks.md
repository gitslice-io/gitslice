# Benchmarks Against Git

Three questions, each with a benchmark that anyone can rerun:

1. [**Landing in parallel.**](#landing-in-parallel) When many agents land
   changes on one codebase at the same moment, how long does it take, and how
   often does an agent have to start over? (`tests/cli/bench_agents_test.go`)
2. [**Opening a workspace.**](#opening-a-workspace) How long until an agent has
   a tree it can work in, for a real repository, from a cold start?
   (`bench/workspace_bench.py`)
3. [**Everyday operations.**](#everyday-operations) Import, commit and
   checkout, next to `git` on the same synthetic files.
   (`tests/cli/bench_git_test.go`)

Every table says what was measured, on what, and what it does not show. Where
`git` is faster, the table says so.

## Landing in parallel

Agents do not take turns. A fleet working on one codebase lands changes
constantly, and with Git every landing moves the branch under the others:
a push is rejected when someone landed first, and the agent pulls, rebases and
pushes again, each round racing the rest. With `N` agents that is up to
`N(N-1)/2` extra rounds in all.

Gitslice admits a change by the paths it changes, on the server: changes to
different paths land with no relation to each other, and with
`gs slice update --merge-on-submit on` the server merges a change whose file
was also edited upstream (design/07 section 7.4) instead of refusing it.

`TestBenchmarkParallelAgents` has every agent in its own workspace or clone,
all based on the same head and set up outside the clock. The clock starts when
they all start, and stops when the last change has landed. A `git` agent runs
`git push`, and when it is rejected `git pull --rebase` and `git push` again. A
`gs` agent runs `gs create --all --submit`, and when the submit is refused for
a stale base runs `gs sync` and `gs submit` again. A retry is one such extra
round. At the end a fresh clone or checkout is read and every agent's change
must be in it.

```bash
GITSLICE_TEST_DATABASE_URL=... GITSLICE_BENCH=1 \
  GITSLICE_BENCH_AGENTS=16 GITSLICE_BENCH_RUNS=3 \
  go test ./tests/cli -run TestBenchmarkParallelAgents -v -timeout 60m
```

Measured 2026-10-21 on the 2-vCPU VM described below; medians; time is to the
last landing, retries are summed over the agents.

| Scenario | agents | git: time | git: retries | gs: time | gs: retries |
| --- | ---: | ---: | ---: | ---: | ---: |
| Each agent edits its own file | 8 | 1.44 s | 28 | 1.15 s | 0 |
| | 16 | 5.28 s | 120 | 2.43 s | 0 |
| One file, a block each | 8 | 1.40 s | 28 | 3.89 s (merge off) | 35 |
| | 16 | 5.37 s | 120 | 11.5 s (merge off) | 145 |
| One file, a block each, merge on submit | 8 | | | 1.84 s | 0 |
| | 16 | | | 3.47 s | 0 |

Read it with these in mind:

- **Retries are the point.** With `git`, the retries were `N(N-1)/2` in every
  run (28 at 8 agents, 120 at 16), and the time grew faster than the number of
  agents (1.4 s to 5.3 s for twice the agents). With `gs` and separate files
  there are none, and the time grows with the agents (1.15 s to 2.4 s).
- **Same file, merge off, is slower than git.** A refused `gs` submit costs a
  `gs sync` and a second submit, more than a rebase and push, and there are
  more of them. This is what merge on submit removes: the server does the
  merge in the submit, so the agent does nothing twice.
- **The Git remote here is a directory on the same disk,** so a `git` retry is
  cheap. Over a network each retry adds round trips to a remote that every
  agent is hitting at once, so `git` times here are the best case.
- **`gs` runs in the test process** and `git` as a process per command, as in
  the other benchmarks; and `gs` lands each change on a server (uploaded,
  validated, published to the global graph, durable) where `git push` writes
  to a directory.
- **No required checks or approvals** are set. With them, a merged change
  waits for its checks again, as a rebased pull request does on any host.
- Merging is serialized per slice on a server instance, so agents landing
  together queue instead of making each other's merge stale. The server also
  tries again, a bounded number of times, when the base moves under a merge
  anyway (another instance, or a landing that did not merge).

## Opening a workspace

Time until an agent has a tree it can work in, for a real repository, from a
cold start. `bench/workspace_bench.py` runs each scenario 3 times, each in a
new directory and (for `gs`) an empty client cache and a new mount supervisor,
and reports the median with the range:

- **to tree:** the command returns and the files are there to open;
- **read 20:** reading 20 files spread evenly over the tree, as an agent that
  opens a few of them to do a task would (what a lazy mount has to fetch);
- **search cold / warm:** `rg` over the whole tree right after, and again.

```bash
bench/workspace_bench.py --git https://github.com/Sunshinin11/Ryujinx_Neo \
  --slice sunny/ryujinx-neo --runs 3 --pattern 'TODO|FIXME'
```

The repository is [Ryujinx_Neo](https://github.com/Sunshinin11/Ryujinx_Neo)
(4,508 files, 58.6 MB, 4,018 commits) and the same content as the Gitslice
slice `sunny/ryujinx-neo`, from a 2-vCPU VM about 33-58 ms from GitHub and,
over the day, 35-195 ms from the Gitslice API, which is why the ranges are
wide. Measured 2026-10-21 with `gs` v0.4.16. Seconds; median (range):

| Scenario | to tree | read 20 files | search, cold | search, warm |
| --- | ---: | ---: | ---: | ---: |
| `git clone --depth 1` | 4.30 (4.24-4.68) | 0.00 | 0.09 | 0.08 |
| `git clone --filter=blob:none` | 5.98 (5.81-6.07) | 0.00 | 0.09 | 0.07 |
| `git clone` (full history) | 8.48 (7.74-9.35) | 0.00 | 0.11 | 0.10 |
| `gs init` | 7.96 (6.89-12.37) | 0.00 | 0.09 | 0.09 |
| `gs init --lazy` | **2.56** (2.47-5.94) | 4.80 (3.98-10.10) | **107** (92-232) | 1.10 |
| `gs init --lazy` + `gs fetch` | 12.6 (5.7-50.8) | 0.01 | 1.52 | 1.10 |

What it says:

- **A full checkout costs what a full clone costs.** `gs init` (8.0 s) is next
  to `git clone` with full history (8.5 s), and slower than a shallow clone
  (4.3 s). Gitslice has no shallow mode to compare with that: the slice is the
  tree.
- **A lazy mount is fast to mount and slow to read the first time.** It is
  mounted in 2.6 s, before any content is fetched, and each file is then
  fetched when it is first read: 20 files took 4.8 s, about 0.24 s each, a
  round trip to the server. That is right for a task that opens a handful of
  files out of thousands, and wrong for one that reads them all: a search
  over the whole tree took 107 s (92-232 s) where Git took 0.09 s.
- **For work that reads the whole tree, fetch first.** `gs fetch` brings every
  file in batches in parallel: 12.6 s after the mount, and the search is
  then 1.5 s. The first run took 51 s and the third 5.7 s; the Gitslice API
  scales to zero and the first requests to a cold instance are slow.
- **A mounted tree is not a local directory.** With every file cached, the
  search takes 1.1 s through the mount where it takes 0.09 s on a checkout:
  every file operation passes through FUSE.
- **The slice is the whole repository here,** so this benchmark gives
  Gitslice no help from slices. What a slice saves is in the one-directory row
  below: a checkout costs what its directory costs.

This is a starting point for work on the lazy mount, not a result to be proud
of: per-file latency is the server round trip (about 0.24 s here, and the
server's share of it is not small), and a reader that walks a tree faster than
the mount fetches it waits on every directory.

## Everyday operations

`tests/cli/bench_git_test.go` times four workloads, the ones the OpenScion and
Scion projects publish for their own clients, so the numbers can be read side
by side.

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
and lists each included path without resolving it first: 4.3 s. Checkout
then made it one call: 2.7 s from an empty client cache, 1.7 s from a warm
one. For comparison, `git clone --depth 1` of the GitHub mirror takes 1.5 s
from the same machine.

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
- `ListDirectory` takes `recursive`: everything under a path inside an
  account (or inside the slice), paged by path, up to 10,000 entries a page.
  The tree store reads it a level at a time, each level's directories
  concurrently. Hydrating a path lists it once instead of once per directory.
- `RepositoryService.Checkout` streams what `gs init` needs in one call: the
  slice and the head commit, everything under the slice's included paths,
  and, when the client's object cache is empty (a fresh machine or sandbox),
  the files' content (each distinct content once, files up to 4 MiB, 256 MiB
  in all), gzip-compressed. A warm cache asks for the listing only and
  fetches what it lacks with `ReadFiles`.
- The CLI needs a server with these calls; it no longer falls back to the
  per-file ones.
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
