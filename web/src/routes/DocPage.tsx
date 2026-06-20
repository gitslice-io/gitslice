import { Link, useParams } from "@tanstack/react-router";

import {
  Badge,
  Card,
  PageHeader,
  Surface,
  buttonClassName
} from "../components/ui";
import { cn } from "../lib/cn";

type DocSection = "start" | "concepts" | "git-users" | "cli";

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
      ["gs slice update", "Change included paths or policy."],
      ["gs slice history", "Inspect slice definition versions."]
    ]
  }
];

function docPath(section: DocSection) {
  return section === "start" ? "/doc" : `/doc/${section}`;
}

function normalizeSection(value: string | undefined): DocSection {
  if (value === "concepts" || value === "git-users" || value === "cli") {
    return value;
  }
  return "start";
}

function InlineCode({ children }: { children: string }) {
  return (
    <code className="rounded-sm bg-surface-container-high px-1.5 py-0.5 font-mono text-xs text-on-surface">
      {children}
    </code>
  );
}

function CommandBlock({ children }: { children: string }) {
  return (
    <pre className="mt-3 overflow-x-auto rounded-sm bg-surface-container-high px-3 py-2 font-mono text-xs leading-5 text-on-surface">
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
      className={cn(
        "block rounded-sm px-4 py-3 text-left transition duration-200 ease-out active:translate-y-px",
        active
          ? "bg-primary text-white"
          : "bg-surface-container-lowest text-on-surface-variant hover:bg-surface-container-high hover:text-primary"
      )}
      to={docPath(section.id)}
    >
      <span className="block font-label text-sm font-semibold">
        {section.title}
      </span>
      <span
        className={cn(
          "mt-1 block text-xs leading-5",
          active ? "text-white/80" : "text-on-surface-muted"
        )}
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
    <section className="mx-auto grid w-full max-w-[96rem] gap-8 text-on-surface lg:grid-cols-[18rem_minmax(0,1fr)]">
      <Surface
        as="aside"
        className="p-3 lg:sticky lg:top-24 lg:self-start"
        level="low"
      >
        <div className="mb-3 px-1">
          <Badge size="md" variant="tertiary">
            Docs
          </Badge>
        </div>
        <nav aria-label="Documentation sections" className="grid gap-2">
          {docSections.map((item) => (
            <SectionLink
              active={section === item.id}
              key={item.id}
              section={item}
            />
          ))}
        </nav>
      </Surface>

      <article className="min-w-0">
        {section === "start" ? <StartHereDoc /> : null}
        {section === "concepts" ? <ConceptsDoc /> : null}
        {section === "git-users" ? <GitUsersDoc /> : null}
        {section === "cli" ? <CliReferenceDoc /> : null}
      </article>
    </section>
  );
}

function StartHereDoc() {
  return (
    <div className="space-y-8">
      <PageHeader
        eyebrow="Gitslice docs"
        title="Start Here"
        description="Use this path if you want to get productive before learning the whole model. Gitslice gives you repository-like slices over one global source graph, and changesets are how work lands."
      />

      <Card as="section" level="low" padding="lg">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <h2 className="text-xl font-semibold">First workflow</h2>
          <Badge size="md" variant="tertiary">
            4 steps
          </Badge>
        </div>
        <ol className="mt-6 space-y-4">
          {startSteps.map((step, index) => (
            <li
              className="grid gap-3 rounded-sm bg-surface-container-lowest p-4 text-sm leading-6 sm:grid-cols-[2rem_minmax(0,1fr)]"
              key={step.title}
            >
              <Badge
                className="h-8 w-8 justify-center rounded-full px-0 py-0"
                variant="tertiary"
              >
                {index + 1}
              </Badge>
              <div className="min-w-0">
                <h3 className="text-base font-semibold">{step.title}</h3>
                <p className="mt-1 text-on-surface-variant">
                  {step.description}
                </p>
                <CommandBlock>{step.command}</CommandBlock>
              </div>
            </li>
          ))}
        </ol>
      </Card>

      <section className="grid gap-4 md:grid-cols-2">
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
    <div className="space-y-8">
      <PageHeader
        eyebrow="Gitslice docs"
        title="Concepts"
        description="Gitslice is native source storage first, Git compatibility second. These are the terms that explain how the system works."
      />

      <section className="grid gap-4 md:grid-cols-2">
        {conceptCards.map((concept) => (
          <Card as="article" key={concept.title} level="low" padding="lg">
            <h2 className="text-xl font-semibold">{concept.title}</h2>
            <p className="mt-3 text-sm leading-6 text-on-surface-variant">
              {concept.description}
            </p>
          </Card>
        ))}
      </section>

      <Card as="section" level="base" padding="lg">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <h2 className="text-xl font-semibold">Submit lifecycle</h2>
          <Badge size="md" variant="tertiary">
            Validation
          </Badge>
        </div>
        <div className="mt-5 grid grid-cols-1 gap-3 text-sm sm:grid-cols-2 lg:grid-cols-4">
          {[
            ["Draft", "Edit and update patchsets."],
            ["Pending publish", "Submit accepted and queued."],
            ["Submitted", "Included in the accepted main tree."],
            ["Native commit", "Immutable snapshot behind refs/global/main."]
          ].map(([title, description]) => (
            <div
              className="rounded-sm bg-surface-container-lowest p-4"
              key={title}
            >
              <h3 className="font-semibold">{title}</h3>
              <p className="mt-2 leading-6 text-on-surface-variant">
                {description}
              </p>
            </div>
          ))}
        </div>
      </Card>
    </div>
  );
}

