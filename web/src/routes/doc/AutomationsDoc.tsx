import {
  BulletPoint,
  CommandBlock,
  DocLink,
  DocSectionCard,
  DocTable,
  InlineCode,
  NextDocCard,
  PageHeader,
} from "./DocParts";

const fieldRows: Array<[string, string]> = [
  [
    "description",
    "Short summary shown in the folder policy drawer, Run modal, and CLI.",
  ],
  [
    "on",
    "Events that start the automation: push, tag.created, changeset.created, changeset.updated, changeset.approved, changeset.submitted, changeset.abandoned, check_run.completed, manual, or call (reusable template invoked via action.uses).",
  ],
  [
    "paths",
    'Globs relative to the folder, "/" for the account root, "!" to exclude. Default: any change under the folder.',
  ],
  [
    "changes",
    "Filter by change kind: added, modified, deleted, moved. Default: any.",
  ],
  ["refs", "Target refs such as main. Default: any."],
  [
    "inputs",
    "Typed manual inputs (type: string | choice | boolean | number, description, required, default, options, pattern). Passed as event.inputs (worker) or INPUT_<NAME> env vars (bash).",
  ],
  [
    "manual",
    "Manual invocation policy: roles ([admin, writer] or [admin]), require_human (true blocks agent accounts), confirm (prompt shown before running).",
  ],
  [
    "secrets",
    "Folder secrets the automation may use (list [FOO] or slot mapping { TEMPLATE_SLOT: CALLER_SECRET } for action.uses). Sealed as opaque placeholders by default in worker and http-gate bash runs.",
  ],
  [
    "reveal",
    "Subset of secrets exposed in plaintext (via gs.revealSecret(name) in workers, or $NAME and $GITSLICE_SECRETS_DIR/<NAME> in bash). Required for secrets used under egress: direct.",
  ],
  [
    "network",
    "Allowlist of public HTTPS hosts (such as api.github.com or *.cloudflare.com) permitted through Gate for worker and http-gate bash automations.",
  ],
  [
    "egress",
    "Bash actions only: offline (default when network is empty), http-gate (default when network is set; sealed secrets unwrapped at egress), or direct (full TCP/UDP internet for psql, ssh, aws, gcloud; requires reveal).",
  ],
  [
    "checks",
    "Check names the automation may report. Each becomes <folder>/<name> and can be marked required on a slice.",
  ],
  [
    "concurrency",
    "cancel-superseded (default for changeset events), serial, or parallel.",
  ],
  [
    "concurrency_group",
    "Worker and bash actions: a name shared with the account's other automations (such as prod-server for deploy and rollback). No two runs in one group run at once; the others wait their turn, oldest first. Implies serial.",
  ],
  [
    "timeout",
    "Maximum run duration. Default 5m (max 15m) for bash actions; default 1h (max 7d) for worker/check runs.",
  ],
  [
    "retry",
    "Retry an interrupted worker or bash run up to 5 times. Default 0 (at most once).",
  ],
  [
    "action",
    "http (url, headers, sign), worker (.js/.mjs module under .gitslice/), bash (Cloudflare Container shell run/script), check (checks from checks.yaml), uses (call an on:[call] reusable template), or steps (sequential multi-step pipeline).",
  ],
];

const bashRuntimeRows: Array<[string, string]> = [
  [
    "base (default)",
    "Debian slim with bash, curl, jq, git, openssh-client, openssl, zstd, and gs-auto.",
  ],
  ["node", "base + Node.js 22 LTS, npm, and pnpm."],
  ["python", "base + Python 3.12 and uv."],
  ["go", "base + Go 1.24 toolchain."],
  [
    "cloud",
    "base + gcloud, aws, kubectl, psql, gh, and terraform for deploy and ops runbooks.",
  ],
];

