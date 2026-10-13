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
    slug: "agent-scale-source-control",
    title: "Source control at agent scale: Scion, Origin and Gitslice",
    dek: "Anthropic built its own version control system for agents, and Cursor built a Git forge for them. Here is where Gitslice fits: slices over one source graph, Git at the edges, open source. And how to point your agents at it.",
    date: "2026-10-13",
    readingMinutes: 5,
    tags: ["agents", "architecture"],
    markdown: `Two announcements this year say the same thing: source control built for humans is straining under coding agents.

- **Anthropic** went from about 2,500 merged changes a day in May to more than 30,000 in September, and built [**Scion**](https://www.anthropic.com/engineering/redesigning-source-control), an in-house version control system. Commits, merges and rebases happen on the server; a changeset records only the files that changed; checkouts are sparse and fetched on demand; and testing moved after merge instead of a merge queue. They are clear it is not for everyone: it is "by no means a suitable replacement for git for general use", and it is not released.
- **Cursor** shipped **Origin** (beta, August 2026), a Git forge inside Cursor where cloud agents clone, branch, commit and open pull requests, with GitHub mirroring. It keeps the familiar model of repositories, branches and pull requests, and moves agents next to it. (We compared Cursor's earlier work on hosting Git at scale in [our first post](/blogs/native-source-graph).)

Gitslice sits between the two: Scion's server-side, agent-first model, but open source and Git-compatible at the edges.

## 1. How it works

- **One source graph, many slices.** A slice is a named set of paths with its own access, members, required checks and approvals, webhooks and Git URL. An agent works in a slice the size of its task, not a clone of the monorepo.
- **Changesets instead of branches.** You edit in a workspace, and \`gs create --submit\` uploads the change, waits for the slice's required checks and lands it on the single main line. Merges and rebases run on the server. Changes are admitted per path, so changes to different files don't wait on each other: there is no global merge lock.
- **Git at the edges.** \`git clone https://gitslice.io/git/<account>/<slice>\` works, \`go get gitslice.io/<account>/<slice>\` works, and \`gs import\` brings in an existing Git repository. You don't migrate your whole toolchain to try it.
- **Agents are users, not integrations.** An agent signs itself up without a browser (\`gs auth register-agent\`) and its human claims it later. It can share a slice with another agent (\`gs slice share\`), and every command has \`--json\`. \`gs blame\` shows the changeset behind each line, \`gs doctor\` diagnoses setup problems, and \`gs feedback\` tells us what broke.

Gitslice is built with Gitslice: its source is the slice \`gitslice/gitslice\`, every change lands as a changeset through its own CI, and GitHub is a read-only mirror. In a demo run, 102 agents each landed a change, with a median of about 15 seconds from push to landed.

## 2. Side by side

| | Scion (Anthropic) | Origin (Cursor) | Gitslice |
|---|---|---|---|
| Model | Its own VCS, server-side | Git forge: repositories, branches, pull requests | Source graph and slices, changesets |
| Git | None | It is Git | At the edges: clone and push per slice, Go modules, import |
| Unit of work | Changeset on trunk | Branch and pull request | Changeset (with revisions) on one main line |
| Partial views | Sparse checkouts, per-directory permissions | Per repository | Slices with their own access, members and checks |
| Testing | After merge | Checks on pull requests | Required checks before landing |
| Availability | Internal only | Paid plans, beta | Open source (Apache-2.0), hosted at gitslice.io |

The question we find most interesting is testing. Scion gave up on a merge queue: when thousands of changes land while tests run, gating each one on a test of the merged result stops making sense, so they test after merge and let agents fix the fallout. Gitslice gates before landing, but per path: a changeset's required checks run on its own revision, and it lands if no other change touched its paths since its base. Changes to different files never wait on each other; changes to the same file are re-based and re-checked. We think that is the right default for most codebases, and a slice could reasonably choose Scion's model instead.

## 3. What it isn't, yet

- It is not Scion scale: one region, Postgres plus object storage. We have run it with hundreds of agents, not 30,000 merges a day.
- There is one main line by design, with no long-lived branches.
- It is early (v0.4.5). Blame on a 14,000-line file takes about ten seconds. Rough edges are why \`gs feedback\` exists, and every piece of feedback gets an answer.

## 4. Point your agents at it

If you run coding agents, give one this and let it set itself up:

> Read https://gitslice.io/llms.txt, install gs, register yourself with \`gs auth register-agent --username <name> --email <my email>\`, do some work in your home slice, and send anything confusing with \`gs feedback\`.

Or by hand:

\`\`\`bash
curl -fsSL https://gitslice.io/install.sh | sh     # or: brew install gitslice-io/tap/gitslice
gs auth register-agent --username <agent-name> --email <you@example.com>
gs init <agent-name>/home
\`\`\`

Then claim your agent at [gitslice.io/claims](https://gitslice.io/claims). Agent feedback comes straight to us, and the answers show up in \`gs feedback list --mine\`.

The source is the slice [gitslice/gitslice](https://gitslice.io/slices/gitslice/gitslice), mirrored at [github.com/gitslice-io/gitslice](https://github.com/gitslice-io/gitslice). We would like to hear what you think of the design, especially gating before landing per path versus testing after merge.`
  },
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

Gitslice's wager is that version control for the next decade — many teams, one codebase, agents writing alongside people — wants a native source graph with a real metadata model, not a fleet of Git servers. Keep Git at the boundary, where the ecosystem lives. Keep the truth in a single content-addressed graph, where the product lives. The best ideas from the Git-hosting world still travel to us — but they arrive changed by what our database is for: a map of one graph, not a journal of one repo. We take what fits and leave the rest, one costume removed.`
  }
];

export function getPost(slug: string): BlogPost | undefined {
  return blogPosts.find((post) => post.slug === slug);
}
