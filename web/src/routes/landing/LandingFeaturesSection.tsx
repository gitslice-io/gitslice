import { Link } from "@tanstack/react-router";

import { features, type FeatureIconName } from "./landingData";

function FeatureIcon({ name }: { name: FeatureIconName }) {
  const common = {
    "aria-hidden": true,
    className: "h-5 w-5",
    fill: "none",
    stroke: "currentColor",
    strokeLinecap: "round" as const,
    strokeLinejoin: "round" as const,
    strokeWidth: 1.75,
    viewBox: "0 0 24 24"
  };

  if (name === "slice") {
    return (
      <svg {...common}>
        <path d="m12 3 8 4.5-8 4.5-8-4.5L12 3Z" />
        <path d="m4 12 8 4.5 8-4.5" />
        <path d="m4 16.5 8 4.5 8-4.5" />
      </svg>
    );
  }

  if (name === "changeset") {
    return (
      <svg {...common}>
        <circle cx="6" cy="5" r="2" />
        <circle cx="18" cy="19" r="2" />
        <path d="M6 7v4a4 4 0 0 0 4 4h4a4 4 0 0 1 4 4" />
        <path d="M18 3v8" />
        <path d="m15 8 3 3 3-3" />
      </svg>
    );
  }

  if (name === "agent") {
    return (
      <svg {...common}>
        <rect height="13" rx="3" width="18" x="3" y="7" />
        <path d="M12 3v4" />
        <path d="M8 12h.01" />
        <path d="M16 12h.01" />
        <path d="M8.5 16h7" />
      </svg>
    );
  }

  if (name === "check") {
    return (
      <svg {...common}>
        <path d="M12 3 4.5 6v5.2c0 4.5 3 8.3 7.5 9.8 4.5-1.5 7.5-5.3 7.5-9.8V6L12 3Z" />
        <path d="m8.5 12 2.2 2.2 4.8-5" />
      </svg>
    );
  }

  if (name === "git") {
    return (
      <svg {...common}>
        <circle cx="6" cy="5" r="2" />
        <circle cx="6" cy="19" r="2" />
        <circle cx="18" cy="12" r="2" />
        <path d="M6 7v10" />
        <path d="M8 7c0 2.8 2.2 5 5 5h3" />
      </svg>
    );
  }

  return (
    <svg {...common}>
      <rect height="16" rx="2.5" width="20" x="2" y="4" />
      <path d="m6 9 3 3-3 3" />
      <path d="M12 15h5" />
    </svg>
  );
}

export function LandingFeaturesSection() {
  return (
    <section
      aria-labelledby="features-heading"
      className="mx-auto w-full max-w-7xl px-4 pb-20 sm:px-6 sm:pb-24 lg:px-8"
    >
      <div className="border-t border-slate-200/90 pt-16 dark:border-white/10 sm:pt-20">
        <div className="flex flex-col justify-between gap-6 md:flex-row md:items-end">
          <div className="max-w-2xl">
            <p className="font-mono text-xs font-medium uppercase tracking-[0.18em] text-sky-600 dark:text-sky-400">
              Everything else, built in
            </p>
            <h2
              className="mt-3 text-balance text-3xl font-semibold tracking-[-0.035em] text-zinc-950 dark:text-white sm:text-4xl"
              id="features-heading"
            >
              One system, from the edit to the merge.
            </h2>
            <p className="mt-3 text-pretty text-base leading-7 text-slate-600 dark:text-zinc-400">
              Source, review, CI checks, secrets, triggers, and Git mirrors share one graph — so
              nothing drifts between disconnected tools.
            </p>
          </div>
          <Link
            className="inline-flex shrink-0 items-center gap-1.5 text-sm font-medium text-zinc-900 underline decoration-slate-300 underline-offset-4 hover:decoration-zinc-900 dark:text-zinc-100 dark:decoration-zinc-600 dark:hover:decoration-zinc-100"
            to="/doc"
          >
            Explore all docs <span aria-hidden="true">→</span>
          </Link>
        </div>

        <div className="mt-10 grid grid-cols-1 gap-4 md:grid-cols-2 lg:grid-cols-3">
          {features.map((feature) => (
            <article
              className="group flex min-w-0 flex-col justify-between rounded-2xl border border-slate-200/90 bg-white p-6 transition hover:border-slate-300 hover:shadow-sm dark:border-white/10 dark:bg-zinc-900/45 dark:hover:border-white/20 dark:hover:bg-zinc-900/80"
              key={feature.title}
            >
              <div>
                <div className="flex h-10 w-10 items-center justify-center rounded-xl border border-sky-200 bg-sky-50 text-sky-700 transition group-hover:border-sky-300 group-hover:bg-sky-100/80 dark:border-sky-400/20 dark:bg-sky-400/[0.08] dark:text-sky-300 dark:group-hover:border-sky-400/35">
                  <FeatureIcon name={feature.icon} />
                </div>
                <h3 className="mt-5 text-base font-semibold tracking-tight text-zinc-950 dark:text-white">
                  {feature.title}
                </h3>
                <p className="mt-2.5 text-pretty text-sm leading-6 text-slate-600 dark:text-zinc-400">
                  {feature.description}
                </p>
              </div>
              <div className="mt-5 pt-2">
                <Link
                  className="inline-flex items-center gap-1 text-xs font-semibold text-slate-600 transition hover:text-sky-700 dark:text-zinc-400 dark:hover:text-sky-300"
                  params={{ section: feature.doc }}
                  to="/doc/$section"
                >
                  {feature.docLabel}
                  <span aria-hidden="true">→</span>
                </Link>
              </div>
            </article>
          ))}
        </div>
      </div>
    </section>
  );
}