const apiRows: Array<[string, string]> = [
  [
    "onEvent(event, gs)",
    "Entry point called once when the worker automation starts.",
  ],
  [
    "onCallback(request, gs)",
    "Called for each HTTP POST to the run's callback URL until the run finishes.",
  ],
  [
    "gs.checks.create / .update",
    "Report check runs with status, summary, detailsUrl, and file annotations (path, line, message).",
  ],
  [
    "gs.files.read(path) / .list(dir)",
    'Read files at the changeset revision, or at the base with { at: "base" }.',
  ],
  ["gs.changeset.get()", "Fetch the changeset and its patchsets."],
  [
    "gs.secret(name)",
    "Returns an opaque placeholder that Gate swaps for the real secret value on outbound requests to allowed network hosts. Put it in a URL's path or query, a header, or the body; it cannot be the whole URL, since a secret may never decide where a request goes.",
  ],
  [
    "gs.revealSecret(name)",
    "Returns the raw value for a secret listed in reveal when script code must compute with it directly.",
  ],
  [
    "gs.callbackUrl(data)",
    "Generate a signed callback URL where an external system can POST results back to this run.",
  ],
  [
    "gs.waitForCallback()",
    "Keep the run open after the current call returns, until gs.finish(), gs.fail() or the timeout.",
  ],
  [
    "gs.finish()",
    "End the run as succeeded (or by its checks' results) once the current call returns.",
  ],
  [
    "gs.fail(message)",
    "End the run as failed with your own message, without a stack trace, once the current call returns.",
  ],
  [
    "gs.setDetailsUrl(url)",
    "Link the run to an https page outside Gitslice, such as the build it started; gs auto show and --follow print it.",
  ],
  [
    "gs.setOutput(key, value)",
    "Emit a named output on the run (secret-masked) for downstream pipeline steps (${{ steps.<id>.outputs.<key> }}) and inspection.",
  ],
  [
    "gs.state.get() / .set(value)",
    "Persist up to 64 KiB of JSON state across callbacks within the run.",
  ],
  [
    "gs.idempotencyKey(label)",
    "Deterministic key that stays identical across retries of the same run.",
  ],
  ["console.log", "Writes to the run's log with secret values masked as ***."],
];

