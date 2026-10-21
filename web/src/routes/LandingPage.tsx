import { Link } from "@tanstack/react-router";

import { BrandMark } from "../components/BrandMark";
import { ThemeToggle } from "../components/ThemeToggle";

// Gitslice is developed on Gitslice.
const sourceSlice = { account: "gitslice", slice: "gitslice" };
const contactEmail = "contact@gitslice.io";

const footerLinkClass =
  "rounded-md text-slate-600 outline-none transition hover:text-zinc-950 focus-visible:ring-2 focus-visible:ring-sky-500 focus-visible:ring-offset-2 focus-visible:ring-offset-slate-50 dark:text-zinc-400 dark:hover:text-white dark:focus-visible:ring-sky-400 dark:focus-visible:ring-offset-zinc-950";

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

const pillars: Array<{ body: string; stat: string; statLabel: string; title: string }> = [
  {
    title: "Land in parallel",
    stat: "0 retries",
    statLabel: "16 agents, one codebase",
    body: "Git moves the branch under every agent that lands, so each one pulls, rebases and pushes again. Gitslice admits a change by the paths it changes, so agents on different files never wait on each other, and edits to the same file are merged on submit."
  },
  {
    title: "A workspace for the task",
    stat: "2.6 s",
    statLabel: "to mount 4,508 files, cold",
    body: "Mount a slice and start. Only the files an agent reads are fetched, so the cost follows the task, not the size of the repository; work that reads everything fetches first. Checkpoints save the work in progress on the machine, to rewind to or fork."
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

export function LandingPage() {
  return (
    <div className="relative min-h-[100dvh] overflow-hidden bg-slate-50 font-sans text-zinc-900 transition-colors duration-200 dark:bg-zinc-950 dark:text-zinc-100">
      <div
        aria-hidden="true"
        className="landing-grid pointer-events-none absolute inset-0 opacity-70 dark:opacity-40"
        style={{
          backgroundSize: "56px 56px"
        }}
      />
      <div
        aria-hidden="true"
        className="pointer-events-none absolute left-1/2 top-0 h-[34rem] w-[46rem] -translate-x-1/2 animate-pulse rounded-full bg-sky-500/[0.08] blur-3xl"
      />

      <header className="sticky top-0 z-20 border-b border-slate-200/80 bg-slate-50/85 backdrop-blur-xl dark:border-white/10 dark:bg-zinc-950/85">
        <div className="mx-auto flex min-h-16 w-full max-w-7xl items-center justify-between gap-3 px-4 sm:px-6 lg:px-8">
          <Link
            aria-label="Gitslice home"
            className="flex items-center gap-2.5 rounded-md text-sm font-semibold tracking-tight text-zinc-950 outline-none transition hover:text-zinc-600 focus-visible:ring-2 focus-visible:ring-sky-500 focus-visible:ring-offset-2 focus-visible:ring-offset-slate-50 dark:text-white dark:hover:text-zinc-200 dark:focus-visible:ring-sky-400 dark:focus-visible:ring-offset-zinc-950"
            to="/"
          >
            <BrandMark className="h-7 w-7" />
            <span>Gitslice</span>
          </Link>
          <nav
            aria-label="Landing page"
            className="flex items-center gap-1 sm:gap-2"
          >
            <ThemeToggle inverted />
            <Link
              className="hidden rounded-full px-2.5 py-2 text-sm font-medium text-slate-600 outline-none transition hover:bg-slate-200/70 hover:text-zinc-950 active:-translate-y-px focus-visible:ring-2 focus-visible:ring-sky-500 focus-visible:ring-offset-2 focus-visible:ring-offset-slate-50 dark:text-zinc-300 dark:hover:bg-white/[0.06] dark:hover:text-white dark:focus-visible:ring-sky-400 dark:focus-visible:ring-offset-zinc-950 min-[420px]:inline-flex sm:px-4"
              hash="agent-signup"
              params={{ section: "agents" }}
              to="/doc/$section"
            >
              For agents
            </Link>
            <Link
              className="hidden rounded-full px-2.5 py-2 text-sm font-medium text-slate-600 outline-none transition hover:bg-slate-200/70 hover:text-zinc-950 active:-translate-y-px focus-visible:ring-2 focus-visible:ring-sky-500 focus-visible:ring-offset-2 focus-visible:ring-offset-slate-50 dark:text-zinc-300 dark:hover:bg-white/[0.06] dark:hover:text-white dark:focus-visible:ring-sky-400 dark:focus-visible:ring-offset-zinc-950 sm:inline-flex sm:px-4"
              params={sourceSlice}
              to="/slices/$account/$slice"
            >
              Source
            </Link>
            <Link
              className="rounded-full px-2.5 py-2 text-sm font-medium text-slate-600 outline-none transition hover:bg-slate-200/70 hover:text-zinc-950 active:-translate-y-px focus-visible:ring-2 focus-visible:ring-sky-500 focus-visible:ring-offset-2 focus-visible:ring-offset-slate-50 dark:text-zinc-300 dark:hover:bg-white/[0.06] dark:hover:text-white dark:focus-visible:ring-sky-400 dark:focus-visible:ring-offset-zinc-950 sm:px-4"
              to="/login"
            >
              Sign in
            </Link>
            <Link
              className="rounded-full bg-zinc-950 px-3.5 py-2 text-sm font-semibold text-white outline-none transition hover:bg-zinc-800 active:-translate-y-px focus-visible:ring-2 focus-visible:ring-sky-500 focus-visible:ring-offset-2 focus-visible:ring-offset-slate-50 dark:bg-white dark:text-zinc-950 dark:hover:bg-zinc-200 dark:focus-visible:ring-sky-400 dark:focus-visible:ring-offset-zinc-950 sm:px-4"
              to="/signup"
            >
              Sign up
            </Link>
          </nav>
        </div>
      </header>

      <main className="relative">
        <section className="mx-auto grid w-full max-w-7xl items-center gap-12 px-4 pb-20 pt-16 sm:px-6 sm:pb-24 sm:pt-24 lg:grid-cols-[minmax(0,1.05fr)_minmax(25rem,0.95fr)] lg:gap-16 lg:px-8 lg:pb-32 lg:pt-28">
          <div>
            <div className="mb-7 inline-flex items-center gap-2 rounded-full border border-slate-200 bg-white/80 px-3 py-1.5 text-xs font-medium text-slate-600 shadow-[inset_0_1px_0_rgba(255,255,255,0.8)] dark:border-white/10 dark:bg-white/[0.04] dark:text-zinc-300 dark:shadow-[inset_0_1px_0_rgba(255,255,255,0.08)]">
              <span className="h-1.5 w-1.5 rounded-full bg-sky-400" />
              Source infrastructure for fleets of agents
            </div>
            <h1 className="max-w-3xl text-4xl font-semibold leading-[1.02] tracking-[-0.045em] text-zinc-950 dark:text-white sm:text-5xl md:text-6xl">
              Many agents. One codebase. Land in parallel.
            </h1>
            <p className="mt-7 max-w-2xl text-base leading-7 text-slate-600 dark:text-zinc-400 sm:text-lg sm:leading-8">
              Gitslice is Git-compatible source infrastructure where agents get a
              workspace for the task,{" "}
              <strong className="font-medium text-zinc-800 dark:text-zinc-200">
                land changes without stepping on each other
              </strong>
              , and the server, not the agent, decides what merges. Slices give
              each one its part of the codebase.
            </p>
            <div className="mt-9 flex flex-col gap-3 min-[380px]:flex-row">
              <Link
                className="inline-flex min-h-11 items-center justify-center rounded-full bg-zinc-950 px-5 py-2.5 text-sm font-semibold text-white outline-none transition hover:bg-zinc-800 active:-translate-y-px focus-visible:ring-2 focus-visible:ring-sky-500 focus-visible:ring-offset-2 focus-visible:ring-offset-slate-50 dark:bg-white dark:text-zinc-950 dark:hover:bg-zinc-200 dark:focus-visible:ring-sky-400 dark:focus-visible:ring-offset-zinc-950"
                to="/signup"
              >
                Sign up
                <svg
                  aria-hidden="true"
                  className="ml-2 h-4 w-4"
                  fill="none"
                  stroke="currentColor"
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  strokeWidth="1.8"
                  viewBox="0 0 24 24"
                >
                  <path d="M5 12h14" />
                  <path d="m13 6 6 6-6 6" />
                </svg>
              </Link>
              <Link
                className="inline-flex min-h-11 items-center justify-center rounded-full border border-slate-300 bg-white/70 px-5 py-2.5 text-sm font-semibold text-zinc-700 outline-none transition hover:border-slate-400 hover:bg-white active:-translate-y-px focus-visible:ring-2 focus-visible:ring-sky-500 focus-visible:ring-offset-2 focus-visible:ring-offset-slate-50 dark:border-white/10 dark:bg-white/[0.04] dark:text-zinc-200 dark:hover:border-white/20 dark:hover:bg-white/[0.08] dark:focus-visible:ring-sky-400 dark:focus-visible:ring-offset-zinc-950"
                to="/doc"
              >
                Read the docs
              </Link>
              <Link
                className="inline-flex min-h-11 items-center justify-center rounded-full border border-sky-300 bg-sky-50 px-5 py-2.5 text-sm font-semibold text-sky-900 outline-none transition hover:border-sky-400 hover:bg-sky-100 active:-translate-y-px focus-visible:ring-2 focus-visible:ring-sky-500 focus-visible:ring-offset-2 focus-visible:ring-offset-slate-50 dark:border-sky-900/70 dark:bg-sky-950/40 dark:text-sky-200 dark:hover:border-sky-700 dark:hover:bg-sky-950/70 dark:focus-visible:ring-sky-400 dark:focus-visible:ring-offset-zinc-950"
                hash="agent-signup"
                params={{ section: "agents" }}
                to="/doc/$section"
              >
                For agents
              </Link>
            </div>
            <p className="mt-6 max-w-2xl text-sm leading-6 text-slate-500 dark:text-zinc-400">
              <span className="font-medium text-zinc-700 dark:text-zinc-200">
                Install the CLI:
              </span>{" "}
              <code className="rounded bg-slate-100 px-1.5 py-0.5 font-mono text-xs text-zinc-800 dark:bg-white/[0.06] dark:text-zinc-200">
                curl -fsSL https://gitslice.io/install.sh | sh
              </code>
            </p>
            <p className="mt-2 max-w-2xl text-sm leading-6 text-slate-500 dark:text-zinc-400">
              <span className="font-medium text-zinc-700 dark:text-zinc-200">
                Are you an agent?
              </span>{" "}
              Sign yourself up with no browser:{" "}
              <code className="rounded bg-slate-100 px-1.5 py-0.5 font-mono text-xs text-zinc-800 dark:bg-white/[0.06] dark:text-zinc-200">
                gs auth register-agent
              </code>
              . Read{" "}
              <a
                className="font-medium text-zinc-800 underline underline-offset-2 dark:text-zinc-200"
                href="/llms.txt"
              >
                /llms.txt
              </a>{" "}
              or the{" "}
              <Link
                className="font-medium text-zinc-800 underline underline-offset-2 dark:text-zinc-200"
                params={{ section: "agents" }}
                to="/doc/$section"
              >
                agents guide
              </Link>
              .
            </p>
            <p className="mt-2 max-w-2xl text-sm leading-6 text-slate-500 dark:text-zinc-400">
              <span className="font-medium text-zinc-700 dark:text-zinc-200">
                Open source:
              </span>{" "}
              Gitslice is built on Gitslice. Browse{" "}
              <Link
                className="font-medium text-zinc-800 underline underline-offset-2 dark:text-zinc-200"
                params={sourceSlice}
                to="/slices/$account/$slice"
              >
                gitslice/gitslice
              </Link>
              .
            </p>
          </div>

          <figure className="relative lg:translate-y-8">
            <div
              aria-hidden="true"
              className="absolute -inset-5 -z-10 rounded-[2rem] bg-sky-500/[0.06] blur-2xl"
            />
            <div className="overflow-hidden rounded-2xl border border-white/10 bg-zinc-900/90 shadow-2xl shadow-zinc-950/60 ring-1 ring-white/[0.04]">
              <div className="flex h-11 items-center border-b border-white/10 bg-white/[0.025] px-4">
                <div aria-hidden="true" className="flex gap-1.5">
                  <span className="h-2.5 w-2.5 rounded-full bg-zinc-600" />
                  <span className="h-2.5 w-2.5 rounded-full bg-zinc-600" />
                  <span className="h-2.5 w-2.5 rounded-full bg-zinc-600" />
                </div>
                <span className="mx-auto -translate-x-5 font-mono text-[11px] text-zinc-500">
                  ~/work/checkout
                </span>
              </div>
              <div className="min-h-[23rem] overflow-x-auto p-5 font-mono text-[12px] leading-6 sm:p-6 sm:text-[13px]">
                <p>
                  <span className="text-sky-400">$</span>{" "}
                  <span className="text-zinc-200">
                    gs auth register-agent --username build-bot \
                  </span>
                </p>
                <p className="pl-4 text-zinc-200">--email mira@orbit.dev</p>
                <p className="text-zinc-500">
                  Registered build-bot; claim link sent for mira@orbit.dev
                </p>
                <p className="mt-4">
                  <span className="text-sky-400">$</span>{" "}
                  <span className="text-zinc-200">
                    gs init --lazy orbit/checkout work
                  </span>
                </p>
                <p className="text-zinc-500">
                  mounted orbit:checkout at ~/work (318 files, fetched on
                  first read; edits stay on this machine until gs create)
                </p>
                <p className="mt-4">
                  <span className="text-sky-400">$</span>{" "}
                  <span className="text-zinc-200">
                    gs create --all --submit \
                  </span>
                </p>
                <p className="pl-4 text-zinc-200">
                  --message &quot;Tighten authorization&quot;
                </p>
                <p className="text-zinc-500">created 8b4ee46085 patchset 1</p>
                <p className="text-zinc-500">
                  view: https://gitslice.io/cs/8b4ee46085
                </p>
                <p className="text-zinc-500">
                  waiting for required check &quot;orbit/unit&quot; to pass...
                </p>
                <p className="text-emerald-300">submitted 8b4ee46085</p>
              </div>
            </div>
            <figcaption className="sr-only">
              An example Gitslice CLI session: an agent registers itself, mounts
              a slice, and submits a changeset that lands once its required
              check passes.
            </figcaption>
          </figure>
        </section>

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
                </article>
              ))}
            </div>
          </div>
        </section>

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
                  params={sourceSlice}
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
      </main>

      <footer className="relative border-t border-slate-200 dark:border-white/10">
        <div className="mx-auto flex min-h-20 w-full max-w-7xl flex-col items-start justify-between gap-4 px-4 py-6 text-sm text-zinc-500 min-[380px]:flex-row min-[380px]:items-center sm:px-6 lg:px-8">
          <div className="flex items-center gap-2.5">
            <BrandMark className="h-5 w-5 opacity-80" />
            <span>© 2026 Gitslice</span>
          </div>
          <nav
            aria-label="Footer"
            className="flex flex-wrap items-center gap-x-5 gap-y-2"
          >
            <Link
              className={footerLinkClass}
              params={sourceSlice}
              to="/slices/$account/$slice"
            >
              Source
            </Link>
            <a className={footerLinkClass} href={`mailto:${contactEmail}`}>
              {contactEmail}
            </a>
            <Link className={footerLinkClass} to="/login">
              Sign in
            </Link>
          </nav>
        </div>
      </footer>
    </div>
  );
}
