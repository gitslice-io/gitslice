import {
  BulletPoint,
  CommandBlock,
  DocLink,
  DocSectionCard,
  DocTable,
  InlineCode,
  NextDocCard,
  PageHeader
} from "./DocParts";

const fieldRows: Array<[string, string]> = [
  [
    "on",
    "Events that fire the trigger: push, tag.created, changeset.created, changeset.updated, changeset.approved, changeset.submitted, changeset.abandoned, check_run.completed, manual."
  ],
  [
    "paths",
    'Globs relative to the folder, "/" for the account root, "!" to exclude. Default: any change under the folder.'
  ],
  ["changes", "Filter by change kind: added, modified, deleted, moved. Default: any."],
  ["refs", "Target refs such as main. Default: any."],
  ["secrets", "Folder secrets the trigger may reference (masked from script code unless listed in reveal)."],
  [
    "reveal",
    "Script triggers: subset of secrets the script may read verbatim via gs.revealSecret(name) (for example, to compute a custom signature)."
  ],
  [
    "network",
    "Script triggers: allowlist of outbound hosts the script may call, such as api.github.com or *.example.com."
  ],
  [
    "checks",
    "Check names the trigger may report. Each becomes <folder>/<name> and can be marked required on a slice."
  ],
  [
    "concurrency",
    "cancel-superseded (default for changeset events), serial, or parallel."
  ],
  ["timeout", "How long a run may wait for callbacks or checks: default 1h, maximum 7d."],
  [
    "retry",
    "Script triggers: retry an interrupted script up to 5 times. Default 0 (at most once)."
  ],
  [
    "action",
    "http (url, headers, sign), worker (a .js module under the .gitslice folder), or check (checks from the folder's checks.yaml, run on the slice's CI daemon)."
  ]
];

const apiRows: Array<[string, string]> = [
  ["onEvent(event, gs)", "Entry point called once when the trigger run starts."],
  ["onCallback(request, gs)", "Called for each HTTP POST to the run's callback URL until the run finishes."],
  [
    "gs.checks.create / .update",
    "Report check runs with status, summary, detailsUrl, and file annotations (path, line, message)."
  ],
  ["gs.files.read(path) / .list(dir)", 'Read files at the changeset revision, or at the base with { at: "base" }.'],
  ["gs.changeset.get()", "Fetch the changeset and its patchsets."],
  [
    "gs.secret(name)",
    "Returns an opaque placeholder that the runtime swaps for the real secret value on outbound requests to allowed network hosts."
  ],
  [
    "gs.revealSecret(name)",
    "Returns the raw value for a secret listed in reveal when script code must compute with it directly."
  ],
  ["gs.callbackUrl(data)", "Generate a signed callback URL where an external system can POST results back to this run."],
  ["gs.state.get() / .set(value)", "Persist up to 64 KiB of JSON state across callbacks within the run."],
  ["gs.idempotencyKey(label)", "Deterministic key that stays identical across retries of the same run."],
  ["console.log", "Writes to the run's log with secret values masked as ***."]
];

