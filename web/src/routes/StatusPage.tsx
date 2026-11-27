import { useQuery } from "@tanstack/react-query";

import { statusHealthQuery } from "../queries/status";
import {
  CONTACT_EMAIL,
  GITHUB_ISSUES_URL,
  SitePageHeader,
  SiteSection
} from "./site/SitePageChrome";

type ProbeState = "checking" | "operational" | "degraded";

const components: Array<{ name: string; detail: string; dependsOnApi?: boolean }> = [
  {
    name: "Core API & Source Graph",
    detail: "Changesets, slices, commits, path admission, and RPC endpoints (api.gitslice.io).",
    dependsOnApi: true
  },
  {
    name: "Web Application & Docs",
    detail: "Source browsing, diff review, folder policy, releases, and documentation (gitslice.io)."
  },
  {
    name: "Git HTTP Protocol & Mirrors",
    detail: "Standard git clone/fetch endpoints, Go module serving, and automatic Slice Git Mirrors.",
    dependsOnApi: true
  },
  {
    name: "Folder Automations & Dispatch",
    detail: "Event hooks, sandboxed worker runs, ephemeral bash containers, and multi-step pipelines.",
    dependsOnApi: true
  }
];

export function StatusPage() {
  const healthQuery = useQuery(statusHealthQuery());

  const probe: ProbeState = healthQuery.isFetching
    ? "checking"
    : healthQuery.isError
      ? "degraded"
      : healthQuery.data?.overall === "operational"
        ? "operational"
        : "degraded";
  const latencyMs = healthQuery.isError ? null : (healthQuery.data?.api.latencyMs ?? null);
  const checkedAt =
    healthQuery.data?.checkedAt ??
    (healthQuery.errorUpdatedAt ? new Date(healthQuery.errorUpdatedAt).toISOString() : "");
  const runProbe = () => {
    void healthQuery.refetch();
  };

  const bannerTone =
    probe === "operational"
      ? "border-emerald-200 bg-emerald-50 text-emerald-900 dark:border-emerald-900/60 dark:bg-emerald-950/35 dark:text-emerald-200"
      : probe === "degraded"
        ? "border-amber-200 bg-amber-50 text-amber-900 dark:border-amber-900/60 dark:bg-amber-950/35 dark:text-amber-200"
        : "border-slate-200 bg-white text-slate-700 dark:border-zinc-800 dark:bg-zinc-900/60 dark:text-zinc-300";

  const dotTone =
    probe === "operational"
      ? "bg-emerald-500"
      : probe === "degraded"
        ? "bg-amber-500"
        : "animate-pulse bg-slate-400";

  const headline =
    probe === "operational"
      ? "All core systems operational"
      : probe === "degraded"
        ? "API health check reported a degraded state"
        : "Checking live API health…";

  return (
    <article className="mx-auto w-full max-w-4xl pb-16">
      <SitePageHeader
        description="Live health check for api.gitslice.io and operational status across Gitslice platform services."
        eyebrow="System Status"
        title="Gitslice Status"
      />

      <div
        aria-live="polite"
        className={`mt-8 flex flex-wrap items-center justify-between gap-3 rounded-xl border px-5 py-4 ${bannerTone}`}
      >
        <div className="flex items-center gap-3">
          <span aria-hidden="true" className={`h-2.5 w-2.5 shrink-0 rounded-full ${dotTone}`} />
          <span className="text-sm font-semibold">{headline}</span>
        </div>
        <div className="flex flex-wrap items-center gap-3">
          <span className="font-mono text-xs opacity-80">
            {latencyMs !== null ? `api.gitslice.io/readyz · ${latencyMs} ms` : null}
            {checkedAt ? `${latencyMs !== null ? " · " : ""}${checkedAt}` : null}
          </span>
          <button
            className="rounded-md border border-current/25 px-2.5 py-1 text-xs font-semibold transition hover:bg-black/5 disabled:opacity-50 dark:hover:bg-white/10"
            disabled={probe === "checking"}
            onClick={() => runProbe()}
            type="button"
          >
            Refresh
          </button>
        </div>
      </div>

      <section className="border-b border-slate-200/80 py-8 dark:border-zinc-800/80">
        <h2 className="text-xl font-semibold tracking-tight text-zinc-950 dark:text-zinc-50">
          Services
        </h2>
        <ul className="mt-4 divide-y divide-slate-200 overflow-hidden rounded-xl border border-slate-200 bg-white dark:divide-zinc-800 dark:border-zinc-800 dark:bg-zinc-900/50">
          {components.map((component) => {
            const degraded = probe === "degraded" && component.dependsOnApi;
            const checking = probe === "checking" && component.dependsOnApi;
            return (
              <li
                className="flex flex-col justify-between gap-2 px-5 py-4 sm:flex-row sm:items-center"
                key={component.name}
              >
                <div className="min-w-0">
                  <p className="text-sm font-semibold text-zinc-950 dark:text-zinc-50">
                    {component.name}
                  </p>
                  <p className="mt-0.5 text-xs text-slate-600 dark:text-zinc-400">
                    {component.detail}
                  </p>
                </div>
                <span
                  className={
                    degraded
                      ? "inline-flex shrink-0 items-center gap-1.5 font-mono text-xs font-medium text-amber-700 dark:text-amber-300"
                      : checking
                        ? "inline-flex shrink-0 items-center gap-1.5 font-mono text-xs font-medium text-slate-500 dark:text-zinc-400"
                        : "inline-flex shrink-0 items-center gap-1.5 font-mono text-xs font-medium text-emerald-700 dark:text-emerald-300"
                  }
                >
                  <span
                    aria-hidden="true"
                    className={
                      degraded
                        ? "h-1.5 w-1.5 rounded-full bg-amber-500"
                        : checking
                          ? "h-1.5 w-1.5 animate-pulse rounded-full bg-slate-400"
                          : "h-1.5 w-1.5 rounded-full bg-emerald-500"
                    }
                  />
                  {degraded ? "Degraded" : checking ? "Checking…" : "Operational"}
                </span>
              </li>
            );
          })}
        </ul>
      </section>

      <SiteSection id="diagnostics" title="Verify from your terminal">
        <p>
          You can check end-to-end CLI authentication, API connectivity, and workspace health at
          any time with:
        </p>
        <pre
          aria-label="Run gs doctor"
          className="overflow-x-auto rounded-lg border border-slate-200 bg-slate-950 p-3.5 font-mono text-xs text-slate-100 dark:border-zinc-800"
          role="region"
          tabIndex={0}
        >
          <code>{`curl -fsS https://api.gitslice.io/readyz\ngs doctor`}</code>
        </pre>
        <p>
          Experiencing an issue? Email{" "}
          <a
            className="font-medium text-sky-700 underline underline-offset-4 dark:text-sky-300"
            href={`mailto:${CONTACT_EMAIL}`}
          >
            {CONTACT_EMAIL}
          </a>{" "}
          or open a report on{" "}
          <a
            className="font-medium text-sky-700 underline underline-offset-4 dark:text-sky-300"
            href={GITHUB_ISSUES_URL}
            rel="noopener noreferrer"
            target="_blank"
          >
            GitHub Issues
          </a>
          .
        </p>
      </SiteSection>
    </article>
  );
}
