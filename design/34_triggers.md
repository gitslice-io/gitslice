# 34. Triggers

Status: built 2026-10-29, not deployed: Phases 0 to 3 and the check action,
except `gs trigger test` and `--draft` runs (see "Implementation"). Builds on
design/17 (checks),
design/24 (webhooks), design/32 (CI dispatch on Cloudflare) and design/33
(folder-scoped secrets, which this design needs and which lands first).

## Goal

One mechanism for "when something changes here, do this":

- A `triggers.yaml` can sit in any folder. A change anywhere under that folder
  is checked against its triggers.
- A trigger can filter by event, by path glob (file types included) and by the
  kind of change to the file (added, modified, deleted, moved).
- Its action is either a declarative HTTP call or a script that runs as a
  sandboxed Worker on Cloudflare.
- A script can write results back to the changeset through a native API:
  check results that gate submit, with a status, summary, link and file
  annotations.

This should replace webhooks: an HTTP action is a webhook that lives next to
the code it is about and filters by path, which design/24 lists under "Not
yet". It should also replace "CI" as a feature built into Gitslice: a trigger calls GitHub Actions,
Buildkite, Cloud Build or anything else, and reports the result. In a
monorepo, ownership follows folders, so automation should too.

What it does **not** replace: running a check's commands. A Worker cannot run
`go test` or start Postgres. `checks.yaml` and the CI daemon (design/31) stay
as the built-in runner. A trigger can call an external CI system, and later
it can start a check on a daemon or a Cloudflare container (Phase 4).

## Example

```
services/billing/
  .gitslice/
    triggers.yaml
    triggers/
      buildkite.js
  api/…
```

```yaml
# services/billing/.gitslice/triggers.yaml
version: 1

triggers:
  notify-deploy:                      # id: services/billing/notify-deploy
    on: [push]                        # a commit landed that touches this folder
    refs: [main]
    paths: ["**/*.go", "!**/*_test.go"]
    action:
      http:
        url: https://deploy.example.com/hooks/billing
        headers:
          Authorization: "Bearer ${{ secrets.DEPLOY_TOKEN }}"
        sign: DEPLOY_HMAC             # X-Gitslice-Signature-256, keyed by this secret

  buildkite:
    on: [changeset.created, changeset.updated]
    paths: ["**/*.go", "/go.mod", "/go.sum"]
    changes: [added, modified, moved]  # deletions alone do not start a build
    concurrency: cancel-superseded     # a new patchset cancels the previous one's run
    checks: [ci]                       # check ids it may report: services/billing/ci
    secrets: [BUILDKITE_TOKEN]
    network: [api.buildkite.com]       # the only hosts the script may reach
    timeout: 2h                        # how long it may wait for its callback
    action:
      worker: triggers/buildkite.js
```

