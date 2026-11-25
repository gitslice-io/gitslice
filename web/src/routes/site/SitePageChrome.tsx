import type { ReactNode } from "react";

export const GITHUB_MIRROR_URL = "https://github.com/gitslice-io/gitslice";
export const RELEASES_URL = "/releases";
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
