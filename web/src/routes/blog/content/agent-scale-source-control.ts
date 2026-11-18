export const markdown = `Two announcements made the same point from different angles: source control designed around human developers starts straining when fleets of coding agents work concurrently.

- **Anthropic** scaled from roughly 2,500 merged changes a day in May to more than 30,000 in September and built [**Scion**](https://www.anthropic.com/engineering/redesigning-source-control), an in-house version control system. Commits, merges, and rebases run on the server; changesets track only changed files; workspaces stay sparse and fetch on demand; and testing runs after merge rather than behind a merge queue. Anthropic notes it is internal only and "by no means a suitable replacement for git for general use."
- **Cursor** launched **Origin** (beta, August 2026), a Git forge where cloud agents clone, branch, open pull requests, and mirror to GitHub. It keeps repositories, branches, and PRs intact and places agent execution beside them. (We compared Cursor's earlier Git hosting architecture in [our first post](/blogs/native-source-graph).)

Gitslice sits squarely between the two: **Scion's server-side, agent-first model, open source and Git-compatible at the boundary.**

## 1. How it works

- **One source graph, many slices.** A [slice](/doc/concepts) is a named projection over paths in your codebase — with its own visibility, members, required [CI checks](/doc/checks), path-owner approvals, [triggers](/doc/triggers), and Git URL. An agent works in a slice sized to its task instead of cloning an entire monorepo.
- **Changesets instead of branches.** Agents edit in a workspace and run \`gs create --submit\` to upload changes, run required checks, and land on the single main line. Merges and rebases run on the server, and changes are admitted per path: edits touching different files land concurrently with zero global merge lock.
- **Git at the edges.** \`git clone https://gitslice.io/git/<account>/<slice>\` works out of the box, \`go get gitslice.io/<account>/<slice>\` works natively, \`gs import\` brings in an existing Git repository, and [Git mirrors](/doc/mirrors) keep GitHub copies up to date. Teams don't have to replace their toolchain to try it.
- **Agents as first-class users.** An agent registers itself without a browser (\`gs auth register-agent\`), receives its own home slice and API key, and can later be claimed by its human owner. Every command supports \`--json\` and \`gs schema\` discovery, \`gs blame\` traces lines back to landing changesets, \`gs doctor\` checks local setups, and \`gs feedback\` reaches us directly.

Gitslice is developed on Gitslice itself inside \`gitslice/gitslice\` — every change lands as a changeset through its own CI checks and mirrors out to GitHub. In stress tests, 102 concurrent agents each landed a change with a median end-to-end time of roughly 15 seconds from push to submit.

## 2. Side by side

| | Scion (Anthropic) | Origin (Cursor) | Gitslice |
|---|---|---|---|
| Model | Its own VCS, server-side | Git forge: repositories, branches, pull requests | Source graph and slices, changesets |
| Git | None | It is Git | At the edges: clone, Go modules, import, mirrors |
| Unit of work | Changeset on trunk | Branch and pull request | Changeset (with revisions) on main |
| Partial views | Sparse checkouts, per-directory permissions | Per repository | Slices with their own access, members and checks |
| Testing | After merge | Checks on pull requests | Required checks before landing |
| Availability | Internal only | Paid plans, beta | Open source (Apache-2.0), hosted at gitslice.io |

The sharpest design choice is pre-landing checks versus post-merge testing. Scion dropped pre-merge queues because when thousands of changes land while tests are running, gating every diff on yesterday's trunk loses signal; they merge first and let follow-up agents fix breakages.

Gitslice gates **before** landing, but checks **per path**: required checks run against a changeset's revision, and it lands whenever no other change touched its paths since its base. Unrelated changes never wait on each other, while overlapping edits restack and re-check cleanly. For most codebases, keeping main green while avoiding queue serialization gives the best of both worlds.

## 3. Where Gitslice stands today

- **Regional scale:** Gitslice currently runs in one region and has been tested with hundreds of concurrent agents, not 30,000 merges a day yet.
- **One main line by design:** Work uses stacked changesets on a single trunk rather than long-lived branches.
- **Shipping rapidly:** Gitslice is under active development — \`gs feedback\` comes straight to us, and every report gets a response.

## 4. Point your agents at it

To try it with a coding agent, give it this prompt (or walk through the [Agents docs](/doc/agents)):

> Read https://gitslice.io/llms.txt, install gs, register yourself with \`gs auth register-agent --username <name> --email <my email>\`, do some work in your home slice, and send anything confusing with \`gs feedback\`.

Or run the commands directly:

\`\`\`bash
curl -fsSL https://gitslice.io/install.sh | sh
gs auth register-agent --username <agent-name> --email <you@example.com>
gs init <agent-name>/home
\`\`\`

Then claim your agent at [gitslice.io/claims](https://gitslice.io/claims). Replies to feedback show up in \`gs feedback list --mine\`.

The source lives in [gitslice/gitslice](https://gitslice.io/slices/gitslice/gitslice) and mirrors to [github.com/gitslice-io/gitslice](https://github.com/gitslice-io/gitslice). We'd love to hear how your agents work with per-path gating versus post-merge testing.`;
