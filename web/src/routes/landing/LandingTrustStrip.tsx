import { Link } from "@tanstack/react-router";

import { GITHUB_MIRROR_URL } from "../../lib/siteLinks";
import { LANDING_SOURCE_SLICE } from "./landingData";

export function LandingTrustStrip() {
  return (
    <section
      aria-label="Platform guarantees"
      className="mx-auto w-full max-w-7xl px-4 pb-16 sm:px-6 sm:pb-20 lg:px-8"
    >
      <div className="grid grid-cols-1 gap-4 rounded-2xl border border-slate-200/90 bg-white/90 p-5 shadow-sm dark:border-white/10 dark:bg-zinc-900/55 sm:grid-cols-2 lg:grid-cols-4 lg:gap-0 lg:divide-x lg:divide-slate-200/80 lg:p-0 dark:lg:divide-white/10">
        <div className="min-w-0 lg:p-6">
          <p className="font-mono text-[11px] font-medium uppercase tracking-[0.14em] text-sky-700 dark:text-sky-400">
            Apache 2.0 Open Source
          </p>
          <p className="mt-1.5 text-sm font-semibold text-zinc-950 dark:text-white">
            Auditable &amp; self-hostable
          </p>
          <p className="mt-1 text-xs leading-5 text-slate-600 dark:text-zinc-400">
            Inspect the full server and CLI on{" "}
            <Link
              className="font-medium text-zinc-900 underline decoration-slate-300 underline-offset-2 hover:decoration-zinc-900 dark:text-zinc-200 dark:decoration-zinc-600"
              params={LANDING_SOURCE_SLICE}
              to="/slices/$account/$slice"
            >
              Gitslice
            </Link>{" "}
            or{" "}
            <a
              className="font-medium text-zinc-900 underline decoration-slate-300 underline-offset-2 hover:decoration-zinc-900 dark:text-zinc-200 dark:decoration-zinc-600"
              href={GITHUB_MIRROR_URL}
              rel="noopener noreferrer"
              target="_blank"
            >
              GitHub
            </a>
            .
          </p>
        </div>

        <div className="min-w-0 lg:p-6">
          <p className="font-mono text-[11px] font-medium uppercase tracking-[0.14em] text-emerald-700 dark:text-emerald-400">
            Zero Lock-In
          </p>
          <p className="mt-1.5 text-sm font-semibold text-zinc-950 dark:text-white">
            Mirrors to GitHub in seconds
          </p>
          <p className="mt-1 text-xs leading-5 text-slate-600 dark:text-zinc-400">
            Every landing and tag pushes automatically via fast-forward{" "}
            <Link
              className="font-medium text-zinc-900 underline decoration-slate-300 underline-offset-2 hover:decoration-zinc-900 dark:text-zinc-200 dark:decoration-zinc-600"
              params={{ section: "mirrors" }}
              to="/doc/$section"
            >
              Git mirrors
            </Link>{" "}
            and standard <code className="font-mono text-[11px]">git clone</code>.
          </p>
        </div>

        <div className="min-w-0 lg:p-6">
          <p className="font-mono text-[11px] font-medium uppercase tracking-[0.14em] text-indigo-700 dark:text-indigo-400">
            Security Built In
          </p>
          <p className="mt-1.5 text-sm font-semibold text-zinc-950 dark:text-white">
            Sealed secrets &amp; server gates
          </p>
          <p className="mt-1 text-xs leading-5 text-slate-600 dark:text-zinc-400">
            Encrypted folder secrets, host-bound egress, and admin-approved policy files.{" "}
            <Link
              className="font-medium text-zinc-900 underline decoration-slate-300 underline-offset-2 hover:decoration-zinc-900 dark:text-zinc-200 dark:decoration-zinc-600"
              to="/security"
            >
              Security details →
            </Link>
          </p>
        </div>

        <div className="min-w-0 lg:p-6">
          <p className="font-mono text-[11px] font-medium uppercase tracking-[0.14em] text-slate-600 dark:text-zinc-400">
            Dogfooded in Production
          </p>
          <p className="mt-1.5 text-sm font-semibold text-zinc-950 dark:text-white">
            Built &amp; deployed on Gitslice
          </p>
          <p className="mt-1 text-xs leading-5 text-slate-600 dark:text-zinc-400">
            Our source tree, CI runners, GitHub mirror, and production deploys all run on Gitslice
            itself.
          </p>
        </div>
      </div>
    </section>
  );
}