export function TriggersDoc() {
  return (
    <div>
      <PageHeader
        eyebrow="Gitslice docs"
        title="Triggers"
        description="React automatically when files in a folder change: send signed HTTP webhooks, run sandboxed JavaScript workers that integrate with external CI and gate submit, or orchestrate folder CI checks."
      />

      <DocSectionCard title="How triggers work">
        <ul className="space-y-2.5">
          <BulletPoint label="Committed beside your code:">
            Put <InlineCode>.gitslice/triggers.yaml</InlineCode> in any folder. A change in that
            folder or below it is evaluated against its triggers and those of every ancestor folder,
            regardless of which slice authored the change.
          </BulletPoint>
          <BulletPoint label="Three action types:">
            An <InlineCode>http</InlineCode> action posts the JSON event payload to a public HTTPS
            URL; a <InlineCode>worker</InlineCode> action runs a JavaScript module from the same{" "}
            <InlineCode>.gitslice</InlineCode> folder in an isolated Cloudflare sandbox; and a{" "}
            <InlineCode>check</InlineCode> action dispatches checks from that folder&apos;s{" "}
            <InlineCode>.gitslice/checks.yaml</InlineCode> on the slice&apos;s CI daemon.
          </BulletPoint>
          <BulletPoint label="Gate submit from external systems:">
            Worker scripts can report checks back to Gitslice, and any slice can mark them required
            just like local <DocLink section="checks">CI checks</DocLink>. If a trigger does not
            fire for a given change (for example, paths didn&apos;t match), its required check
            counts as passed.
          </BulletPoint>
          <BulletPoint label="Trusted target-branch definitions:">
            For changeset events, Gitslice evaluates triggers from the head of the target ref rather
            than the unsubmitted changeset, so trigger edits take effect only after landing. Any
            change under a <InlineCode>.gitslice</InlineCode> folder requires approval from an
            account owner or admin.
          </BulletPoint>
        </ul>
      </DocSectionCard>

      <DocSectionCard title="Write a triggers.yaml">
        <CommandBlock label="triggers.yaml example">{`# services/billing/.gitslice/triggers.yaml
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
        sign: DEPLOY_HMAC          # X-Gitslice-Signature-256: sha256=<hex>

  ci:
    on: [changeset.created, changeset.updated]
    paths: ["**/*.go", "/go.mod"]
    checks: [ci]                   # reported as services/billing/ci
    secrets: [GH_TOKEN]
    network: [api.github.com]
    timeout: 1h
    action:
      worker: triggers/ci.js`}</CommandBlock>
        <p>
          For <InlineCode>http</InlineCode> actions with <InlineCode>sign</InlineCode> set, Gitslice
          sends <InlineCode>X-Gitslice-Signature-256</InlineCode> formatted as{" "}
          <InlineCode>sha256=&lt;hex&gt;</InlineCode> (HMAC-SHA256 of the raw request body keyed by
          the named <DocLink section="secrets">secret</DocLink>), along with{" "}
          <InlineCode>X-Gitslice-Event-ID</InlineCode> so receivers can deduplicate retries.
        </p>
        <DocTable ariaLabel="triggers.yaml fields" firstColMono head={["Field", "Meaning"]} rows={fieldRows} />
      </DocSectionCard>

      <DocSectionCard title="Start CI checks from a trigger">
        <p>
          A <InlineCode>check</InlineCode> action runs checks from the same folder&apos;s{" "}
          <InlineCode>.gitslice/checks.yaml</InlineCode> on the slice&apos;s CI daemon — letting{" "}
          <InlineCode>triggers.yaml</InlineCode> control <em>when</em> CI runs while{" "}
          <InlineCode>checks.yaml</InlineCode> defines <em>how</em>. Set{" "}
          <InlineCode>started_by: trigger</InlineCode> on those checks so they no longer run
          automatically on every patchset. The trigger run waits for the checks and fails if any
          check fails.
        </p>
        <CommandBlock label="trigger-started checks example">{`# .gitslice/checks.yaml
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
      </DocSectionCard>

      <DocSectionCard title="Worker scripts">
        <p>
          A worker trigger is a standard JavaScript ES module stored under{" "}
          <InlineCode>.gitslice/</InlineCode>. It may import sibling modules from the same{" "}
          <InlineCode>.gitslice</InlineCode> folder and has standard Web APIs (
          <InlineCode>fetch</InlineCode>, <InlineCode>crypto.subtle</InlineCode>,{" "}
          <InlineCode>URL</InlineCode>), with no external npm packages.
        </p>
        <CommandBlock label="trigger worker script example">{`// services/billing/.gitslice/triggers/ci.js
export default {
  async onEvent(event, gs) {
    const check = await gs.checks.create({ name: "ci", status: "queued" });
    await fetch("https://api.github.com/repos/acme/ci/actions/workflows/ci.yml/dispatches", {
      method: "POST",
      headers: { Authorization: \`Bearer \${gs.secret("GH_TOKEN")}\` },
      body: JSON.stringify({
        ref: "main",
        inputs: {
          commit: event.changeset.commit_id,
          callback: gs.callbackUrl(),
        },
      }),
    });
    await gs.state.set({ check: check.id });
  },

  // The external workflow's final step:
  // curl -X POST "$CALLBACK" -d '{"conclusion":"success"}'
  async onCallback(request, gs) {
    const { check } = await gs.state.get();
    const { conclusion } = await request.json();
    await gs.checks.update(check, {
      status: conclusion === "success" ? "passed" : "failed",
    });
  },
};`}</CommandBlock>
        <DocTable ariaLabel="Worker script runtime API" firstColMono head={["API", "What it does"]} rows={apiRows} />
        <ul className="space-y-2">
          <li>
            A run that creates a check stays open until every check it reported reaches a terminal
            status or <InlineCode>timeout</InlineCode> expires. Each script invocation has a
            25-second execution budget; offload long work to an external system and report back via{" "}
            <InlineCode>gs.callbackUrl()</InlineCode>.
          </li>
          <li>
            Multiple runs of the same script may share a warm isolate, so store per-run data in{" "}
            <InlineCode>gs.state</InlineCode> rather than module-level globals.
          </li>
          <li>
            By default, <InlineCode>gs.secret(name)</InlineCode> hands your code an opaque
            placeholder that the runtime substitutes only when calling hosts listed in{" "}
            <InlineCode>network</InlineCode>. If your script must compute directly with the secret
            value (such as signing a custom request), list it in <InlineCode>reveal</InlineCode> and
            call <InlineCode>gs.revealSecret(name)</InlineCode>.
          </li>
        </ul>
      </DocSectionCard>

      <DocSectionCard title="Inspect and control runs">
        <p>
          The changeset page displays trigger runs and logs alongside checks. From the CLI:
        </p>
        <CommandBlock label="trigger run CLI commands">{`gs trigger ls [folder]                            # triggers applying at a folder
gs trigger runs --changeset <id>                  # or --trigger <id>, --account <account>
gs trigger show <run-id>
gs trigger logs <run-id>
gs trigger run <trigger-id> --input reason=test   # for triggers listening on manual
gs trigger rerun <run-id>
gs trigger cancel <run-id>`}</CommandBlock>
        <ul className="space-y-2">
          <li>
            Each trigger fires at most once per event. Failed <InlineCode>http</InlineCode>{" "}
            deliveries are retried for up to 24 hours with the same{" "}
            <InlineCode>X-Gitslice-Event-ID</InlineCode>. An interrupted worker script is marked
            errored rather than re-executed unless <InlineCode>retry</InlineCode> is set.
          </li>
          <li>
            Uploading a newer patchset cancels in-flight runs for older patchsets by default (
            <InlineCode>cancel-superseded</InlineCode>). A trigger never fires on events it caused
            itself.
          </li>
        </ul>
      </DocSectionCard>

      <section className="mt-8 grid gap-4 md:grid-cols-2">
        <NextDocCard
          description="Folder scoping, precedence, role access, and masking for secrets."
          section="secrets"
          title="Secrets"
        />
        <NextDocCard
          description="Define folder-scoped build and test checks on your own CI daemon."
          section="checks"
          title="CI Checks"
        />
      </section>
    </div>
  );
}
