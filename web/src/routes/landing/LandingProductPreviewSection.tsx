import { useId, useRef, useState, type KeyboardEvent } from "react";
import { Link } from "@tanstack/react-router";

import { cn } from "../../lib/cn";

type PreviewId = "review" | "automations" | "mirror";

const previewTabs: Array<{ id: PreviewId; label: string; subtitle: string }> = [
  {
    id: "review",
    label: "Changeset & Server Gates",
    subtitle: "Path-scoped diffs, server-enforced checks, and human approvals"
  },
  {
    id: "automations",
    label: "Folder Policy & Pipelines",
    subtitle: "Reusable on:[call] templates, sealed secrets, and step outputs"
  },
  {
    id: "mirror",
    label: "Zero-Lock-In Git Mirror",
    subtitle: "Automatic fast-forward backup to GitHub on every landing and tag"
  }
];

export function LandingProductPreviewSection() {
  const [active, setActive] = useState<PreviewId>("review");
  const baseId = useId();
  const tabRefs = useRef<Partial<Record<PreviewId, HTMLButtonElement | null>>>({});

  function handleKeyDown(event: KeyboardEvent<HTMLButtonElement>, currentIndex: number) {
    let nextIndex = -1;
    if (event.key === "ArrowRight") {
      nextIndex = (currentIndex + 1) % previewTabs.length;
    } else if (event.key === "ArrowLeft") {
      nextIndex = (currentIndex - 1 + previewTabs.length) % previewTabs.length;
    } else if (event.key === "Home") {
      nextIndex = 0;
    } else if (event.key === "End") {
      nextIndex = previewTabs.length - 1;
    }
    if (nextIndex >= 0) {
      event.preventDefault();
      const target = previewTabs[nextIndex].id;
      setActive(target);
      tabRefs.current[target]?.focus();
    }
  }

  return (
    <section
      aria-labelledby="product-preview-heading"
      className="mx-auto w-full max-w-7xl px-4 pb-20 sm:px-6 sm:pb-24 lg:px-8"
      style={{ contentVisibility: "auto", containIntrinsicSize: "auto none auto 540px" }}
    >
      <div className="border-t border-slate-200/90 pt-16 dark:border-white/10 sm:pt-20">
        <div className="flex flex-col justify-between gap-6 lg:flex-row lg:items-end">
          <div className="max-w-2xl">
            <p className="font-mono text-xs font-medium uppercase tracking-[0.18em] text-sky-600 dark:text-sky-400">
              Inside the web console
            </p>
            <h2
              className="mt-3 text-balance text-3xl font-semibold tracking-[-0.035em] text-zinc-950 dark:text-white sm:text-4xl"
              id="product-preview-heading"
            >
              Full visibility across code, checks, and deployments.
            </h2>
            <p className="mt-3 text-pretty text-base leading-7 text-slate-600 dark:text-zinc-400">
              Review agent and human changesets side by side, inspect folder-inherited policy and
              sealed secrets, and watch every landing mirror out to GitHub.
            </p>
          </div>
          <Link
            className="inline-flex shrink-0 items-center gap-1.5 text-sm font-medium text-zinc-900 underline decoration-slate-300 underline-offset-4 hover:decoration-zinc-900 dark:text-zinc-100 dark:decoration-zinc-600 dark:hover:decoration-zinc-100"
            params={{ account: "gitslice", slice: "gitslice" }}
            to="/slices/$account/$slice"
          >
            Browse live source on Gitslice <span aria-hidden="true">→</span>
          </Link>
        </div>

        <div className="mt-8 overflow-hidden rounded-2xl border border-slate-200/90 bg-white shadow-sm dark:border-white/10 dark:bg-zinc-900/70">
          <div
            aria-label="Product interface views"
            className="flex overflow-x-auto border-b border-slate-200/90 bg-slate-50/80 px-2 pt-2 dark:border-white/10 dark:bg-zinc-950/60"
            role="tablist"
          >
            {previewTabs.map((tab, idx) => {
              const selected = active === tab.id;
              return (
                <button
                  aria-controls={`${baseId}-panel-${tab.id}`}
                  aria-selected={selected}
                  className={cn(
                    "whitespace-nowrap rounded-t-lg border border-b-0 px-4 py-2.5 text-xs font-semibold outline-none transition focus-visible:ring-2 focus-visible:ring-sky-500",
                    selected
                      ? "border-slate-200/90 bg-white text-zinc-950 dark:border-white/10 dark:bg-zinc-900 dark:text-white"
                      : "border-transparent text-slate-600 hover:text-zinc-950 dark:text-zinc-400 dark:hover:text-zinc-100"
                  )}
                  id={`${baseId}-tab-${tab.id}`}
                  key={tab.id}
                  onClick={() => setActive(tab.id)}
                  onKeyDown={(e) => handleKeyDown(e, idx)}
                  ref={(el) => {
                    tabRefs.current[tab.id] = el;
                  }}
                  role="tab"
                  tabIndex={selected ? 0 : -1}
                  type="button"
                >
                  {tab.label}
                </button>
              );
            })}
          </div>

          <div
            aria-labelledby={`${baseId}-tab-review`}
            className="p-5 sm:p-7"
            hidden={active !== "review"}
            id={`${baseId}-panel-review`}
            role="tabpanel"
          >
            <div className="grid grid-cols-1 gap-6 lg:grid-cols-[minmax(0,1.25fr)_minmax(0,0.75fr)]">
              <div className="min-w-0 rounded-xl border border-slate-200 bg-slate-50/70 p-4 dark:border-zinc-800 dark:bg-zinc-950/70">
                <div className="flex flex-wrap items-center justify-between gap-2 border-b border-slate-200 pb-3 dark:border-zinc-800">
                  <div className="min-w-0">
                    <p className="font-mono text-xs text-slate-500 dark:text-zinc-400">
                      cs/7f3a91c2 · patchset 2 · author: deploy-agent (agent)
                    </p>
                    <p className="mt-0.5 text-sm font-semibold text-zinc-950 dark:text-zinc-50">
                      billing: enforce idempotency key on retry webhook
                    </p>
                  </div>
                  <span className="rounded-full border border-emerald-200 bg-emerald-50 px-2.5 py-0.5 font-mono text-[11px] font-semibold text-emerald-800 dark:border-emerald-900/60 dark:bg-emerald-950/40 dark:text-emerald-300">
                    Ready to land
                  </span>
                </div>
                <div className="mt-3 space-y-1.5 font-mono text-xs">
                  <div className="flex items-center justify-between rounded-md bg-white px-3 py-2 dark:bg-zinc-900">
                    <span className="truncate text-zinc-800 dark:text-zinc-200">
                      /acme/billing/internal/webhooks/retry.go
                    </span>
                    <span className="shrink-0 text-emerald-700 dark:text-emerald-400">+42 -6</span>
                  </div>
                  <div className="flex items-center justify-between rounded-md bg-white px-3 py-2 dark:bg-zinc-900">
                    <span className="truncate text-zinc-800 dark:text-zinc-200">
                      /acme/billing/internal/webhooks/retry_test.go
                    </span>
                    <span className="shrink-0 text-emerald-700 dark:text-emerald-400">+68 -0</span>
                  </div>
                  <div className="flex items-center justify-between rounded-md bg-white px-3 py-2 dark:bg-zinc-900">
                    <span className="truncate text-zinc-800 dark:text-zinc-200">
                      /acme/billing/.gitslice/automations.yaml
                    </span>
                    <span className="shrink-0 text-amber-700 dark:text-amber-300">
                      admin approval required
                    </span>
                  </div>
                </div>
              </div>

              <div className="min-w-0 space-y-3">
                <div className="rounded-xl border border-slate-200 bg-white p-4 dark:border-zinc-800 dark:bg-zinc-950/70">
                  <p className="font-mono text-[11px] font-semibold uppercase tracking-wide text-slate-500 dark:text-zinc-400">
                    Server admission gates
                  </p>
                  <ul className="mt-3 space-y-2 text-xs">
                    {[
                      ["billing/test", "Passed · 4.2 s"],
                      ["billing/vet", "Passed · 1.1 s"],
                      ["Human admin approval (.gitslice/**)", "Approved by @elena"],
                      ["Merge on submit", "Disjoint paths · 0 retries"]
                    ].map(([label, state]) => (
                      <li className="flex items-center justify-between gap-2" key={label}>
                        <span className="text-zinc-800 dark:text-zinc-200">{label}</span>
                        <span className="font-mono text-emerald-700 dark:text-emerald-400">
                          {state}
                        </span>
                      </li>
                    ))}
                  </ul>
                </div>
              </div>
            </div>
          </div>

          <div
            aria-labelledby={`${baseId}-tab-automations`}
            className="p-5 sm:p-7"
            hidden={active !== "automations"}
            id={`${baseId}-panel-automations`}
            role="tabpanel"
          >
            <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
              <div className="min-w-0 rounded-xl border border-slate-200 bg-slate-50/70 p-4 dark:border-zinc-800 dark:bg-zinc-950/70">
                <div className="flex items-center justify-between gap-2">
                  <p className="font-mono text-xs font-semibold text-zinc-950 dark:text-zinc-50">
                    Folder Policy · /acme/services/billing
                  </p>
                  <span className="rounded border border-indigo-200 bg-indigo-50 px-2 py-0.5 font-mono text-[11px] font-medium text-indigo-800 dark:border-indigo-900/60 dark:bg-indigo-950/40 dark:text-indigo-200">
                    Reusable templates
                  </span>
                </div>
                <ul className="mt-3 space-y-2 font-mono text-xs">
                  <li className="rounded-lg border border-slate-200 bg-white px-3 py-2.5 dark:border-zinc-800 dark:bg-zinc-900">
                    <div className="flex items-center justify-between gap-2">
                      <span className="font-semibold text-zinc-950 dark:text-zinc-50">
                        billing/release
                      </span>
                      <span className="text-slate-500 dark:text-zinc-400">on manual · 3 steps</span>
                    </div>
                    <p className="mt-1 text-[11px] text-slate-600 dark:text-zinc-400">
                      secrets: DEPLOY_TOKEN (sealed · api.example.com) · require_human: true
                    </p>
                  </li>
                  <li className="rounded-lg border border-slate-200 bg-white px-3 py-2.5 dark:border-zinc-800 dark:bg-zinc-900">
                    <div className="flex items-center justify-between gap-2">
                      <span className="font-semibold text-zinc-950 dark:text-zinc-50">
                        platform/deploy-service@v1.0.0
                      </span>
                      <span className="text-indigo-700 dark:text-indigo-300">on call</span>
                    </div>
                    <p className="mt-1 text-[11px] text-slate-600 dark:text-zinc-400">
                      Pinned immutable slice tag · caller-bounded ceilings enforced
                    </p>
                  </li>
                </ul>
              </div>

              <div className="min-w-0 rounded-xl border border-slate-200 bg-slate-950 p-4 font-mono text-xs text-slate-200 dark:border-zinc-800">
                <p className="text-[11px] font-semibold uppercase tracking-wide text-slate-400">
                  Pipeline run · trun_534b3739 (succeeded)
                </p>
                <ol className="mt-3 space-y-2">
                  <li className="flex flex-wrap items-center justify-between gap-2 rounded-md bg-zinc-900 px-3 py-2">
                    <span>1. build (bash · go)</span>
                    <span className="text-emerald-400">outputs image=v1.8.4</span>
                  </li>
                  <li className="flex flex-wrap items-center justify-between gap-2 rounded-md bg-zinc-900 px-3 py-2">
                    <span>2. deploy (uses platform/deploy-service@v1.0.0)</span>
                    <span className="text-emerald-400">succeeded</span>
                  </li>
                  <li className="flex flex-wrap items-center justify-between gap-2 rounded-md bg-zinc-900 px-3 py-2">
                    <span>3. notify (if: always · http)</span>
                    <span className="text-emerald-400">delivered · 200 OK</span>
                  </li>
                </ol>
              </div>
            </div>
          </div>

          <div
            aria-labelledby={`${baseId}-tab-mirror`}
            className="p-5 sm:p-7"
            hidden={active !== "mirror"}
            id={`${baseId}-panel-mirror`}
            role="tabpanel"
          >
            <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
              <div className="min-w-0 rounded-xl border border-slate-200 bg-slate-50/70 p-4 dark:border-zinc-800 dark:bg-zinc-950/70">
                <p className="font-mono text-xs font-semibold text-zinc-950 dark:text-zinc-50">
                  Slice Git Mirror · acme/billing
                </p>
                <dl className="mt-3 grid grid-cols-1 gap-2 text-xs sm:grid-cols-2">
                  <div className="rounded-lg border border-slate-200 bg-white p-3 dark:border-zinc-800 dark:bg-zinc-900">
                    <dt className="text-slate-500 dark:text-zinc-400">Remote repository</dt>
                    <dd className="mt-1 truncate font-mono font-medium text-zinc-950 dark:text-zinc-50">
                      github.com/acme/billing.git
                    </dd>
                  </div>
                  <div className="rounded-lg border border-slate-200 bg-white p-3 dark:border-zinc-800 dark:bg-zinc-900">
                    <dt className="text-slate-500 dark:text-zinc-400">Push policy</dt>
                    <dd className="mt-1 font-mono font-medium text-emerald-700 dark:text-emerald-400">
                      Fast-forward only · never force-pushes
                    </dd>
                  </div>
                  <div className="rounded-lg border border-slate-200 bg-white p-3 dark:border-zinc-800 dark:bg-zinc-900">
                    <dt className="text-slate-500 dark:text-zinc-400">Last synced</dt>
                    <dd className="mt-1 font-mono font-medium text-zinc-950 dark:text-zinc-50">
                      9 s after submit · sha256:c23d6547
                    </dd>
                  </div>
                  <div className="rounded-lg border border-slate-200 bg-white p-3 dark:border-zinc-800 dark:bg-zinc-900">
                    <dt className="text-slate-500 dark:text-zinc-400">Credentials</dt>
                    <dd className="mt-1 font-mono font-medium text-zinc-950 dark:text-zinc-50">
                      Secret ref: GITHUB_MIRROR_TOKEN
                    </dd>
                  </div>
                </dl>
              </div>

              <div className="flex flex-col justify-center rounded-xl border border-slate-200 bg-white p-5 dark:border-zinc-800 dark:bg-zinc-950/70">
                <h3 className="text-base font-semibold text-zinc-950 dark:text-zinc-50">
                  Adopt Gitslice without betting the farm
                </h3>
                <p className="mt-2 text-sm leading-6 text-slate-600 dark:text-zinc-400">
                  Keep GitHub as an up-to-the-second backup, keep existing GitHub Actions running
                  if you want, and let anyone clone with plain <code className="font-mono text-xs">git clone</code>.
                  If a remote ever diverges, Gitslice pauses instead of overwriting history.
                </p>
                <div className="mt-4">
                  <Link
                    className="text-xs font-semibold text-sky-700 underline underline-offset-4 dark:text-sky-300"
                    params={{ section: "mirrors" }}
                    to="/doc/$section"
                  >
                    Read Git Mirrors docs →
                  </Link>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>
    </section>
  );
}
