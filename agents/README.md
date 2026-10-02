# Gitslice agents on Cloudflare Artifacts

**Artifacts gives every agent a repo. Gitslice gives them one codebase.**

Every agent gets its own Git repository on Cloudflare Artifacts, a fork of the
slice it works on, and uses plain `git`. Everything the agents push lands in
one Gitslice codebase:

- reviewed by a Workers AI agent;
- validated path by path;
- merged line by line when two agents touched the same file;
- escalated to a person when policy says so.

There are no branches to rebase and no merge queue to wait in.

**Live:** https://agents.gitslice.io is the dashboard for the demo slice
`demo/store`.

## Why

Coding agents need two things that pull in opposite directions:

- **Isolation:** a repository of their own, where a bad change can't break
  anyone else.
- **Integration:** their work has to become one codebase, at agent speed.

**Artifacts provides the isolation.** A repo per agent is cheap. Its own docs
advise "one repo per agent" and not to use one shared repo as a queue for many
agents.

**Gitslice provides the integration.** It is a native source graph with Git at
the boundary: code lives in one global tree, and a *slice* is a Git-compatible
view of part of it. Changes are changesets, and Gitslice validates each one
against the base of every path it touches, not against the head of a branch.
So two agents that change different files both land, even when each started
from an old snapshot.

## How it works

```
 agent ──git push──▶ Artifacts repo (fork of the slice baseline)
                         │  repo.pushed event (Event Subscriptions → Queue)
                         ▼
               Worker: gitslice-agents
                 · diff the push against the baseline   (Artifacts binding)
                 · open or update a Gitslice changeset  (one patchset per push)
                 · review agent on Workers AI           (gpt-oss-120b)
                 · submit; on a stale path, merge line by line and resubmit
                 · Durable Object per slice: sessions, who touches what, live stream
                         │
                         ▼
               Gitslice: per-path validation → one landed history (plain Git)
                         │
                         └──▶ new Artifacts baseline (import from Gitslice's Git endpoint)
```

1. **Open a session.** `POST /v1/sessions` forks the slice's baseline in
   Artifacts and returns a remote and a scoped write token. The Hub tells the
   agent who is already working on the paths it intends to touch.
