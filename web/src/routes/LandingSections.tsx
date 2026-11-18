import { Link } from "@tanstack/react-router";

// Gitslice is developed on Gitslice.
export const LANDING_SOURCE_SLICE = { account: "gitslice", slice: "gitslice" } as const;

type FeatureIconName =
  | "slice"
  | "changeset"
  | "agent"
  | "check"
  | "git"
  | "terminal";

const features: Array<{
  description: string;
  icon: FeatureIconName;
  title: string;
}> = [
  {
    title: "Merge on submit",
    description:
      "When a change's base moved while an agent worked, the server merges it onto the latest base instead of sending the agent back to start over. Overlapping edits are still refused, with the files named.",
    icon: "changeset"
  },
  {
    title: "Slices",
    description:
      "Repo-like views of one global source graph. Give each agent the part it owns, with its own access and rules, without splitting the history apart.",
    icon: "slice"
  },
  {
    title: "Lazy workspaces",
    description:
      "Mount a slice in seconds and fetch files as they are read, so the cost follows the task, not the repository. Edits stay on the machine, with checkpoints to rewind to or fork, until the agent creates a changeset.",
    icon: "terminal"
  },
  {
    title: "Checks the server enforces",
    description:
      "Required checks and approvals are conditions of landing, not habits. CI runs on any machine with Docker, with build images cached between runs.",
    icon: "check"
  },
  {
    title: "Built for agents",
    description:
      "Agents register themselves with no browser, work through the gs CLI or an MCP server, and leave an audit trail of changesets and patchsets.",
    icon: "agent"
  },
  {
    title: "Git compatible",
    description:
      "Clone and fetch a slice with plain Git, and push to create a changeset. Compatibility stays at the boundary while the native graph is the source of truth.",
    icon: "git"
  }
];

// Measured by tests/cli/bench_agents_test.go (design/28_benchmarks.md,
// "Landing in parallel"): agents landing at once, medians, 2-vCPU VM.
const parallelRows: Array<{
  agents: number;
  gitRetries: number;
  gitSeconds: string;
  gsRetries: number;
  gsSeconds: string;
  scenario: string;
}> = [
  { scenario: "Each agent edits its own file", agents: 8, gitSeconds: "1.4 s", gitRetries: 28, gsSeconds: "1.2 s", gsRetries: 0 },
  { scenario: "Each agent edits its own file", agents: 16, gitSeconds: "5.3 s", gitRetries: 120, gsSeconds: "2.4 s", gsRetries: 0 },
  { scenario: "One file, a block each (merge on submit)", agents: 8, gitSeconds: "1.4 s", gitRetries: 28, gsSeconds: "1.8 s", gsRetries: 0 },
  { scenario: "One file, a block each (merge on submit)", agents: 16, gitSeconds: "5.4 s", gitRetries: 120, gsSeconds: "3.5 s", gsRetries: 0 }
];

const pillars: Array<{ body: string; doc?: "lazy"; stat: string; statLabel: string; title: string }> = [
  {
    title: "Land in parallel",
    stat: "0 retries",
    statLabel: "16 agents, one codebase",
    body: "Git moves the branch under every agent that lands, so each one pulls, rebases and pushes again. Gitslice admits a change by the paths it changes, so agents on different files never wait on each other, and edits to the same file are merged on submit."
  },
  {
    title: "A workspace for the task",
    doc: "lazy",
    stat: "2.6 s",
    statLabel: "to mount 4,508 files, cold",
    body: "Mount a slice and start. Only the files an agent reads are fetched, so the cost follows the task, not the size of the repository; a walk of the whole tree is read ahead of. Checkpoints save the work in progress on the machine, to rewind to or fork."
  },
  {
    title: "The server decides",
    stat: "Enforced",
    statLabel: "checks and approvals, on the server",
    body: "An agent can claim anything. What lands is decided where the commit is made: path by path, against the slice's checks and approvals, with every iteration kept as an immutable patchset."
  }
];

