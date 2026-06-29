import { Link, useParams } from "@tanstack/react-router";

type DocSection =
  | "start"
  | "concepts"
  | "agents"
  | "checks"
  | "git-users"
  | "cli";

interface DocParams {
  section?: string;
}

const docSections: Array<{
  description: string;
  id: DocSection;
  title: string;
}> = [
  {
    id: "start",
    title: "Start Here",
    description: "Sign in, open your home slice, make a first changeset."
  },
  {
    id: "concepts",
    title: "Concepts",
    description: "How accounts, slices, workspaces, changesets, and commits fit."
  },
  {
    id: "agents",
    title: "Agents",
    description: "Run your own coding agent and drive it from a slice."
  },
  {
    id: "checks",
    title: "CI Checks",
    description: "Define build/test checks that run on every patchset and gate submit."
  },
  {
    id: "git-users",
    title: "For Git Users",
    description: "Map Git habits to Gitslice without learning internals first."
  },
  {
    id: "cli",
    title: "CLI Reference",
    description: "A compact command lookup for daily `gs` work."
  }
];

const startSteps = [
  {
    title: "Sign in",
    description: "Authenticate once, then use the same identity for CLI and web.",
    command: "gs auth login\ngs auth status"
  },
  {
    title: "Open your home slice",
    description:
      "Your personal account has a `home` slice rooted at /<you>. Use it for a first workflow.",
    command: "gs init <you>:home\n# example: gs init nic:home"
  },
  {
    title: "Make a local edit",
    description: "Edit files normally, then inspect what the workspace will send.",
    command: "gs status\ngs diff"
  },
  {
    title: "Create and submit a changeset",
    description:
      "A changeset is the review and submit unit. When it lands, it is included in the accepted main tree.",
    command: 'gs create --message "my first change"\ngs submit\ngs status'
  }
];

const conceptCards = [
  {
    title: "Accounts and paths",
    description:
      "Every source path starts under a globally unique account slug, such as /nic or /acme. Account kind is metadata, not part of the path."
  },
  {
    title: "Slices",
    description:
      "A slice is a repository-like projection over the global source graph. It defines included paths, visibility, roles, and submit settings."
  },
  {
    title: "Workspaces",
    description:
      "A workspace is bound to exactly one slice and hydrates the files needed for that scope instead of cloning the whole graph."
  },
  {
    title: "Changesets",
    description:
      "A changeset contains one or more patchsets for one authoring slice. Cross-slice changesets are intentionally not supported."
  },
  {
    title: "Submit validation",
    description:
      "Approvals, checks, path locks, and slice rules are enforced server-side before work is included in main."
  },
  {
    title: "Native commits",
    description:
      "Commits exist as immutable accepted snapshots. Users normally create changesets; the system creates a native commit when a changeset lands."
  }
];

const gitTranslations = [
  ["Git repository", "Slice"],
  ["Pull request", "Changeset"],
  ["Commit you make locally", "Patchset/change content in a changeset"],
  ["Merged commit on main", "Native commit created when a changeset lands"],
  ["main branch", "Latest accepted tree at refs/global/main"],
  ["Working tree", "Workspace"],
  ["Clone URL", "Per-slice Git endpoint when Git HTTP is enabled"]
];

const gitQuestions = [
  {
    question: "Do I still make commits?",
    answer:
      "In normal Gitslice work, no. You edit files, create a changeset, and submit it. The system creates the accepted native commit after submit validation passes."
  },
  {
    question: "What replaces a pull request?",
    answer:
      "A changeset. It is scoped to one authoring slice, carries patchsets, and is the unit that submit validation accepts or rejects."
  },
  {
    question: "Where is main?",
    answer:
      "The accepted tree is the native ref refs/global/main. The web UI usually calls it latest or main tree."
  },
  {
    question: "Can one change touch multiple repositories?",
    answer:
      "A changeset can only touch paths included by its authoring slice. If you need broader work, define a slice that includes the intended paths."
  },
  {
    question: "Can I clone with Git?",
    answer:
      "Yes when the deployment enables the Git smart-HTTP gateway. Use the Clone dropdown on a slice page for the concrete URL."
  }
];

