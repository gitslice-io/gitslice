import {
  BulletPoint,
  CommandBlock,
  DocLink,
  DocSectionCard,
  InlineCode,
  NextDocCard,
  PageHeader
} from "./DocParts";

const quickstartSteps = [
  {
    title: "Commit a checks.yaml file",
    description:
      "Add .gitslice/checks.yaml inside the folder you want to guard. It is versioned alongside your source code.",
    command: undefined,
    label: undefined
  },
  {
    title: "Mark required checks on the slice",
    description:
      "Require a check so it gates submit. Reference it by its qualified ID (<folder>/<name>, such as backend/test).",
    command: "gs slice update <account>/<slice> --required-check backend/test",
    label: "set required check command"
  },
  {
    title: "Test locally, then capture a patchset",
    description:
      "Run gs ci to test pending edits locally without creating a changeset, then create a changeset and watch its checks run.",
    command: `gs ci
gs create --all --message "fix retry backoff"
gs check list
gs check logs backend/test --follow`,
    label: "run and inspect checks commands"
  },
  {
    title: "Optional: attach a designated CI daemon",
    description:
      "For checks that need files outside the slice (such as a repo-root check) or consistent server-side execution with secrets, designate a daemon in Slice Settings or via the CLI.",
    command: "gs slice set-ci-daemon <account>/<slice> <daemon-id>",
    label: "set CI daemon command"
  }
];

