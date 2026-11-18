export const markdown = `Most version-control platforms start by running Git servers and spend years layering monorepo permissions, sparse tooling, and review workflows around them. Gitslice makes a different bet: **we don't run a Git server underneath at all.**

Inside, Gitslice is a single **native, content-addressed source graph** backed by a queryable metadata index; Git is a **compatibility projection at the edge**. Standard Git clients see ordinary repositories, while everything underneath — overlapping team views, per-path access control, move-preserving history, and concurrent agent writes — operates directly on one unified graph.

## 1. The graph is the truth; Git is the interface

One principle shapes the entire system:

> Native global source graph first. Git compatibility at the boundary.

In practice, that gives teams and coding agents five guarantees out of the box:

- **One global commit graph.** Accounts organize code under canonical paths such as \`/acme/payments/...\` inside a single shared history rather than juggling disconnected repositories.
- **Content-addressed immutable history.** Blobs, trees, and commits are identified by cryptographic hashes rather than Git SHAs. Deterministic Git object IDs are generated at the edge when projecting a slice for Git clients.
- **Queryable history and indexes.** Refs, slice definitions, changesets, and changed-path indexes are indexed alongside immutable snapshots, so questions like "which commits touched \`/acme/payments\`?" or "do two open changesets collide?" resolve directly without walking packfiles.
- **Slices as lightweight views.** A [slice](/doc/concepts) is a named projection over any set of paths — one service, a library plus its consumers, or an entire team directory. Slices can overlap freely without duplicating history.
- **Validated changesets as the write model.** Humans and agents don't blindly force commits onto shared history; they propose reviewable changesets scoped to a slice, and the server validates path bases, required checks, and owners before advancing main.

Trying to bolt overlapping views, per-path permissions, global move-aware history, and lock-free agent concurrency onto isolated packfiles fights Git's data model. On top of one content-addressed graph and structured index, they fit naturally.

## 2. Why not just host Git repositories?

Because the bottlenecks in a multi-team, agent-heavy codebase are coordination and partial visibility, not single-repo pack compression:

- **Overlapping views without submodules.** Two teams work inside separate repository-sized slices that share underlying files seamlessly, with independent visibility and reviewers.
- **Per-path concurrency instead of global locks.** Every changeset records the paths it touches and the base revisions it built against. Disjoint edits land concurrently without waiting on unrelated commits, while overlapping edits restack and re-check against required owners and tests.
- **Global history across renames.** Slice logs come straight from an indexed history graph rather than walking a full monorepo DAG, and file history follows moves intact.

Git still runs the developer ecosystem — editors, Go modules, CI tools, and existing workflows. So we maintain clean compatibility at the edge: deterministic \`git clone\`, Go module imports, \`gs import\`, and [Git mirrors](/doc/mirrors) (see [For Git Users](/doc/git-users)). Git stays welcome at the boundary; it simply doesn't dictate the storage model inside.

## 3. One graph, many workflows

Turning a repository from a storage silo into a named view simplifies everyday workflows:

- **Lazy workspaces on demand.** Check out or [mount](/doc/lazy) only a slice's paths and fetch file bytes lazily on first open instead of downloading unrelated code.
- **Agent-safe writes.** Coding agents declare path intents, checkpoint locally, and submit changesets gated by checks and owners — leaving no stale remote branches behind.
- **Selective visibility without copies.** Keep a core package private inside an internal slice while exposing a public slice over shared interfaces from the exact same commits.

## 4. What we learned from Cursor's architecture

We study other large-scale version control designs closely, and Cursor's [*Git at any scale*](https://cursor.com/blog/git-at-any-scale) is one of the sharpest architectural writeups in the field. It solves a different problem than Gitslice — hosting *unmodified* Git at massive scale — and reading it clarified a crisp design line between the two approaches:

| | Cursor "Continuity" | Gitslice |
|---|---|---|
| Strategy | Scale the repository | Dissolve the repository |
| Engine | Standard Git on local NVMe | Native content-addressed graph |
| Source of truth | Write-ahead log in object storage | Structured index + immutable content store |
| The index | Git-on-disk, rebuilt from the log | Queryable global path and slice index |
| Solves | Hosting unmodified Git at massive scale | Agent-driven, multi-account codebases |

Continuity's cleverest move is removing any database from the serving path: an append-only log stores writes, rendezvous hashing places replicas, and standard Git on local NVMe acts as the queryable index.

Gitslice intentionally makes the opposite trade because **our index *is* the product**: overlapping slices, per-path authorization, cross-slice conflict detection, and global move-preserving history simply do not exist inside a single Git repository on disk. The right substrate follows from what your system indexes: Continuity indexes one repository log; Gitslice indexes one connected graph.

## 5. The bet

Version control is shifting from human teams isolated in separate repos to fleets of humans and agents building across shared codebases. We believe that future wants a single content-addressed source graph with a first-class metadata model — keeping Git at the boundary where your tools live, and keeping the graph inside where your coordination lives.`;
