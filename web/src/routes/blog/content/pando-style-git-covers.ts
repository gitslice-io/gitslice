export const markdown = `OpenAI recently wrote about [Pando](https://openai.com/index/scaling-git-at-openai/), the backend powering Git at their monorepo scale. Two of its core ideas directly addressed a bottleneck we hit when serving Git clones over Gitslice's source graph: answer fetches from stored, already-compressed packs instead of building a pack per client, and never force fresh readers or CI jobs to fetch the same tree one file at a time.

Applying both ideas cut a cold \`git clone\` of Gitslice from timing out past 100 seconds to **5.1 seconds**, and collapsed a fresh CI tree checkout from over a thousand requests to **1**.

## 1. How Gitslice serves Git

Gitslice keeps history as changesets, paths, and slice definitions inside a single source graph, and exposes each slice to Git clients as an ordinary repository. Because slices can grow to tens of gigabytes, that Git view is materialized lazily: commits and trees are derived from history, while file payloads are fetched on demand when a client requests them.

That works well for incremental fetches and sparse clones, but an uncached full \`git clone\` asks for every historical object in the slice at once.

## 2. What an uncached clone used to cost

We timed a fresh \`git clone\` of the Gitslice slice itself (623 commits, 11 MB packed clone size) with no prebuilt pack cache:

| Step on an uncached clone | Time |
|---|---:|
| Materialize commit and tree history | 3.9 s |
| Read every file in history (5,364 files, 162 MB) | 41 s |
| Compress objects into a pack for that single client | > 55 s |
| **Result** | **timed out past 100 s** |

Once warmed, the same clone finished in about 5 seconds. Nearly every slow second on that first attempt went into reading thousands of historical files one by one and compressing an identical pack that was thrown away as soon as the clone finished. That is the exact problem Pando was built to solve.

## 3. Covers: prebuilt packs for fast cold clones

Pando answers fetches using stored, precompressed packs called **covers**, compacting smaller incremental packs together over time much like an LSM tree. Gitslice applies the same design at its Git boundary:

- **Precomputed pack and bitmap:** After a full clone, Gitslice builds a cover asynchronously by gathering every reachable object, repacking them with deltas and a bitmap index (\`git repack -a -d -b\`), and storing that immutable snapshot alongside the head commit it covers.
- **Fast cold clones:** When an uncached clone arrives, Gitslice restores the cover directly and uses its bitmap index and stored deltas to stream the pack immediately — zero historical files fetched or recompressed per client.
- **Bounded incremental packs:** Subsequent landings add small packs on top. Once more than eight incremental packs accumulate, the next full clone triggers a fresh compacted cover in the background.
- **Scoped where it helps most:** Partial and shallow clones stay lightweight and lazy, and covers apply to slice projections up to 512 MiB so pack snapshots stay fast to load.

Clients need zero new flags or configuration. Any standard \`git clone\` benefits automatically. Building the first cover for \`gitslice/gitslice\` took 35 seconds asynchronously and came to 10.5 MB; subsequent uncached clones completed end to end in **5.1 seconds**:

| Cold clone of \`gitslice/gitslice\` | Before | After |
|---|---:|---:|
| Time | over 100 s (timeout) | **5.1 s** |
| Historical files read per clone | 5,364 | 0 |
| Work per clone | read every file, compress every object | stream stored cover |

One practical lesson shaped how covers trigger: originally, Gitslice scheduled cover creation only *after* a full clone finished. The slices that needed a cover most timed out before finishing, so they never received one. Gitslice now queues cover generation whenever any full clone begins, regardless of whether the first request completes in time.

## 4. A CI checkout in one request

Pando's second lesson is about fan-out: when multiple CI jobs or agents need the exact same revision, reading files individually multiplies load and network latency.

Previously, Gitslice's CI runner populated a clean workspace by walking the patchset tree one directory and file at a time — 133 directory listings and up to ~1,400 individual file reads for the Gitslice tree when the local cache started empty.

Now the single streaming \`Checkout\` operation used by \`gs init\` also supports patchset trees and scoped path prefixes, enforcing the exact same per-path permissions. The CI runner makes one streaming request and receives the listing and file contents together (while very large files still stream cleanly on their own):

| CI checkout of the Gitslice tree | Before | After |
|---|---:|---:|
| Requests | 133 listings + up to ~1,400 reads | **1** |
| Time | — | 1.5 s |

Paired with cover packs on Git endpoints, both native CI workspaces and Git clones avoid repeating per-file work when readers converge on the same tree.

## 5. Where we differ from Pando

Pando keeps Git's repository model intact and scales branches independently. Gitslice changes the underlying write model: changesets are admitted per path on a single main line, so agents working on different files never wait on each other or manage long-lived branches, while slices provide sparse, permissioned views over the codebase.

Git remains our compatibility boundary — see [For Git Users](/doc/git-users) and [CI Checks](/doc/checks) to try \`git clone\` or \`gs agent start --ci\` against your own slice or inspect [gitslice/gitslice](https://gitslice.io/slices/gitslice/gitslice).`;
