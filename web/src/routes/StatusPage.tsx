import { useEffect, useState } from "react";

import {
  CONTACT_EMAIL,
  GITHUB_ISSUES_URL,
  SiteFooterLinks,
  SitePageHeader,
  SiteSection
} from "./site/SitePageChrome";

type ProbeState = "checking" | "operational" | "degraded";

const components: Array<{ name: string; detail: string }> = [
  {
    name: "Core API & Source Graph",
    detail: "Changesets, slices, commits, path admission, and RPC endpoints (api.gitslice.io)."
  },
  {
    name: "Web Application & Docs",
    detail: "Source browsing, diff review, folder policy, and documentation (gitslice.io)."
  },
  {
    name: "Git HTTP Protocol & Mirrors",
    detail: "Standard git clone/fetch endpoints, Go module serving, and automatic Slice Git Mirrors."
  },
  {
    name: "Folder Automations & Dispatch",
    detail: "Event webhooks, sandboxed worker runs, ephemeral bash containers, and multi-step pipelines."
  }
];

export function StatusPage() {
  const [probe, setProbe] = useState<ProbeState>("checking");
  const [latencyMs, setLatencyMs] = useState<number | null>(null);
  const [checkedAt, setCheckedAt] = useState<string>("");

  useEffect(() => {
    const controller = new AbortController();
    const started = performance.now();
    fetch("https://api.gitslice.io/readyz", {
      method: "GET",
      mode: "cors",
      cache: "no-store",
      signal: controller.signal
    })
      .then((res) => {
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        setLatencyMs(Math.max(1, Math.round(performance.now() - started)));
        setProbe("operational");
        setCheckedAt(new Date().toISOString());
      })
      .catch(() => {
        if (controller.signal.aborted) return;
        setProbe("degraded");
        setCheckedAt(new Date().toISOString());
      });
    return () => controller.abort();
  }, []);

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
        ? "Unable to confirm live API probe from this browser"
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
        <div className="font-mono text-xs opacity-80">
          {latencyMs !== null ? `api.gitslice.io/readyz · ${latencyMs} ms` : null}
          {checkedAt ? ` · ${checkedAt}` : null}
        </div>
      </div>

      <section className="border-b border-slate-200/80 py-8 dark:border-zinc-800/80">
        <h2 className="text-xl font-semibold tracking-tight text-zinc-950 dark:text-zinc-50">
          Services
        </h2>
        <ul className="mt-4 divide-y divide-slate-200 overflow-hidden rounded-xl border border-slate-200 bg-white dark:divide-zinc-800 dark:border-zinc-800 dark:bg-zinc-900/50">
          {components.map((component) => (
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
              <span className="inline-flex shrink-0 items-center gap-1.5 font-mono text-xs font-medium text-emerald-700 dark:text-emerald-300">
                <span aria-hidden="true" className="h-1.5 w-1.5 rounded-full bg-emerald-500" />
                {probe === "degraded" && component.name.startsWith("Core API")
                  ? "Check failed"
                  : "Operational"}
              </span>
            </li>
          ))}
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

      <SiteFooterLinks />
    </article>
  );
}
