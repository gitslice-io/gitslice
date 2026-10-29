import { PageHeader, CommandBlock, NextDocCard, inlineCode } from "./DocParts";

const sectionClass = "mt-8 rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-5";
const headingClass = "text-base font-semibold text-zinc-950 dark:text-zinc-50";
const textClass = "mt-2 text-sm leading-6 text-slate-600 dark:text-zinc-400";
const strongClass = "font-semibold text-zinc-950 dark:text-zinc-50";
const listClass = "mt-4 space-y-3 text-sm leading-6 text-slate-600 dark:text-zinc-400";

const fieldRows: Array<[string, string]> = [
  ["on", "push, tag.created, changeset.created, changeset.updated, changeset.approved, changeset.submitted, changeset.abandoned, check_run.completed, manual"],
  ["paths", "Globs relative to the folder, \"/\" for the account root, \"!\" to exclude. Default: any change under the folder."],
  ["changes", "added, modified, deleted, moved. Default: any."],
  ["refs", "Target refs, such as main. Default: any."],
  ["secrets", "Secrets at the folder the trigger may use."],
  ["network", "Script triggers: the only hosts the script may call, such as api.github.com or *.example.com."],
  ["checks", "Check names the trigger may report. Each becomes <folder>/<name> and can be a required check."],
  ["concurrency", "cancel-superseded (default for changeset events), serial or parallel."],
  ["timeout", "How long a run may wait for callbacks: default 1h, at most 7d."],
  ["retry", "Script triggers: run an interrupted script again, up to 5 times. Default 0 (at most once)."],
  ["action", "http (url, headers, sign), worker (a .js module under the .gitslice folder), or check (checks of the folder's checks.yaml, run on the slice's CI daemon)."]
];

const apiRows: Array<[string, string]> = [
  ["onEvent(event, gs)", "Called once per run."],
  ["onCallback(request, gs)", "Called for each POST to the run's callback URL, until the run ends."],
  ["gs.checks.create / .update", "Report a check: status, summary, detailsUrl, annotations (path, line, message)."],
  ["gs.files.read(path) / .list(dir)", "Read the changeset's files, or the base with { at: \"base\" }."],
  ["gs.changeset.get()", "The changeset and its patchsets."],
  ["gs.secret(name)", "A placeholder the runtime swaps for the value on requests to network hosts."],
  ["gs.callbackUrl(data)", "Where an outside system calls the run back."],
  ["gs.state.get() / .set(value)", "Up to 64 KiB of JSON kept for the run."],
  ["gs.idempotencyKey(label)", "The same value on every attempt of a run."],
  ["console.log", "Goes to the run's log; secret values show as ***."]
];