```js
// services/billing/.gitslice/triggers/buildkite.js
export default {
  async onEvent(event, gs) {
    const check = await gs.checks.create({ name: "ci", status: "queued" });
    const res = await fetch("https://api.buildkite.com/v2/organizations/acme/pipelines/billing/builds", {
      method: "POST",
      headers: { Authorization: `Bearer ${gs.secret("BUILDKITE_TOKEN")}` },
      body: JSON.stringify({
        commit: event.changeset.commit_id,
        branch: `gitslice/${event.changeset.handle}`,
        meta_data: { gitslice_callback: gs.callbackUrl({ check: check.id }) },
      }),
    });
    const build = await res.json();
    await check.update({ status: "running", detailsUrl: build.web_url });
    await gs.state.set({ check: check.id, build: build.number });
  },

  async onCallback(request, gs) {
    const { check } = await gs.state.get();
    const body = await request.json();
    await gs.checks.update(check, {
      status: body.build.state === "passed" ? "passed" : "failed",
      summary: `Buildkite build #${body.build.number}: ${body.build.state}`,
      annotations: body.failures?.map((f) => ({ path: f.file, line: f.line, message: f.message })),
    });
  },
};
```

## Decisions

These are the answers to the questions raised on 2026-10-28. The user chose
(1); (2) to (4) are recommendations to confirm.

1. **Secrets are scoped to folders** (design/33). A trigger gets the secrets
   at its own folder, the folder that holds its `.gitslice/`, and only those
   it lists in `secrets:`.
2. **For changeset events, definitions come from the target ref's head, not
   the patchset.** Otherwise anyone who can open a changeset could edit
   `triggers.yaml` or the script and send the trigger's secrets anywhere
   (GitHub's `pull_request_target` lesson). A changed trigger takes effect
   when it lands. `gs trigger test` runs a draft's version without secrets
   (see "Trying a trigger").
3. **Trigger code lives under `.gitslice/`, and changes to `.gitslice/` need an
   owner's approval.** A changeset that adds, changes or deletes any file
   under a `.gitslice/` folder needs one approval from an owner or admin of
   the account, besides the slice's own rules. That covers `triggers.yaml`,
   scripts, and `checks.yaml`, which has the same problem today.
4. **The file is `<dir>/.gitslice/triggers.yaml`,** beside `checks.yaml`, with the
   same folder cascade and the same id scheme. It is plural because one file
   holds several triggers.

## The triggers file

| Field | Req | Meaning |
| --- | --- | --- |
| `version` | yes | `1`. Unknown major versions are refused. |
| `triggers` | yes | Map of local name (`[a-zA-Z0-9_-]+`) to trigger. The id is `<dir>/<name>`, or `<name>` at the root, as for checks. |
| `on` | yes | Events (below), or `schedule` / `manual` entries. |
| `paths` | no | Globs, relative to the folder or `/`-absolute, with `!` to exclude. At least one changed path must match. Empty means any change under the folder. |
| `changes` | no | `added`, `modified`, `deleted`, `moved`. Default all. Matched against the same changed paths. |
| `refs` | no | Target refs (`main` means `refs/global/main`). Default: the slice's default target. |
| `secrets` | no | Names of secrets at the trigger's folder that it may use. |
| `network` | no | `worker` only: hosts the script may call (exact, or `*.example.com`). Default: none, so the script reaches only the Gitslice API. |
| `checks` | no | Local check names the trigger may report. They become `<dir>/<name>` and can be listed in `required_checks`. |
| `concurrency` | no | `cancel-superseded` (default for changeset events), `serial` (one run at a time, in order), or `parallel`. |
| `timeout` | no | Longest wait for callbacks, default `1h`, at most `7d`. Script execution itself is bounded by Worker limits. |
| `retry` | no | `worker` only: run an interrupted script again, up to N times (at most 5). Default 0, at most once. See "Delivery guarantees". |
| `action` | yes | Exactly one of `http`, `worker` or `check` (names of checks in the folder's `checks.yaml`, run on the slice's CI daemon; built 2026-10-29). |

A trigger and a check in the same folder may not have the same id: the file
fails to parse, and the error is shown wherever that folder's checks and
triggers are listed.

### Events

| Event | Paths matched against | Fires triggers in |
| --- | --- | --- |
| `push` | the landed commit's changed paths | every folder on the path from a changed file up to the root |
| `changeset.created`, `changeset.updated` | the patchset's changed paths | the same, for the patchset's changes |
| `changeset.approved`, `changeset.submitted`, `changeset.abandoned` | the changeset's affected paths | the same |
| `check_run.completed` | the changeset's affected paths; `checks:` in `on` filters by check id | the same |
| `tag.created` | none | folders that hold one of the tagging slice's included paths, or are above one |
| `schedule` (`on: [{schedule: "0 6 * * *"}]`) | none | the trigger's own folder, at the head of `refs` |
| `manual` (`gs trigger run`, or a button in the UI) | none | the trigger, with optional `inputs` |

Event payloads are the webhook envelope of design/24 with three additions:
`trigger` (id, folder), `changes` (each changed path with its kind and, for a
move, its old path), and `definition_commit` (the commit the definition was
read from).

A trigger never fires for an event its own run caused. Each event carries
`sender.kind` (`user`, `agent`, `trigger`) and a `depth`. Triggers skip
events from themselves, and nothing fires past depth 3.

## Actions

### `http`

This is design/24's delivery with the URL and filter moved into the tree:

- The same envelope and headers, `X-Gitslice-Trigger` added.
- Signing with a named secret.
- The same SSRF rules: https only, public addresses only, checked after DNS.
- The same retries: 1 m, 5 m, 30 m, 2 h, 6 h, 12 h.

`${{ secrets.NAME }}` may appear in `url` and `headers` only, never in the
body, and the name must be in `secrets:`. HTTP actions are sent by the Go
server's existing webhook dispatcher, not by Cloudflare. They need no
sandbox, and the server already does it.

### `worker`

`worker:` names a JavaScript module under the same `.gitslice/` folder. It
may import other modules under that folder, but not npm packages in v1 (vendor
them into the folder). TypeScript is a follow-up; the dispatcher would strip
types with esbuild-wasm.

The module's default export may have:

- `onEvent(event, gs)`: called once per run;
- `onCallback(request, gs)`: called for each request to the run's callback
  URL, until the run finishes or times out.

A run finishes when `onEvent` returns, unless the script called
`gs.waitForCallback()` or created a check that is still `queued` or `running`.
In that case it finishes when those checks reach a final status, when the
script calls `gs.finish()`, or at `timeout`.

## Script examples

All of these are plain JavaScript modules under a `.gitslice/` folder. They
run on Workers, so they have `fetch`, `crypto.subtle`, `URL` and the other
web APIs, but no Node APIs and, in v1, no npm packages.

**Post to Slack when billing code lands.**

```yaml
# services/billing/.gitslice/triggers.yaml
triggers:
  slack-on-land:
    on: [push]
    refs: [main]
    paths: ["**/*.go", "!**/*_test.go"]
    secrets: [SLACK_WEBHOOK_URL]
    network: [hooks.slack.com]
    action:
      worker: triggers/slack.js
