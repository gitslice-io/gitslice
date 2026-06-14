const concepts = [
  {
    title: "Accounts & paths",
    description:
      "Every source path is rooted under an account slug: /{account}/... (e.g. /nicholas, /acme). Account kind (user / org / service) is just metadata; there are no /users or /orgs prefixes.",
  },
  {
    title: "Slices",
    description:
      "A slice is a repository-like projection of the global source graph defined by its included paths, a product view rather than a storage shard. Slices carry visibility (public / account / private) and roles (reader / writer / admin / owner).",
  },
  {
    title: "Workspaces",
    description:
      "A virtual workspace hydrates only the files you, or an agent, actually need from a slice, instead of cloning everything.",
  },
  {
    title: "Changesets",
    description:
      "The unit of review and submission. A changeset has exactly one authoring slice; cross-slice changesets are not supported. Every changed path must be included by the authoring slice.",
  },
  {
    title: "Submit validation",
    description:
      "Submit requirements (required approvals, required checks, path locks) are explicit slice settings enforced server-side. Caches and watchers speed things up, but the server decides correctness.",
  },
];

const quickstartSteps = [
  {
    title: "Sign in",
    description:
      "Use an existing account, or create a new one, then check the active session.",
    command:
      "gs auth login\n# or: gs auth signup --username <you>\ngs auth status",
  },
  {
    title: "Open a shell / workspace",
    description:
      "Create a workspace for a slice, then open an interactive session.",
    command: "gs init <account>/<slice>\n# example: gs init nic/home\ngs shell",
  },
  {
    title: "Add or edit files",
    description:
      "Work locally, upload existing files, and inspect pending edits before review.",
    command: "gs fs upload ./notes /nic/notes --recursive\ngs status\ngs diff",
  },
  {
    title: "Create a changeset",
    description:
      "Package the pending work into the review and submission unit.",
    command: 'gs cs create --title "update notes"\ngs cs diff',
  },
  {
    title: "Submit",
    description:
      "The server runs the slice's submit validation before the change lands on the global graph.",
    command: "gs cs submit",
  },
];

const commandGroups = [
  {
    title: "Auth",
    commands: [
      ["gs auth login", "Sign in to an account."],
      ["gs auth status", "Show the active session."],
      ["gs auth logout", "Clear local authentication."],
      ["gs auth token", "Print an auth token for local tooling."],
    ],
  },
  {
    title: "Workspace & browsing",
    commands: [
      ["gs init <slice>", "Create a workspace for a slice."],
      ["gs status", "Show pending workspace edits."],
      ["gs diff", "Inspect pending content changes."],
      ["gs log", "Read recent history."],
      ["gs show <commit>", "Inspect a specific commit."],
      ["gs fs ...", "Run file operations."],
      ["gs browse", "Open the web companion."],
    ],
  },
  {
    title: "Changesets",
    commands: [
      ["gs cs create", "Create a changeset from pending edits."],
      ["gs cs diff", "Review changeset content."],
      ["gs cs submit", "Submit a changeset for server validation."],
    ],
  },
  {
    title: "Slices",
    commands: [
      ["gs slice ...", "Manage slice definitions, visibility, and roles."],
    ],
  },
];

const exampleCommands = `gs auth signup --username nic
gs init nic/home
gs fs upload ./notes /nic/notes --recursive
gs status
gs cs create --title "update notes"
gs cs diff
gs cs submit`;

function CommandBlock({ children }: { children: string }) {
  return (
    <pre className="mt-3 overflow-x-auto rounded-md bg-slate-50 px-3 py-2 font-mono text-xs text-slate-700">
      <code className="whitespace-pre">{children}</code>
    </pre>
  );
}

export function DocPage() {
  return (
    <section className="mx-auto w-full max-w-7xl">
      <div className="border-b border-slate-200 pb-5">
        <p className="text-xs font-semibold uppercase tracking-normal text-slate-500">
          Gitslice
        </p>
        <h1 className="mt-2 text-2xl font-semibold tracking-normal text-zinc-950">
          Docs
        </h1>
        <p className="mt-2 max-w-3xl text-sm leading-6 text-slate-600">
          Gitslice is a source-graph platform that gives teams one coherent
          codebase without forcing every workflow through a single physical Git
          repo. It feels familiar to Git users, but treats slices, workspaces,
          changesets, and submit validation as first-class. The CLI (
          <code className="rounded-md bg-slate-50 px-1.5 py-0.5 font-mono text-xs text-slate-700">
            gs
          </code>
          ) is the primary surface; this web app is a companion for browsing and
          review.
        </p>
      </div>

      <div className="mt-8">
        <h2 className="text-base font-semibold text-zinc-950">Core concepts</h2>
        <div className="mt-4 grid gap-4 md:grid-cols-2">
          {concepts.map((concept) => (
            <article
              className="rounded-lg border border-slate-200 bg-white p-5 shadow-sm shadow-slate-200/50"
              key={concept.title}
            >
              <h3 className="text-base font-semibold text-zinc-950">
                {concept.title}
              </h3>
              <p className="mt-2 text-sm leading-6 text-slate-600">
                {concept.description}
              </p>
            </article>
          ))}
        </div>
      </div>

      <div className="mt-8 rounded-lg border border-slate-200 bg-white p-5 shadow-sm shadow-slate-200/50">
        <h2 className="text-base font-semibold text-zinc-950">Get started</h2>
        <ol className="mt-4 space-y-5">
          {quickstartSteps.map((step, index) => (
            <li className="text-sm leading-6 text-slate-600" key={step.title}>
              <div className="flex gap-3">
                <span className="font-semibold text-zinc-950">
                  {index + 1}.
                </span>
                <div className="min-w-0 flex-1">
                  <h3 className="text-sm font-semibold text-zinc-950">
                    {step.title}
                  </h3>
                  <p className="mt-1">{step.description}</p>
                  <CommandBlock>{step.command}</CommandBlock>
                </div>
              </div>
            </li>
          ))}
        </ol>

        <div className="mt-6 border-t border-slate-200 pt-5">
          <h3 className="text-sm font-semibold text-zinc-950">
            Compact example
          </h3>
          <CommandBlock>{exampleCommands}</CommandBlock>
        </div>
      </div>

      <div className="mt-8">
        <h2 className="text-base font-semibold text-zinc-950">
          Command reference
        </h2>
        <div className="mt-4 grid gap-4 md:grid-cols-2">
          {commandGroups.map((group) => (
            <section
              className="rounded-lg border border-slate-200 bg-white p-5 shadow-sm shadow-slate-200/50"
              key={group.title}
            >
              <h3 className="text-base font-semibold text-zinc-950">
                {group.title}
              </h3>
              <dl className="mt-3 space-y-3">
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
            </section>
          ))}
        </div>
      </div>

      <div className="mt-8 rounded-lg border border-slate-200 bg-white p-5 shadow-sm shadow-slate-200/50">
        <h2 className="text-base font-semibold text-zinc-950">
          Git compatibility
        </h2>
        <p className="mt-2 text-sm leading-6 text-slate-600">
          Git clients can clone/fetch over the Git smart-HTTP gateway;
          authentication maps to the same account, slice, and policy model as
          the CLI. Git access still flows through changesets and submit
          validation, so it never bypasses them. Use the per-slice Clone button
          on a slice page for the concrete URL.
        </p>
      </div>
    </section>
  );
}
