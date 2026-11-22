import { useEffect, useId } from "react";
import { Link } from "@tanstack/react-router";

import { ensureCopyCommandListener } from "../../lib/copyCommand";

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
        <div className="grid grid-cols-1 divide-y divide-slate-200/90 dark:divide-white/10 lg:grid-cols-2 lg:divide-x lg:divide-y-0">
          <div className="min-w-0 p-7 sm:p-10">
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

          <div className="min-w-0 bg-slate-50/70 p-7 dark:bg-zinc-950/60 sm:p-10">
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
            <div className="mt-5 flex min-w-0 items-center gap-2 rounded-xl border border-slate-200 bg-white px-3.5 py-2.5 dark:border-white/10 dark:bg-zinc-900">
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