function GitUsersDoc() {
  return (
    <div className="space-y-8">
      <PageHeader
        eyebrow="Gitslice docs"
        title="For Git Users"
        description="Use this page if your mental model starts with repos, branches, commits, and pull requests. Gitslice keeps familiar boundaries but changes the native write path."
      />

      <Card as="section" level="low" padding="lg">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <h2 className="text-xl font-semibold">Git to Gitslice</h2>
          <Badge size="md" variant="tertiary">
            Translation
          </Badge>
        </div>
        <div className="mt-4 overflow-x-auto">
          <div className="min-w-[34rem] text-sm">
            <div className="grid grid-cols-2 px-4 py-2 font-label text-xs font-semibold uppercase text-tertiary">
              <span>Git term</span>
              <span>Gitslice term</span>
            </div>
            <div className="grid gap-2">
              {gitTranslations.map(([git, gitslice]) => (
                <div
                  className="grid grid-cols-2 rounded-sm bg-surface-container-lowest"
                  key={git}
                >
                  <div className="px-4 py-3 font-medium text-on-surface">
                    {git}
                  </div>
                  <div className="px-4 py-3 text-on-surface-variant">
                    {gitslice}
                  </div>
                </div>
              ))}
            </div>
          </div>
        </div>
      </Card>

      <Card as="section" level="base" padding="lg">
        <h2 className="text-xl font-semibold">
          Familiar workflow, native submit
        </h2>
        <CommandBlock>{`gs auth login
gs init <account>:<slice>
# edit files
gs status
gs create --message "change title"
gs submit`}</CommandBlock>
        <p className="mt-4 text-sm leading-6 text-on-surface-variant">
          The important shift: you do not need to create a local commit before
          review. A changeset is the review unit, and submit creates the accepted
          native snapshot.
        </p>
      </Card>

      <section className="grid gap-4 md:grid-cols-2">
        {gitQuestions.map((item) => (
          <Card as="article" key={item.question} level="low" padding="lg">
            <h2 className="text-xl font-semibold">{item.question}</h2>
            <p className="mt-3 text-sm leading-6 text-on-surface-variant">
              {item.answer}
            </p>
          </Card>
        ))}
      </section>
    </div>
  );
}

function CliReferenceDoc() {
  return (
    <div className="space-y-8">
      <PageHeader
        eyebrow="Gitslice docs"
        title="CLI Reference"
        description="A compact lookup for the commands used in normal Gitslice work. Use Start Here for the walkthrough."
      />

      <Card as="section" level="low" padding="lg">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <h2 className="text-xl font-semibold">Install the gs CLI</h2>
          <Badge size="md" variant="tertiary">
            Setup
          </Badge>
        </div>
        <p className="mt-3 text-sm leading-6 text-on-surface-variant">
          The CLI is a single Go binary named <InlineCode>gs</InlineCode>.
          Installing it requires Go 1.24 or newer.
        </p>

        <h3 className="mt-6 text-base font-semibold">
          Option A - go install (quickest)
        </h3>
        <CommandBlock>{`go install github.com/gitslice-io/gitslice/cmd/gs@latest`}</CommandBlock>
        <p className="mt-3 text-sm leading-6 text-on-surface-variant">
          Make sure your Go bin directory is on <InlineCode>PATH</InlineCode>:
        </p>
        <CommandBlock>{`export PATH="$PATH:$(go env GOPATH)/bin"`}</CommandBlock>

        <h3 className="mt-6 text-base font-semibold">
          Option B - build from source
        </h3>
        <CommandBlock>{`git clone https://github.com/gitslice-io/gitslice.git
cd gitslice
make install   # builds gs (and gitslice-server) into your Go bin`}</CommandBlock>

        <h3 className="mt-6 text-base font-semibold">Verify</h3>
        <p className="mt-3 text-sm leading-6 text-on-surface-variant">
          <InlineCode>gs</InlineCode> defaults to the hosted endpoint, so you
          can sign in right away. Point at a different server with{" "}
          <InlineCode>--server</InlineCode> or{" "}
          <InlineCode>GS_SERVER_ADDR</InlineCode>.
        </p>
        <CommandBlock>{`gs version
gs auth login
gs auth status`}</CommandBlock>
      </Card>

      <section className="grid gap-4 md:grid-cols-2">
        {commandGroups.map((group) => (
          <Card as="article" key={group.title} level="low" padding="lg">
            <h2 className="text-xl font-semibold">{group.title}</h2>
            <dl className="mt-4 space-y-4">
              {group.commands.map(([command, description]) => (
                <div key={command}>
                  <dt>
                    <code className="rounded-sm bg-surface-container-high px-2 py-1 font-mono text-xs text-on-surface">
                      {command}
                    </code>
                  </dt>
                  <dd className="mt-2 text-sm leading-6 text-on-surface-variant">
                    {description}
                  </dd>
                </div>
              ))}
            </dl>
          </Card>
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
      className="group block rounded-sm bg-surface-container-low p-5 text-on-surface transition duration-200 ease-out hover:bg-surface-container-high active:translate-y-px"
      to={docPath(section)}
    >
      <h2 className="text-xl font-semibold">{title}</h2>
      <p className="mt-3 text-sm leading-6 text-on-surface-variant">
        {description}
      </p>
      <span
        className={buttonClassName({
          className: "mt-4 px-0",
          size: "sm",
          variant: "tertiary"
        })}
      >
        Open section
      </span>
    </Link>
  );
}