```

```js
// services/billing/.gitslice/triggers/slack.js
export default {
  async onEvent(event, gs) {
    const files = event.changes.map((c) => `• ${c.kind} ${c.path}`).join("\n");
    await fetch(gs.secret("SLACK_WEBHOOK_URL"), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ text: `*${event.sender.username}* landed in billing:\n${event.commit.title}\n${files}` }),
    });
  },
};
```

**A gating check with no external CI: migrations are append-only.** It
needs no `network:`, so it can only read the changeset through `gs`.

```yaml
# .gitslice/triggers.yaml
triggers:
  migrations-append-only:
    on: [changeset.created, changeset.updated]
    paths: ["internal/postgres/migrations/*.sql"]
    checks: [migrations]
    action:
      worker: triggers/migrations.js
```

```js
// .gitslice/triggers/migrations.js
export default {
  async onEvent(event, gs) {
    const check = await gs.checks.create({ name: "migrations", status: "running" });
    const problems = [];
    for (const c of event.changes) {
      if (c.kind !== "added") {
        problems.push({ path: c.path, message: `Migrations are append-only; this one was ${c.kind}.` });
        continue;
      }
      // Migrations are split on ";", so a ";" in a comment breaks them.
      const text = await gs.files.read(c.path);
      text.split("\n").forEach((line, i) => {
        if (line.trim().startsWith("--") && line.includes(";")) {
          problems.push({ path: c.path, line: i + 1, message: "A ; in a comment splits the statement." });
        }
      });
    }
    await check.update({
      status: problems.length ? "failed" : "passed",
      summary: problems.length ? `${problems.length} problem(s)` : "Migrations look fine.",
      annotations: problems,
    });
  },
};
```

**Hand off to GitHub Actions and wait for the result.**

```yaml
triggers:
  gha-ci:
    on: [changeset.created, changeset.updated]
    paths: ["**/*.ts", "package.json", "package-lock.json"]
    checks: [web-ci]
    secrets: [GH_TOKEN]
    network: [api.github.com]
    timeout: 1h
    action:
      worker: triggers/gha.js
```

```js
// .gitslice/triggers/gha.js
export default {
  async onEvent(event, gs) {
    const check = await gs.checks.create({ name: "web-ci", status: "queued" });
    await fetch("https://api.github.com/repos/acme/web-ci/actions/workflows/ci.yml/dispatches", {
      method: "POST",
      headers: { Authorization: `Bearer ${gs.secret("GH_TOKEN")}`, Accept: "application/vnd.github+json" },
      body: JSON.stringify({
        ref: "main",
        inputs: {
          gitslice_clone: event.changeset.clone_url,
          gitslice_commit: event.changeset.commit_id,
          callback: gs.callbackUrl({ check: check.id }),
        },
      }),
    });
    await gs.state.set({ check: check.id });
    // The queued check keeps the run open until the callback or the timeout.
  },

  // The workflow's last step: curl -X POST "$GITSLICE_CALLBACK" -d '{"conclusion":"success","run_url":"…"}'
  async onCallback(request, gs) {
    const { check } = await gs.state.get();
    const r = await request.json();
    await gs.checks.update(check, {
      status: r.conclusion === "success" ? "passed" : "failed",
      summary: `GitHub Actions: ${r.conclusion}`,
      detailsUrl: r.run_url,
    });
  },
};
```

**Reviewer rules that depend on what changed.** It also fires on approval,
so the check flips to passed when the right person approves.

```yaml
triggers:
  review-rules:
    on: [changeset.created, changeset.updated, changeset.approved]
    checks: [review-rules]
    action:
      worker: triggers/review-rules.js
```

```js
// .gitslice/triggers/review-rules.js
const OWNERS = {
  "internal/postgres/migrations/": ["nic"],
  ".gitslice/": ["nic"],
};

