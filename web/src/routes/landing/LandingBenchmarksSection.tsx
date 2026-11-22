import { Link } from "@tanstack/react-router";

import { LANDING_SOURCE_SLICE, parallelRows } from "./landingData";

export function LandingBenchmarksSection() {
  return (
    <section
      aria-labelledby="benchmarks-heading"
      className="mx-auto w-full max-w-7xl px-4 pb-20 sm:px-6 sm:pb-24 lg:px-8"
    >
      <div className="grid grid-cols-1 items-start gap-10 border-t border-slate-200/90 pt-16 dark:border-white/10 sm:pt-20 lg:grid-cols-[minmax(0,0.82fr)_minmax(0,1.18fr)] lg:gap-14">
        <div className="min-w-0">
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
          className="min-w-0 overflow-x-auto rounded-2xl border border-slate-200/90 bg-white shadow-sm dark:border-white/10 dark:bg-zinc-900/50"
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
