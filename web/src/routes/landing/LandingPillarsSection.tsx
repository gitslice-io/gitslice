import { Link } from "@tanstack/react-router";

import { pillars } from "./landingData";

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
          <div className="grid grid-cols-1 gap-px lg:grid-cols-3">
            {pillars.map((pillar) => (
              <article
                className="flex min-w-0 flex-col justify-between bg-white p-6 dark:bg-zinc-950 sm:p-8"
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