export function AutomationsDoc() {
  return (
    <div>
      <PageHeader
        eyebrow="Gitslice docs"
        title="Automations"
        description="Run folder workflows on repository events or invoke secure manual runbooks with typed inputs, folder-scoped secrets, Cloudflare Workers, and ephemeral Cloudflare Containers."
      />

      <DocSectionCard title="How automations work">
        <ul className="space-y-2.5">
          <BulletPoint label="Committed beside your code:">
            Put <InlineCode>.gitslice/automations.yaml</InlineCode> in any
            folder. Changes in that folder or below it evaluate its automations
            and those of every ancestor folder, regardless of which slice
            authored the change. Inspect effective automations, checks, owners,
            and secrets with <InlineCode>gs policy</InlineCode> or the folder{" "}
            <span className="font-semibold text-zinc-950 dark:text-zinc-50">
              Policy
            </span>{" "}
            drawer in the web UI.
          </BulletPoint>
          <BulletPoint label="Four action types:">
            <InlineCode>http</InlineCode> sends a signed JSON webhook;{" "}
            <InlineCode>worker</InlineCode> runs a JavaScript ES module from{" "}
            <InlineCode>.gitslice/</InlineCode> in an isolated Cloudflare
            Dynamic Worker; <InlineCode>bash</InlineCode> executes shell
            commands or scripts inside a single-run Cloudflare Container; and{" "}
            <InlineCode>check</InlineCode> dispatches checks from{" "}
            <InlineCode>.gitslice/checks.yaml</InlineCode> on the account CI
            runner pool.
          </BulletPoint>
          <BulletPoint label="Secure manual runbooks:">
            Add <InlineCode>manual</InlineCode> to <InlineCode>on</InlineCode>{" "}
            with typed <InlineCode>inputs</InlineCode> and optional{" "}
            <InlineCode>manual.roles</InlineCode>,{" "}
            <InlineCode>manual.require_human</InlineCode>, and{" "}
            <InlineCode>manual.confirm</InlineCode> guards. Admins can expose
            safe, locked-down capabilities (such as{" "}
            <InlineCode>deploy-staging</InlineCode>) to writers or agents
            without ever handing them raw credentials.
          </BulletPoint>
          <BulletPoint label="Trusted landed definitions:">
            Gitslice always loads{" "}
            <InlineCode>.gitslice/automations.yaml</InlineCode> and referenced
            scripts from the head of the target ref (
            <InlineCode>refs/global/main</InlineCode>), never from an
            unsubmitted changeset. Any edit under{" "}
            <InlineCode>.gitslice/</InlineCode> requires approval from an
            account owner or admin.
          </BulletPoint>
        </ul>
      </DocSectionCard>

      <DocSectionCard title="Write an automations.yaml">
        <CommandBlock label="automations.yaml example">{`# services/billing/.gitslice/automations.yaml
version: 1
automations:
  deploy-staging:
    description: Deploy the billing service to staging
    on: [manual]
    manual:
      roles: [admin, writer]
      require_human: true
      confirm: "Deploy billing service to staging?"
    inputs:
      version:
        type: string
        required: true
        description: Release tag or commit SHA
        pattern: "^[a-zA-Z0-9._-]+$"
      region:
        type: choice
        options: [us-central1, europe-west1]
        default: us-central1
      dry_run:
        type: boolean
        default: false
    secrets: [CLOUDFLARE_API_TOKEN]
    network: [api.cloudflare.com]        # http-gate: sealed placeholder unwrapped at egress
    concurrency: serial
    timeout: 10m
    action:
      bash:
        runtime: cloud                   # base | node | python | go | cloud
        size: basic                      # lite | basic | standard-1 | standard-2
        checkout: folder                 # none | folder | account
        run: |
          echo "Deploying $INPUT_VERSION to $INPUT_REGION (dry_run=$INPUT_DRY_RUN)"
          curl -fsS -H "Authorization: Bearer $CLOUDFLARE_API_TOKEN" \\
            https://api.cloudflare.com/client/v4/user/tokens/verify

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
        sign: DEPLOY_HMAC                # X-Gitslice-Signature-256: sha256=<hex>`}</CommandBlock>
        <p>
          For <InlineCode>http</InlineCode> actions with{" "}
          <InlineCode>sign</InlineCode> set, Gitslice sends{" "}
          <InlineCode>X-Gitslice-Signature-256</InlineCode> formatted as{" "}
          <InlineCode>sha256=&lt;hex&gt;</InlineCode> (HMAC-SHA256 of the raw
          request body keyed by the named{" "}
          <DocLink section="secrets">secret</DocLink>), along with{" "}
          <InlineCode>X-Gitslice-Event-ID</InlineCode> so receivers can
          deduplicate retries.
        </p>
        <DocTable
          ariaLabel="automations.yaml fields"
          firstColMono
          head={["Field", "Meaning"]}
          rows={fieldRows}
        />
      </DocSectionCard>

      <DocSectionCard title="Bash automations on Cloudflare Containers">
        <p>
          A <InlineCode>bash</InlineCode> action runs inline{" "}
          <InlineCode>run:</InlineCode> commands or a{" "}
          <InlineCode>script:</InlineCode> under{" "}
          <InlineCode>.gitslice/</InlineCode> inside a fresh, single-run
          Cloudflare Container destroyed immediately upon exit. Inputs are
          passed strictly as data (
          <InlineCode>$INPUT_&lt;UPPER_SNAKE&gt;</InlineCode> and{" "}
          <InlineCode>$GITSLICE_EVENT_FILE</InlineCode>) — never
          text-interpolated into shell source.
        </p>
        <DocTable
          ariaLabel="bash container runtimes"
          firstColMono
          head={["Runtime", "Preinstalled tools"]}
          rows={bashRuntimeRows}
        />
        <ul className="mt-4 space-y-2.5">
          <BulletPoint label="Three egress modes:">
            Omit <InlineCode>network</InlineCode> for <strong>offline</strong>{" "}
            execution (no public internet). Specify{" "}
            <InlineCode>network: [hosts...]</InlineCode> for{" "}
            <strong>http-gate</strong> (default): Cloudflare disables direct
            internet and intercepts HTTPS through <InlineCode>Gate</InlineCode>,
            unsealing <InlineCode>$SECRET</InlineCode> placeholders only for
            allowlisted hosts so raw secrets never enter container RAM. Use{" "}
            <InlineCode>egress: direct</InlineCode> +{" "}
            <InlineCode>reveal: [SECRET]</InlineCode> for non-HTTP TCP/gRPC CLIs
            such as <InlineCode>psql</InlineCode>, <InlineCode>ssh</InlineCode>,{" "}
            <InlineCode>kubectl</InlineCode>, <InlineCode>aws</InlineCode>, or{" "}
            <InlineCode>gcloud</InlineCode>.
          </BulletPoint>
          <BulletPoint label="Secrets as env vars and tmpfs files:">
            Every declared secret is injected as <InlineCode>$NAME</InlineCode>{" "}
            and written to an in-memory file at{" "}
            <InlineCode>$GITSLICE_SECRETS_DIR/&lt;NAME&gt;</InlineCode> (
            <InlineCode>/run/secrets/&lt;NAME&gt;</InlineCode>, mode{" "}
            <InlineCode>0400</InlineCode>), making multi-line credentials such
            as GCP service account JSON or SSH private keys clean to pass via{" "}
            <InlineCode>--key-file</InlineCode> or <InlineCode>-i</InlineCode>.
            All log output is scrubbed of secret values in both the dispatcher
            and the server.
          </BulletPoint>
          <BulletPoint label="Helper CLI inside the container:">
            Use <InlineCode>gs-auto check create|update</InlineCode>,{" "}
            <InlineCode>gs-auto state get|set</InlineCode>,{" "}
            <InlineCode>gs-auto set-output &lt;key&gt; &lt;value&gt;</InlineCode>
            , and <InlineCode>gs-auto set-details-url &lt;url&gt;</InlineCode>{" "}
            without exposing the run token to shell code.
          </BulletPoint>
        </ul>
        <CommandBlock label="direct-egress database migration example">{`# services/billing/.gitslice/automations.yaml
version: 1
automations:
  migrate-staging-db:
    description: Apply SQL migrations to staging Postgres
    on: [manual]
    manual:
      roles: [admin]
      require_human: true
      confirm: "Apply pending SQL migrations to staging?"
    secrets: [STAGING_DATABASE_URL]
    reveal: [STAGING_DATABASE_URL]
    egress: direct
    concurrency: serial
    timeout: 15m
    action:
      bash:
        runtime: cloud
        script: scripts/migrate.sh`}</CommandBlock>
      </DocSectionCard>

      <DocSectionCard title="Start CI checks from an automation">
        <p>
          A <InlineCode>check</InlineCode> action runs checks from the same
          folder&apos;s <InlineCode>.gitslice/checks.yaml</InlineCode> on the
          account&apos;s CI runner pool — letting{" "}
          <InlineCode>automations.yaml</InlineCode> control <em>when</em> CI
          runs while <InlineCode>checks.yaml</InlineCode> defines <em>how</em>.
          Set <InlineCode>started_by: automation</InlineCode> on those checks so
          they no longer run automatically on every patchset.
        </p>
        <CommandBlock label="automation-started checks example">{`# .gitslice/checks.yaml
version: 1
defaults:
  started_by: automation
checks:
  test:
    run: go test ./...
    paths: ["**/*.go"]

# .gitslice/automations.yaml
version: 1
automations:
  ci:
    on: [changeset.created, changeset.updated, manual]
    action:
      check: [test]`}</CommandBlock>
      </DocSectionCard>

      <DocSectionCard title="Worker scripts">
        <p>
          A worker automation is a standard JavaScript ES module stored under{" "}
          <InlineCode>.gitslice/</InlineCode>. It may import sibling modules
          from the same <InlineCode>.gitslice</InlineCode> folder and has
          standard Web APIs (<InlineCode>fetch</InlineCode>,{" "}
          <InlineCode>crypto.subtle</InlineCode>, <InlineCode>URL</InlineCode>),
          with no external npm packages.
        </p>
        <CommandBlock label="automation worker script example">{`// services/billing/.gitslice/scripts/ci.js
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
    await gs.state.set({ checkId: check.id });
    gs.waitForCallback();
  },

  async onCallback(request, gs) {
    const body = await request.json();
    const { checkId } = await gs.state.get();
    await gs.checks.update(checkId, {
      status: body.passed ? "passed" : "failed",
      summary: body.summary,
    });
    gs.finish();
  },
};`}</CommandBlock>
        <DocTable
          ariaLabel="worker script API"
          firstColMono
          head={["API", "Purpose"]}
          rows={apiRows}
        />
      </DocSectionCard>

      <DocSectionCard title="Reusable automation templates">
        <p>
          Define a reusable template in any folder by adding{" "}
          <InlineCode>on: [call]</InlineCode> in{" "}
          <InlineCode>.gitslice/automations.yaml</InlineCode>. Callers invoke it
          with <InlineCode>action.uses</InlineCode>, pass static or interpolated
          parameters via <InlineCode>with</InlineCode>, and bind folder secrets
          by name or slot mapping. Unbound template inputs automatically appear
          on the caller&apos;s manual Run modal and CLI prompt.
        </p>
        <CommandBlock label="template and caller example">{`# /acme/platform/.gitslice/automations.yaml  (reusable template)
version: 1
automations:
  deploy-cloudrun:
    description: Deploy a service to Google Cloud Run
    on: [call]
    inputs:
      service: { type: string, required: true }
      region:  { type: choice, options: [us-central1, europe-west1], default: us-central1 }
      image:   { type: string, required: true }
    secrets: [GCP_SA_KEY]
    reveal: [GCP_SA_KEY]
    egress: direct
    action:
      bash:
        runtime: cloud
        run: |
          gcloud auth activate-service-account --key-file="$GITSLICE_SECRETS_DIR/GCP_SA_KEY"
          gcloud run deploy "$INPUT_SERVICE" --region="$INPUT_REGION" --image="$INPUT_IMAGE"
          gs-auto set-output service_url "https://$INPUT_SERVICE.run.app"

# /acme/services/billing/.gitslice/automations.yaml  (caller)
version: 1
automations:
  deploy-prod:
    on: [manual]
    manual: { roles: [admin], require_human: true, confirm: "Deploy billing to prod?" }
    secrets:
      GCP_SA_KEY: BILLING_PROD_GCP_KEY     # template slot -> caller folder secret
    reveal: [BILLING_PROD_GCP_KEY]         # caller explicitly grants reveal + direct egress
    egress: direct
    action:
      uses: /acme/platform/deploy-cloudrun # same-account live; cross-account must pin @v1.0.0 or @sha256:...
      with:
        service: billing-api               # bound here; "region" and "image" stay promptable inputs`}</CommandBlock>
        <ul className="space-y-2">
          <BulletPoint label="Pinning rules:">
            Same-account references may omit a version pin to follow the
            caller&apos;s tree live, or pin{" "}
            <InlineCode>@&lt;slice-tag&gt;</InlineCode> /{" "}
            <InlineCode>@sha256:&lt;commit&gt;</InlineCode>. Cross-account
            references must always pin an immutable public slice tag or commit
            SHA. To publish a versioned template for other accounts, expose its
            folder in a public slice and cut an immutable tag with{" "}
            <InlineCode>gs tag create v1.0.0</InlineCode>, then inspect it with{" "}
            <InlineCode>
              gs auto inspect /&lt;account&gt;/&lt;folder&gt;/&lt;name&gt;@v1.0.0
            </InlineCode>
            .
          </BulletPoint>
          <BulletPoint label="Caller-bounded security ceilings:">
            Secrets always resolve from the caller&apos;s folder (never the
            template&apos;s). A template cannot reveal secrets, use{" "}
            <InlineCode>egress: direct</InlineCode>, or exceed the caller&apos;s
            explicit <InlineCode>timeout</InlineCode> without caller opt-in.
            Cross-account callers must also allowlist every host the template
            declares in <InlineCode>network</InlineCode>.
          </BulletPoint>
        </ul>
      </DocSectionCard>

      <DocSectionCard title="Multi-step pipelines">
        <p>
          Chain up to 20 sequential steps with{" "}
          <InlineCode>action.steps</InlineCode>. Each step can invoke a reusable
          template (<InlineCode>uses</InlineCode>) or run inline{" "}
          <InlineCode>bash</InlineCode>, <InlineCode>worker</InlineCode>,{" "}
          <InlineCode>http</InlineCode>, or <InlineCode>check</InlineCode>{" "}
          actions. Steps emit outputs via{" "}
          <InlineCode>gs-auto set-output</InlineCode> (bash) or{" "}
          <InlineCode>gs.setOutput</InlineCode> (worker) and pass them
          downstream with{" "}
          <InlineCode>{"${{ steps.<id>.outputs.<key> }}"}</InlineCode>. Control
          error handling with <InlineCode>if: success</InlineCode> (default),{" "}
          <InlineCode>if: failure</InlineCode>, or{" "}
          <InlineCode>if: always</InlineCode>.
        </p>
        <CommandBlock label="pipeline example">{`# /acme/services/billing/.gitslice/automations.yaml
version: 1
automations:
  release:
    on: [manual]
    secrets: [GCP_SA_KEY, SLACK_WEBHOOK]
    reveal: [GCP_SA_KEY]
    egress: direct
    action:
      steps:
        - id: build
          bash:
            runtime: go
            checkout: folder
            run: |
              TAG="v$(date +%Y%m%d%H%M)"
              gs-auto set-output image "gcr.io/acme/billing:$TAG"
        - id: deploy
          uses: /acme/platform/deploy-cloudrun
          with:
            service: billing-api
            image: \${{ steps.build.outputs.image }}
        - id: notify
          if: always
          http:
            url: https://hooks.slack.com/services/\${{ secrets.SLACK_WEBHOOK }}`}</CommandBlock>
      </DocSectionCard>

      <DocSectionCard title="Inspect and run automations">
        <CommandBlock label="automation CLI commands">{`gs auto ls [folder]                                  # automations applying at a folder
gs auto inspect /acme/platform/deploy-cloudrun       # inspect a reusable on:[call] template & tags
gs auto runs --changeset <id>                        # runs for a changeset (or --automation, --account)
gs auto show <run-id>                                # run status, template, outputs, and pipeline steps
gs auto logs <run-id> --follow                       # stream live stdout/stderr (including pipeline steps)
gs auto run <id> -i version=v1.4.2 --yes --follow    # invoke a manual automation and follow logs
gs auto rerun <run-id>                               # rerun a finished automation
gs auto cancel <run-id>                              # stop a queued, running, or waiting run`}</CommandBlock>
        <p>
          An http automation ends when its endpoint accepts the post: its run
          says <InlineCode>delivered</InlineCode> in the log, and what the
          endpoint does next is not tracked. To follow an outside job to its
          end, use a worker that passes{" "}
          <InlineCode>gs.callbackUrl()</InlineCode> and calls{" "}
          <InlineCode>gs.waitForCallback()</InlineCode>.
        </p>
      </DocSectionCard>

      <DocSectionCard title="Try an automation before it lands">
        <CommandBlock label="trying automations">{`# On the server, as defined in an open changeset (one that lists secrets needs an account admin)
gs auto run <id> --from-changeset <changeset> --follow

# Locally with node, from the workspace's files, with a stand-in gs
gs auto run <id> --local --secret TOKEN=test-value --callback build-done.json`}</CommandBlock>
        <p>
          <InlineCode>--local</InlineCode> runs a worker's{" "}
          <InlineCode>onEvent</InlineCode>, then{" "}
          <InlineCode>onCallback</InlineCode> once per{" "}
          <InlineCode>--callback</InlineCode> file, with the same rules as Gate:
          https only, hosts in <InlineCode>network</InlineCode>, and secrets
          only where they cannot change a request's origin. Secrets without a{" "}
          <InlineCode>--secret</InlineCode> value stay placeholders.
        </p>
        <p>
          In the web UI, open the{" "}
          <span className="font-semibold text-zinc-950 dark:text-zinc-50">
            Policy
          </span>{" "}
          drawer on any folder to inspect effective automations, run manual
          runbooks through the typed input modal, and stream live logs.
        </p>
      </DocSectionCard>

      <section className="mt-8 grid gap-4 md:grid-cols-2">
        <NextDocCard
          description="Define folder-scoped build and test checks and required submit gates."
          section="checks"
          title="CI Checks"
        />
        <NextDocCard
          description="Scope encrypted tokens and credentials to folders for checks and automations."
          section="secrets"
          title="Secrets"
        />
      </section>
    </div>
  );
}
