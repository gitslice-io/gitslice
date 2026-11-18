import { useEffect, useId, type ReactNode } from "react";
import { Link } from "@tanstack/react-router";

import { ensureCopyCommandListener } from "../../lib/copyCommand";
import { docSections, docPath, type DocSection } from "./docData";

export function CommandBlock({
  children,
  label = "command"
}: {
  children: string;
  label?: string;
}) {
  const codeId = useId();

  useEffect(() => {
    ensureCopyCommandListener();
  }, []);

  const commandAttrs = {
    command: "--copy",
    commandfor: codeId
  } as Record<string, string>;

  return (
    <div className="doc-copyable mt-3">
      <span className="doc-copy-wrap inline-flex items-center">
        <button
          aria-label={`Copy ${label}`}
          className="doc-copy-button group inline-flex min-h-6 min-w-6 items-center justify-center rounded border border-slate-200 dark:border-zinc-700 bg-white/95 dark:bg-zinc-900/95 px-1.5 py-1 text-slate-600 dark:text-zinc-300 shadow-sm hover:border-slate-300 dark:hover:border-zinc-600 hover:text-zinc-950 dark:hover:text-zinc-50 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-sky-600"
          type="button"
          {...commandAttrs}
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
            className="hidden h-3.5 w-3.5 text-emerald-600 dark:text-emerald-400 group-data-[copied]:block"
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
          className="doc-copy-tip absolute right-0 top-full z-10 mt-1 whitespace-nowrap rounded bg-zinc-950 dark:bg-zinc-100 px-2 py-0.5 text-[11px] font-medium text-white dark:text-zinc-950 shadow"
          role="status"
        />
      </span>
      <pre
        className="min-w-0 flex-1 overflow-x-auto rounded-md border border-slate-200 dark:border-zinc-800 bg-slate-50 dark:bg-zinc-950 px-3 py-2.5 pr-11 font-mono text-xs leading-5 text-slate-700 dark:text-zinc-300"
        tabIndex={0}
      >
        <code className="whitespace-pre" id={codeId}>
          {children}
        </code>
      </pre>
    </div>
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
      aria-current={active ? "page" : undefined}
      className={[
        "block rounded-md border px-3 py-1.5 text-left transition active:scale-[0.98] lg:px-4 lg:py-3",
        active
          ? "border-zinc-950 dark:border-zinc-100 bg-zinc-950 dark:bg-zinc-100 text-white dark:text-zinc-950"
          : "border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 text-slate-700 dark:text-zinc-300 hover:bg-slate-50 dark:hover:bg-zinc-950 hover:text-zinc-950 dark:hover:text-zinc-50"
      ].join(" ")}
      to={docPath(section.id)}
    >
      <span className="block text-sm font-semibold">{section.title}</span>
      <span
        className={[
          "mt-1 hidden text-xs leading-5 lg:block",
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

export function InlineCode({ children }: { children: string }) {
  return <code className={inlineCode}>{children}</code>;
}

export function DocSectionCard({
  children,
  id,
  title
}: {
  children: ReactNode;
  id?: string;
  title: string;
}) {
  return (
    <section
      className="mt-8 rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-5"
      id={id}
    >
      <h2 className="text-base font-semibold text-zinc-950 dark:text-zinc-50">{title}</h2>
      <div className="mt-3 space-y-3 text-sm leading-6 text-slate-600 dark:text-zinc-400">
        {children}
      </div>
    </section>
  );
}

export function BulletPoint({
  children,
  label
}: {
  children: ReactNode;
  label: ReactNode;
}) {
  return (
    <li>
      <span className="font-semibold text-zinc-950 dark:text-zinc-50">{label}</span> {children}
    </li>
  );
}

export function DocLink({
  children,
  section
}: {
  children: ReactNode;
  section: DocSection;
}) {
  return (
    <Link
      className="font-medium text-zinc-950 underline decoration-slate-300 underline-offset-4 hover:decoration-zinc-950 dark:text-zinc-50 dark:decoration-zinc-600 dark:hover:decoration-zinc-50"
      to={docPath(section)}
    >
      {children}
    </Link>
  );
}

export function DocTable({
  ariaLabel,
  firstColMono = false,
  head,
  rows
}: {
  ariaLabel: string;
  firstColMono?: boolean;
  head: [string, string];
  rows: Array<[ReactNode, ReactNode]>;
}) {
  return (
    <div
      aria-label={ariaLabel}
      className="mt-4 overflow-x-auto"
      role="region"
      tabIndex={0}
    >
      <table className="min-w-full divide-y divide-slate-200 dark:divide-zinc-800 text-left text-sm">
        <thead className="bg-slate-50 dark:bg-zinc-950 text-xs font-semibold uppercase tracking-normal text-slate-500 dark:text-zinc-400">
          <tr>
            <th className="px-4 py-3" scope="col">
              {head[0]}
            </th>
            <th className="px-4 py-3" scope="col">
              {head[1]}
            </th>
          </tr>
        </thead>
        <tbody className="divide-y divide-slate-100 dark:divide-zinc-800">
          {rows.map(([first, second], index) => (
            <tr key={typeof first === "string" ? first : index}>
              <td
                className={
                  firstColMono
                    ? "whitespace-nowrap px-4 py-3 font-mono text-xs font-medium text-zinc-950 dark:text-zinc-50"
                    : "whitespace-nowrap px-4 py-3 font-medium text-zinc-950 dark:text-zinc-50"
                }
              >
                {first}
              </td>
              <td className="px-4 py-3 text-slate-600 dark:text-zinc-400">{second}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

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
      className="block rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-5 transition hover:bg-slate-50 dark:hover:bg-zinc-950 active:scale-[0.98]"
      to={docPath(section)}
    >
      <h2 className="text-base font-semibold text-zinc-950 dark:text-zinc-50">{title}</h2>
      <p className="mt-2 text-sm leading-6 text-slate-600 dark:text-zinc-400">{description}</p>
    </Link>
  );
}
