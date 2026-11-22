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

        <div className="mt-10 grid grid-cols-1 gap-5 lg:grid-cols-2">
          <div className="min-w-0 rounded-2xl border border-slate-200/90 bg-white/85 p-6 dark:border-white/10 dark:bg-zinc-900/50 sm:p-7">
            <div className="flex flex-wrap items-center justify-between gap-2">
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
                  className="flex flex-col gap-1 rounded-lg border border-slate-200/80 bg-slate-50 px-3 py-2 dark:border-white/[0.07] dark:bg-zinc-950/70 sm:flex-row sm:items-center sm:justify-between sm:gap-3"
                  key={row.agent}
                >
                  <span className="shrink-0 text-zinc-700 dark:text-zinc-300">{row.agent}</span>
                  <span
                    className={
                      row.ok
                        ? "text-emerald-700 dark:text-emerald-300 sm:truncate"
                        : "text-rose-600 dark:text-rose-300 sm:truncate"
                    }
                  >
                    {row.state}
                  </span>
                </div>
              ))}
            </div>
          </div>

          <div className="min-w-0 rounded-2xl border border-sky-200/90 bg-white p-6 shadow-sm dark:border-sky-500/25 dark:bg-zinc-900/70 sm:p-7">
            <div className="flex flex-wrap items-center justify-between gap-2">
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
                  className="flex flex-col gap-1 rounded-lg border border-slate-200/80 bg-slate-50 px-3 py-2 dark:border-white/[0.07] dark:bg-zinc-950/70 sm:flex-row sm:items-center sm:justify-between sm:gap-3"
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
