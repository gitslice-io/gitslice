import { Link } from "@tanstack/react-router";

import { BrandMark } from "./BrandMark";

const footerLinkClass =
  "rounded px-1 py-0.5 text-xs text-slate-600 outline-none transition hover:text-zinc-950 focus-visible:ring-2 focus-visible:ring-sky-500 dark:text-zinc-400 dark:hover:text-zinc-100";

export function AppFooter() {
  return (
    <footer className="border-t border-slate-200/90 bg-white/80 px-3 py-5 text-xs text-slate-500 transition-colors duration-200 dark:border-zinc-800/90 dark:bg-zinc-950/80 dark:text-zinc-400 sm:px-4 md:px-6">
      <div className="mx-auto flex w-full max-w-[100rem] flex-col items-start justify-between gap-3 sm:flex-row sm:items-center">
        <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1">
          <Link
            aria-label="Gitslice home"
            className="inline-flex items-center gap-1.5 font-semibold text-zinc-900 transition hover:text-zinc-600 dark:text-zinc-100 dark:hover:text-zinc-300"
            to="/"
          >
            <BrandMark className="size-4 opacity-85" />
            <span>Gitslice</span>
          </Link>
          <span aria-hidden="true" className="text-slate-300 dark:text-zinc-700">
            ·
          </span>
          <span>© 2026 Gitslice</span>
          <span aria-hidden="true" className="hidden text-slate-300 dark:text-zinc-700 sm:inline">
            ·
          </span>
          <span className="hidden font-mono text-[11px] sm:inline">Apache 2.0</span>
        </div>

        <nav
          aria-label="Footer"
          className="flex flex-wrap items-center gap-x-3.5 gap-y-1.5"
        >
          <Link className={footerLinkClass} to="/doc">
            Docs
          </Link>
          <a className={footerLinkClass} href="/releases">
            CLI Releases
          </a>
          <Link className={footerLinkClass} to="/changelog">
            Changelog
          </Link>
          <Link
            className={`inline-flex items-center gap-1.5 ${footerLinkClass}`}
            to="/status"
          >
            <span aria-hidden="true" className="h-1.5 w-1.5 rounded-full bg-emerald-500" />
            Status
          </Link>
          <Link className={footerLinkClass} to="/security">
            Security
          </Link>
          <Link className={footerLinkClass} to="/pricing">
            Pricing
          </Link>
          <Link className={footerLinkClass} to="/about">
            About
          </Link>
          <Link className={footerLinkClass} to="/privacy">
            Privacy
          </Link>
          <Link className={footerLinkClass} to="/terms">
            Terms
          </Link>
          <a className={footerLinkClass} href="mailto:contact@gitslice.io">
            Contact
          </a>
        </nav>
      </div>
    </footer>
  );
}
