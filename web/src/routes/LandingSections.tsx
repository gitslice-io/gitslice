import { useEffect, useId } from "react";
import { Link } from "@tanstack/react-router";

import { ensureCopyCommandListener } from "../lib/copyCommand";
import type { DocSection } from "./doc/docData";

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
  doc: DocSection;
  docLabel: string;
  icon: FeatureIconName;
  title: string;
}> = [
  {
    title: "Merge on submit",
    description:
      "When main moves while an agent works, the server merges non-overlapping edits onto the latest base and reruns CI instead of making the agent start over. Conflicting files are still rejected cleanly by path.",
    icon: "changeset",
    doc: "concepts",
    docLabel: "Submit lifecycle"
  },
  {
    title: "Slices",
    description:
      "Repository-like views over one global source graph. Give each team or agent only the folders it owns — with its own visibility, roles, approver groups, and rules — without splitting history apart.",
    icon: "slice",
    doc: "concepts",
    docLabel: "Core concepts"
  },
  {
    title: "Lazy workspaces",
    description:
      "Mount a slice with FUSE and fetch files on first read so bandwidth tracks the task, not repo size. Save local checkpoints to rewind or fork side by side before uploading a single edit.",
    icon: "terminal",
    doc: "lazy",
    docLabel: "Lazy workspaces"
  },
  {
    title: "Checks the server enforces",
    description:
      "Commit folder-scoped checks, triggers, and encrypted secrets next to your code. CI runs on your own Docker or Podman daemons, contributor patchsets run secretless until allowed, and submit blocks until required checks pass.",
    icon: "check",
    doc: "checks",
    docLabel: "CI checks & triggers"
  },
  {
    title: "Built for agents",
    description:
      "Agents register without a browser or receive owner-minted keys, work over the gs CLI or stdio MCP, reserve paths with advisory intents, and leave an immutable audit trail of patchsets.",
    icon: "agent",
    doc: "agents",
    docLabel: "Agents guide"
  },
  {
    title: "Git compatible",
    description:
      "Import existing repositories, clone slices with plain Git, serve single-path slices as native Go modules, and mirror every landing and tag out to external Git hosts automatically.",
    icon: "git",
    doc: "mirrors",
    docLabel: "Git mirrors & CLI"
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
  {
    scenario: "Each agent edits its own file",
    agents: 8,
    gitSeconds: "1.4 s",
    gitRetries: 28,
    gsSeconds: "1.2 s",
    gsRetries: 0
  },
  {
    scenario: "Each agent edits its own file",
    agents: 16,
    gitSeconds: "5.3 s",
    gitRetries: 120,
    gsSeconds: "2.4 s",
    gsRetries: 0
  },
  {
    scenario: "One file, a block each (merge on submit)",
    agents: 8,
    gitSeconds: "1.4 s",
    gitRetries: 28,
    gsSeconds: "1.8 s",
    gsRetries: 0
  },
  {
    scenario: "One file, a block each (merge on submit)",
    agents: 16,
    gitSeconds: "5.4 s",
    gitRetries: 120,
    gsSeconds: "3.5 s",
    gsRetries: 0
  }
];

const pillars: Array<{
  body: string;
  doc?: DocSection;
  docLabel?: string;
  index: string;
  stat: string;
  statLabel: string;
  title: string;
}> = [
  {
    index: "01",
    title: "Land in parallel",
    stat: "0 retries",
    statLabel: "16 agents, one codebase",
    body: "Git moves the branch under every agent that lands, so each loser pulls, rebases, and pushes again. Gitslice admits changes by path, so agents on different files never wait on each other — and same-file edits merge on submit."
  },
  {
    index: "02",
    title: "A workspace for the task",
    doc: "lazy",
    docLabel: "How lazy workspaces work",
    stat: "2.6 s",
    statLabel: "to mount 4,508 files, cold",
    body: "Mount a slice and start. Only the files an agent reads are fetched, so cost follows the task instead of repository size; tree walks are read ahead automatically. Local checkpoints let you rewind or fork approaches cheaply."
  },
  {
    index: "03",
    title: "The server decides",
    doc: "checks",
    docLabel: "How server checks work",
    stat: "Enforced",
    statLabel: "checks and approvals, on the server",
    body: "An agent can claim tests passed. What lands is decided where the commit is made: path by path, against required checks, human approval gates, and contributor secret isolation, with every revision kept as an immutable patchset."
  }
];

function FeatureIcon({ name }: { name: FeatureIconName }) {
  const common = {
    "aria-hidden": true,
    className: "h-5 w-5",
    fill: "none",
    stroke: "currentColor",
    strokeLinecap: "round" as const,
    strokeLinejoin: "round" as const,
    strokeWidth: 1.75,
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
      className="mx-auto w-full max-w-7xl px-4 pb-20 sm:px-6 sm:pb-24 lg:px-8"
    >
      <div className="border-t border-slate-200/90 pt-16 dark:border-white/10 sm:pt-20">
        <p className="font-mono text-xs font-medium uppercase tracking-[0.18em] text-sky-600 dark:text-sky-400">
          Why Gitslice
        </p>
        <h2
          className="mt-3 max-w-3xl text-balance text-3xl font-semibold tracking-[-0.035em] text-zinc-950 dark:text-white sm:text-4xl"
          id="pillars-heading"
        >
          Git was built for people who merge now and then. Agents land constantly.
        </h2>

        <div className="mt-10 overflow-hidden rounded-2xl border border-slate-200/90 bg-slate-200/80 dark:border-white/10 dark:bg-white/10">
          <div className="grid gap-px lg:grid-cols-3">
            {pillars.map((pillar) => (
              <article
                className="flex flex-col justify-between bg-white p-6 dark:bg-zinc-950 sm:p-8"
                key={pillar.title}
              >
                <div>
                  <div className="flex items-center justify-between">
                    <span className="font-mono text-xs text-slate-400 dark:text-zinc-500">
                      {pillar.index}
                    </span>
                    <span className="h-1.5 w-1.5 rounded-full bg-sky-500/80 dark:bg-sky-400/80" />
                  </div>
                  <p className="mt-6 text-4xl font-semibold tabular-nums tracking-[-0.035em] text-zinc-950 dark:text-white">
                    {pillar.stat}
                  </p>
                  <p className="mt-1.5 font-mono text-xs text-slate-500 dark:text-zinc-400">
                    {pillar.statLabel}
                  </p>
                  <h3 className="mt-6 text-lg font-semibold tracking-tight text-zinc-900 dark:text-zinc-100">
                    {pillar.title}
                  </h3>
                  <p className="mt-2.5 text-pretty text-sm leading-6 text-slate-600 dark:text-zinc-400">
                    {pillar.body}
                  </p>
                </div>

                {pillar.doc && pillar.docLabel ? (
                  <div className="mt-6 pt-2">
                    <Link
                      className="inline-flex items-center gap-1.5 text-sm font-medium text-sky-700 underline-offset-4 hover:underline dark:text-sky-300"
                      params={{ section: pillar.doc }}
                      to="/doc/$section"
                    >
                      {pillar.docLabel}
                      <span aria-hidden="true">→</span>
                    </Link>
                  </div>
                ) : null}
              </article>
            ))}
          </div>
        </div>
      </div>
    </section>
  );
}

export function LandingArchitectureSection() {
  return (
    <section
      aria-labelledby="architecture-heading"
      className="mx-auto w-full max-w-7xl px-4 pb-20 sm:px-6 sm:pb-24 lg:px-8"
    >
      <div className="border-t border-slate-200/90 pt-16 dark:border-white/10 sm:pt-20">
        <div className="max-w-3xl">
          <p className="font-mono text-xs font-medium uppercase tracking-[0.18em] text-sky-600 dark:text-sky-400">
            How landing works
          </p>
          <h2
            className="mt-3 text-balance text-3xl font-semibold tracking-[-0.035em] text-zinc-950 dark:text-white sm:text-4xl"
            id="architecture-heading"
          >
            Stop serializing fleets behind one branch pointer.
          </h2>
        </div>

        <div className="mt-10 grid gap-5 lg:grid-cols-2">
          <div className="rounded-2xl border border-slate-200/90 bg-white/85 p-6 dark:border-white/10 dark:bg-zinc-900/50 sm:p-7">
            <div className="flex items-center justify-between gap-3">
              <h3 className="text-base font-semibold text-zinc-950 dark:text-white">
                Traditional Git branch race
              </h3>
              <span className="rounded-full border border-rose-200 bg-rose-50 px-2.5 py-0.5 font-mono text-xs font-medium text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
                O(n²) retries
              </span>
            </div>
            <p className="mt-2 text-pretty text-sm leading-6 text-slate-600 dark:text-zinc-400">
              Every agent pushes against the same <code className="font-mono text-xs">main</code>{" "}
              ref. One push wins; everyone else is rejected and loops through fetch, rebase,
              retest, and push.
            </p>
            <div className="mt-5 space-y-2 font-mono text-xs">
              {[
                { agent: "agent-01", state: "push accepted", ok: true },
                { agent: "agent-02", state: "non-fast-forward → pull → rebase → retry", ok: false },
                { agent: "agent-03", state: "non-fast-forward → pull → rebase → retry", ok: false },
                { agent: "agent-16", state: "15 retries before landing", ok: false }
              ].map((row) => (
                <div
                  className="flex items-center justify-between gap-3 rounded-lg border border-slate-200/80 bg-slate-50 px-3 py-2 dark:border-white/[0.07] dark:bg-zinc-950/70"
                  key={row.agent}
                >
                  <span className="shrink-0 text-zinc-700 dark:text-zinc-300">{row.agent}</span>
                  <span
                    className={
                      row.ok
                        ? "truncate text-emerald-700 dark:text-emerald-300"
                        : "truncate text-rose-600 dark:text-rose-300"
                    }
                  >
                    {row.state}
                  </span>
                </div>
              ))}
            </div>
          </div>

          <div className="rounded-2xl border border-sky-200/90 bg-white p-6 shadow-sm dark:border-sky-500/25 dark:bg-zinc-900/70 sm:p-7">
            <div className="flex items-center justify-between gap-3">
              <h3 className="text-base font-semibold text-zinc-950 dark:text-white">
                Gitslice path-level admission
              </h3>
              <span className="rounded-full border border-emerald-200 bg-emerald-50 px-2.5 py-0.5 font-mono text-xs font-medium text-emerald-700 dark:border-emerald-500/30 dark:bg-emerald-500/10 dark:text-emerald-300">
                0 client retries
              </span>
            </div>
            <p className="mt-2 text-pretty text-sm leading-6 text-slate-600 dark:text-zinc-400">
              Each agent submits a changeset scoped to its slice. The server validates checks and
              approvals, admits disjoint paths directly, merges non-overlapping same-file edits,
              and pushes mirrors automatically.
            </p>
            <div className="mt-5 space-y-2 font-mono text-xs">
              {[
                { agent: "agent-01", path: "/acme/api", state: "admitted → landed" },
                { agent: "agent-02", path: "/acme/billing", state: "admitted → landed" },
                { agent: "agent-03", path: "/acme/web", state: "admitted → landed" },
                { agent: "agent-16", path: "/acme/api (same file)", state: "merged on submit → landed" }
              ].map((row) => (
                <div
                  className="flex items-center justify-between gap-3 rounded-lg border border-slate-200/80 bg-slate-50 px-3 py-2 dark:border-white/[0.07] dark:bg-zinc-950/70"
                  key={row.agent}
                >
                  <div className="flex min-w-0 items-center gap-2">
                    <span className="shrink-0 text-zinc-800 dark:text-zinc-200">{row.agent}</span>
                    <span className="truncate text-slate-400 dark:text-zinc-500">{row.path}</span>
                  </div>
                  <span className="shrink-0 text-emerald-700 dark:text-emerald-300">
                    {row.state}
                  </span>
                </div>
              ))}
            </div>
          </div>
        </div>
      </div>
    </section>
  );
}

export function LandingBenchmarksSection() {
  return (
    <section
      aria-labelledby="benchmarks-heading"
      className="mx-auto w-full max-w-7xl px-4 pb-20 sm:px-6 sm:pb-24 lg:px-8"
    >
      <div className="grid items-start gap-10 border-t border-slate-200/90 pt-16 dark:border-white/10 sm:pt-20 lg:grid-cols-[0.82fr_1.18fr] lg:gap-14">
        <div>
          <p className="font-mono text-xs font-medium uppercase tracking-[0.18em] text-sky-600 dark:text-sky-400">
            Measured, not claimed
          </p>
          <h2
            className="mt-3 text-balance text-3xl font-semibold tracking-[-0.035em] text-zinc-950 dark:text-white sm:text-4xl"
            id="benchmarks-heading"
          >
            Agents landing at the same moment
          </h2>
          <p className="mt-4 text-pretty text-base leading-7 text-slate-600 dark:text-zinc-400">
            Agents each make a change, then all of them land at once. With Git, every push that
            loses the race is a retry: 28 at eight agents, 120 at sixteen. With Gitslice there are
            none, even when they edit the same file. At eight agents on one file, Git is still
            faster.
          </p>

          <div className="mt-6 grid grid-cols-2 gap-3">
            <div className="rounded-xl border border-slate-200/90 bg-white px-4 py-3 dark:border-white/10 dark:bg-zinc-900/60">
              <p className="font-mono text-xl font-semibold tabular-nums text-zinc-950 dark:text-white">
                120 → 0
              </p>
              <p className="mt-0.5 text-xs text-slate-500 dark:text-zinc-400">
                push retries at 16 agents
              </p>
            </div>
            <div className="rounded-xl border border-slate-200/90 bg-white px-4 py-3 dark:border-white/10 dark:bg-zinc-900/60">
              <p className="font-mono text-xl font-semibold tabular-nums text-zinc-950 dark:text-white">
                2.2×
              </p>
              <p className="mt-0.5 text-xs text-slate-500 dark:text-zinc-400">
                faster landing across 16 files
              </p>
            </div>
          </div>

          <p className="mt-5 text-pretty text-sm leading-6 text-slate-500 dark:text-zinc-400">
            Medians of a benchmark you can rerun, on a 2-vCPU VM. The Git remote is a local
            directory, so its retries are as cheap as they get. Where Git is faster, the{" "}
            <Link
              className="font-medium text-zinc-800 underline decoration-slate-300 underline-offset-4 hover:decoration-zinc-800 dark:text-zinc-200 dark:decoration-zinc-600 dark:hover:decoration-zinc-200"
              params={LANDING_SOURCE_SLICE}
              search={{ path: "/gitslice/gitslice/design/28_benchmarks.md" }}
              to="/slices/$account/$slice"
            >
              full benchmarks
            </Link>{" "}
            and{" "}
            <Link
              className="font-medium text-zinc-800 underline decoration-slate-300 underline-offset-4 hover:decoration-zinc-800 dark:text-zinc-200 dark:decoration-zinc-600 dark:hover:decoration-zinc-200"
              params={{ slug: "benchmarking-gitslice-against-git" }}
              to="/blogs/$slug"
            >
              benchmark write-up
            </Link>{" "}
            say so.
          </p>
        </div>

        <div
          aria-label="Parallel landing benchmark results"
          className="overflow-x-auto rounded-2xl border border-slate-200/90 bg-white shadow-sm dark:border-white/10 dark:bg-zinc-900/50"
          role="region"
          tabIndex={0}
        >
          <table className="w-full min-w-[36rem] text-left text-sm">
            <caption className="sr-only">
              Time to land, and retries, for agents landing at once
            </caption>
            <thead>
              <tr className="border-b border-slate-200/90 bg-slate-50/80 text-xs uppercase tracking-wide text-slate-500 dark:border-white/10 dark:bg-white/[0.02] dark:text-zinc-400">
                <th className="px-5 py-3.5 font-medium" scope="col">
                  Scenario
                </th>
                <th className="px-4 py-3.5 font-medium" scope="col">
                  Agents
                </th>
                <th className="px-4 py-3.5 font-medium" scope="col">
                  Git: time
                </th>
                <th className="px-4 py-3.5 font-medium" scope="col">
                  Git: retries
                </th>
                <th className="px-4 py-3.5 font-medium" scope="col">
                  Gitslice: time
                </th>
                <th className="px-4 py-3.5 font-medium" scope="col">
                  Gitslice: retries
                </th>
              </tr>
            </thead>
            <tbody className="font-mono text-zinc-800 tabular-nums dark:text-zinc-200">
              {parallelRows.map((row) => (
                <tr
                  className="border-b border-slate-100 last:border-0 hover:bg-slate-50/70 dark:border-white/[0.06] dark:hover:bg-white/[0.02]"
                  key={`${row.scenario}-${row.agents}`}
                >
                  <th className="px-5 py-4 font-sans text-xs font-medium text-zinc-900 dark:text-zinc-100" scope="row">
                    {row.scenario}
                  </th>
                  <td className="px-4 py-4">{row.agents}</td>
                  <td className="px-4 py-4 text-slate-600 dark:text-zinc-400">{row.gitSeconds}</td>
                  <td className="px-4 py-4">
                    <span className="inline-flex min-w-10 items-center justify-center rounded-md border border-rose-200/80 bg-rose-50 px-2 py-0.5 text-xs font-semibold text-rose-700 dark:border-rose-500/25 dark:bg-rose-500/10 dark:text-rose-300">
                      {row.gitRetries}
                    </span>
                  </td>
                  <td className="px-4 py-4 font-semibold text-zinc-950 dark:text-white">
                    {row.gsSeconds}
                  </td>
                  <td className="px-4 py-4">
                    <span className="inline-flex min-w-8 items-center justify-center rounded-md border border-emerald-200/80 bg-emerald-50 px-2 py-0.5 text-xs font-semibold text-emerald-700 dark:border-emerald-500/25 dark:bg-emerald-500/10 dark:text-emerald-300">
                      {row.gsRetries}
                    </span>
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
      className="mx-auto w-full max-w-7xl px-4 pb-20 sm:px-6 sm:pb-24 lg:px-8"
    >
      <div className="border-t border-slate-200/90 pt-16 dark:border-white/10 sm:pt-20">
        <div className="flex flex-col justify-between gap-6 md:flex-row md:items-end">
          <div className="max-w-2xl">
            <p className="font-mono text-xs font-medium uppercase tracking-[0.18em] text-sky-600 dark:text-sky-400">
              Everything else, built in
            </p>
            <h2
              className="mt-3 text-balance text-3xl font-semibold tracking-[-0.035em] text-zinc-950 dark:text-white sm:text-4xl"
              id="features-heading"
            >
              One system, from the edit to the merge.
            </h2>
            <p className="mt-3 text-pretty text-base leading-7 text-slate-600 dark:text-zinc-400">
              Source, review, CI checks, secrets, triggers, and Git mirrors share one graph — so
              nothing drifts between disconnected tools.
            </p>
          </div>
          <Link
            className="inline-flex shrink-0 items-center gap-1.5 text-sm font-medium text-zinc-900 underline decoration-slate-300 underline-offset-4 hover:decoration-zinc-900 dark:text-zinc-100 dark:decoration-zinc-600 dark:hover:decoration-zinc-100"
            to="/doc"
          >
            Explore all docs <span aria-hidden="true">→</span>
          </Link>
        </div>

        <div className="mt-10 grid gap-4 md:grid-cols-2 lg:grid-cols-3">
          {features.map((feature) => (
            <article
              className="group flex flex-col justify-between rounded-2xl border border-slate-200/90 bg-white p-6 transition hover:border-slate-300 hover:shadow-sm dark:border-white/10 dark:bg-zinc-900/45 dark:hover:border-white/20 dark:hover:bg-zinc-900/80"
              key={feature.title}
            >
              <div>
                <div className="flex h-10 w-10 items-center justify-center rounded-xl border border-sky-200 bg-sky-50 text-sky-700 transition group-hover:border-sky-300 group-hover:bg-sky-100/80 dark:border-sky-400/20 dark:bg-sky-400/[0.08] dark:text-sky-300 dark:group-hover:border-sky-400/35">
                  <FeatureIcon name={feature.icon} />
                </div>
                <h3 className="mt-5 text-base font-semibold tracking-tight text-zinc-950 dark:text-white">
                  {feature.title}
                </h3>
                <p className="mt-2.5 text-pretty text-sm leading-6 text-slate-600 dark:text-zinc-400">
                  {feature.description}
                </p>
              </div>
              <div className="mt-5 pt-2">
                <Link
                  className="inline-flex items-center gap-1 text-xs font-semibold text-slate-600 transition hover:text-sky-700 dark:text-zinc-400 dark:hover:text-sky-300"
                  params={{ section: feature.doc }}
                  to="/doc/$section"
                >
                  {feature.docLabel}
                  <span aria-hidden="true">→</span>
                </Link>
              </div>
            </article>
          ))}
        </div>
      </div>
    </section>
  );
}

export function LandingCtaSection() {
  const promptId = useId();

  useEffect(() => {
    ensureCopyCommandListener();
  }, []);

  const copyAttrs = {
    command: "--copy",
    commandfor: promptId
  } as Record<string, string>;

  return (
    <section
      aria-labelledby="cta-heading"
      className="mx-auto w-full max-w-7xl px-4 pb-20 sm:px-6 sm:pb-28 lg:px-8"
    >
      <div className="overflow-hidden rounded-2xl border border-slate-200/90 bg-white dark:border-white/10 dark:bg-zinc-900/60">
        <div className="grid divide-y divide-slate-200/90 dark:divide-white/10 lg:grid-cols-2 lg:divide-x lg:divide-y-0">
          <div className="p-7 sm:p-10">
            <p className="font-mono text-xs font-medium uppercase tracking-[0.18em] text-sky-600 dark:text-sky-400">
              For teams &amp; engineers
            </p>
            <h2
              className="mt-3 text-balance text-2xl font-semibold tracking-[-0.03em] text-zinc-950 dark:text-white sm:text-3xl"
              id="cta-heading"
            >
              Start with your home slice in minutes.
            </h2>
            <p className="mt-3 max-w-lg text-pretty text-sm leading-6 text-slate-600 dark:text-zinc-400">
              Import an existing Git repo with <code className="font-mono text-xs">gs import</code>,
              keep external Git mirrors up to date automatically, and enforce CI checks on your own
              runners.
            </p>
            <div className="mt-6 flex flex-wrap items-center gap-3">
              <Link
                className="inline-flex min-h-11 items-center justify-center rounded-full bg-zinc-950 px-5 py-2.5 text-sm font-semibold text-white outline-none transition hover:bg-zinc-800 focus-visible:ring-2 focus-visible:ring-sky-500 focus-visible:ring-offset-2 focus-visible:ring-offset-white dark:bg-white dark:text-zinc-950 dark:hover:bg-zinc-200 dark:focus-visible:ring-sky-400 dark:focus-visible:ring-offset-zinc-900"
                reloadDocument
                to="/signup"
              >
                Create free account
              </Link>
              <Link
                className="inline-flex min-h-11 items-center justify-center rounded-full border border-slate-300 bg-white px-5 py-2.5 text-sm font-semibold text-zinc-800 outline-none transition hover:border-slate-400 hover:bg-slate-50 focus-visible:ring-2 focus-visible:ring-sky-500 focus-visible:ring-offset-2 focus-visible:ring-offset-white dark:border-white/15 dark:bg-white/[0.04] dark:text-zinc-200 dark:hover:border-white/25 dark:hover:bg-white/[0.08] dark:focus-visible:ring-sky-400 dark:focus-visible:ring-offset-zinc-900"
                to="/doc"
              >
                Start Here guide
              </Link>
            </div>
          </div>

          <div className="bg-slate-50/70 p-7 dark:bg-zinc-950/60 sm:p-10">
            <p className="font-mono text-xs font-medium uppercase tracking-[0.18em] text-emerald-700 dark:text-emerald-400">
              For coding agents
            </p>
            <h3 className="mt-3 text-balance text-2xl font-semibold tracking-[-0.03em] text-zinc-950 dark:text-white sm:text-3xl">
              Hand your agent one prompt.
            </h3>
            <p className="mt-3 max-w-lg text-pretty text-sm leading-6 text-slate-600 dark:text-zinc-400">
              No browser needed. Your agent installs <code className="font-mono text-xs">gs</code>,
              registers an account under your email, and sends you a claim link for co-ownership.
            </p>
            <div className="mt-5 flex items-center gap-2 rounded-xl border border-slate-200 bg-white px-3.5 py-2.5 dark:border-white/10 dark:bg-zinc-900">
              <code
                className="min-w-0 flex-1 overflow-x-auto whitespace-nowrap font-mono text-xs text-zinc-800 dark:text-zinc-200"
                id={promptId}
              >
                Read https://gitslice.io/llms.txt, install gs, and register yourself under my email.
              </code>
              <span className="relative inline-flex shrink-0 items-center">
                <button
                  aria-label="Copy agent prompt"
                  className="landing-copy-btn group inline-flex min-h-7 min-w-7 items-center justify-center rounded-md border border-slate-200 bg-slate-50 px-1.5 py-1 text-slate-600 transition hover:border-slate-300 hover:bg-slate-100 hover:text-zinc-950 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-sky-500 dark:border-white/10 dark:bg-zinc-800 dark:text-zinc-300 dark:hover:border-white/20 dark:hover:text-white"
                  type="button"
                  {...copyAttrs}
                >
                  <svg
                    aria-hidden="true"
                    className="h-3.5 w-3.5 group-data-[copied]:hidden"
                    fill="none"
                    stroke="currentColor"
                    strokeLinecap="round"
                    strokeLinejoin="round"
                    strokeWidth="1.75"
                    viewBox="0 0 24 24"
                  >
                    <rect height="14" rx="2" width="14" x="8" y="8" />
                    <path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2" />
                  </svg>
                  <svg
                    aria-hidden="true"
                    className="hidden h-3.5 w-3.5 text-emerald-600 group-data-[copied]:block dark:text-emerald-400"
                    fill="none"
                    stroke="currentColor"
                    strokeLinecap="round"
                    strokeLinejoin="round"
                    strokeWidth="2"
                    viewBox="0 0 24 24"
                  >
                    <polyline points="20 6 9 17 4 12" />
                  </svg>
                </button>
                <span
                  className="landing-copy-tip absolute right-0 top-full z-10 mt-1 whitespace-nowrap rounded bg-zinc-950 px-2 py-0.5 text-[11px] font-medium text-white shadow dark:bg-zinc-100 dark:text-zinc-950"
                  role="status"
                />
              </span>
            </div>
            <div className="mt-4 flex flex-wrap items-center gap-4 text-xs font-medium">
              <a
                className="text-zinc-800 underline decoration-slate-300 underline-offset-4 hover:decoration-zinc-800 dark:text-zinc-200 dark:decoration-zinc-600 dark:hover:decoration-zinc-200"
                href="/llms.txt"
              >
                Read /llms.txt
              </a>
              <Link
                className="text-zinc-800 underline decoration-slate-300 underline-offset-4 hover:decoration-zinc-800 dark:text-zinc-200 dark:decoration-zinc-600 dark:hover:decoration-zinc-200"
                params={{ section: "agents" }}
                to="/doc/$section"
              >
                Agents &amp; MCP guide
              </Link>
            </div>
          </div>
        </div>
      </div>
    </section>
  );
}
