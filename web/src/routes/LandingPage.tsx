import { useEffect, useId } from "react";
import { Link } from "@tanstack/react-router";

import { BrandMark } from "../components/BrandMark";
import { ThemeToggle } from "../components/ThemeToggle";
import { ensureCopyCommandListener } from "../lib/copyCommand";
import { LandingHeroTerminal } from "./LandingHeroTerminal";
import {
  LANDING_SOURCE_SLICE,
  LandingArchitectureSection,
  LandingBenchmarksSection,
  LandingCtaSection,
  LandingFeaturesSection,
  LandingPillarsSection
} from "./LandingSections";

const contactEmail = "contact@gitslice.io";

const navLinkClass =
  "rounded-full px-2.5 py-2 text-sm font-medium text-slate-600 outline-none transition hover:bg-slate-200/70 hover:text-zinc-950 active:-translate-y-px focus-visible:ring-2 focus-visible:ring-sky-500 focus-visible:ring-offset-2 focus-visible:ring-offset-slate-50 dark:text-zinc-300 dark:hover:bg-white/[0.06] dark:hover:text-white dark:focus-visible:ring-sky-400 dark:focus-visible:ring-offset-zinc-950 sm:px-3.5";

// Padding and an equal negative margin: a 32px target in a line of text.
const footerLinkClass =
  "-my-1.5 inline-block rounded-md py-1.5 text-slate-600 outline-none transition hover:text-zinc-950 focus-visible:ring-2 focus-visible:ring-sky-500 focus-visible:ring-offset-2 focus-visible:ring-offset-slate-50 dark:text-zinc-400 dark:hover:text-white dark:focus-visible:ring-sky-400 dark:focus-visible:ring-offset-zinc-950";

