import { Link } from "@tanstack/react-router";

import { docSections, docPath, type DocSection } from "./docData";

export function CommandBlock({ children }: { children: string }) {
  return (
    <pre className="mt-3 overflow-x-auto rounded-md border border-slate-200 dark:border-zinc-800 bg-slate-50 dark:bg-zinc-950 px-3 py-2 font-mono text-xs leading-5 text-slate-700 dark:text-zinc-300">
      <code className="whitespace-pre">{children}</code>
    </pre>
  );
}

export function SectionLink({
  active,
  section
}: {
  active: boolean;
  section: (typeof docSections)[number];
}) {
  return (
    <Link
      className={[
        "block rounded-md border px-4 py-3 text-left transition active:scale-[0.98]",
        active
          ? "border-zinc-950 dark:border-zinc-100 bg-zinc-950 dark:bg-zinc-100 text-white dark:text-zinc-950"
          : "border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 text-slate-700 dark:text-zinc-300 hover:bg-slate-50 dark:hover:bg-zinc-950 hover:text-zinc-950 dark:hover:text-zinc-50"
      ].join(" ")}
      to={docPath(section.id)}
    >
      <span className="block text-sm font-semibold">{section.title}</span>
      <span
        className={[
          "mt-1 block text-xs leading-5",
          active ? "text-slate-300 dark:text-zinc-500" : "text-slate-500 dark:text-zinc-400"
        ].join(" ")}
      >
        {section.description}
      </span>
    </Link>
  );
}

export function PageHeader({
  eyebrow,
  title,
  description
}: {
  description: string;
  eyebrow: string;
  title: string;
}) {
  return (
    <header className="border-b border-slate-200 dark:border-zinc-800 pb-5">
      <p className="text-xs font-semibold uppercase tracking-normal text-slate-500 dark:text-zinc-400">
        {eyebrow}
      </p>
      <h1 className="mt-2 text-xl font-semibold tracking-normal text-zinc-950 dark:text-zinc-50 sm:text-2xl">
        {title}
      </h1>
      <p className="mt-2 max-w-3xl text-sm leading-6 text-slate-600 dark:text-zinc-400">
        {description}
      </p>
    </header>
  );
}

export const inlineCode =
  "rounded bg-slate-50 dark:bg-zinc-950 px-1.5 py-0.5 font-mono text-xs text-slate-700 dark:text-zinc-300";

export function NextDocCard({
  description,
  section,
  title
}: {
  description: string;
  section: DocSection;
  title: string;
}) {
  return (
    <Link
      className="rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-5 transition hover:bg-slate-50 dark:hover:bg-zinc-950 active:scale-[0.98]"
      to={docPath(section)}
    >
      <h2 className="text-base font-semibold text-zinc-950 dark:text-zinc-50">{title}</h2>
      <p className="mt-2 text-sm leading-6 text-slate-600 dark:text-zinc-400">{description}</p>
    </Link>
  );
}
