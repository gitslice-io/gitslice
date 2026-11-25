import type { ReactNode } from "react";
import { Link } from "@tanstack/react-router";

export const GITHUB_MIRROR_URL = "https://github.com/gitslice-io/gitslice";
export const GITHUB_RELEASES_URL = "https://github.com/gitslice-io/gitslice/releases";
export const GITHUB_ISSUES_URL = "https://github.com/gitslice-io/gitslice/issues";
export const CONTACT_EMAIL = "contact@gitslice.io";
export const SECURITY_EMAIL = "security@gitslice.io";

interface SitePageHeaderProps {
  eyebrow: string;
  title: string;
  description: string;
  updated?: string;
}

export function SitePageHeader({
  eyebrow,
  title,
  description,
  updated
}: SitePageHeaderProps) {
  return (
    <header className="border-b border-slate-200 pb-8 pt-4 dark:border-zinc-800">
      <p className="font-mono text-xs font-medium uppercase tracking-[0.18em] text-sky-700 dark:text-sky-400">
        {eyebrow}
      </p>
      <h1 className="mt-3 text-balance text-[clamp(2rem,4.5vw,2.75rem)] font-semibold leading-[1.08] tracking-[-0.03em] text-zinc-950 dark:text-zinc-50">
        {title}
      </h1>
      <p className="mt-3 max-w-2xl text-pretty text-base leading-7 text-slate-600 dark:text-zinc-400 sm:text-lg sm:leading-8">
        {description}
      </p>
      {updated ? (
        <p className="mt-4 font-mono text-xs text-slate-500 dark:text-zinc-400">
          Last updated: {updated}
        </p>
      ) : null}
    </header>
  );
}

export function SiteSection({
  id,
  title,
  children
}: {
  id?: string;
  title: string;
  children: ReactNode;
}) {
  return (
    <section
      aria-labelledby={id}
      className="border-b border-slate-200/80 py-8 last:border-b-0 dark:border-zinc-800/80"
    >
      <h2
        className="text-xl font-semibold tracking-tight text-zinc-950 dark:text-zinc-50"
        id={id}
      >
        {title}
      </h2>
      <div className="mt-4 space-y-3 text-sm leading-7 text-slate-700 dark:text-zinc-300">
        {children}
      </div>
    </section>
  );
}

export function SiteFooterLinks() {
  const linkClass =
    "text-xs font-medium text-slate-600 underline-offset-4 transition hover:text-zinc-950 hover:underline dark:text-zinc-400 dark:hover:text-zinc-100";
  return (
    <nav
      aria-label="Trust and legal"
      className="mt-10 flex flex-wrap items-center gap-x-5 gap-y-2 border-t border-slate-200 pt-6 dark:border-zinc-800"
    >
      <Link className={linkClass} to="/security">
        Security
      </Link>
      <Link className={linkClass} to="/pricing">
        Pricing
      </Link>
      <Link className={linkClass} to="/status">
        Status
      </Link>
      <Link className={linkClass} to="/changelog">
        Changelog
      </Link>
      <Link className={linkClass} to="/about">
        About
      </Link>
      <Link className={linkClass} to="/privacy">
        Privacy
      </Link>
      <Link className={linkClass} to="/terms">
        Terms
      </Link>
    </nav>
  );
}