export function LandingPage() {
  const installId = useId();

  useEffect(() => {
    ensureCopyCommandListener();
  }, []);

  const installCopyAttrs = {
    command: "--copy",
    commandfor: installId
  } as Record<string, string>;

  return (
    <div className="relative min-h-[100dvh] overflow-x-clip bg-slate-50 font-sans text-zinc-900 transition-colors duration-200 dark:bg-zinc-950 dark:text-zinc-100">
      <div
        aria-hidden="true"
        className="landing-grid pointer-events-none absolute inset-0 opacity-60 dark:opacity-35"
        style={{ backgroundSize: "56px 56px" }}
      />
      <div
        aria-hidden="true"
        className="landing-sheen pointer-events-none absolute inset-x-0 top-0 h-[38rem]"
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
            className="flex items-center gap-1 sm:gap-1.5"
          >
            <ThemeToggle inverted />
            <Link className={`hidden sm:inline-flex ${navLinkClass}`} to="/doc">
              Docs
            </Link>
            <Link
              className={`hidden min-[420px]:inline-flex ${navLinkClass}`}
              hash="agent-signup"
              params={{ section: "agents" }}
              to="/doc/$section"
            >
              For agents
            </Link>
            <Link
              className={`hidden md:inline-flex ${navLinkClass}`}
              params={LANDING_SOURCE_SLICE}
              to="/slices/$account/$slice"
            >
              Source
            </Link>
            <Link className={`hidden sm:inline-flex ${navLinkClass}`} to="/blogs">
              Blog
            </Link>
            <Link className={navLinkClass} reloadDocument to="/login">
              Sign in
            </Link>
            <Link
              className="rounded-full bg-zinc-950 px-3.5 py-2 text-sm font-semibold text-white outline-none transition hover:bg-zinc-800 active:-translate-y-px focus-visible:ring-2 focus-visible:ring-sky-500 focus-visible:ring-offset-2 focus-visible:ring-offset-slate-50 dark:bg-white dark:text-zinc-950 dark:hover:bg-zinc-200 dark:focus-visible:ring-sky-400 dark:focus-visible:ring-offset-zinc-950 sm:px-4"
              reloadDocument
              to="/signup"
            >
              Sign up
            </Link>
          </nav>
        </div>
      </header>

      <main className="relative">
        <section className="mx-auto grid w-full max-w-7xl items-center gap-12 px-4 pb-20 pt-14 sm:px-6 sm:pb-24 sm:pt-20 lg:grid-cols-[minmax(0,1.02fr)_minmax(26rem,0.98fr)] lg:gap-14 lg:px-8 lg:pb-28 lg:pt-24">
          <div>
            <div className="mb-6 inline-flex items-center gap-2 rounded-full border border-slate-200/90 bg-white/90 px-3 py-1.5 text-xs font-medium text-slate-600 shadow-sm dark:border-white/10 dark:bg-white/[0.04] dark:text-zinc-300">
              <span className="h-1.5 w-1.5 rounded-full bg-emerald-500 dark:bg-emerald-400" />
              Source infrastructure for fleets of agents
            </div>

            <h1 className="max-w-2xl text-balance text-4xl font-semibold leading-[1.03] tracking-[-0.045em] text-zinc-950 dark:text-white sm:text-5xl md:text-[3.5rem]">
              Many agents. One codebase. Land in parallel.
            </h1>

            <p className="mt-6 max-w-xl text-pretty text-base leading-7 text-slate-600 dark:text-zinc-400 sm:text-lg sm:leading-8">
              Gitslice is Git-compatible source infrastructure where every agent gets a focused
              workspace for the task,{" "}
              <strong className="font-medium text-zinc-900 dark:text-zinc-100">
                lands changes without stepping on each other
              </strong>
              , and the server — not the agent — enforces checks and approvals.
            </p>

            <div className="mt-8 grid grid-cols-2 gap-3 sm:flex sm:flex-wrap">
              <Link
                className="col-span-2 inline-flex min-h-11 items-center justify-center whitespace-nowrap rounded-full bg-zinc-950 px-5 py-2.5 text-sm font-semibold text-white outline-none transition hover:bg-zinc-800 active:-translate-y-px focus-visible:ring-2 focus-visible:ring-sky-500 focus-visible:ring-offset-2 focus-visible:ring-offset-slate-50 dark:bg-white dark:text-zinc-950 dark:hover:bg-zinc-200 dark:focus-visible:ring-sky-400 dark:focus-visible:ring-offset-zinc-950"
                reloadDocument
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
                className="inline-flex min-h-11 items-center justify-center whitespace-nowrap rounded-full border border-slate-300 bg-white px-5 py-2.5 text-sm font-semibold text-zinc-800 outline-none transition hover:border-slate-400 hover:bg-slate-50 active:-translate-y-px focus-visible:ring-2 focus-visible:ring-sky-500 focus-visible:ring-offset-2 focus-visible:ring-offset-slate-50 dark:border-white/15 dark:bg-white/[0.04] dark:text-zinc-200 dark:hover:border-white/25 dark:hover:bg-white/[0.08] dark:focus-visible:ring-sky-400 dark:focus-visible:ring-offset-zinc-950"
                to="/doc"
              >
                Read the docs
              </Link>
              <Link
                className="inline-flex min-h-11 items-center justify-center whitespace-nowrap rounded-full border border-sky-300/90 bg-sky-50 px-5 py-2.5 text-sm font-semibold text-sky-900 outline-none transition hover:border-sky-400 hover:bg-sky-100 active:-translate-y-px focus-visible:ring-2 focus-visible:ring-sky-500 focus-visible:ring-offset-2 focus-visible:ring-offset-slate-50 dark:border-sky-500/30 dark:bg-sky-500/10 dark:text-sky-200 dark:hover:border-sky-400/50 dark:hover:bg-sky-500/20 dark:focus-visible:ring-sky-400 dark:focus-visible:ring-offset-zinc-950"
                hash="agent-signup"
                params={{ section: "agents" }}
                to="/doc/$section"
              >
                For agents
              </Link>
            </div>

            <div className="mt-7 max-w-xl">
              <div className="flex items-center gap-2 rounded-xl border border-slate-200/90 bg-white px-3.5 py-2.5 shadow-sm dark:border-white/10 dark:bg-zinc-900/80">
                <span aria-hidden="true" className="select-none font-mono text-xs text-sky-600 dark:text-sky-400">
                  $
                </span>
                <code
                  className="min-w-0 flex-1 overflow-x-auto whitespace-nowrap font-mono text-xs text-zinc-800 dark:text-zinc-200"
                  id={installId}
                >
                  curl -fsSL https://gitslice.io/install.sh | sh
                </code>
                <span className="relative inline-flex shrink-0 items-center">
                  <button
                    aria-label="Copy install command"
                    className="landing-copy-btn group inline-flex min-h-7 min-w-7 items-center justify-center rounded-md border border-slate-200 bg-slate-50 px-1.5 py-1 text-slate-600 transition hover:border-slate-300 hover:bg-slate-100 hover:text-zinc-950 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-sky-500 dark:border-white/10 dark:bg-zinc-800 dark:text-zinc-300 dark:hover:border-white/20 dark:hover:text-white"
                    type="button"
                    {...installCopyAttrs}
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

              <div className="mt-3.5 flex flex-wrap items-center gap-x-4 gap-y-1.5 text-xs text-slate-500 dark:text-zinc-400">
                <span>
                  Agent? Sign up via{" "}
                  <code className="rounded bg-slate-200/70 px-1 py-0.5 font-mono text-[11px] text-zinc-800 dark:bg-white/[0.08] dark:text-zinc-200">
                    gs auth register-agent
                  </code>{" "}
                  or{" "}
                  <a
                    className="font-medium text-zinc-800 underline decoration-slate-300 underline-offset-4 hover:decoration-zinc-800 dark:text-zinc-200 dark:decoration-zinc-600 dark:hover:decoration-zinc-200"
                    href="/llms.txt"
                  >
                    /llms.txt
                  </a>
                </span>
                <span aria-hidden="true" className="hidden text-slate-300 dark:text-zinc-700 sm:inline">
                  ·
                </span>
                <span>
                  Open source:{" "}
                  <Link
                    className="font-medium text-zinc-800 underline decoration-slate-300 underline-offset-4 hover:decoration-zinc-800 dark:text-zinc-200 dark:decoration-zinc-600 dark:hover:decoration-zinc-200"
                    params={LANDING_SOURCE_SLICE}
                    to="/slices/$account/$slice"
                  >
                    gitslice/gitslice
                  </Link>
                </span>
              </div>
            </div>
          </div>

          <LandingHeroTerminal />
        </section>

        <LandingPillarsSection />
        <LandingArchitectureSection />
        <LandingBenchmarksSection />
        <LandingFeaturesSection />
        <LandingCtaSection />
      </main>

      <footer className="relative border-t border-slate-200/90 dark:border-white/10">
        <div className="mx-auto flex min-h-20 w-full max-w-7xl flex-col items-start justify-between gap-4 px-4 py-6 text-sm text-zinc-500 min-[420px]:flex-row min-[420px]:items-center sm:px-6 lg:px-8">
          <div className="flex items-center gap-2.5">
            <BrandMark className="h-5 w-5 opacity-80" />
            <span>© 2026 Gitslice · Built on Gitslice</span>
          </div>
          <nav
            aria-label="Footer"
            className="flex flex-wrap items-center gap-x-5 gap-y-2"
          >
            <Link className={footerLinkClass} to="/doc">
              Docs
            </Link>
            <Link
              className={footerLinkClass}
              params={LANDING_SOURCE_SLICE}
              to="/slices/$account/$slice"
            >
              Source
            </Link>
            <Link className={footerLinkClass} to="/blogs">
              Blog
            </Link>
            <a className={footerLinkClass} href="/llms.txt">
              llms.txt
            </a>
            <a className={footerLinkClass} href={`mailto:${contactEmail}`}>
              {contactEmail}
            </a>
            <Link className={footerLinkClass} reloadDocument to="/login">
              Sign in
            </Link>
          </nav>
        </div>
      </footer>
    </div>
  );
}
