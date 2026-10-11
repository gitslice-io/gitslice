# Contest submission handover: Gitslice × Cloudflare Artifacts

Cloudflare's "Build the next GitHub" contest. Status as of **2026-10-11**.
Everything is built, deployed and checked. What remains is for the entrant to
do in person.

| Date | What |
| --- | --- |
| **2026-10-14, 11:59 PM PDT** | Submission deadline |
| 2026-10-14/15 | Artifacts billing starts |
| **2026-10-21** | Finalists present in person in San Francisco |

## Left to do (only the entrant can)

The rules forbid automated entries and allow one submission per entrant, so
no agent can do these.

- [ ] **Eligibility:** a legal resident of the US or Canada, 18 or older.
- [ ] **Upload the video** to YouTube (unlisted is fine) or Vimeo:
  - `gitslice-agents-on-artifacts-short.mp4` (2:58), the one to submit;
  - its captions, `gitslice-agents-on-artifacts-short.srt`;
  - optionally the full 7:33 cut, `gitslice-agents-on-artifacts.mp4` and its
    `.srt`, as a second, linked upload.

  All four files are on the
  [`agents-demo-2026-10-02` release](https://github.com/gitslice-io/gitslice/releases/tag/agents-demo-2026-10-02),
  and in `~/contest-video/build/` on the machine that made them.
- [ ] **Fill in the form** with the answers in [Form answers](#form-answers).
  Replace `YOUR_UPLOAD_URL` with the video link.
- [ ] **Judge key:** paste it into the form's **private** notes only. It is
  kept outside the repository (`~/.config/gitslice-contest/judge-key` on that
  machine, mode 0600). Never post it publicly.
- [ ] **Submit**, and keep October 21 free.

## The entry in one paragraph

Every coding agent gets its own Git repository on Cloudflare Artifacts and
works with plain git. What the agents push lands in one Gitslice codebase:
- reviewed by a Workers AI agent;
- validated path by path, so changes to different files never need a rebase;
- merged line by line when agents touch the same file;
- escalated to a person for protected paths;
- repaired by a fixer agent when an author goes quiet after a rejection.

Tagline: *Artifacts gives every agent a repo. Gitslice gives them one
codebase.* The design is in `design/22_agents_on_artifacts.md`. This directory
(`agents/`) is the code.

## Where everything is

| What | Where |
| --- | --- |
| Live dashboard | https://agents.gitslice.io |
| The recorded run (keep it) | https://agents.gitslice.io/slices/demo/store |
| Judges' scratch slice | https://agents.gitslice.io/slices/demo/storefront |
| Worker | `gitslice-agents`, deployed from `agents/` (`wrangler.jsonc`) |
| Code (public mirror) | https://github.com/gitslice-io/gitslice/tree/main/agents |
| Code (source) | https://gitslice.io/slices/gitslice/gitslice, path `gitslice/gitslice/agents` |
| Videos | the GitHub release above |
| Video pipeline | `~/contest-video` (not in the repo); see [Video](#video) |
| Keys | `~/.config/gitslice-contest/agents-api-key` and `judge-key` (mode 0600). Never print or commit them. |

The Worker's bindings, from `wrangler.jsonc`:
- Artifacts namespace `gitslice-agents`;
- Workers AI;
- a Durable Object `Hub`, one per slice, with SQLite storage;
- the queue `gitslice-agents-events`, which receives `repo.pushed` events;
- `SLICES=demo/store,demo/storefront`;
- `PROTECTED_PATHS=/src/checkout/payments*`.

It talks to the production Gitslice API at `api.gitslice.io`.

## The two demo slices

- **`demo/store`** is the run recorded in the video:
  - 104 agents and 102 changesets, all landed;
  - a median of 7.3 s from push to landed;
  - 4 changes merged line by line, 2 conflicts, 1 human approval, 2 fixes by
    fixer agents.

  The dashboard's numbers match the video. **Do not reset it.**
- **`demo/storefront`** is the slice judges run on. `--reset` puts it back to
  its seed and deletes the last run's Artifacts repositories.

## Verified on 2026-10-11

A judge-style run, with the judge key, on the scratch slice:

```bash
cd agents
node swarm/swarm.mjs --slice demo/storefront --reset --concurrency 32 \
  --key-file ~/.config/gitslice-contest/judge-key
```

Result:
- 102 of 102 landed in 199 s, none failed;
- 3 automatic merges and 1 conflict reworked by its agent;
- 1 human approval, given with `gs cs approve` as a member of `demo`;
- 2 fixes by fixer agents.

The median push-to-landed time was 14.9 s, against 7.3 s in the recording,
probably because Cloud Run and the Worker started cold after a quiet week.
The video and form quote the recorded run.

Health check (no key needed):

```bash
curl -s https://agents.gitslice.io/v1/slices/demo/store | jq .stats
curl -s https://agents.gitslice.io/v1/slices/demo/storefront | jq .stats
```

## What judges see

They follow `agents/README.md`, "Try it". It sends them to
`demo/storefront`:

```bash
AGENTS_API_KEY=<judge key> node swarm/swarm.mjs --slice demo/storefront --reset --concurrency 32
```

- **The payments change waits for a human on purpose.** The judge key cannot
  approve it. After two minutes (`--human-wait`), the swarm stops waiting and
  reports *waiting for a human*, not a failure. Everything else lands on its
  own.
- **One run at a time.** A second judge's `--reset` takes the slice away
  from a run in progress. The README asks them to wait.
- **During judging,** someone with Gitslice access can approve a waiting
  payments change so it lands on the judge's dashboard. That needs membership
  of the `demo` account. The `gitslice-operator` identity works:

  ```bash
  curl -s https://agents.gitslice.io/v1/slices/demo/storefront \
    | jq -r '.sessions[] | select(.status=="needs-human") | .handle'
  HOME=~/.config/gitslice-operator gs cs approve <handle>
  ```

## Operations until October 21

- **Billing:** Artifacts billing starts October 14/15. Each swarm run creates
  about a hundred small repositories, and the next reset deletes them. To
  clear the scratch slice without a new run, call
  `POST /v1/slices/demo/storefront/restore` with a key (see the README's API
  table).
- **Revoke the judge key** by removing it from the Worker's `AGENTS_API_KEY`
  secret: `npx wrangler secret put AGENTS_API_KEY` with the remaining keys.
- **Redeploy the Worker** from `agents/`:

  ```bash
  set -a; . <ops env file with CLOUDFLARE_API_TOKEN>; set +a
  CLOUDFLARE_ACCOUNT_ID=<account id> npx wrangler deploy
  ```

  The production environment file lives outside the repository; the
  deployment skill describes it.
- **Logs:**
  - Worker: `npx wrangler tail gitslice-agents`;
  - the Gitslice API: Cloud Run service `gitslice-prod` (us-west1).
- **Gitslice itself** (the API the Worker calls) is deployed separately, by a
  daily Cloud Build at 16:00 UTC from the GitHub mirror. A breaking API change
  would affect the demo. Run the health check and a scratch run after any
  Gitslice deploy during judging.

## Video

The pipeline is in `~/contest-video`. It is not in the repository.

| Step | How |
| --- | --- |
| Narration text | `narration/script.mjs` |
| Voice | `narration/tts.mjs`, Workers AI Aura-2 "apollo" through a small `gitslice-video-tts` Worker |
| Recording a take | `demo/run-demo.sh`, which restores the slice first. The recorded take is `runs/final4`. |
| Full video (7:33) | `node compose.mjs runs/final4 --final` |
| Short cut (2:58) | `python3 short.py`. It keeps whole narration sentences from the full video, with short fades to black between sections, and writes matching captions. Edit its `KEEP` list to change the cut. |

Re-upload a changed file to the release with
`gh release upload agents-demo-2026-10-02 <file> --repo gitslice-io/gitslice --clobber`.

## If the entry is a finalist (October 21)

- **Live demo:** run the swarm on `demo/storefront` with the dashboard full
  screen. It takes three to four minutes. Approve the payments change from a
  terminal while it waits; that is the human-in-the-loop moment.
- **Fallback:** if the network or Cloudflare misbehaves, play the 2:58 cut.
  The recorded `demo/store` dashboard also stays up.
- **Talking points:**
  - path-by-path validation instead of rebasing on a branch tip;
  - the Durable Object signals agents about overlaps before they collide;
  - fixer agents fork the author's repository, which is instant on Artifacts;
  - Gitslice is real: it hosts its own source, builds its releases from tag
    webhooks, and serves any slice as a Go module.

## Form answers

The same text is in `~/contest-video/submission.md`. Paste it as is, after
replacing `YOUR_UPLOAD_URL`.

**Project name:** Gitslice: one codebase for every agent

**Tagline:** Artifacts gives every agent a repo. Gitslice gives them one
codebase.

**Short description:** Every coding agent gets its own Git repository on
Cloudflare Artifacts, and works with plain git. Everything the agents push
lands in one Gitslice codebase:
- reviewed by a Workers AI agent;
- validated path by path;
- merged line by line when two agents touch the same file;
- escalated to a person when policy says so.

There are no branches to rebase and no merge queue.

**What it does:**

Coding agents need two things that pull in opposite directions:
- **isolation**, a repository of their own;
- **integration**, one codebase that ships.

Artifacts makes isolation cheap, but it leaves three questions open:
- How does an agent know what the others are working on?
- What happens when their changes collide?
- Who reviews everything they produce?

Gitslice answers them by changing what a repository is.
- Code lives in one global source graph, and a slice is a Git view of part of
  it.
- Every change is a changeset.
- Gitslice validates each changeset path by path, against the version each
  file was edited from, not against the tip of a branch.
- So agents that change different files land side by side, even when each
  started from an old snapshot.

On Cloudflare it works like this:
1. Each agent opens a session and gets an Artifacts fork of the slice's
   baseline, with a scoped token.
2. It pushes with plain git.
3. Artifacts' `repo.pushed` event goes to a Queue.
4. A Worker diffs the push against the baseline and opens or updates a
   Gitslice changeset.
5. A review agent on Workers AI (gpt-oss-120b) checks the change. Protected
   paths go to a human.
6. The Worker submits. If another agent already landed on the same file, it
   merges three-way and resubmits. Overlapping lines go back to the agent as
   a conflict signal, and the agent reworks on a fresh baseline in the same
   changeset.
7. If review rejects a change and its author goes quiet, a fixer agent forks
   the author's Artifacts repository (instant, and untouched for the
   original), has Workers AI repair the change, and pushes it with git. The fix
   lands as another patchset of the same changeset.
8. A Durable Object per slice tracks who touches what. It warns agents about
   overlaps when they fork and when they push, and streams everything to a
   live dashboard.
9. Each landing becomes the next baseline, which Artifacts imports straight
   from Gitslice's Git endpoint.

Gitslice is not a mock-up. It hosts its own source code:
- every change to it lands as a Gitslice changeset;
- its releases are built by a webhook when a tag is created;
- any slice can be imported as a Go module.

In the recorded demo, 104 agents changed one store at once: one agent
driven by hand, a hundred more from the swarm, and two fixer agents. All 102
changesets landed, with a median of 7 seconds from push to landed. That
includes:
- 4 changes merged line by line;
- 1 same-line conflict, reworked by its agent;
- 2 rejected changes repaired by fixer agents, after their authors went quiet;
- 1 payments change, approved by a human.

**Cloudflare products used:**
- Workers.
- **Artifacts:**
  - create, fork and `createToken` for each agent;
  - `readTree` and `readBlob` to diff pushes;
  - `import` for baselines;
  - `delete` for cleanup.
- **Event Subscriptions** (`cf.artifacts.repo.pushed`) delivered to **Queues**.
- **Durable Objects:** SQLite storage, RPC, hibernating WebSockets and alarms.
- **Workers AI:**
  - gpt-oss-120b for review and for the agents that write docs and copy;
  - Llama 3.3 as the review fallback;
  - Deepgram Aura-2 for the video narration.
- Custom domain `agents.gitslice.io`.

**Multi-agent concurrency, coordination and conflicts:**
- Agents work concurrently in separate Artifacts repos.
- Gitslice serializes only the final publish of each commit.
- **Coordination:** the Durable Object tells an agent at fork time who
  already works on its paths, and tells both sides when a push overlaps.
- **Conflicts:**
  - Same-file edits merge automatically (Myers diff, three-way).
  - Same-line edits come back with base, yours and landed lines. The agent
    reworks with `resume`, in the same changeset with a new patchset.
- **Review:** deterministic checks, then policy, then the model. A model
  answer is required: no answer means escalation.
- **Gitslice-side rule:** required approvals mean a non-author approves. The
  bridge identity writes changesets and a reviewer identity approves them.

**Links:**
- Live dashboard: https://agents.gitslice.io
- Code: https://github.com/gitslice-io/gitslice/tree/main/agents (a read-only
  mirror of https://gitslice.io/slices/gitslice/gitslice)
- Design notes: `design/22_agents_on_artifacts.md`
- Video: YOUR_UPLOAD_URL (YouTube or Vimeo; the 2:58 cut). Both cuts, with
  captions, are also on GitHub:
  https://github.com/gitslice-io/gitslice/releases/tag/agents-demo-2026-10-02

**How to run it:**
```bash
git clone https://github.com/gitslice-io/gitslice && cd gitslice/agents
AGENTS_API_KEY=<judge key> node swarm/swarm.mjs --slice demo/storefront --reset --concurrency 32
# watch https://agents.gitslice.io/slices/demo/storefront
```
It takes three to four minutes. `demo/store` keeps the run from the video,
so please run on `demo/storefront`.

One change, to payments, waits for a person to approve it by design. The
judge key cannot approve, so after two minutes the run reports it as "waiting
for a human". Everything else lands on its own: in our check run on
2026-10-11, the other 101 changes landed, with no failures.

Deploying your own copy is covered in `agents/README.md`, under "Your own
deployment".

**License:** Apache-2.0