2. **Work with plain git.** Clone, edit and commit. The message body (the
   agent's notes) becomes the changeset description. Then
   `git push origin HEAD:main`.
3. **Land.**
   - Artifacts emits `repo.pushed`, and the Worker's queue consumer picks it
     up.
   - The Worker diffs the pushed tree against the baseline with the Artifacts
     binding (`readTree`, `readBlob`).
   - It uploads the changed files to Gitslice and opens or updates the
     agent's changeset.
4. **Review.**
   - Deterministic checks run first; JSON must parse.
   - Protected paths (`PROTECTED_PATHS`) escalate to a human.
   - A Workers AI model reviews the rest: approve, request changes, or
     escalate.
   - A separate reviewer identity approves, so Gitslice's "required
     approvals" rule holds.
5. **Submit.**
   - Disjoint changes land concurrently.
   - If another agent already landed on the same file, the submit fails with
     a stale path base. The Worker then three-way merges the file onto the
     current head and resubmits.
   - Overlapping edits go back to the agent as a `conflict` signal. The agent
     opens a new session with `resume` on a fresh baseline and reworks its
     change. It stays the same changeset, with a new patchset.
6. **Coordinate.**
   - A Durable Object per slice (`Hub`) keeps the sessions and the paths each
     one touches.
   - It sends the signals: overlaps, reviews, merges, conflicts, escalations,
     landings.
   - It streams them to the dashboard.
   - When something lands, the next session imports a fresh baseline from
     Gitslice's Git endpoint.

The landed history is an ordinary Git repository. Clone it with:

```bash
git clone https://gitslice.io/git/demo/store.git
```

Each commit is one changeset, with a `Gitslice-Commit:` trailer.

## Try it

### Against the hosted deployment

You need Node 20+, `git`, and an API key. The submission form includes a key
for the judges.

```bash
cd agents
AGENTS_API_KEY=<key> node swarm/swarm.mjs --slice demo/store --reset --concurrency 32
```

Open https://agents.gitslice.io/slices/demo/store while it runs. `--reset`
first puts the demo store back to its seed, so every agent has its work to do.
It takes about two minutes.

**The swarm** (`swarm/storefront.mjs`) runs a hundred agents:
- translators for twenty-eight locales, eight of them new;
- accessibility fixes for twenty components;
- twenty catalog products;
- unit tests;
- docs pages and product copy written with gpt-oss-120b on Workers AI;
- deliberate collisions:
  - two edits to one file that merge;
  - two edits to one line that conflict;
  - a payment change that needs a human;
  - an invalid push the reviewer rejects.

**One agent by hand:**

```bash
curl -s -X POST https://agents.gitslice.io/v1/sessions \
  -H "authorization: Bearer $AGENTS_API_KEY" -H 'content-type: application/json' \
  -d '{"slice":"demo/store","agent":"me","task":"Fix a typo in the README","intent":["demo/store/README.md"]}'
# → { "id", "remote", "token", "signals", ... }
git -c http.extraHeader="Authorization: Bearer $TOKEN" clone "$REMOTE" work && cd work
$EDITOR demo/store/README.md
git commit -am "docs: fix a typo" -m "Why I changed it."
git -c http.extraHeader="Authorization: Bearer $TOKEN" push origin HEAD:main
curl -s https://agents.gitslice.io/v1/sessions/$ID     # status, changeset, signals
```

To approve an escalated change, run `gs cs approve <changeset>` as a member of
the slice. The Hub notices and lands it.

### Your own deployment

**Requirements:**
- a Cloudflare account on Workers Paid (Artifacts, Queues, Workers AI);
- a Gitslice server (`gitslice.io` or your own);
- two Gitslice identities with write access to the slice: one authors
  changesets (bridge), one approves them (reviewer).

```bash
npm install
npx wrangler queues create gitslice-agents-events
npx wrangler queues subscription create gitslice-agents-events --source artifacts --events repo.pushed
# Edit wrangler.jsonc: route, namespace, GITSLICE_* URLs, SLICES, PROTECTED_PATHS.
npx wrangler secret put GITSLICE_TOKEN            # bridge identity's API key
npx wrangler secret put GITSLICE_REVIEWER_TOKEN   # reviewer identity's API key
npx wrangler secret put AGENTS_API_KEY            # comma-separated keys for agents
npx wrangler deploy
```

Public slices are imported straight from Gitslice. For a private slice,
publish the baseline yourself with `scripts/publish-baseline.sh <account/slice>`.

## API

| Route | |
|---|---|
| `POST /v1/sessions` | `{slice, agent, task, intent?, resume?}` → `{id, remote, token, signals, baseline}`. Needs a key. |
| `GET /v1/sessions/:id` | Status, changeset, review and signals. |
| `POST /v1/sessions/:id/pushed` | Optional nudge after a push. Artifacts events do this on their own. |
| `GET /v1/slices/:account/:slice` | Stats and sessions. |
| `GET /v1/slices/:account/:slice/stream` | WebSocket: snapshot, session updates, events. |
| `POST /v1/slices/:account/:slice/baseline` | `{action: "create" \| "adopt" \| "import"}`: a pushed baseline, or a fresh import now. Needs a key. |
| `POST /v1/slices/:account/:slice/restore` | Put a demo slice back to its seed, clear the dashboard and delete the last run's repos. Needs a key. |
| `POST /v1/assist` | A Workers AI completion for agents that write with a model. |
| `GET /slices/:account/:slice` | The dashboard. |

## Code

| | |
|---|---|
| `src/index.ts` | Routes and the queue consumer. |
| `src/hub.ts` | The per-slice Durable Object: sessions, baselines, coordination, stream. |
| `src/land.ts` | The landing pipeline: diff, changeset, review, submit, merge. |
| `src/review.ts` | The review agent: checks, policy, Workers AI. |
| `src/merge.ts` | Myers diff, three-way merge, unified diff. Tests: `npm test`. |
| `src/artifacts.ts` | Tree diffs and naming over the Artifacts binding. |
| `src/gitslice.ts` | Connect-JSON client for the Gitslice API. |
| `src/dashboard.ts` | The live dashboard. |
| `swarm/` | The swarm runner and the storefront scenario. |
| `demo/storefront/` | The demo codebase the slices were seeded with. |

The design notes are in
[`design/22_agents_on_artifacts.md`](../design/22_agents_on_artifacts.md).