function FeatureIcon({ name }: { name: FeatureIconName }) {
  const common = {
    "aria-hidden": true,
    className: "h-6 w-6",
    fill: "none",
    stroke: "currentColor",
    strokeLinecap: "round" as const,
    strokeLinejoin: "round" as const,
    strokeWidth: 1.7,
    viewBox: "0 0 24 24"
  };

  if (name === "slice") {
    return (
      <svg {...common}>
        <path d="m12 3 8 4.5-8 4.5-8-4.5L12 3Z" />
        <path d="m4 12 8 4.5 8-4.5" />
        <path d="m4 16.5 8 4.5 8-4.5" />
      </svg>
    );
  }

  if (name === "changeset") {
    return (
      <svg {...common}>
        <circle cx="6" cy="5" r="2" />
        <circle cx="18" cy="19" r="2" />
        <path d="M6 7v4a4 4 0 0 0 4 4h4a4 4 0 0 1 4 4" />
        <path d="M18 3v8" />
        <path d="m15 8 3 3 3-3" />
      </svg>
    );
  }

  if (name === "agent") {
    return (
      <svg {...common}>
        <rect height="13" rx="3" width="18" x="3" y="7" />
        <path d="M12 3v4" />
        <path d="M8 12h.01" />
        <path d="M16 12h.01" />
        <path d="M8.5 16h7" />
      </svg>
    );
  }

  if (name === "check") {
    return (
      <svg {...common}>
        <path d="M12 3 4.5 6v5.2c0 4.5 3 8.3 7.5 9.8 4.5-1.5 7.5-5.3 7.5-9.8V6L12 3Z" />
        <path d="m8.5 12 2.2 2.2 4.8-5" />
      </svg>
    );
  }

  if (name === "git") {
    return (
      <svg {...common}>
        <circle cx="6" cy="5" r="2" />
        <circle cx="6" cy="19" r="2" />
        <circle cx="18" cy="12" r="2" />
        <path d="M6 7v10" />
        <path d="M8 7c0 2.8 2.2 5 5 5h3" />
      </svg>
    );
  }

  return (
    <svg {...common}>
      <rect height="16" rx="2.5" width="20" x="2" y="4" />
      <path d="m6 9 3 3-3 3" />
      <path d="M12 15h5" />
    </svg>
  );
}

export function LandingPillarsSection() {
  return (
    <section
      aria-labelledby="pillars-heading"
      className="mx-auto w-full max-w-7xl px-4 pb-24 sm:px-6 sm:pb-28 lg:px-8"
    >
      <div className="border-t border-slate-200 pt-20 dark:border-white/10 sm:pt-24">
        <p className="font-mono text-xs font-medium uppercase tracking-[0.18em] text-sky-400">
          Why Gitslice
        </p>
        <h2
          className="mt-4 max-w-3xl text-3xl font-semibold tracking-[-0.035em] text-zinc-950 dark:text-white sm:text-4xl"
          id="pillars-heading"
        >
          Git was built for people who merge now and then. Agents land
          constantly.
        </h2>
        <div className="mt-12 grid gap-4 lg:grid-cols-3 lg:gap-5">
          {pillars.map((pillar) => (
            <article
              className="rounded-2xl border border-slate-200 bg-white/75 p-6 shadow-[inset_0_1px_0_rgba(255,255,255,0.85)] dark:border-white/10 dark:bg-white/[0.035] dark:shadow-[inset_0_1px_0_rgba(255,255,255,0.06)] sm:p-7"
              key={pillar.title}
            >
              <p className="text-3xl font-semibold tracking-tight text-zinc-950 dark:text-white">
                {pillar.stat}
              </p>
              <p className="mt-1 font-mono text-xs text-slate-500 dark:text-zinc-400">
                {pillar.statLabel}
              </p>
              <h3 className="mt-6 text-lg font-semibold tracking-tight text-zinc-900 dark:text-zinc-100">
                {pillar.title}
              </h3>
              <p className="mt-3 text-sm leading-6 text-slate-600 dark:text-zinc-400">
                {pillar.body}
              </p>
              {pillar.doc ? (
                <Link
                  className="-mb-1.5 mt-2.5 inline-block py-1.5 text-sm font-semibold text-sky-700 underline-offset-4 hover:underline dark:text-sky-300"
                  params={{ section: pillar.doc }}
                  to="/doc/$section"
                >
                  How lazy workspaces work
                </Link>
              ) : null}
            </article>
          ))}
        </div>
      </div>
    </section>
  );
}