export function ChecksDoc() {
  return (
    <div>
      <PageHeader
        eyebrow="Gitslice docs"
        title="CI Checks"
        description="Define build, test, and lint commands directly in your source tree. Applicable checks run automatically on every patchset, report back to the changeset, and gate submit — on machines you control."
      />

      <DocSectionCard title="How it works">
        <ul className="space-y-2.5">
          <BulletPoint label="Defined in source:">
            Checks live in <InlineCode>.gitslice/checks.yaml</InlineCode> files committed alongside
            your code — one per folder — so check definitions evolve with the revision they test.
          </BulletPoint>
          <BulletPoint label="Folder-scoped and cascading:">
            For each path a patchset modifies, every <InlineCode>checks.yaml</InlineCode> from that
            folder up to the root applies. A check defined in <InlineCode>backend/</InlineCode> runs
            only when files under <InlineCode>backend/</InlineCode> change, and an optional{" "}
            <InlineCode>paths</InlineCode> filter narrows it further.
          </BulletPoint>
          <BulletPoint label="Per-patchset results:">
            Every new patchset runs its applicable checks and records pass/fail against that exact
            patchset. Updating a changeset with <InlineCode>gs modify</InlineCode> triggers a fresh
            run.
          </BulletPoint>
          <BulletPoint label="Enforced submit gates:">
            A slice can mark checks as <em>required</em>. <InlineCode>gs submit</InlineCode> blocks
            until every required check passes on the current patchset.
          </BulletPoint>
          <BulletPoint label="Flexible execution:">
            Checks run inside containers when <InlineCode>image</InlineCode> or{" "}
            <InlineCode>build</InlineCode> is set, or directly on the host otherwise. Checks that
            need secrets or files outside the slice run on the slice&apos;s designated CI daemon.
          </BulletPoint>
        </ul>
      </DocSectionCard>

      <section className="mt-8 rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-5">
        <h2 className="text-base font-semibold text-zinc-950 dark:text-zinc-50">
          Add your first check
        </h2>
        <ol className="mt-4 space-y-5">
          {quickstartSteps.map((step, index) => (
            <li className="flex gap-3 text-sm leading-6" key={step.title}>
              <span className="flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-zinc-950 dark:bg-zinc-100 text-xs font-semibold text-white dark:text-zinc-950">
                {index + 1}
              </span>
              <div className="min-w-0 flex-1">
                <h3 className="font-semibold text-zinc-950 dark:text-zinc-50">{step.title}</h3>
                <p className="mt-1 text-slate-600 dark:text-zinc-400">{step.description}</p>
                {step.command ? (
                  <CommandBlock label={step.label}>{step.command}</CommandBlock>
                ) : null}
              </div>
            </li>
          ))}
        </ol>
      </section>

      <DocSectionCard title="The checks.yaml format">
        <p>
          Place this file at <InlineCode>&lt;folder&gt;/.gitslice/checks.yaml</InlineCode>. A
          check&apos;s qualified ID is its folder path plus its name — for example,{" "}
          <InlineCode>backend/test</InlineCode>.
        </p>
        <CommandBlock label="checks.yaml example">{`version: 1

# optional defaults applied to every check in this file
defaults:
  image: "golang:1.24"     # run in this container; omit to run on the host
  timeout: "10m"
  setup:                   # baked once into a cached image, reused across runs
    - "apt-get update && apt-get install -y protobuf-compiler"

checks:
  test:
    run: "go test ./..."   # required: shell command
    paths: ["**/*.go"]     # optional: only run when matching paths change
  lint:
    run: "golangci-lint run"
  smoke:
    run: "./scripts/smoke.sh"
    include: ["/go.mod", "/go.sum"]  # extra paths to make available
    network: true                    # allow outbound network (default: denied)`}</CommandBlock>
        <ul className="space-y-2">
          <BulletPoint label={<InlineCode>run</InlineCode>}>
            (required) — shell command executed via <InlineCode>sh -c</InlineCode> from the
            check&apos;s folder. Exit status 0 passes.
          </BulletPoint>
          <BulletPoint label={<InlineCode>paths</InlineCode>}>
            — globs relative to the folder (or <InlineCode>/</InlineCode>-rooted for account paths);
            the check is skipped if no changed path matches.
          </BulletPoint>
          <BulletPoint
            label={
              <>
                <InlineCode>image</InlineCode>, <InlineCode>env</InlineCode>,{" "}
                <InlineCode>network</InlineCode>, <InlineCode>timeout</InlineCode>,{" "}
                <InlineCode>working_dir</InlineCode>, <InlineCode>include</InlineCode>,{" "}
                <InlineCode>cache</InlineCode>
              </>
            }
          >
            — optional per check or under <InlineCode>defaults</InlineCode>. Outbound network access
            is denied by default unless <InlineCode>network: true</InlineCode> is set.
          </BulletPoint>
          <BulletPoint label={<InlineCode>started_by: trigger</InlineCode>}>
            — prevents a check from starting automatically on every patchset so a{" "}
            <InlineCode>triggers.yaml</InlineCode> rule with a <InlineCode>check</InlineCode> action
            controls when it runs (see <DocLink section="triggers">Triggers</DocLink>).
          </BulletPoint>
          <BulletPoint label={<InlineCode>setup</InlineCode>}>
            — change-independent commands run once to prepare the container image (such as
            installing system packages or toolchains). The result is cached and reused across runs.
            Keep workspace files out of <InlineCode>setup</InlineCode>; use{" "}
            <InlineCode>cache</InlineCode> or <InlineCode>build</InlineCode> for language
            dependencies like <InlineCode>go mod</InlineCode> or <InlineCode>npm</InlineCode>.
          </BulletPoint>
        </ul>
      </DocSectionCard>

      <DocSectionCard title="Inspect, rerun, and allow checks">
        <p>
          Run applicable checks locally against pending workspace edits with{" "}
          <InlineCode>gs ci</InlineCode> (with setup and run timing), or inspect and control remote
          patchset runs with <InlineCode>gs check</InlineCode>:
        </p>
        <CommandBlock label="check inspection and rerun commands">{`gs ci                                 # run applicable checks locally against pending edits
gs check list [changeset]             # latest run of each check on the current patchset
gs check logs <check> --follow        # stream live logs until the check finishes
gs check rerun <check>                # rerun a check on the current patchset
gs check allow [changeset]            # allow a contributor patchset to run with secrets`}</CommandBlock>
      </DocSectionCard>

      <DocSectionCard title="Build the check image from your tree">
        <p>
          Use <InlineCode>build</InlineCode> instead of <InlineCode>image</InlineCode> when the
          container environment itself belongs in source control. They are mutually exclusive. The
          daemon builds the image from the tree and reuses it until the Dockerfile or one of its
          declared inputs changes.
        </p>
        <CommandBlock label="Dockerfile check build example">{[
          "defaults:",
          "  build:",
          "    dockerfile: ci/Dockerfile",
          '    context: "."',
          "    inputs:",
          "      - go.mod",
          "      - go.sum",
          "      - web/package-lock.json",
          '    timeout: "30m"'
        ].join("\n")}</CommandBlock>
        <p>
          Only the Dockerfile and listed <InlineCode>inputs</InlineCode> are sent to the build
          context. Include dependency manifests such as <InlineCode>go.mod</InlineCode>,{" "}
          <InlineCode>go.sum</InlineCode>, and <InlineCode>package-lock.json</InlineCode> so
          dependencies are pre-installed in the cached image. To bound disk usage, the daemon keeps
          the two newest images per Dockerfile.
        </p>
      </DocSectionCard>

      <DocSectionCard title="Add services for a check">
        <p>
          Define a top-level <InlineCode>services</InlineCode> map for sidecar containers (such as
          Postgres or Redis), then list service names on any containerized check (
          <InlineCode>image</InlineCode> or <InlineCode>build</InlineCode>). Each service accepts{" "}
          <InlineCode>image</InlineCode>, <InlineCode>env</InlineCode>,{" "}
          <InlineCode>ready</InlineCode>, <InlineCode>ready_timeout</InlineCode>, and{" "}
          <InlineCode>memory</InlineCode>.
        </p>
        <CommandBlock label="check services example">{[
          "services:",
          "  postgres:",
          "    image: postgres:16-alpine",
          "    env: { POSTGRES_HOST_AUTH_METHOD: trust }",
          '    ready: "pg_isready -U postgres -h 127.0.0.1"',
          '    ready_timeout: "60s"',
          '    memory: "1g"',
          "",
          "checks:",
          "  e2e:",
          "    build:",
          "      dockerfile: ci/Dockerfile",
          "      inputs: [go.mod, go.sum]",
          "    services: [postgres]",
          "    env:",
          '      GITSLICE_TEST_DATABASE_URL: "postgres://postgres@postgres:5432/postgres?sslmode=disable"',
          '    run: "go test ./tests/..."'
        ].join("\n")}</CommandBlock>
        <p>
          Every check run gets fresh service containers on a private Docker network and connects to
          each service by its key name (for example, <InlineCode>postgres</InlineCode>). The
          network stays internal unless the check sets <InlineCode>network: true</InlineCode>.
        </p>
        <p>
          Containers run as the daemon&apos;s user, and a check&apos;s{" "}
          <InlineCode>cache</InlineCode> directories live under that daemon&apos;s cache folder so
          they stay writable and easy to prune.
        </p>
      </DocSectionCard>

      <DocSectionCard title="Run a CI daemon on any machine">
        <p>
          A CI-only daemon runs checks and nothing else. It needs only{" "}
          <InlineCode>gs</InlineCode> and — when checks use <InlineCode>image</InlineCode> or{" "}
          <InlineCode>build</InlineCode> — Docker or Podman.
        </p>
        <CommandBlock label="CI daemon installation commands">{[
          "gs auth login",
          "gs agent doctor --ci",
          "gs agent install --ci --name my-ci",
          "gs agent expose my-ci <account>/<slice>",
          "",
          "# A slice owner designates the exposed daemon and can add backups to the pool.",
          "gs slice set-ci-daemon <account>/<slice> <daemon-id>",
          "gs slice set-ci-daemon <account>/<slice> <backup-daemon-id> --add",
          "",
          "# Remove the user service when no longer needed.",
          "gs agent uninstall my-ci"
        ].join("\n")}</CommandBlock>
        <p>
          With backup daemons configured, each run dispatches to the first online daemon in the
          slice&apos;s pool, and runs waiting on a disconnected daemon move automatically to the
          next one. If all daemons are offline, runs stay queued and both the Checks panel and{" "}
          <InlineCode>gs submit</InlineCode> report that checks will begin once a daemon reconnects.
        </p>
        <p>
          An idle CI-only daemon detaches after two minutes and sleeps on a lightweight server wake
          signal (doorbell) that wakes it as soon as a check is queued, polling once an hour as a
          safety net (or every 30 seconds on deployments without wake signals). Pass{" "}
          <InlineCode>--detach-after 0</InlineCode> to keep a persistent connection.
        </p>
      </DocSectionCard>

      <DocSectionCard title="Pass secrets to checks">
        <p>
          Checks that need tokens or keys receive them as environment variables populated from
          account secrets. Account owners and admins attach secrets to the folders whose checks need
          them, and log output masks secret values as <InlineCode>***</InlineCode>. Patchsets opened
          by <InlineCode>contributor</InlineCode>-role members run without secrets by default until
          a slice writer or admin approves them with <InlineCode>gs check allow</InlineCode>.
        </p>
        <CommandBlock label="create secret for checks command">
          {'printf %s "$TOKEN" | gs secret create DEPLOY_TOKEN --path /acme/api --path /acme/worker'}
        </CommandBlock>
        <div className="mt-4">
          <NextDocCard
            description="Folder scoping, precedence, role access, masking, and filling local env files."
            section="secrets"
            title="Secrets"
          />
        </div>
      </DocSectionCard>

      <section className="mt-8 grid gap-4 md:grid-cols-2">
        <NextDocCard
          description="Control when checks run or trigger external webhooks and scripts on folder changes."
          section="triggers"
          title="Triggers"
        />
        <NextDocCard
          description="Configure coding agents with safe contributor roles, MCP, and CI daemons."
          section="agents"
          title="Agents"
        />
      </section>
    </div>
  );
}