const commandGroups = [
  {
    title: "Auth",
    commands: [
      ["gs auth login", "Sign in to an account."],
      ["gs auth status", "Show the active session."],
      ["gs auth logout", "Clear local authentication."],
      ["gs auth token", "Print an auth token for local tooling."]
    ]
  },
  {
    title: "Workspace and source",
    commands: [
      ["gs init <account>:<slice>", "Create a workspace for one slice."],
      ["gs shell", "Open the server-backed file shell."],
      ["gs status", "Show pending workspace edits."],
      ["gs diff", "Inspect pending content changes."],
      ["gs ci", "Run the slice's checks against your pending edits, locally."],
      ["gs log", "Read recent accepted history."],
      ["gs show <commit>", "Inspect an accepted native commit."]
    ]
  },
  {
    title: "File operations",
    commands: [
      ["gs fs ls <path>", "List server files."],
      ["gs fs cat <path>", "Read a server file."],
      ["gs fs upload <local> <path>", "Upload local content into a pending edit."],
      ["gs fs mkdir <path>", "Create a directory."]
    ]
  },
  {
    title: "Changesets",
    commands: [
      ["gs create --message <title>", "Create a changeset from pending edits."],
      ["gs modify", "Create a new patchset on the active changeset."],
      ["gs diff", "Review workspace or changeset content."],
      ["gs submit", "Submit for validation and publish."],
      ["gs deps", "Inspect dependent changesets."]
    ]
  },
  {
    title: "Slices",
    commands: [
      ["gs slice list", "List account slices."],
      ["gs slice create", "Create a slice definition."],
      ["gs slice update", "Change included paths or policy (e.g. --required-check)."],
      ["gs slice set-ci-daemon", "Designate the daemon that runs out-of-slice checks."],
      ["gs slice history", "Inspect slice definition versions."]
    ]
  },
  {
    title: "Agents",
    commands: [
      [
        "gs agent start",
        "Run a local agent daemon in an empty directory (codex runtime)."
      ],
      ["gs agent status", "List your agent daemons and whether they are online."],
      ["gs agent stop", "How to stop a running daemon (Ctrl-C)."],
      [
        "gs cs conversation",
        "Show the agent conversation that produced each patchset."
      ]
    ]
  }
];

function docPath(section: DocSection) {
  return section === "start" ? "/doc" : `/doc/${section}`;
}

function normalizeSection(value: string | undefined): DocSection {
  if (
    value === "concepts" ||
    value === "agents" ||
    value === "checks" ||
    value === "git-users" ||
    value === "cli"
  ) {
    return value;
  }
  return "start";
}

function CommandBlock({ children }: { children: string }) {
  return (
    <pre className="mt-3 overflow-x-auto rounded-md border border-slate-200 bg-slate-50 px-3 py-2 font-mono text-xs leading-5 text-slate-700">
      <code className="whitespace-pre">{children}</code>
    </pre>
  );
}

function SectionLink({
  active,
  section
}: {
  active: boolean;
  section: (typeof docSections)[number];
}) {
  return (
    <Link
      className={[
        "block rounded-md border px-4 py-3 text-left transition active:scale-[0.98]",
        active
          ? "border-zinc-950 bg-zinc-950 text-white"
          : "border-slate-200 bg-white text-slate-700 hover:bg-slate-50 hover:text-zinc-950"
      ].join(" ")}
      to={docPath(section.id)}
    >
      <span className="block text-sm font-semibold">{section.title}</span>
      <span
        className={[
          "mt-1 block text-xs leading-5",
          active ? "text-slate-300" : "text-slate-500"
        ].join(" ")}
      >
        {section.description}
      </span>
    </Link>
  );
}

