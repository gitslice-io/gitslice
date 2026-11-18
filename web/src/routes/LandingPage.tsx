import { Link } from "@tanstack/react-router";

import { BrandMark } from "../components/BrandMark";
import { ThemeToggle } from "../components/ThemeToggle";
import {
  LANDING_SOURCE_SLICE,
  LandingBenchmarksSection,
  LandingFeaturesSection,
  LandingPillarsSection
} from "./LandingSections";

const contactEmail = "contact@gitslice.io";

// Padding and an equal negative margin: a 32px target in a line of text.
const footerLinkClass =
  "-my-1.5 inline-block rounded-md py-1.5 text-slate-600 outline-none transition hover:text-zinc-950 focus-visible:ring-2 focus-visible:ring-sky-500 focus-visible:ring-offset-2 focus-visible:ring-offset-slate-50 dark:text-zinc-400 dark:hover:text-white dark:focus-visible:ring-sky-400 dark:focus-visible:ring-offset-zinc-950";

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
              params={LANDING_SOURCE_SLICE}
              to="/slices/$account/$slice"
            >
              Source
            </Link>
            <Link
              className="hidden rounded-full px-2.5 py-2 text-sm font-medium text-slate-600 outline-none transition hover:bg-slate-200/70 hover:text-zinc-950 active:-translate-y-px focus-visible:ring-2 focus-visible:ring-sky-500 focus-visible:ring-offset-2 focus-visible:ring-offset-slate-50 dark:text-zinc-300 dark:hover:bg-white/[0.06] dark:hover:text-white dark:focus-visible:ring-sky-400 dark:focus-visible:ring-offset-zinc-950 sm:inline-flex sm:px-4"
              to="/blogs"
            >
              Blog
            </Link>
            <Link
              className="rounded-full px-2.5 py-2 text-sm font-medium text-slate-600 outline-none transition hover:bg-slate-200/70 hover:text-zinc-950 active:-translate-y-px focus-visible:ring-2 focus-visible:ring-sky-500 focus-visible:ring-offset-2 focus-visible:ring-offset-slate-50 dark:text-zinc-300 dark:hover:bg-white/[0.06] dark:hover:text-white dark:focus-visible:ring-sky-400 dark:focus-visible:ring-offset-zinc-950 sm:px-4"
              reloadDocument
              to="/login"
            >
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
            <div className="mt-9 grid grid-cols-2 gap-3 sm:flex">
              <Link
                className="col-span-2 inline-flex min-h-11 items-center justify-center whitespace-nowrap rounded-full bg-zinc-950 px-4 py-2.5 sm:px-5 text-sm font-semibold text-white outline-none transition hover:bg-zinc-800 active:-translate-y-px focus-visible:ring-2 focus-visible:ring-sky-500 focus-visible:ring-offset-2 focus-visible:ring-offset-slate-50 dark:bg-white dark:text-zinc-950 dark:hover:bg-zinc-200 dark:focus-visible:ring-sky-400 dark:focus-visible:ring-offset-zinc-950"
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
                className="inline-flex min-h-11 items-center justify-center whitespace-nowrap rounded-full border border-slate-300 bg-white/70 px-4 py-2.5 sm:px-5 text-sm font-semibold text-zinc-700 outline-none transition hover:border-slate-400 hover:bg-white active:-translate-y-px focus-visible:ring-2 focus-visible:ring-sky-500 focus-visible:ring-offset-2 focus-visible:ring-offset-slate-50 dark:border-white/10 dark:bg-white/[0.04] dark:text-zinc-200 dark:hover:border-white/20 dark:hover:bg-white/[0.08] dark:focus-visible:ring-sky-400 dark:focus-visible:ring-offset-zinc-950"
                to="/doc"
              >
                Read the docs
              </Link>
              <Link
                className="inline-flex min-h-11 items-center justify-center whitespace-nowrap rounded-full border border-sky-300 bg-sky-50 px-4 py-2.5 sm:px-5 text-sm font-semibold text-sky-900 outline-none transition hover:border-sky-400 hover:bg-sky-100 active:-translate-y-px focus-visible:ring-2 focus-visible:ring-sky-500 focus-visible:ring-offset-2 focus-visible:ring-offset-slate-50 dark:border-sky-900/70 dark:bg-sky-950/40 dark:text-sky-200 dark:hover:border-sky-700 dark:hover:bg-sky-950/70 dark:focus-visible:ring-sky-400 dark:focus-visible:ring-offset-zinc-950"
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
                params={LANDING_SOURCE_SLICE}
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

        <LandingPillarsSection />
        <LandingBenchmarksSection />
        <LandingFeaturesSection />
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
              params={LANDING_SOURCE_SLICE}
              to="/slices/$account/$slice"
            >
              Source
            </Link>
            <Link className={footerLinkClass} to="/blogs">
              Blog
            </Link>
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
