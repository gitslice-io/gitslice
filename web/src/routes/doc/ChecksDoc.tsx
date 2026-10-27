import { PageHeader, CommandBlock, NextDocCard } from "./DocParts";

export function ChecksDoc() {
  return (
    <div>
      <PageHeader
        eyebrow="Gitslice docs"
        title="CI Checks"
        description="Checks are build/test/lint commands defined in your source. They run automatically on every new patchset and can gate submit. No separate CI service to configure — checks run on a machine you control and report back."
      />

      <section className="mt-8 rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-5">
        <h2 className="text-base font-semibold text-zinc-950 dark:text-zinc-50">How it works</h2>
        <ul className="mt-4 space-y-3 text-sm leading-6 text-slate-600 dark:text-zinc-400">
          <li>
            <span className="font-semibold text-zinc-950 dark:text-zinc-50">Defined in source:</span>{" "}
            checks live in{" "}
            <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">
              .gitslice/checks.yaml
            </code>{" "}
            files committed alongside your code — one per folder. They are
            versioned with the revision they apply to.
          </li>
          <li>
            <span className="font-semibold text-zinc-950 dark:text-zinc-50">Folder-scoped &amp; cascading:</span>{" "}
            for each path a patchset changes, every{" "}
            <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">
              checks.yaml
            </code>{" "}
            from that folder up to the repository root applies. A check defined in{" "}
            <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">
              backend/
            </code>{" "}
            only runs when something under{" "}
            <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">
              backend/
            </code>{" "}
            changes; an optional{" "}
            <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">
              paths
            </code>{" "}
            filter narrows it further.
          </li>
          <li>
            <span className="font-semibold text-zinc-950 dark:text-zinc-50">Runs on each patchset:</span>{" "}
            when you capture a patchset (including each agent turn), the applicable
            checks run and their pass/fail is recorded against that patchset. A new
            patchset re-runs them.
          </li>
          <li>
            <span className="font-semibold text-zinc-950 dark:text-zinc-50">Gates submit:</span> a
            slice can mark checks as <em>required</em>. The changeset cannot submit
            until every required check passes for the current patchset.
          </li>
          <li>
            <span className="font-semibold text-zinc-950 dark:text-zinc-50">Where they run:</span> in
            the common case checks run on the same machine that authored the change
            (your agent or CLI), in a container when an{" "}
            <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">
              image
            </code>{" "}
            is set, otherwise directly. Checks that need files beyond the slice run
            on the slice&apos;s designated CI daemon.
          </li>
        </ul>
      </section>

      <section className="mt-8 rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-5">
        <h2 className="text-base font-semibold text-zinc-950 dark:text-zinc-50">
          The checks.yaml format
        </h2>
        <p className="mt-2 text-sm leading-6 text-slate-600 dark:text-zinc-400">
          Place this at{" "}
          <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">
            &lt;folder&gt;/.gitslice/checks.yaml
          </code>
          . A check&apos;s id is its folder path plus its name, e.g.{" "}
          <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">
            backend/test
          </code>
          .
        </p>
        <CommandBlock>{`version: 1

# optional defaults applied to every check in this file
defaults:
  image: "golang:1.22"     # run in this container; omit to run on the host
  timeout: "10m"
  setup:                   # baked once into a cached image, reused across runs
    - "apt-get update && apt-get install -y protobuf-compiler"

checks:
  test:
    run: "go test ./..."   # required: the shell command
    paths: ["**/*.go"]     # optional: only run when these change
  lint:
    run: "golangci-lint run"
  smoke:
    run: "./scripts/smoke.sh"
    include: ["/go.mod", "/go.sum"]  # extra paths to make available
    network: true                    # allow network (default: denied)`}</CommandBlock>
        <ul className="mt-4 space-y-2 text-sm leading-6 text-slate-600 dark:text-zinc-400">
          <li>
            <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">
              run
            </code>{" "}
            (required) — the command, run via{" "}
            <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">
              sh -c
            </code>{" "}
            from the check&apos;s folder. Exit 0 passes.
          </li>
          <li>
            <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">
              paths
            </code>{" "}
            — globs (folder-relative, or{" "}
            <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">
              /
            </code>
            -absolute); the check is skipped if none match the change.
          </li>
          <li>
            <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">
              image
            </code>
            ,{" "}
            <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">
              env
            </code>
            ,{" "}
            <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">
              network
            </code>
            ,{" "}
            <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">
              timeout
            </code>
            ,{" "}
            <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">
              working_dir
            </code>
            ,{" "}
            <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">
              include
            </code>{" "}
            — all optional per check (or under{" "}
            <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">
              defaults
            </code>
            ).
          </li>
          <li>
            <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">
              setup
            </code>{" "}
            — commands run once to prepare the container image (install
            toolchains/system packages). The result is baked into a cached image
            and reused on later runs, so setup does not repeat. Keep it
            change-independent (no workspace files); use a dependency cache for
            per-change caches like{" "}
            <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">
              go mod
            </code>{" "}
            or{" "}
            <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">
              npm
            </code>
            .
          </li>
        </ul>
        <p className="mt-4 text-sm leading-6 text-slate-600 dark:text-zinc-400">
          Run the applicable checks for your current workspace at any time with{" "}
          <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">
            gs ci
          </code>{" "}
          — it runs the same set locally (with setup/run timing) and reports
          pass/fail without capturing or submitting.
        </p>
      </section>

      <section className="mt-8 rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-5">
        <h2 className="text-base font-semibold text-zinc-950 dark:text-zinc-50">
          Build the check image from your tree
        </h2>
        <p className="mt-2 text-sm leading-6 text-slate-600 dark:text-zinc-400">
          Use <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">build</code>{" "}
          instead of <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">image</code>{" "}
          when the environment belongs in the revision. They are mutually
          exclusive. The daemon builds the image from the tree and reuses it
          until the Dockerfile or an input changes.
        </p>
        <CommandBlock>{[
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
        <p className="mt-4 text-sm leading-6 text-slate-600 dark:text-zinc-400">
          Only the Dockerfile and listed <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">inputs</code>{" "}
          are sent to the build. Include dependency manifests such as{" "}
          <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">go.mod</code>,{" "}
          <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">go.sum</code>,
          and <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">package-lock.json</code>{" "}
          there, then install dependencies in the image so later runs reuse
          them. To bound disk use, the daemon keeps the two newest images for
          each Dockerfile.
        </p>
      </section>

      <section className="mt-8 rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-5">
        <h2 className="text-base font-semibold text-zinc-950 dark:text-zinc-50">
          Add services for a check
        </h2>
        <p className="mt-2 text-sm leading-6 text-slate-600 dark:text-zinc-400">
          Define a top-level <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">services</code>{" "}
          map, then list service names on a check. A service accepts{" "}
          <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">image</code>,{" "}
          <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">env</code>,{" "}
          <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">ready</code>,{" "}
          <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">ready_timeout</code>,
          and <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">memory</code>.
        </p>
        <CommandBlock>{[
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
        <p className="mt-4 text-sm leading-6 text-slate-600 dark:text-zinc-400">
          Every run gets fresh service containers on a private network and
          reaches each service by its name (for example, <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">postgres</code>). The network is
          internal unless the check sets <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">network: true</code>.
          Services need a containerized check, using <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">image</code>{" "}
          or <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">build</code>.
        </p>
        <p className="mt-3 text-sm leading-6 text-slate-600 dark:text-zinc-400">
          Containers run as the daemon&apos;s user. A check&apos;s <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">cache</code>{" "}
          paths are folders under that daemon&apos;s cache directory, so they are
          writable and can be cleaned up there.
        </p>
      </section>

      <section className="mt-8 rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-5">
        <h2 className="text-base font-semibold text-zinc-950 dark:text-zinc-50">
          Run a CI daemon on any machine
        </h2>
        <p className="mt-2 text-sm leading-6 text-slate-600 dark:text-zinc-400">
          A CI-only daemon runs checks and nothing else.
          It needs only <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">gs</code>{" "}
          and, when checks use <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">image</code>{" "}
          or <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">build</code>, Docker or Podman.
        </p>
        <CommandBlock>{[
          "gs auth login",
          "gs agent doctor --ci",
          "gs agent install --ci --name my-ci",
          "gs agent expose my-ci <account>:<slice>",
          "",
          "# A slice owner chooses the exposed daemon.",
          "gs slice set-ci-daemon <account>:<slice> <daemon-id>",
          "",
          "# Remove the user service when it is no longer needed.",
          "gs agent uninstall my-ci"
        ].join("\n")}</CommandBlock>
        <p className="mt-4 text-sm leading-6 text-slate-600 dark:text-zinc-400">
          When this daemon is offline, its runs remain queued. The Checks panel
          and <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">gs submit</code>{" "}
          say that the daemon is offline and checks begin when it reconnects.
        </p>
        <p className="mt-3 text-sm leading-6 text-slate-600 dark:text-zinc-400">
          A checks-only daemon with nothing to run detaches from the server
          after two minutes and polls every 30 seconds for queued checks, so an
          idle daemon costs the server almost nothing. It stays online, and a
          queued check may start up to about 30 seconds later.{" "}
          <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">--detach-after 0</code>{" "}
          keeps it connected.
        </p>
      </section>

      <section className="mt-8 rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-5">
        <h2 className="text-base font-semibold text-zinc-950 dark:text-zinc-50">Pass secrets to checks</h2>
        <p className="mt-2 text-sm leading-6 text-slate-600 dark:text-zinc-400">
          A secret is a value Gitslice keeps encrypted and gives to a check as an
          environment variable named after it. It belongs to an account and
          reaches the slices it is scoped to, or all of the account&apos;s slices.
          The account&apos;s owners and admins manage secrets with 
          <code className="rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300">gs secret</code> 
          or on the account&apos;s Secrets page. A value is never shown again once
          it is set.
        </p>
        <CommandBlock>{[
          "printf %s \"$TOKEN\" | gs secret create DEPLOY_TOKEN --slice api --slice worker",
          "printf %s \"$TOKEN\" | gs secret create NPM_TOKEN --all-slices",
          "gs secret ls",
          "gs secret update <secret-id> --add-slice web",
          "gs secret rm <secret-id>"
        ].join("\n")}</CommandBlock>
        <p className="mt-4 text-sm leading-6 text-slate-600 dark:text-zinc-400">
          A slice gets at most one secret of each name: a secret that lists the
          slice overrides an all-slices one there. Values of 4 or more
          characters are shown as *** in check logs and summaries. Masking does
          not stop a check that sets out to leak a secret: every writer of a
          slice can read its secrets from a check, and so can its CI daemon, so
          widen a scope with care.
        </p>
      </section>

      <section className="mt-8 rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-5">
        <h2 className="text-base font-semibold text-zinc-950 dark:text-zinc-50">Add checks</h2>
        <ol className="mt-4 space-y-5">
          {[
            {
              title: "Commit a checks.yaml",
              description:
                "Add .gitslice/checks.yaml at the folder you want to guard, with one or more checks. It is a normal source file in your slice.",
              command: undefined
            },
            {
              title: "Mark the ones that must pass",
              description:
                "Make a check required so it gates submit. Reference it by its qualified id (folder path + name).",
              command:
                "gs slice update <account>:<slice> --required-check backend/test"
            },
            {
              title: "Capture and watch",
              description:
                "Capture a patchset (or let your agent do it). Applicable checks run and show on the changeset's Checks panel; required failures block submit until fixed.",
              command: "gs submit   # blocked until required checks pass"
            },
            {
              title: "Optional: a designated CI runner",
              description:
                "Checks that need files outside the slice (e.g. a repo-root check) run on a daemon you designate for the slice, in Settings or via the CLI. You can designate daemons you started, those of agents you own, and daemons whose owners exposed them to the slice.",
              command: "gs slice set-ci-daemon <account>:<slice> <daemon-id>"
            }
          ].map((step, index) => (
            <li className="flex gap-3 text-sm leading-6" key={step.title}>
              <span className="flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-zinc-950 dark:bg-zinc-100 text-xs font-semibold text-white dark:text-zinc-950">
                {index + 1}
              </span>
              <div className="min-w-0 flex-1">
                <h3 className="font-semibold text-zinc-950 dark:text-zinc-50">{step.title}</h3>
                <p className="mt-1 text-slate-600 dark:text-zinc-400">{step.description}</p>
                {step.command ? (
                  <CommandBlock>{step.command}</CommandBlock>
                ) : null}
              </div>
            </li>
          ))}
        </ol>
      </section>

      <section className="mt-8 grid gap-4 md:grid-cols-2">
        <NextDocCard
          description="See how changesets, patchsets, and submit validation fit together."
          section="concepts"
          title="Understand the model"
        />
        <NextDocCard
          description="Let an agent author changes — checks run on each of its turns."
          section="agents"
          title="Run an agent"
        />
      </section>
    </div>
  );
}