export function DocPage() {
  const params = useParams({ strict: false }) as DocParams;
  const section = normalizeSection(params.section);

  return (
    <section className="mx-auto grid w-full max-w-[100rem] gap-8 lg:grid-cols-[17rem_minmax(0,1fr)]">
      <aside className="lg:sticky lg:top-6 lg:self-start">
        <div className="grid gap-2">
          {docSections.map((item) => (
            <SectionLink
              active={section === item.id}
              key={item.id}
              section={item}
            />
          ))}
        </div>
      </aside>

      <div className="min-w-0">
        {section === "start" ? <StartHereDoc /> : null}
        {section === "concepts" ? <ConceptsDoc /> : null}
        {section === "agents" ? <AgentsDoc /> : null}
        {section === "checks" ? <ChecksDoc /> : null}
        {section === "git-users" ? <GitUsersDoc /> : null}
        {section === "cli" ? <CliReferenceDoc /> : null}
      </div>
    </section>
  );
}

function PageHeader({
  eyebrow,
  title,
  description
}: {
  description: string;
  eyebrow: string;
  title: string;
}) {
  return (
    <header className="border-b border-slate-200 pb-5">
      <p className="text-xs font-semibold uppercase tracking-normal text-slate-500">
        {eyebrow}
      </p>
      <h1 className="mt-2 text-xl font-semibold tracking-normal text-zinc-950 sm:text-2xl">
        {title}
      </h1>
      <p className="mt-2 max-w-3xl text-sm leading-6 text-slate-600">
        {description}
      </p>
    </header>
  );
}

function StartHereDoc() {
  return (
    <div>
      <PageHeader
        eyebrow="Gitslice docs"
        title="Start Here"
        description="Use this path if you want to get productive before learning the whole model. Gitslice gives you repository-like slices over one global source graph, and changesets are how work lands."
      />

      <section className="mt-8 rounded-md border border-slate-200 bg-white p-5">
        <h2 className="text-base font-semibold text-zinc-950">
          First workflow
        </h2>
        <ol className="mt-4 space-y-5">
          {startSteps.map((step, index) => (
            <li className="flex gap-3 text-sm leading-6" key={step.title}>
              <span className="flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-zinc-950 text-xs font-semibold text-white">
                {index + 1}
              </span>
              <div className="min-w-0 flex-1">
                <h3 className="font-semibold text-zinc-950">{step.title}</h3>
                <p className="mt-1 text-slate-600">{step.description}</p>
                <CommandBlock>{step.command}</CommandBlock>
              </div>
            </li>
          ))}
        </ol>
      </section>

      <section className="mt-8 grid gap-4 md:grid-cols-2">
        <NextDocCard
          description="Learn the native nouns once the first workflow makes sense."
          section="concepts"
          title="Understand the model"
        />
        <NextDocCard
          description="Coming from Git? Start with the translation table."
          section="git-users"
          title="Read the Git guide"
        />
      </section>
    </div>
  );
}

function ConceptsDoc() {
  return (
    <div>
      <PageHeader
        eyebrow="Gitslice docs"
        title="Concepts"
        description="Gitslice is native source storage first, Git compatibility second. These are the terms that explain how the system works."
      />

      <section className="mt-8 grid gap-4 md:grid-cols-2">
        {conceptCards.map((concept) => (
          <article
            className="rounded-md border border-slate-200 bg-white p-5"
            key={concept.title}
          >
            <h2 className="text-base font-semibold text-zinc-950">
              {concept.title}
            </h2>
            <p className="mt-2 text-sm leading-6 text-slate-600">
              {concept.description}
            </p>
          </article>
        ))}
      </section>

      <section className="mt-8 rounded-md border border-slate-200 bg-white p-5">
        <h2 className="text-base font-semibold text-zinc-950">
          Submit lifecycle
        </h2>
        <div className="mt-4">
          <div className="grid grid-cols-1 gap-3 text-sm sm:grid-cols-2 lg:grid-cols-4">
            {[
              ["Draft", "Edit and update patchsets."],
              ["Pending publish", "Submit accepted and queued."],
              ["Submitted", "Included in the accepted main tree."],
              ["Native commit", "Immutable snapshot behind refs/global/main."]
            ].map(([title, description]) => (
              <div
                className="rounded-md border border-slate-200 bg-slate-50 p-4"
                key={title}
              >
                <h3 className="font-semibold text-zinc-950">{title}</h3>
                <p className="mt-2 leading-6 text-slate-600">{description}</p>
              </div>
            ))}
          </div>
        </div>
      </section>
    </div>
  );
}