export default {
  async onEvent(event, gs) {
    const cs = await gs.changeset.get(); // with approvals of the current patchset
    const approvers = new Set(cs.approvals.map((a) => a.username));
    const missing = Object.entries(OWNERS)
      .filter(([prefix, owners]) => event.changes.some((c) => c.path.startsWith(prefix)) && !owners.some((o) => approvers.has(o)))
      .map(([prefix, owners]) => `- ${prefix} needs approval from one of: ${owners.join(", ")}`);
    await gs.checks.create({
      name: "review-rules",
      status: missing.length ? "failed" : "passed",
      summary: missing.length ? missing.join("\n") : "All owners approved.",
    });
  },
};
```

### What a script can use

| | |
| --- | --- |
| `onEvent(event, gs)` | Called once per run. |
| `onCallback(request, gs)` | Called for each POST to the run's callback URL, until the run ends. |
| `event` | `event`, `sender`, `trigger`, `changes[]` (`path`, `kind`, `old_path`), and `changeset`, `commit` or `tag` depending on the event. |
| `gs.checks.create` / `.update` | `status`, `summary`, `detailsUrl`, `annotations[]` (`path`, `line`, `message`). |
| `gs.files.read(path)` / `.list(dir)` | Read the event's tree, or the base tree with `{at: "base"}`. |
| `gs.changeset.get()` | The changeset, its current patchset and approvals. |
| `gs.secret(name)` / `gs.revealSecret(name)` | A sealed placeholder, or the raw value with `reveal:`. |
| `gs.callbackUrl(data)`, `gs.state.get()` / `.set()` | Callbacks and up to 64 KiB of per-run JSON. |
| `fetch` | Only to hosts in `network:`. |
| `console.log` | Goes to the run's log, with secret values masked. |

## Running scripts on Cloudflare

```
Go server (Cloud Run, source of truth)                Cloudflare (triggers.gitslice.io)
  event recorded (webhook_events, as today)
  fan-out: resolve triggers.yaml along each changed
  path's folders at the target ref's head; filter;
  insert trigger_runs (queued), same transaction
     │ POST /run {run_id}  (HMAC) ───────────────────►  Worker "trigger-dispatch"
  sweep: queued runs not yet started are posted       1. TriggerService.StartRun(run_id) ──► server claims
  again (the webhook sweep, design/24)                   the run, returns event,
                                                             modules, secrets, run token, limits
                                                          2. env.LOADER.get(hash(modules, network))
                                                             → dynamic Worker, globalOutbound = gate(network)
                                                          3. entrypoint.onEvent(event, gs)   gs = per-run stub
                                                          4. TriggerService.FinishRun / WaitRun
  external CI ── POST /cb/<run>/<sig> ────────────────►  TriggerRunService.StartCallback
                                                          → onCallback (state lives on the run row;
                                                          the server's sweep times runs out)
```

**Isolation.** Each script runs in a Dynamic Worker, loaded through the
Worker Loader binding (Workers Paid; Cloudflare's pricing page now calls it
production-ready). It is a V8
isolate with no bindings. It gets only what the dispatcher hands it:

- **`gs`** is an RPC stub, a `WorkerEntrypoint` of the dispatcher created for
  one run and passed as an argument to `onEvent` or `onCallback`. Its calls go
  to the Gitslice API with that run's token. The script never holds the token,
  and a stub from one run is useless to another.
- **`globalOutbound`** is the dispatcher's gate. Every `fetch` the script
  makes passes through it, and it:
  - refuses hosts that are not in `network:`;
  - refuses private addresses;
  - swaps secret placeholders for values (below);
  - counts requests against limits.

**One isolate per script, not per run.** The isolate id is a hash of the
script's modules and its `network:` list. Those are the only things fixed at
load time, so the gate's host list is set when the isolate loads. Everything
else that varies per run reaches the script as call arguments: the event, the
`gs` stub and the placeholders. Runs of the same script reuse a warm isolate,
and module-level variables can survive from one run to the next. Scripts
must keep per-run data in `gs.state`, not in globals. The docs and the
examples say so.

**Cost.** A Dynamic Worker is identified by its id and its code; a change to
either makes a new one. Cloudflare charges $0.002 per unique Dynamic Worker
per day beyond 1,000 a month included, on top of normal Workers CPU and
request pricing. Waiting on `fetch` uses no CPU. With ids keyed by script, ten
scripts loaded every day are about 300 a month, inside the free amount. Each
landed edit of a script is one more unique Worker. Per-run ids would have
used up the free amount at about 33 runs a day.

**Limits that shape the dispatcher.**

- **Distinct Dynamic Workers in flight.** One Worker request can have at most
  4 distinct Dynamic Workers with requests in flight, and a Durable Object at
  most 10. Calls to the same Dynamic Worker count once. The dispatcher
  therefore runs **one trigger run per request** (one `POST /run`, one
  callback) and never loops over several runs in one invocation.
- **Number of distinct ids.** The docs state no limit; the only cost is
  above.
- **Warm isolates.** These are not guaranteed: "a later call with the same ID
  may start a new isolate from scratch". The code callback of `LOADER.get` may
  also run more than once. So the callback only returns modules that the
  dispatcher already holds from `StartRun`. It never claims a run or calls the
  API itself.

Workers for Platforms (a dispatch namespace, $25 a month, uploads on landing)
is the fallback if Worker Loader changes.

**Secrets never enter the isolate as values.** `gs.secret("NAME")` returns a
placeholder, `gs-secret:v1:<sealed>`:

- `<sealed>` is the value encrypted with AES-GCM under a key only the
  dispatcher holds (a Worker secret).
- Its associated data binds it to the trigger id, the run id and an expiry
  (the run's deadline).
- The gate opens placeholders in the URL, headers and body of outgoing
  requests. It does so only when the request goes to a host in that script's
  `network:`, and only before the expiry. Anything else is sent unopened, or
  refused if the host is not allowed.

This keeps the gate stateless while sharing isolates between runs. A script
cannot print a secret, store it in state or send it to a host the definition
does not allow. A script that has to compute with a value (to sign a request,
for example) asks for it with `gs.revealSecret("NAME")`. That needs
`reveal: [NAME]` in the trigger, so the exception is written down where
reviewers can see it. Revealed values are masked in logs, as in check output
(design/33).

**Code delivery.** `StartRun` returns the modules read from the definition
commit's tree. Those are the files under the trigger's `.gitslice/` folder
that the entry module imports, at most 1 MiB. The dispatcher's request needs
the dispatcher's service credential (a Worker secret) and succeeds only when
it claims the run (see "Delivery guarantees"). The request to the dispatcher
carries only the run id.

**Logs.** The dispatcher adds a first module that wraps `console.*` and sends
lines to `gs.log` in batches. Each line is masked, then stored in
`trigger_run_logs`, the same shape as check logs, and streamed to the UI.
How much of this the Worker Loader allows (tail Workers on dynamic Workers,
for example) is a spike question.

**Limits per run:**

- wall time: 25 s per `onEvent` or `onCallback` call (the dispatcher runs a
  posted run in `waitUntil`, which gets 30 s after the response);
- subrequests: 50;
- state: 64 KiB;
- callbacks: 100;
- wall time: `timeout`.

Runs share an account-wide cap of 20 running at once. Beyond that, runs stay
`queued` until a slot frees, and the sweep posts them then.

## Delivery guarantees

No queue can deliver exactly once; Cloudflare Queues, like the others, deliver
at least once. So "a trigger runs once per event" comes from the database and
from idempotent writes, not from delivery. v1 has no message queue: the API
posts run ids straight to the dispatcher, and the existing webhook sweep
retries posts that failed.

| Step | How it could happen twice | What prevents it |
| --- | --- | --- |
| Fan-out: an event becomes runs | Fan-out repeats after a crash or retry | Unique `trigger_runs(trigger_id, event_id)`: at most one run per trigger per event. A manual rerun is a new event (`<event id>.rerun.<n>`). |
| Starting a run | The post is retried, or two dispatchers race | `StartRun` claims with one conditional update: `status = 'running', attempt = attempt + 1, lease_until = now() + 2m`, applied only while the run is `queued`. Whoever loses gets "already started" and drops the request. The dispatcher extends the lease while the script runs. |
| Writes into Gitslice | A `gs` call retried after a network error | Checks are upserted by `(run_id, check name)`. State writes replace the whole value. Check results are per patchset, as today. |
| Calls to the outside world | A run retried after it already called out | Cannot be made exactly-once by Gitslice. See below. |

**A run that dies part-way.** If the dispatcher stops while a script runs,
the lease runs out. Nobody knows whether the script's external calls
happened. The trigger chooses what happens next:

- **At most once (the default for `worker` actions).** An expired lease marks
  the run `errored` ("interrupted") and it is not run again. A person reruns
  it from the UI or with `gs trigger rerun`. A script never posts twice to
  Slack or starts two builds on its own.
- **At least once (`retry: N`, up to 5).** The trigger states that its script
  is safe to repeat. An expired lease puts the run back to `queued` until it
  has had N attempts. Two things help scripts stay safe:
  - `gs.idempotencyKey(label)` returns the same value on every attempt of a
    run, for APIs that accept an `Idempotency-Key` header;
  - `gs.state` lets a script check what an earlier attempt already did ("build
    started? then only wait").

**`http` actions** are at least once, like webhooks today. Retries keep the
same `X-Gitslice-Event-ID` (and `X-Gitslice-Trigger-Run`), and receivers drop
duplicates. A single POST cannot do better.

**Callbacks** come from outside systems, which may send one twice. The
callback URL is valid for any number of calls until the run ends, so
`onCallback` must tolerate repeats. Setting a check to the status it already
has changes nothing.

**When to add a queue.** Cloudflare Queues would retry independently of Cloud
Run being awake, and would absorb bursts. The production API serves
10,000-45,000 requests a day, so the sweep runs often enough without one. If
dispatch failures or bursts show up, a queue can go between the API and the
dispatcher without changing any of the guarantees above, because
`StartRun`'s claim already removes duplicates. A queue consumer gets messages
in batches in one invocation, and one invocation can have at most 4 distinct
Dynamic Workers in flight. So the consumer would use `max_batch_size: 1`, or
post each message on to the dispatcher as a request of its own.

## Writing results back

`TriggerService` (`proto/core/v1/trigger.proto`) is what `gs` calls. A run
token, or the dispatcher on its behalf, can call it. Each call is scoped to
one run: its trigger, event, changeset and patchset.

| Call | Does |
| --- | --- |
| `CreateCheck`, `UpdateCheck` | Create or update a check run named `<dir>/<name>` (a name in `checks:`) on the event's patchset. Status `queued`, `running`, `passed`, `failed`, `errored`, `canceled` or `skipped`; `summary` (Markdown, 64 KiB); `details_url`; `annotations` (path, line range, level, message; at most 200). `passed` and `failed` upsert `check_results` as today, so `required_checks` and the submit gate do not change. |
| `ReadFile`, `ListDirectory` | Read the event's tree (the patchset's result tree, or the landed commit), limited to the trigger's account. |
| `GetChangeset` | The changeset and patchset the event is about. |
| `GetState`, `SetState` | Up to 64 KiB of JSON per run, kept on the run (`trigger_runs.state`). |
| `CallbackUrl` | `https://triggers.gitslice.io/cb/<run>/<hmac>` with optional query data. Valid until the run ends. |
| `Log`, `Finish`, `WaitForCallback` | As described above. |

**Patchset safety.** A check is tied to the patchset its run was for. A
callback that arrives after a newer patchset is recorded on the old patchset
and never satisfies the gate for the new one. `cancel-superseded` cancels the
old run and its checks.

**External CI reporting directly.** There is no long-lived token for
reporting checks. An external system reports through its run's callback URL,
or with the run token, which the script hands over on purpose
(`gs.runToken()`, which needs `reveal: [RUN_TOKEN]`). With the token,
`UpdateCheck` works over plain HTTPS until the run ends:

```
curl -X POST https://api.gitslice.io/gitslice.core.v1.TriggerService/UpdateCheck \
  -H "Authorization: Bearer $GITSLICE_RUN_TOKEN" -d '{"checkId":"…","status":"passed"}'
```

**Data model.** `check_runs` gains:

- `provenance = "trigger"`;
- `trigger_run_id`;
- `details_url`;
- `annotations` (jsonb).

New tables:

- `trigger_runs(id, account_id, trigger_id, folder, event_id, event_type,
  changeset_id, patchset_id, commit_id, definition_commit_id, script_hash,
  status, attempt, lease_until, error, deadline, created_at, started_at,
  finished_at)`, unique on `(trigger_id, event_id)`.
  Status is `queued`, `running`, `waiting`, `succeeded`, `failed`, `errored`,
  `canceled` or `timed_out`.
- `trigger_run_logs(run_id, seq, stream, chunk, created_at)`.

## Trying a trigger

Not built yet (2026-10-29). Until it is, try a trigger in a scratch folder
with `on: [manual]` and `gs trigger run`.

A trigger cannot run its own draft with real secrets (decision 2). There will
be two ways to try one:

- **`gs trigger test <trigger> --event <fixture.json|changeset>`** runs the
  workspace's version locally in `workerd`, through Miniflare, with a fake
  `gs`. Checks and logs print to the terminal, and secrets are whatever you
  pass with `--secret NAME=value`.
- **`gs trigger run <trigger> --changeset <cs> --draft`** runs the patchset's
  version on Cloudflare. It gets no secrets and no `network:` (only the
  Gitslice API), and it reports checks as advisory (`draft/…`), never as
  gating ones.

## Where things show up

- **Changeset page:** trigger runs appear beside check runs, with their logs,
  status, details link and annotations on the diff.
- **Folder view:** a Triggers view on the slice page lists the triggers that
  apply at a folder (its own and its ancestors') and their recent runs, so you
  can see what a change there will set off. `gs trigger ls` shows the same.
- **CLI:** `gs trigger ls [folder]`, `gs trigger runs [--changeset | --trigger
  | --account]`, `gs trigger show <run>`, `gs trigger logs <run>`,
  `gs trigger run <trigger> [--input k=v]`, `gs trigger rerun <run>`,
  `gs trigger cancel <run>`.
- **Errors:** a `triggers.yaml` that does not parse, or a script that fails
  to load, shows up as an `errored` run against the event that tried to use it,
  and in the folder view. It never blocks a submit unless a required check
  depends on it.

## Migration

| Today | With triggers |
| --- | --- |
| Slice webhook `tag.created` → Cloud Build release trigger (design/24) | `/gitslice/gitslice/.gitslice/triggers.yaml` `release: {on: [tag.created], action: {http: {url: "${{ secrets.RELEASE_WEBHOOK_URL }}"}}}`, with the URL file's contents as a secret on `/gitslice/gitslice` |
| `checks.yaml` + CI daemon | Unchanged. Phase 4 lets a trigger hand work to a daemon. |
| Webhook UI and API | Kept for receivers that should not be in the tree. Marked "consider a trigger" in the UI. Removed only if nobody uses them. |

## Phases

0. **Spike (2 days).** A dispatcher Worker with Worker Loader in our Cloudflare
   account. It should confirm that:
   - an RPC stub can be passed per call into a reused isolate;
   - `globalOutbound` can be a gate configured at load time;
   - the gate can open sealed placeholders;
   - `console` can be captured.

   It also measures start time, warm reuse and the bill. Confirm or replace
   the parts marked "spike".
1. **HTTP triggers.** Parse `triggers.yaml` (generalize `internal/checks`
   resolution into a shared folder-config resolver). Fan out `push` and
   `tag.created` in the webhook dispatcher; `trigger_runs`; `gs trigger ls`,
   `runs` and `run`; the `.gitslice/` approval rule. Move the release webhook.
2. **Changeset events and write-back.** Changeset, approval and check events;
   `changes:`, `concurrency`; `TriggerService` check calls; check run fields
   and annotations on the changeset page.
3. **Worker actions.** Dispatcher, run claims and leases, callbacks, secret
   placeholders, `network:`, logs, limits; `gs trigger test`.

Phases 0 to 3 were built together on 2026-10-29, with the folder view and
the check action from Phase 4, without `gs trigger test` and `--draft`
runs.
4. **Later.**
   - `schedule`;
   - `action: {check: …}`, which queues a `checks.yaml` check on the slice's
     CI daemon or a Cloudflare container runner (design/32 layer 2), so
     everything starts from triggers;
   - TypeScript;
   - npm imports;
   - comments on changesets (Gitslice has no comment model yet).

## Implementation (2026-10-29)

**Where the code is.**

| Part | Code |
| --- | --- |
| Parsing, resolving, change kinds | `internal/triggers` (`parse.go`, `resolve.go`, `changes.go`) |
| Fan-out, http sends, posting runs, the sweep, run tokens | `internal/triggers/engine.go`, called from the webhook dispatcher's drains (`webhooks.Options.Triggers`) |
| Runs and logs | `trigger_runs`, `trigger_run_logs` (migration 0048); `storage.TriggerStore` in `internal/postgres/trigger_store.go` and `internal/storage/memory/triggers.go` |
| Check fields | `check_runs.trigger_run_id`, `details_url`, `annotations`; `CheckRun` fields 18 to 20, `CheckAnnotation` |
| API | `proto/core/v1/trigger.proto`: `TriggerService` (user calls) and `TriggerRunService` (run token or dispatcher key; listed as public in the auth interceptors, since it carries its own credential) |
| Submit rules | `service/trigger_submit.go`, on single, merge and stack submits |
| CLI | `internal/cli/trigger.go`, `gs help triggers` |
| Web | `TriggerRunsPanel` and trigger checks in `ChecksPanel` on the changeset page |
| Dispatcher | `trigger-dispatch/` (Worker `gitslice-trigger-dispatch`, route `triggers.gitslice.io`) |

**The check action and the Triggers view (2026-10-29, second round).**

- **`action: {check: [names]}`** runs checks of the trigger's folder's
  `checks.yaml` on the changeset's slice CI daemon, through the same code
  that dispatches patchset checks (`checkDispatcher.sendChecks`). It runs
  only on `changeset.created`, `changeset.updated` or `manual`, and takes no
  secrets: checks get their folder's secrets as always.
  - Each run is created with `trigger_run_id` and keeps provenance `ci`.
  - A check whose paths do not match, or that already has a run on the
    patchset, is left alone.
  - The trigger run waits, and finishes when its checks do: the
    `check_run.completed` event calls `Engine.FinishIfDone`.
- **`started_by: trigger`** in `checks.yaml` (on a check, or under
  `defaults`) moves a check from the plan's `Runnable` to `Triggered`. It no
  longer starts with the patchset, and agents do not bundle it. Reruns and
  daemon replays still find it (`Plan.Spec`).
- **The Triggers view** is a drawer on the slice page (`?triggers=1`, like
  History). It shows the triggers that apply at the folder (or a file's
  folder), with files that do not parse, the last 5 runs of each with their
  logs, and a Run button for triggers that list `manual`.
- **Migrating our own CI.** `.gitslice/triggers.yaml` and
  `web/.gitslice/triggers.yaml` start the existing checks with check actions,
  and the checks files gain `started_by: trigger`. This has to land in two
  changesets, after the server and a `gs` release that know `started_by`:
  1. the triggers files, while the checks still start with patchsets (the
     launcher skips checks that already ran, so nothing runs twice);
  2. `started_by: trigger`.

  In that order, every patchset has a way to get its checks. Older parsers
  refuse unknown fields, so a checks file with `started_by` would error every
  check on an old server or `gs`.

**Decisions made while building.**

- **No Durable Object.** A run's state lives on its row, and the server's
  sweep times out waiting runs. Callbacks go dispatcher →
  `TriggerRunService.StartCallback` → `onCallback`. That leaves one
  Cloudflare component, the dispatcher Worker.
- **The wire.** All of these are keyed by `GITSLICE_TRIGGER_DISPATCH_SECRET`,
  which is the dispatcher's `DISPATCH_SECRET`:
  - The server posts `{"run_id","ts"}` to `/run`, signed with
    `X-Gitslice-Dispatch-Signature` = hex HMAC-SHA256 of
    `"dispatch" 0x00 body`. The dispatcher refuses posts older than 5 minutes.
  - A run token is `gstr_<run>.<unix expiry>.<HMAC of "run-token" 0 run 0 expiry>`.
  - A callback signature is the first 32 hex characters of the HMAC of
    `"callback" 0 run`.
  - Placeholders are `gs-secret:v1:<AES-GCM of {v, code, exp}>`, sealed under
    HMAC(secret, "seal"). The gate opens one only for the same code hash,
    before `exp` (the run's deadline), and only on requests to a `network:`
    host over https.
- **A script call** runs in an isolate keyed by
  `trigger-<sha256(modules, network)>`. It gets the event JSON, the `gs` RPC
  target, the run's placeholders and its callback URL as arguments.
  - Console output is kept per call with AsyncLocalStorage (`nodejs_als`), so
    runs sharing an isolate do not mix their logs.
  - Imports are found by a pattern for relative specifiers (`from "./x.js"`,
    `import("./x.js")`), and must stay inside the `.gitslice` folder: at most 50
    modules and 1 MiB.
- **How a run ends.** It waits while a check it created is queued or running,
  or after `gs.waitForCallback()`, and ends when they all have a result
  (failed if one failed). An error in the script, or a call over 25 s, ends it
  as errored and closes its open checks as errored.
- **The `.gitslice/` approval rule.** It is satisfied when the author is an
  owner or admin of the account, or a different owner or admin approved the
  current patchset. CopyPath and MovePath bypass it, as they bypass every
  submit requirement.
- **Required checks reported by triggers.** A required check that a trigger
  lists in `checks:` counts as passed when no event of an open changeset would
  set that trigger off for the current patchset (its paths, changes and refs do
  not match) and the patchset has no run of the check. This mirrors a skipped
  `checks.yaml` check.
- **Events without files.** A changeset event that has no patchset yet
  (`changeset.created` before the first upload) fires nothing. Tags fire the
  triggers in the tagging slice's folders and above.
- **`serial`** orders the runs of a trigger across all changesets.
- **When events are resolved.** Requests that record events drain them inline
  when an active webhook or any `.gitslice/triggers.yaml` exists, checked
  through a 30 s cache. Landing a triggers file resets the cache. Otherwise
  they nudge the background loop. Fan-out never depends on the cache.

**Configuration.**

- Server: `GITSLICE_TRIGGER_DISPATCH_URL` (`https://triggers.gitslice.io`) and
  `GITSLICE_TRIGGER_DISPATCH_SECRET`. Without both, http triggers still run and
  worker triggers error with a clear message.
- Dispatcher: the var `GITSLICE_API` and the secret `DISPATCH_SECRET`.

**Testing.**

- **Unit tests:** `internal/triggers` (parsing, resolving, change kinds),
  `service/trigger_submit_test.go` (the approval rule), and the web panel
  tests.
- **RPC:** `tests/rpc/trigger_test.go`, with a fake dispatcher. It covers the
  http trigger on push with a secret and a signature, a worker run through
  `StartRun`, checks, logs with masking, state, a callback with a forged and
  a real signature, the submit gate, a skipped required check, rerun and
  cancel.
- **CLI:** `tests/cli/triggers_test.go`.
- **End to end** (`GITSLICE_TRIGGER_E2E=1`): `tests/rpc/trigger_dispatch_e2e_test.go`
  runs the real dispatcher under `wrangler dev`, with the script as a Dynamic
  Worker. It checks:
  - a relative import loads;
  - the gate passes the secret to httpbin, which echoes it back and the log
    masks it;
  - the gate refuses another host;
  - a callback finishes the run, and a late callback gets 410.

**Rollout.**

1. Deploy the server (migrations 0047 and 0048). Nothing changes until a
   triggers file lands. `cloudbuild.yaml` sets
   `GITSLICE_TRIGGER_DISPATCH_URL` and takes `GITSLICE_TRIGGER_DISPATCH_SECRET`
   from the Secret Manager secret `gitslice-trigger-dispatch-secret`.
2. Deploy the dispatcher: `wrangler deploy` in `trigger-dispatch/`, then
   `wrangler secret put DISPATCH_SECRET`, with the same value piped from Secret
   Manager.
3. Release `gs` with `gs trigger`.
4. Move the release webhook to a trigger.

## Alternatives considered

**Our own runner.** Cloud Tasks would push each run to a separate Cloud Run
service that runs the script in QuickJS compiled to WebAssembly (wazero). That
avoids depending on a newer Cloudflare product, and `gs trigger test` could
use exactly the production runtime.
We chose Dynamic Workers instead for four reasons:

- V8 with the Workers web APIs is more capable than QuickJS;
- Cloudflare's isolation keeps untrusted code entirely off our infrastructure;
- an isolate keyed by script makes the cost negligible;
- it leaves less runtime code for us to maintain.

The runner stays a fallback behind the same `TriggerService` if Worker Loader
changes in a way that rules it out.

## Open questions

- **Required checks that depend on a trigger.** If the trigger's definition is
  removed or never fires, submit waits forever. Proposal: a required check
  that no trigger or `checks.yaml` defines at the head says so on the
  changeset page, as a missing CI daemon does today.
- **Ancestor fan-out cost.** A root trigger fires for every change in the
  monorepo. That is intended, but `paths:` should be strongly recommended at
  the root, and the folder view should show how often each trigger ran.
- **Events on other slices.** `push` fires triggers in every folder a landed
  commit touches, whichever slice authored it. Changeset events fire for
  changesets of any slice whose changes touch the folder. That is the point of
  folder ownership, but it means a slice admin cannot stop a trigger above
  them, only the account's admins can.
- **Rate limits on callbacks** from the public internet, beyond the HMAC:
  per run, and per account.