function Table({ rows, head }: { rows: Array<[string, string]>; head: [string, string] }) {
  return (
    <div className="mt-4 overflow-x-auto">
      <table className="min-w-full divide-y divide-slate-200 dark:divide-zinc-800 text-left text-sm">
        <thead className="bg-slate-50 dark:bg-zinc-950 text-xs font-semibold uppercase tracking-normal text-slate-500 dark:text-zinc-400">
          <tr>
            <th className="px-4 py-3">{head[0]}</th>
            <th className="px-4 py-3">{head[1]}</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-slate-100 dark:divide-zinc-800">
          {rows.map(([name, what]) => (
            <tr key={name}>
              <td className="whitespace-nowrap px-4 py-3 font-mono text-xs font-medium text-zinc-950 dark:text-zinc-50">{name}</td>
              <td className="px-4 py-3 text-slate-600 dark:text-zinc-400">{what}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

export function TriggersDoc() {
  return (
    <div>
      <PageHeader
        eyebrow="Gitslice docs"
        title="Triggers"
        description="A trigger runs when files in a folder change: an HTTP call to your service, or a script on Cloudflare that can call any CI system and report checks that gate submit."
      />

      <section className={sectionClass}>
        <h2 className={headingClass}>How triggers work</h2>
        <ul className={listClass}>
          <li>
            <span className={strongClass}>They live next to the code.</span> Put{" "}
            <code className={inlineCode}>.gitslice/triggers.yaml</code> in any folder. A change in that folder, or
            below it, is checked against its triggers and those of every folder above, whichever slice made it.
          </li>
          <li>
            <span className={strongClass}>An action is an HTTP call or a script.</span> An http action posts the
            event, as JSON, to a URL. A worker action runs a JavaScript module from the same{" "}
            <code className={inlineCode}>.gitslice</code> folder in a sandbox on Cloudflare.
          </li>
          <li>
            <span className={strongClass}>Scripts can gate submit.</span> A script reports checks, which a slice can
            require like any other check. A required check whose trigger does not fire for a change counts as passed.
          </li>
          <li>
            <span className={strongClass}>Definitions come from the target branch.</span> For changeset events,
            Gitslice reads triggers from the head of the target ref, not from the changeset, so a changed trigger takes
            effect once it lands. Changes under any <code className={inlineCode}>.gitslice</code> folder need an
            account owner&apos;s or admin&apos;s approval.
          </li>
        </ul>
      </section>

      <section className={sectionClass}>
        <h2 className={headingClass}>Write one</h2>
        <CommandBlock>{`# services/billing/.gitslice/triggers.yaml
version: 1
triggers:
  notify:
    on: [push]
    refs: [main]
    paths: ["**/*.go", "!**/*_test.go"]
    secrets: [DEPLOY_TOKEN, DEPLOY_HMAC]
    action:
      http:
        url: https://deploy.example.com/hooks/billing
        headers:
          Authorization: "Bearer \${{ secrets.DEPLOY_TOKEN }}"
        sign: DEPLOY_HMAC          # X-Gitslice-Signature-256

  ci:
    on: [changeset.created, changeset.updated]
    paths: ["**/*.go", "/go.mod"]
    checks: [ci]                   # reported as services/billing/ci
    secrets: [GH_TOKEN]
    network: [api.github.com]
    timeout: 1h
    action:
      worker: triggers/ci.js`}</CommandBlock>
        <Table head={["Field", "Meaning"]} rows={fieldRows} />
      </section>

      <section className={sectionClass}>
        <h2 className={headingClass}>Start CI checks</h2>
        <p className={textClass}>
          A check action runs checks from the same folder&apos;s <code className={inlineCode}>.gitslice/checks.yaml</code>{" "}
          on the slice&apos;s CI daemon, so the triggers file decides when CI runs and checks.yaml how. Mark those checks{" "}
          <code className={inlineCode}>started_by: trigger</code> so they no longer start with every patchset on their own.
          The run waits for the checks and fails if one fails.
        </p>
        <CommandBlock>{`# .gitslice/checks.yaml
version: 1
defaults:
  started_by: trigger
checks:
  test:
    run: go test ./...
    paths: ["**/*.go"]

# .gitslice/triggers.yaml
version: 1
triggers:
  ci:
    on: [changeset.created, changeset.updated]
    action:
      check: [test]`}</CommandBlock>
      </section>

      <section className={sectionClass}>
        <h2 className={headingClass}>Scripts</h2>
        <p className={textClass}>
          A script is a JavaScript module. It may import other modules from the same{" "}
          <code className={inlineCode}>.gitslice</code> folder, and has <code className={inlineCode}>fetch</code>,{" "}
          <code className={inlineCode}>crypto.subtle</code> and the other web APIs, but no npm packages.
        </p>
        <CommandBlock>{`// services/billing/.gitslice/triggers/ci.js
export default {
  async onEvent(event, gs) {
    const check = await gs.checks.create({ name: "ci", status: "queued" });
    await fetch("https://api.github.com/repos/acme/ci/actions/workflows/ci.yml/dispatches", {
      method: "POST",
      headers: { Authorization: \`Bearer \${gs.secret("GH_TOKEN")}\` },
      body: JSON.stringify({ ref: "main", inputs: {
        commit: event.changeset.commit_id,
        callback: gs.callbackUrl(),
      } }),
    });
    await gs.state.set({ check: check.id });
  },

  // The workflow's last step: curl -X POST "$CALLBACK" -d '{"conclusion":"success"}'
  async onCallback(request, gs) {
    const { check } = await gs.state.get();
    const { conclusion } = await request.json();
    await gs.checks.update(check, { status: conclusion === "success" ? "passed" : "failed" });
  },
};`}</CommandBlock>
        <Table head={["API", "What it does"]} rows={apiRows} />
        <ul className={listClass}>
          <li>
            A run that reports a check waits until every check it reported has a result, or until{" "}
            <code className={inlineCode}>timeout</code>. Each call of a script may take 25 seconds; long work happens
            elsewhere and calls back.
          </li>
          <li>
            Runs of one script share a warm sandbox, so keep per-run data in{" "}
            <code className={inlineCode}>gs.state</code>, not in module variables.
          </li>
          <li>
            A script never sees a secret&apos;s value. If it must compute with one, for example to sign a request,
            list the secret in <code className={inlineCode}>reveal</code> and call{" "}
            <code className={inlineCode}>gs.revealSecret(name)</code>.
          </li>
        </ul>
      </section>

      <section className={sectionClass}>
        <h2 className={headingClass}>Runs</h2>
        <p className={textClass}>
          The changeset page shows its trigger runs beside its checks, with their logs. From the CLI:
        </p>
        <CommandBlock>{`gs trigger ls [folder]                  # triggers that apply at a folder
gs trigger runs --changeset <id>        # or --trigger <id>, --account <account>
gs trigger show <run-id>
gs trigger logs <run-id>
gs trigger run <trigger-id> --input reason=test   # triggers that listen for manual
gs trigger rerun <run-id>
gs trigger cancel <run-id>`}</CommandBlock>
        <ul className={listClass}>
          <li>
            A trigger runs at most once per event. An http action is retried for up to a day, like a webhook, with
            the same <code className={inlineCode}>X-Gitslice-Event-ID</code>. A script that is interrupted is marked
            errored rather than run twice, unless the trigger sets <code className={inlineCode}>retry</code>.
          </li>
          <li>
            A newer patchset cancels the runs of the older one (<code className={inlineCode}>cancel-superseded</code>
            ). A trigger never fires on events it caused itself.
          </li>
        </ul>
      </section>

      <section className="mt-8 grid gap-4 md:grid-cols-2">
        <NextDocCard description="Folders, precedence, and who can read a secret." section="secrets" title="Secrets" />
        <NextDocCard description="Checks that run on your own CI daemon." section="checks" title="CI checks" />
      </section>
    </div>
  );
}