function AgentsDoc() {
  return (
    <div>
      <PageHeader
        eyebrow="Gitslice docs"
        title="Agents"
        description="Bring your own coding agent. Run an agent daemon on your machine and drive it from a slice's Agents page; each conversation works in its own slice workspace, and its edits land as a normal changeset."
      />

      <section className="mt-8 rounded-md border border-slate-200 bg-white p-5">
        <h2 className="text-base font-semibold text-zinc-950">How it works</h2>
        <ul className="mt-4 space-y-3 text-sm leading-6 text-slate-600">
          <li>
            <span className="font-semibold text-zinc-950">Daemon:</span> you run{" "}
            <code className="rounded bg-slate-50 px-1.5 py-0.5 font-mono text-xs text-slate-700">
              gs agent start
            </code>{" "}
            in an empty directory. It holds one outbound connection to the server,
            so it works from behind a firewall with no inbound ports.
          </li>
          <li>
            <span className="font-semibold text-zinc-950">Runtime:</span> the
            first supported agent is the{" "}
            <code className="rounded bg-slate-50 px-1.5 py-0.5 font-mono text-xs text-slate-700">
              codex
            </code>{" "}
            CLI, which must be installed and on your PATH.
          </li>
          <li>
            <span className="font-semibold text-zinc-950">Conversations:</span>{" "}
            open a slice&apos;s Agents page and start a conversation. Each one runs
            in its own workspace sub-directory bound to that slice, so multiple
            conversations stay isolated.
          </li>
          <li>
            <span className="font-semibold text-zinc-950">Changesets:</span> after
            each turn the agent&apos;s edits are captured as a patchset on a
            changeset for the slice — linked back to the exact conversation that
            produced it.
          </li>
        </ul>
      </section>

      <section className="mt-8 rounded-md border border-slate-200 bg-white p-5">
        <h2 className="text-base font-semibold text-zinc-950">Get started</h2>
        <ol className="mt-4 space-y-5">
          {[
            {
              title: "Start a daemon",
              description:
                "Sign in once, then start the daemon in a fresh, empty directory. It stays online while the process runs.",
              command: "gs auth login\nmkdir my-agent && cd my-agent\ngs agent start"
            },
            {
              title: "Open the Agents page",
              description:
                "On any slice you can write, use the Agents link in the header (next to Changesets and Settings) and start a conversation against your online daemon.",
              command: undefined
            },
            {
              title: "Review the result",
              description:
                "As the agent works, its edits land as patchsets. Open the changeset to see each patchset alongside the conversation that produced it, or use the CLI.",
              command: "gs cs conversation <changeset>"
            }
          ].map((step, index) => (
            <li className="flex gap-3 text-sm leading-6" key={step.title}>
              <span className="flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-zinc-950 text-xs font-semibold text-white">
                {index + 1}
              </span>
              <div className="min-w-0 flex-1">
                <h3 className="font-semibold text-zinc-950">{step.title}</h3>
                <p className="mt-1 text-slate-600">{step.description}</p>
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
          description="Look up the gs agent and conversation commands."
          section="cli"
          title="CLI reference"
        />
        <NextDocCard
          description="Understand slices, workspaces, and changesets first."
          section="concepts"
          title="Core concepts"
        />
      </section>
    </div>
  );
}

function ChecksDoc() {
  return (
    <div>
      <PageHeader
        eyebrow="Gitslice docs"
        title="CI Checks"
        description="Checks are build/test/lint commands defined in your source. They run automatically on every new patchset and can gate submit. No separate CI service to configure — checks run on a machine you control and report back."
      />

      <section className="mt-8 rounded-md border border-slate-200 bg-white p-5">
        <h2 className="text-base font-semibold text-zinc-950">How it works</h2>
        <ul className="mt-4 space-y-3 text-sm leading-6 text-slate-600">
          <li>
            <span className="font-semibold text-zinc-950">Defined in source:</span>{" "}
            checks live in{" "}
            <code className="rounded bg-slate-50 px-1.5 py-0.5 font-mono text-xs text-slate-700">
              .gitslice/checks.yaml
            </code>{" "}
            files committed alongside your code — one per folder. They are
            versioned with the revision they apply to.
          </li>
          <li>
            <span className="font-semibold text-zinc-950">Folder-scoped &amp; cascading:</span>{" "}
            for each path a patchset changes, every{" "}
            <code className="rounded bg-slate-50 px-1.5 py-0.5 font-mono text-xs text-slate-700">
              checks.yaml
            </code>{" "}
            from that folder up to the repository root applies. A check defined in{" "}
            <code className="rounded bg-slate-50 px-1.5 py-0.5 font-mono text-xs text-slate-700">
              backend/
            </code>{" "}
            only runs when something under{" "}
            <code className="rounded bg-slate-50 px-1.5 py-0.5 font-mono text-xs text-slate-700">
              backend/
            </code>{" "}
            changes; an optional{" "}
            <code className="rounded bg-slate-50 px-1.5 py-0.5 font-mono text-xs text-slate-700">
              paths
            </code>{" "}
            filter narrows it further.
          </li>
          <li>
            <span className="font-semibold text-zinc-950">Runs on each patchset:</span>{" "}
            when you capture a patchset (including each agent turn), the applicable
            checks run and their pass/fail is recorded against that patchset. A new
            patchset re-runs them.
          </li>
          <li>
            <span className="font-semibold text-zinc-950">Gates submit:</span> a
            slice can mark checks as <em>required</em>. The changeset cannot submit
            until every required check passes for the current patchset.
          </li>
          <li>
            <span className="font-semibold text-zinc-950">Where they run:</span> in
            the common case checks run on the same machine that authored the change
            (your agent or CLI), in a container when an{" "}
            <code className="rounded bg-slate-50 px-1.5 py-0.5 font-mono text-xs text-slate-700">
              image
            </code>{" "}
            is set, otherwise directly. Checks that need files beyond the slice run
            on the slice&apos;s designated CI daemon.
          </li>
        </ul>
      </section>

      <section className="mt-8 rounded-md border border-slate-200 bg-white p-5">
        <h2 className="text-base font-semibold text-zinc-950">
          The checks.yaml format
        </h2>
        <p className="mt-2 text-sm leading-6 text-slate-600">
          Place this at{" "}
          <code className="rounded bg-slate-50 px-1.5 py-0.5 font-mono text-xs text-slate-700">
            &lt;folder&gt;/.gitslice/checks.yaml
          </code>
          . A check&apos;s id is its folder path plus its name, e.g.{" "}
          <code className="rounded bg-slate-50 px-1.5 py-0.5 font-mono text-xs text-slate-700">
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
        <ul className="mt-4 space-y-2 text-sm leading-6 text-slate-600">
          <li>
            <code className="rounded bg-slate-50 px-1.5 py-0.5 font-mono text-xs text-slate-700">
              run
            </code>{" "}
            (required) — the command, run via{" "}
            <code className="rounded bg-slate-50 px-1.5 py-0.5 font-mono text-xs text-slate-700">
              sh -c
            </code>{" "}
            from the check&apos;s folder. Exit 0 passes.
          </li>
          <li>
            <code className="rounded bg-slate-50 px-1.5 py-0.5 font-mono text-xs text-slate-700">
              paths
            </code>{" "}
            — globs (folder-relative, or{" "}
            <code className="rounded bg-slate-50 px-1.5 py-0.5 font-mono text-xs text-slate-700">
              /
            </code>
            -absolute); the check is skipped if none match the change.
          </li>
          <li>
            <code className="rounded bg-slate-50 px-1.5 py-0.5 font-mono text-xs text-slate-700">
              image
            </code>
            ,{" "}
            <code className="rounded bg-slate-50 px-1.5 py-0.5 font-mono text-xs text-slate-700">
              env
            </code>
            ,{" "}
            <code className="rounded bg-slate-50 px-1.5 py-0.5 font-mono text-xs text-slate-700">
              network
            </code>
            ,{" "}
            <code className="rounded bg-slate-50 px-1.5 py-0.5 font-mono text-xs text-slate-700">
              timeout
            </code>
            ,{" "}
            <code className="rounded bg-slate-50 px-1.5 py-0.5 font-mono text-xs text-slate-700">
              working_dir
            </code>
            ,{" "}
            <code className="rounded bg-slate-50 px-1.5 py-0.5 font-mono text-xs text-slate-700">
              include
            </code>{" "}
            — all optional per check (or under{" "}
            <code className="rounded bg-slate-50 px-1.5 py-0.5 font-mono text-xs text-slate-700">
              defaults
            </code>
            ).
          </li>
          <li>
            <code className="rounded bg-slate-50 px-1.5 py-0.5 font-mono text-xs text-slate-700">
              setup
            </code>{" "}
            — commands run once to prepare the container image (install
            toolchains/system packages). The result is baked into a cached image
            and reused on later runs, so setup does not repeat. Keep it
            change-independent (no workspace files); use a dependency cache for
            per-change caches like{" "}
            <code className="rounded bg-slate-50 px-1.5 py-0.5 font-mono text-xs text-slate-700">
              go mod
            </code>{" "}
            or{" "}
            <code className="rounded bg-slate-50 px-1.5 py-0.5 font-mono text-xs text-slate-700">
              npm
            </code>
            .
          </li>
        </ul>
        <p className="mt-4 text-sm leading-6 text-slate-600">
          Run the applicable checks for your current workspace at any time with{" "}
          <code className="rounded bg-slate-50 px-1.5 py-0.5 font-mono text-xs text-slate-700">
            gs ci
          </code>{" "}
          — it runs the same set locally (with setup/run timing) and reports
          pass/fail without capturing or submitting.
        </p>
      </section>

      <section className="mt-8 rounded-md border border-slate-200 bg-white p-5">
        <h2 className="text-base font-semibold text-zinc-950">Add checks</h2>
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
                "Checks that need files outside the slice (e.g. a repo-root check) run on a daemon you designate for the slice, in Settings or via the CLI.",
              command: "gs slice set-ci-daemon <account>:<slice> <daemon-id>"
            }
          ].map((step, index) => (
            <li className="flex gap-3 text-sm leading-6" key={step.title}>
              <span className="flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-zinc-950 text-xs font-semibold text-white">
                {index + 1}
              </span>
              <div className="min-w-0 flex-1">
                <h3 className="font-semibold text-zinc-950">{step.title}</h3>
                <p className="mt-1 text-slate-600">{step.description}</p>
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

function GitUsersDoc() {
  return (
    <div>
      <PageHeader
        eyebrow="Gitslice docs"
        title="For Git Users"
        description="Use this page if your mental model starts with repos, branches, commits, and pull requests. Gitslice keeps familiar boundaries but changes the native write path."
      />

      <section className="mt-8 rounded-md border border-slate-200 bg-white p-5">
        <h2 className="text-base font-semibold text-zinc-950">
          Git to Gitslice
        </h2>
        <div className="mt-4 overflow-x-auto">
          <table className="min-w-full divide-y divide-slate-200 text-left text-sm">
            <thead className="bg-slate-50 text-xs font-semibold uppercase tracking-normal text-slate-500">
              <tr>
                <th className="px-4 py-3">Git term</th>
                <th className="px-4 py-3">Gitslice term</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100">
              {gitTranslations.map(([git, gitslice]) => (
                <tr key={git}>
                  <td className="px-4 py-3 font-medium text-zinc-950">{git}</td>
                  <td className="px-4 py-3 text-slate-600">{gitslice}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>

      <section className="mt-8 rounded-md border border-slate-200 bg-white p-5">
        <h2 className="text-base font-semibold text-zinc-950">
          Familiar workflow, native submit
        </h2>
        <CommandBlock>{`gs auth login
gs init <account>:<slice>
# edit files
gs status
gs create --message "change title"
gs submit`}</CommandBlock>
        <p className="mt-3 text-sm leading-6 text-slate-600">
          The important shift: you do not need to create a local commit before
          review. A changeset is the review unit, and submit creates the accepted
          native snapshot.
        </p>
      </section>

      <section className="mt-8 grid gap-4 md:grid-cols-2">
        {gitQuestions.map((item) => (
          <article
            className="rounded-md border border-slate-200 bg-white p-5"
            key={item.question}
          >
            <h2 className="text-base font-semibold text-zinc-950">
              {item.question}
            </h2>
            <p className="mt-2 text-sm leading-6 text-slate-600">
              {item.answer}
            </p>
          </article>
        ))}
      </section>
    </div>
  );
}

function CliReferenceDoc() {
  return (
    <div>
      <PageHeader
        eyebrow="Gitslice docs"
        title="CLI Reference"
        description="A compact lookup for the commands used in normal Gitslice work. Use Start Here for the walkthrough."
      />

      <section className="mt-8 rounded-md border border-slate-200 bg-white p-5">
        <h2 className="text-base font-semibold text-zinc-950">
          Install the gs CLI
        </h2>
        <p className="mt-2 text-sm leading-6 text-slate-600">
          The CLI is a single Go binary named{" "}
          <code className="rounded bg-slate-50 px-1.5 py-0.5 font-mono text-xs text-slate-700">
            gs
          </code>
          . Installing it requires Go 1.24 or newer.
        </p>

        <h3 className="mt-5 text-sm font-semibold text-zinc-950">
          Option A — go install (quickest)
        </h3>
        <CommandBlock>{`go install github.com/gitslice-io/gitslice/cmd/gs@latest`}</CommandBlock>
        <p className="mt-2 text-sm leading-6 text-slate-600">
          Make sure your Go bin directory is on{" "}
          <code className="rounded bg-slate-50 px-1.5 py-0.5 font-mono text-xs text-slate-700">
            PATH
          </code>
          :
        </p>
        <CommandBlock>{`export PATH="$PATH:$(go env GOPATH)/bin"`}</CommandBlock>

        <h3 className="mt-5 text-sm font-semibold text-zinc-950">
          Option B — build from source
        </h3>
        <CommandBlock>{`git clone https://github.com/gitslice-io/gitslice.git
cd gitslice
make install   # builds gs (and gitslice-server) into your Go bin`}</CommandBlock>

        <h3 className="mt-5 text-sm font-semibold text-zinc-950">Verify</h3>
        <p className="mt-2 text-sm leading-6 text-slate-600">
          <code className="rounded bg-slate-50 px-1.5 py-0.5 font-mono text-xs text-slate-700">
            gs
          </code>{" "}
          defaults to the hosted endpoint, so you can sign in right away. Point
          at a different server with{" "}
          <code className="rounded bg-slate-50 px-1.5 py-0.5 font-mono text-xs text-slate-700">
            --server
          </code>{" "}
          or{" "}
          <code className="rounded bg-slate-50 px-1.5 py-0.5 font-mono text-xs text-slate-700">
            GS_SERVER_ADDR
          </code>
          .
        </p>
        <CommandBlock>{`gs version
gs auth login
gs auth status`}</CommandBlock>
      </section>

      <section className="mt-8 grid gap-4 md:grid-cols-2">
        {commandGroups.map((group) => (
          <article
            className="rounded-md border border-slate-200 bg-white p-5"
            key={group.title}
          >
            <h2 className="text-base font-semibold text-zinc-950">
              {group.title}
            </h2>
            <dl className="mt-4 space-y-3">
              {group.commands.map(([command, description]) => (
                <div key={command}>
                  <dt>
                    <code className="rounded-md bg-slate-50 px-2 py-1 font-mono text-xs text-slate-700">
                      {command}
                    </code>
                  </dt>
                  <dd className="mt-1 text-sm leading-6 text-slate-600">
                    {description}
                  </dd>
                </div>
              ))}
            </dl>
          </article>
        ))}
      </section>
    </div>
  );
}

function NextDocCard({
  description,
  section,
  title
}: {
  description: string;
  section: DocSection;
  title: string;
}) {
  return (
    <Link
      className="rounded-md border border-slate-200 bg-white p-5 transition hover:bg-slate-50 active:scale-[0.98]"
      to={docPath(section)}
    >
      <h2 className="text-base font-semibold text-zinc-950">{title}</h2>
      <p className="mt-2 text-sm leading-6 text-slate-600">{description}</p>
    </Link>
  );
}
