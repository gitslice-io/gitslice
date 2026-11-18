export const markdown = `Google recently described [ABFS](https://developers.googleblog.com/accelerating-development-for-massive-git-repositories/), the virtual file system its Android engineers use to work inside a source tree far too large to check out in full. An ABFS workspace makes every file visible immediately, but downloads a file's contents only the first time something reads it. Nobody waits for a giant checkout, and nobody stores files they never open.

That is the same model behind a Gitslice [lazy workspace](/doc/lazy) (\`gs init --lazy\`), so we compared the two designs side by side. The core architecture already matched. Three things did not — and all three are now shipped:

| | Before | After |
| --- | ---: | ---: |
| Sync a mount six commits forward (1,661 files) | 526 KB (full listing) | **36 KB**, only what changed |
| Local cache size | Unbounded | **10 GB default**, oldest content removed first |
| \`rm -rf\` of 10,000 files in a mount | 15.9 s | **2.6 s** |

## 1. What already matched

- **Content arrives on first read.** Opening a file downloads it on demand. Directory listings, \`stat\`, and tree walks run locally without network round trips.
- **Content is stored once per machine.** Fetched files live in a shared, content-addressed cache across every workspace on your machine. A second workspace for the same slice reuses whatever the first one already fetched.
- **Edits stay local until you share them.** Writes stay on your machine. Nothing is uploaded until \`gs create\` or \`gs modify\`, and even then only your changes move.

## 2. Syncs now download only what changed

A lazy workspace needs the slice's file listing up front: every path, its size, and its content id. Previously, every \`gs sync\` downloaded the full listing again even if only two files had changed — a cost that scaled with the size of the slice rather than the size of the change.

Now \`gs\` remembers the last listing it received for a slice and asks only for what changed since that commit. The server compares the two commits folder by folder, skips unchanged subtrees, and streams back only added or updated entries alongside removed paths.

On our own slice (1,661 files), syncing a mount six commits forward dropped from **526 KB** to **36 KB** (106 changed files and 21 removed paths). When nothing changed, the response is under 1 KB.

No flags or migration steps are required: \`gs sync\` in a mount, \`gs init --lazy\`, and standard \`gs init\` checkouts all use incremental listings automatically. If there is no prior listing on your machine, or the slice definition changed, Gitslice falls back cleanly to a full listing.

## 3. A bounded local cache

ABFS keeps its cache under a disk budget by evicting the least recently used content first. Previously, the Gitslice client cache only grew: every file any workspace had ever read stayed on disk until deleted by hand.

Now, once cached objects exceed \`GS_CACHE_MAX\` (**10 GB** by default), \`gs\` automatically evicts the least recently used content until usage drops below 80% of the limit (checked at most once an hour after commands or from a running mount). Content you still rely on is protected from eviction:

- anything read in the last six hours, so long builds do not lose files opened at the start;
- checkpoints in lazy workspaces and both sides of an unresolved sync conflict, because only your machine holds those bytes.

Evicting cached objects is always safe: any file you need later is simply re-downloaded on its next read. You can inspect, adjust, or test pruning at any time:

\`\`\`bash
gs cache status                      # size, limit, and pinned content
gs cache prune --max 2G --dry-run    # preview pruning down to 2 GB
gs cache prune                       # prune now
export GS_CACHE_MAX=50G              # raise the limit (or "off" to disable pruning)
\`\`\`

## 4. Build outputs and fast deletes inside a mount

ABFS keeps build artifacts outside its virtualized workspace because tools like compilers and package managers emit thousands of small files, and tracking every write in a mounted file system adds overhead. We timed writing, deleting, and scanning 10,000 small files inside a Gitslice mount (similar to \`npm install\` populating \`node_modules/\`):

| 10,000 small files | Plain disk | Mount (before) | Mount (after) |
| --- | ---: | ---: | ---: |
| Write them | 1.4 s | 7.3 s | 7.3 s |
| \`rm -rf\` them | 0.2 s | 15.9 s | **2.6 s** |
| \`gs status\` with one edit | | 1.2 s | 1.2 s |

Deleting directories was the real bottleneck: each removed path checked against every modified entry in the workspace, turning a tree delete into quadratic work (at 20,000 written files, a single delete took 1.8 ms). Removals now inspect only the subtree being removed, dropping that cost to 51 µs and making \`rm -rf\` on large build folders **6x faster**.

Writing thousands of brand-new files in a mount is still slower than plain disk because each file operation crosses the operating system's mount boundary. We considered adding a scratch overlay inside the mount to bypass tracking for build directories, but the OS mount boundary itself accounts for roughly 85% of the write cost — saving about 15% wasn't worth the extra complexity.

When a build tool needs native disk throughput, point its output or cache directory outside the workspace (as Go already does with its build cache). Build artifacts that do stay inside the workspace remain ignored by \`gs status\` whenever \`.gitignore\` covers them, and \`gs restore --ignored\` clears them cleanly.

## 5. What we intentionally left out

ABFS can also fetch prebuilt compiler outputs built on another machine when source inputs match. That works cleanly inside Android because its build system strictly declares every input to every build step, guaranteeing cache soundness.

Gitslice hosts all kinds of projects and toolchains, most without hermitic input manifests. Sharing remote build outputs across machines without complete input tracking risks handing you a stale artifact that looks valid — far worse than running the build. Tools that already verify their own inputs, such as Go's test cache, continue to reuse cached results safely inside a mount.

## 6. Try it

Incremental syncs, bounded caching, and fast deletes require \`gs\` v0.4.31 or later:

\`\`\`bash
gs upgrade
gs init --lazy acme/payments ~/work/payments
cd ~/work/payments
gs sync          # downloads only what changed since the last listing
gs cache status
\`\`\`

See the [Lazy Workspaces docs](/doc/lazy) for mount management, prefetch patterns, local checkpoints, and cache tuning.`;
