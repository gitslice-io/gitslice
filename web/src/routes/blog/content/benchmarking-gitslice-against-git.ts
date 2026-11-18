export const markdown = `How fast is Gitslice compared with Git on the operations developers and coding agents wait on every day? Rather than guess, we built an open benchmark suite, ran it across both tools, eliminated the round trips that were costing us minutes, and measured again. This post has the numbers — including the ones where Git wins.

## 1. What we measured

Our benchmark harness runs four core workloads — matching the suite published by the OpenScion and Scion projects so the numbers can be read side by side. Each fixture creates 10 directories of 4 KiB files filled with seeded random bytes: 100 files per directory (1,000 files total) or 1,000 per directory (10,000 files total):

| Scenario | git | gs |
|---|---|---|
| Import a directory | \`init\`, \`add -A\`, \`commit\` | \`gs create --all --submit\` in a fresh workspace until published |
| Commit one changed file | \`commit -a\` | \`gs create --all\` (recorded on the server) |
| Full clone / checkout | \`clone --no-local\` | \`gs init <account>/home\` |
| One directory | \`clone --filter=blob:none --sparse\`, then \`sparse-checkout set dir0000\` | \`gs init\` of a slice covering \`dir0000\` |

Every run starts with fresh state and empty client caches so nothing is deduplicated from earlier iterations, and every checked-out workspace is compared byte for byte with the source fixture. Before each timed operation, background indexing and disk flushes settle so neither tool inherits cleanup noise from the other.

## 2. Local benchmark results

Medians in milliseconds on a 2-vCPU Linux 5.15 machine (Xeon E5-2680 v2, 3.3 GiB RAM, Go 1.24, Git 2.34; seven runs at 1,000 files, five runs at 10,000 files):

| Scenario | git, 1k | gs, 1k | git, 10k | gs, 10k |
|---|---:|---:|---:|---:|
| Import a directory | 446 | 1,325 | 4,175 | 11,329 |
| Commit one changed file | 70 | 149 | 259 | 485 |
| Full clone / checkout | 734 | **413** | 2,069 | 3,888 |
| One directory | 180 | **83** | 459 | **343** |

- **One-directory checkout:** Gitslice wins at both sizes (**2.2x faster** at 1,000 files, **1.3x** at 10,000). A checkout costs what the slice costs rather than the full repository: it fetches that directory's files and nothing else.
- **Full checkout:** Gitslice wins at 1,000 files (**1.8x faster**) and loses at 10,000 (1.9x slower), where populating both the shared local object cache and the workspace adds extra local disk writes compared with Git's single pack extraction.
- **Import:** Gitslice is about 3x slower on initial import because it uploads blobs remotely, validates every path, and publishes them to the shared global graph rather than writing to a local repository.
- **Commit one file:** Gitslice is roughly 2x slower locally because creating a changeset persists a reviewable revision on the server while Git stays purely local.

Read these numbers with that difference in mind: Gitslice's import and commit paths upload, validate, and persist shared state on a server, whereas local Git stays on disk; at the same time, running client and server on one machine hides real network latency.

## 3. What the first run exposed

Our initial run clocked Gitslice at 5.5 s to import and 4.9 s to check out 1,000 files — and **54 s to import and 53 s to check out 10,000 files**. Nothing in particular was slow on its own; nearly every step paid full per-file overhead:

- **One RPC per file:** Checkouts downloaded one file per call and uploads sent one blob per call, repeating authentication, authorization, and persistence work for every entry.
- **Repeated folder decoding:** Resolving multiple files inside the same folder decoded that folder's directory metadata repeatedly instead of reusing it.
- **Full workspace scans:** Creating a changeset re-read and hashed every file in the workspace just to spot the one file that changed.

## 4. What changed

Six targeted fixes cut local import times by roughly **4x** and checkouts by **12x–14x**:

- **Batched transfers:** Clients read and upload files in parallel batches (up to 256 files or 4 MiB per request; up to 1,000 items server-side), paying authentication and slice checks once per batch.
- **Recursive paginated listings:** Directory listings traverse subtrees in pages instead of issuing a separate round trip per directory.
- **Streamed checkouts:** \`gs init\` receives slice metadata, head commit, file listing, and cold file contents gzip-compressed in a single stream.
- **Immutable tree caching:** Server path resolution caches decoded directory nodes once per content ID so reading a 1,000-entry directory decodes it once.
- **Short-lived token verification caching:** Verified session tokens reuse recent checks in memory while revoked API keys still take effect immediately.
- **Workspace stat cache:** Like Git's index, \`gs\` tracks file size, modification time, and mode so \`gs status\` and \`gs create\` rehash only modified files.

## 5. Over a real network

Loopback benchmarks hide latency — and over a real WAN link, round trips dwarf local CPU time. We timed \`gs init gitslice/gitslice\` (779 files, 9.8 MB) with an empty client cache from a machine roughly 200 ms from the API:

| Step | Time |
|---|---:|
| v0.4.6: one read per file | 455 s |
| Batched reads and uploads | 61.5 s |
| Recursive listing (110 sequential calls collapsed into paged batches) | 8 s |
| 4 MiB HTTP/2 stream windows, gzip compression, fewer preflight lookups | 4.3 s |
| Single streamed checkout (v0.4.7) | **2.7 s** |
| Same checkout with a warm client cache | **1.7 s** |

Tuning HTTP/2 flow control made a huge difference: gRPC's default 64 KiB stream window serialized 9.8 MB across more than 150 round trips. Opening 4 MiB stream and 16 MiB connection windows with gzip compression (shrinking 9.8 MB of source to 2.8 MB on the wire) removed nearly all of that stall.

From the same machine, \`git clone --depth 1\` against GitHub finishes in 1.5 s. Cutting cold \`gs init\` from **455 s to 2.7 s** (**1.7 s** warm) brings full checkouts close to Git — and focused checkouts or [lazy mounts](/doc/lazy) pull ahead as soon as a repository outgrows a single task.

## 6. Where we are still behind

Initial bulk imports and single-file commits remain 2x–3x slower locally than writing strictly to disk, and 10,000-file full checkouts still pay a double local write. We aren't claiming to beat local Git at disk-only writes; our goal is to stay fast on repeated everyday operations while beating full clones when you check out only what you need.

## 7. Reproduce it

The benchmark suite is open source in [gitslice/gitslice](https://gitslice.io/slices/gitslice/gitslice) and can be run locally against a test database:

\`\`\`bash
GITSLICE_TEST_DATABASE_URL=... GITSLICE_BENCH=1 \\
  GITSLICE_BENCH_FILES_PER_DIR=100 GITSLICE_BENCH_RUNS=7 \\
  go test ./tests/cli -run TestBenchmarkAgainstGit -v -timeout 60m
\`\`\`

Run \`gs upgrade\` for v0.4.7+ streamed checkouts and batched transfers, and send any slow workload with \`gs feedback\`.`;
