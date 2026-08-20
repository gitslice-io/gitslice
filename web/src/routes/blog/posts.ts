export interface BlogPost {
  slug: string;
  title: string;
  dek: string;
  date: string;
  readingMinutes: number;
  tags: string[];
  markdown: string;
}

export const blogPosts: BlogPost[] = [
  {
    slug: "native-source-graph",
    title: "A native source graph, with Git at the boundary",
    dek: "Gitslice doesn't run a Git server. Inside, it's one content-addressed source graph; Git is a projection at the edge — here's the model, and why we bet on it.",
    date: "2026-08-19",
    readingMinutes: 6,
    tags: ["architecture", "storage"],
    markdown: `Gitslice makes a bet most version-control systems don't: we don't run a Git server at all. Internally, Gitslice is a **native, content-addressed source graph**; Git is a **compatibility projection at the edge**. Git clients see ordinary repositories, but the source of truth underneath is a single global graph with a real metadata database — not a pile of packfiles. This post is about that model, and why it's the right shape for a large, multi-team, agent-driven codebase.

## 1. The graph is the truth, Git is a costume

Our guiding line is one sentence:

> Native global source graph first. Git compatibility at the boundary.

Concretely, that means:

- **One global commit graph** across every account and every repository-like *slice*, not a pile of independent repos. Accounts own top-level namespaces like \`/acme/payment/…\`, and every file has one canonical absolute path.
- **Content-addressed immutable objects** — blobs, trees, and commits — with our own hashes (\`sha256:…\` with domain separators), *not* Git object ids. Git SHAs are a projection artifact we generate on demand, never our identity.
- **PostgreSQL as the metadata source of truth**: refs, commit metadata, slice definitions, changesets, indexes. Object storage holds the immutable bytes; a commit row stores a \`root_tree_id\` hash, not a file snapshot.
- **Slices are projections, not storage.** A slice is a repository-like *view* over a set of absolute paths in the global graph. Slices can overlap. Cloning one is a Git-shaped read of that view; it isn't a physical repo somewhere.
- **Changesets are the write model.** Users and agents don't push commits directly. They propose a changeset — a reviewable unit scoped to one slice; the server validates it and *it* creates the commit and moves the ref.

We do all this instead of hosting Git because Git's data model fights the product we actually want. Overlapping views of one codebase, per-path authorization, agent-native writes, global indexing, move-preserving history, reviewed submission — all *hard* to bolt onto "a bag of independent packfiles," all *natural* on top of a single content-addressed graph with a real metadata database.

## 2. Why not just host Git, like everyone else?

Because the pain points that dominate a large, multi-team, agent-heavy codebase aren't the ones Git optimizes for.

**Overlap.** Two teams want repository-scoped views that share the same underlying files. In Git that's submodules or a monorepo-with-sparse-checkout and a mountain of tooling. For us it's two slices covering an overlapping path prefix — same bytes, two projections, resolved by a range query over path prefixes.

**Writes you can reason about.** A changeset records, per path, a *base predicate* — what it expected to be true at its base commit — plus its read and write sets. Conflict detection is a per-path compare-and-swap against a \`path_heads\` table, not a three-way merge of opaque trees. Semantic conflicts fall to required checks. This is only tractable because writes go through a validated model, not \`git push\`.

**History and indexing at global scale.** A slice's history is "the global commits that touched the paths this slice currently includes" — answered by a changed-path index, not by walking a repo's DAG. Derived indexes are event-driven and rebuildable from the immutable objects.

Git still matters enormously — for clone, fetch, CI, IDEs, and every tool in the ecosystem. So we keep faith with it at the boundary: projected refs, stable synthetic commit ids, and protected pushes that turn into changesets. Git just doesn't get to define the internals.

## 3. One graph, many cheap views

Once storage is a single graph and a repository is just a named view over it, the things that are painful elsewhere become cheap — and a few things that are impossible elsewhere become ordinary.

- **Sparse, virtual workspaces.** A human or agent hydrates only the paths their slice includes. No cloning the world to touch one service; files stream on demand.
- **Agent-native writes.** An agent proposes a changeset through the API; the server checks coverage, conflicts, and required checks before anything lands. No loose branches to garbage-collect.
- **Overlap without ceremony.** Two teams, two slices, shared bytes. Visibility and roles live on the slice, so the same file can be public through one view and private through another.
- **Global history & identity.** One changed-path index answers "who touched this" across every account, and stable file identity lets history follow a move instead of losing it at a rename.

The repository stops being a storage boundary and becomes what it always should have been: a named view with access rules. That single reframing is where most of Gitslice's leverage comes from.

## 4. What we learned from Cursor

We pay attention to how others solve version control at scale, and Cursor's [*Git at any scale*](https://cursor.com/blog/git-at-any-scale) is the sharpest recent example. It answers a different question than we do — how to host *unmodified* Git at massive scale — and reading it sent us to stress-test our own write path. Ours held up. The lesson we took isn't a fix we borrowed; it's an architectural line we can now draw cleanly.

| | Cursor "Continuity" | Gitslice |
|---|---|---|
| Strategy | Scale the repository | Dissolve the repository |
| Engine | Standard Git on local NVMe | Native content-addressed graph |
| Source of truth | Write-ahead log in object storage | Postgres (metadata) + object store (bytes) |
| The index | Git-on-disk, rebuilt from the log | Postgres |
| Solves | Hosting unmodified Git at massive scale | Agent-driven, multi-account codebases |

The striking thing about Continuity is that it runs with **no database in the hosting path**. Cursor can do that because *Git-on-disk is their index*: object storage holds an append-only log, and a real Git repository — rebuilt on demand, discarded when idle — answers every query about refs and objects. Placement is *computed* with rendezvous hashing instead of stored in a routing table. The two jobs a database usually does — the durable ordered journal and the queryable index — are handled by object storage and by Git itself.

We can't take that shortcut, and we don't want to. **Our Postgres *is* our index** — overlapping slices, per-path authorization, global history, reviewed changesets. None of that falls out of Git-on-disk; it's the whole reason we run a native graph instead of a pile of repositories. So the deepest move Cursor demonstrates — object-storage-as-source-of-truth, no DB in the path — is one we studied closely and deliberately set aside. For us it would replace only the journal while leaving the index exactly where it is.

If we ever outgrow a single primary, the honest answer isn't a bespoke log — it's a geo-distributed SQL database, because what we'd need to distribute is the *index*, not just the journal. That's the real lesson: the right substrate follows from what your database is actually for. Cursor's is a journal; ours is the map.

## 5. The bet

Gitslice's wager is that version control for the next decade — many teams, one codebase, agents writing alongside people — wants a native source graph with a real metadata model, not a fleet of Git servers. Keep Git at the boundary, where the ecosystem lives. Keep the truth in a single content-addressed graph, where the product lives. The best ideas from the Git-hosting world, like an object-storage write log, apply to us too — we just get to put them to work on our own terms, one costume removed.`
  }
];

export function getPost(slug: string): BlogPost | undefined {
  return blogPosts.find((post) => post.slug === slug);
}
