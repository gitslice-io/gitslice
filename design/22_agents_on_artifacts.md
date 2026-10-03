# 22. Agents On Cloudflare Artifacts

Status (2026-10-02):

- Live at https://agents.gitslice.io, code in `agents/`.
- Built for Cloudflare's "Build the next GitHub" competition.

## Goal

Let any number of agents work on one Gitslice codebase at once:

- each agent in an isolated Git repository;
- every change landed in one history;
- no rebases and no merge queue.

Answer the competition's three questions:

- What are other agents working on?
- What happens when changes collide?
- Who reviews everything?

## Decisions

- **Artifacts for isolation, Gitslice for integration.** Artifacts recommends
  a repo per agent and warns against one shared repo used as a queue. Gitslice
  already validates each changeset path by path against its base. So
  per-agent repos do not need rebasing: they need a landing pipeline.
- **Baselines come from Gitslice's Git endpoint.**
  - Artifacts imports the slice's projection with `import()`.
  - The projection is deterministic, and every projected commit carries a
    `Gitslice-Commit:` trailer. So the baseline maps back to the native
    commit a changeset is based on, with no extra state.
  - Private slices use a pushed baseline (`scripts/publish-baseline.sh`).
- **Pushes are events.** An Event Subscription sends Artifacts `repo.pushed`
  to a Queue. The Worker's consumer lands each push, deduplicated per commit
  by the Hub. Agents may also nudge (`POST …/pushed`). Both paths are safe.
- **One changeset per line of work.**
  - Every push becomes a patchset.
  - A conflicted agent opens a new session with `resume`. That continues the
    same changeset on a newer baseline, so review history and context stay in
    one place.
- **The bridge merges, but never guesses.**
  - When a submit fails because another agent landed on the same file, the
    Worker three-way merges that file (base, ours, head) and resubmits with
    the head as the new base.
  - Overlapping hunks go back to the agent with the base, ours and theirs
    lines.
  - Binary files and delete-versus-edit always go back to the agent.
- **Review has two identities.** The bridge authors changesets and a reviewer
  identity approves them, so "required approvals: 1" on the slice means a
  non-author approval. The review agent:
  - runs deterministic checks first;
  - escalates protected paths to a human;
  - asks gpt-oss-120b on Workers AI, falling back to Llama 3.3;
  - never approves without a model answer: it escalates instead.
- **Sessions never wait for an import.**
  - Each landing makes the baseline stale. The next session forks the
    baseline the Hub has and starts a background import, at most one every
    four seconds.
  - A stale baseline only means the bridge merges more.
  - Two cases do wait for a fresh import:
    - A rework (`resume`) waits, because it has to see the change it
      conflicted with.
    - After a reset, sessions wait for a baseline imported after the reset.
- **Demo slices can be restored.**
  - `POST …/restore` writes the seed back as one changeset.
    - The seed is the oldest commit with files under the slice root. The
      first commit only creates the directory.
    - The changeset reuses the seed's blob ids, approved by the reviewer
      identity.
  - It then clears the Hub and deletes the previous run's session repos and
    retired baselines.
  - The swarm runs it with `--reset`, so judges can rerun the demo on a
    clean store.
- **Fixer agents repair rejected changes.**
  - If review requests changes and the author does not push again within
    12 seconds, the Hub queues a fixer.
  - The fixer forks the author's repository, which Artifacts does in constant
    time without touching the original. It gets the task, the review feedback
    and the pushed files, asks gpt-oss-120b for corrected files, validates
    them, and pushes.
  - The fix lands as a new patchset on the same changeset. The author's
    session is marked landed by the fixer.
  - Artifacts has no write API, so the fixer pushes with isomorphic-git over an
    in-memory filesystem, inside the Worker. No container is needed for this.
    Repairs that need a build or tests would need a sandbox with a real
    toolchain.
  - A fixer's own rejection goes to a person.
- **Landings that stall are retried.** If a Worker invocation dies mid-landing,
  nothing else would move the change: the push was already claimed. The Hub
  alarm queues stalled landings again after 90 s (three times), and re-reviews
  a change that was escalated only because no review model answered.
- **Coordination state per slice.**
  - A Durable Object per slice holds the sessions and the paths each one
    intends or touched.
  - It tells an agent at fork time who else is on its paths, and tells both
    sides when a push overlaps.
  - It streams everything to the dashboard over hibernating WebSockets.

## Scale

| Component | Scaling |
|---|---|
| Artifacts | Forks are O(1). The limits that matter are 2,000 Git requests per 10 s per repo and 2,000 control-plane requests per 10 s per namespace. A repo per agent spreads the Git load. Session creation is the control-plane cost. |
| Queue consumer | Runs up to 25 invocations at once (`max_concurrency`). |
| Each landing | About a dozen Gitslice RPCs, plus one Workers AI call. |
| Gitslice | Validation contends only on shared paths. Publishing is serialized per ref, which bounds landings per second. |
| One Hub per slice | Comfortable at hundreds of live sessions. Larger fleets split into slices, and slices can overlap. |
| Baselines | One background import at a time per slice, at most every four seconds. Old baselines are deleted on reset. |

A rehearsal ran 99 agents plus one driven by hand on `demo/storefront`:
- 98 of the 99 swarm agents landed in 112 s, with a median of 23 s from start
  to landed;
- the review loop, an auto-merge and a human approval all worked;
- the conflicted agent's rework failed, as expected: it needs a fresh
  baseline import, and that needs the side-band fix in production.

## Findings while building it

- Artifacts could import GitHub repositories but not Gitslice slices. Its
  importer fetches without side-band. Gitslice served `git http-backend` with
  `CombinedOutput()`, which mixed pack progress into the packfile. Fixed in
  Gitslice (`TestGitFetchWithoutSideBand`); see the execution log.
- Durable Object RPC types collapse to `never` for values typed `unknown`.
  Signal payloads are typed as JSON.
- Model calls need their own timeouts. A hung Workers AI call held a landing
  in "reviewing" for as long as the invocation lived. Every call now times out
  at 25 to 45 s and falls back to the next model, and finally to a person.
- A reviewer that never saw the Product type invented required fields
  (`description`, `price`) and rejected a correct fix. The deterministic
  checks now say what they cover, and the model only judges scope and intent.
- Change pages showed signed-out viewers a red "missing subject" error from
  `ListCheckRuns`. That RPC needs a signed-in caller, even on a public slice.
  The checks panel now asks them to sign in instead.

## Open questions

- Should Gitslice push baselines to Artifacts on every landing, instead of
  importing lazily?
- Should checks run as Workers, reporting through `ReportCheckResult`, rather
  than on a CI daemon host?