export function LandingBenchmarksSection() {
  return (
    <section
      aria-labelledby="benchmarks-heading"
      className="mx-auto w-full max-w-7xl px-4 pb-24 sm:px-6 sm:pb-28 lg:px-8"
    >
      <div className="grid items-start gap-10 border-t border-slate-200 pt-20 dark:border-white/10 sm:pt-24 lg:grid-cols-[0.8fr_1.2fr] lg:gap-16">
        <div>
          <p className="font-mono text-xs font-medium uppercase tracking-[0.18em] text-sky-400">
            Measured, not claimed
          </p>
          <h2
            className="mt-4 text-3xl font-semibold tracking-[-0.035em] text-zinc-950 dark:text-white sm:text-4xl"
            id="benchmarks-heading"
          >
            Agents landing at the same moment
          </h2>
          <p className="mt-4 text-base leading-7 text-slate-600 dark:text-zinc-400">
            Agents each make a change, then all of them land at once. With
            Git, every push that loses the race is a retry: 28 at eight
            agents, 120 at sixteen. With Gitslice there are none, even
            when they edit the same file. At eight agents on one file,
            Git is still faster.
          </p>
          <p className="mt-4 text-sm leading-6 text-slate-500 dark:text-zinc-400">
            Medians of a benchmark you can rerun, on a 2-vCPU VM. The Git
            remote is a local directory, so its retries are as cheap as
            they get. Where Git is faster, the{" "}
            <Link
              className="font-medium text-zinc-800 underline underline-offset-2 dark:text-zinc-200"
              params={LANDING_SOURCE_SLICE}
              search={{ path: "/gitslice/gitslice/design/28_benchmarks.md" }}
              to="/slices/$account/$slice"
            >
              full benchmarks
            </Link>{" "}
            say so.
          </p>
        </div>
        <div className="overflow-x-auto rounded-2xl border border-slate-200 bg-white/75 dark:border-white/10 dark:bg-white/[0.035]">
          <table className="w-full min-w-[36rem] text-left text-sm">
            <caption className="sr-only">
              Time to land, and retries, for agents landing at once
            </caption>
            <thead>
              <tr className="border-b border-slate-200 text-xs uppercase tracking-wide text-slate-500 dark:border-white/10 dark:text-zinc-400">
                <th className="px-5 py-3 font-medium" scope="col">
                  Scenario
                </th>
                <th className="px-5 py-3 font-medium" scope="col">
                  Agents
                </th>
                <th className="px-5 py-3 font-medium" scope="col">
                  Git: time
                </th>
                <th className="px-5 py-3 font-medium" scope="col">
                  Git: retries
                </th>
                <th className="px-5 py-3 font-medium" scope="col">
                  Gitslice: time
                </th>
                <th className="px-5 py-3 font-medium" scope="col">
                  Gitslice: retries
                </th>
              </tr>
            </thead>
            <tbody className="font-mono text-zinc-800 dark:text-zinc-200">
              {parallelRows.map((row) => (
                <tr
                  className="border-b border-slate-100 last:border-0 dark:border-white/5"
                  key={`${row.scenario}-${row.agents}`}
                >
                  <th className="px-5 py-4 font-sans text-xs font-medium" scope="row">
                    {row.scenario}
                  </th>
                  <td className="px-5 py-4">{row.agents}</td>
                  <td className="px-5 py-4">{row.gitSeconds}</td>
                  <td className="px-5 py-4 text-rose-600 dark:text-rose-300">
                    {row.gitRetries}
                  </td>
                  <td className="px-5 py-4">{row.gsSeconds}</td>
                  <td className="px-5 py-4 text-emerald-700 dark:text-emerald-300">
                    {row.gsRetries}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>
    </section>
  );
}

export function LandingFeaturesSection() {
  return (
    <section
      aria-labelledby="features-heading"
      className="mx-auto w-full max-w-7xl px-4 pb-24 sm:px-6 sm:pb-32 lg:px-8"
    >
      <div className="border-t border-slate-200 pt-20 dark:border-white/10 sm:pt-24">
        <div className="grid gap-6 md:grid-cols-[0.7fr_1.3fr] md:items-end">
          <p className="font-mono text-xs font-medium uppercase tracking-[0.18em] text-sky-400">
            Everything else, built in
          </p>
          <div>
            <h2
              className="text-3xl font-semibold tracking-[-0.035em] text-zinc-950 dark:text-white sm:text-4xl"
              id="features-heading"
            >
              One system, from the edit to the merge.
            </h2>
            <p className="mt-4 max-w-2xl text-base leading-7 text-slate-600 dark:text-zinc-400">
              Source, policy, review and automation share one graph, so
              nothing has to be reconciled between tools—and nobody has to
              carry the whole thing.
            </p>
          </div>
        </div>

        <div className="mt-12 grid gap-4 md:grid-cols-2 lg:gap-5">
          {features.map((feature, index) => (
            <article
              className={`group rounded-2xl border border-slate-200 bg-white/75 p-6 shadow-[inset_0_1px_0_rgba(255,255,255,0.85)] transition duration-300 hover:-translate-y-1 hover:border-slate-300 hover:bg-white dark:border-white/10 dark:bg-white/[0.035] dark:shadow-[inset_0_1px_0_rgba(255,255,255,0.06)] dark:hover:border-white/20 dark:hover:bg-white/[0.06] sm:p-7 ${
                index === 0 || index === 3 ? "md:translate-y-5" : ""
              }`}
              key={feature.title}
            >
              <div className="flex h-11 w-11 items-center justify-center rounded-xl border border-sky-300 bg-sky-50 text-sky-700 transition group-hover:border-sky-400 group-hover:bg-sky-100 dark:border-sky-400/20 dark:bg-sky-400/[0.08] dark:text-sky-300 dark:group-hover:border-sky-400/30 dark:group-hover:bg-sky-400/[0.12]">
                <FeatureIcon name={feature.icon} />
              </div>
              <h3 className="mt-6 text-lg font-semibold tracking-tight text-zinc-900 dark:text-zinc-100">
                {feature.title}
              </h3>
              <p className="mt-3 max-w-xl text-sm leading-6 text-slate-600 dark:text-zinc-400">
                {feature.description}
              </p>
            </article>
          ))}
        </div>
      </div>
    </section>
  );
}
