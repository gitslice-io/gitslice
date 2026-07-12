import { Link } from "@tanstack/react-router";

import { BrandMark } from "../components/BrandMark";
import { ThemeToggle } from "../components/ThemeToggle";

type FeatureIconName =
  | "slice"
  | "changeset"
  | "agent"
  | "check"
  | "git"
  | "terminal";

const features: Array<{
  description: string;
  icon: FeatureIconName;
  title: string;
}> = [
  {
    title: "Slices",
    description:
      "Small, policy-aware projections of one global source graph. Work with the code you need without splitting its history apart.",
    icon: "slice"
  },
  {
    title: "Changesets & patchsets",
    description:
      "A first-class unit for review and submit, with immutable patchsets that make every iteration clear and auditable.",
    icon: "changeset"
  },
  {
    title: "Built for agents",
    description:
      "Scoped workspaces keep coding agents focused, while bring-your-own-agent conversations preserve context around the work.",
    icon: "agent"
  },
  {
    title: "CI checks",
    description:
      "Required checks and approvals live with the slice. The server enforces them before changes reach the global graph.",
    icon: "check"
  },
  {
    title: "Git compatible",
    description:
      "Clone and fetch a slice with plain Git. Compatibility stays at the boundary while the native graph remains the source of truth.",
    icon: "git"
  },
  {
    title: "CLI-first",
    description:
      "Use `gs` end-to-end: authenticate, hydrate a workspace, capture edits, create a changeset, and submit from your terminal.",
    icon: "terminal"
  }
];

function FeatureIcon({ name }: { name: FeatureIconName }) {
  const common = {
    "aria-hidden": true,
    className: "h-6 w-6",
    fill: "none",
    stroke: "currentColor",
    strokeLinecap: "round" as const,
    strokeLinejoin: "round" as const,
    strokeWidth: 1.7,
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

export function LandingPage() {
  return (
    <div className="relative min-h-[100dvh] overflow-hidden bg-slate-50 font-sans text-zinc-900 transition-colors duration-200 dark:bg-zinc-950 dark:text-zinc-100">
      <div
        aria-hidden="true"
        className="landing-grid pointer-events-none absolute inset-0 opacity-70 dark:opacity-40"
        style={{
          backgroundSize: "56px 56px"
        }}
      />
      <div
        aria-hidden="true"
        className="pointer-events-none absolute left-1/2 top-0 h-[34rem] w-[46rem] -translate-x-1/2 animate-pulse rounded-full bg-sky-500/[0.08] blur-3xl"
      />

      <header className="sticky top-0 z-20 border-b border-slate-200/80 bg-slate-50/85 backdrop-blur-xl dark:border-white/10 dark:bg-zinc-950/85">
        <div className="mx-auto flex min-h-16 w-full max-w-7xl items-center justify-between gap-3 px-4 sm:px-6 lg:px-8">
          <Link
            aria-label="Gitslice home"
            className="flex items-center gap-2.5 rounded-md text-sm font-semibold tracking-tight text-zinc-950 outline-none transition hover:text-zinc-600 focus-visible:ring-2 focus-visible:ring-sky-500 focus-visible:ring-offset-2 focus-visible:ring-offset-slate-50 dark:text-white dark:hover:text-zinc-200 dark:focus-visible:ring-sky-400 dark:focus-visible:ring-offset-zinc-950"
            to="/"
          >
            <BrandMark className="h-7 w-7" />
            <span>Gitslice</span>
          </Link>
          <nav
            aria-label="Landing page"
            className="flex items-center gap-1 sm:gap-2"
          >
            <ThemeToggle inverted />
            <Link
              className="rounded-full px-2.5 py-2 text-sm font-medium text-slate-600 outline-none transition hover:bg-slate-200/70 hover:text-zinc-950 active:-translate-y-px focus-visible:ring-2 focus-visible:ring-sky-500 focus-visible:ring-offset-2 focus-visible:ring-offset-slate-50 dark:text-zinc-300 dark:hover:bg-white/[0.06] dark:hover:text-white dark:focus-visible:ring-sky-400 dark:focus-visible:ring-offset-zinc-950 sm:px-4"
              to="/login"
            >
              Sign in
            </Link>
            <Link
              className="rounded-full bg-zinc-950 px-3.5 py-2 text-sm font-semibold text-white outline-none transition hover:bg-zinc-800 active:-translate-y-px focus-visible:ring-2 focus-visible:ring-sky-500 focus-visible:ring-offset-2 focus-visible:ring-offset-slate-50 dark:bg-white dark:text-zinc-950 dark:hover:bg-zinc-200 dark:focus-visible:ring-sky-400 dark:focus-visible:ring-offset-zinc-950 sm:px-4"
              to="/login"
            >
              Get started
            </Link>
          </nav>
        </div>
      </header>

      <main className="relative">
        <section className="mx-auto grid w-full max-w-7xl items-center gap-12 px-4 pb-20 pt-16 sm:px-6 sm:pb-24 sm:pt-24 lg:grid-cols-[minmax(0,1.05fr)_minmax(25rem,0.95fr)] lg:gap-16 lg:px-8 lg:pb-32 lg:pt-28">
          <div>
            <div className="mb-7 inline-flex items-center gap-2 rounded-full border border-slate-200 bg-white/80 px-3 py-1.5 text-xs font-medium text-slate-600 shadow-[inset_0_1px_0_rgba(255,255,255,0.8)] dark:border-white/10 dark:bg-white/[0.04] dark:text-zinc-300 dark:shadow-[inset_0_1px_0_rgba(255,255,255,0.08)]">
              <span className="h-1.5 w-1.5 rounded-full bg-sky-400" />
              Source infrastructure for humans and agents
            </div>
            <h1 className="max-w-3xl text-4xl font-semibold leading-[1.02] tracking-[-0.045em] text-zinc-950 dark:text-white sm:text-5xl md:text-6xl">
              One source graph. A thousand small workspaces.
            </h1>
            <p className="mt-7 max-w-2xl text-base leading-7 text-slate-600 dark:text-zinc-400 sm:text-lg sm:leading-8">
              Gitslice gives you repo-like{" "}
              <strong className="font-medium text-zinc-800 dark:text-zinc-200">
                slices
              </strong>{" "}
              over one global codebase,{" "}
              <strong className="font-medium text-zinc-800 dark:text-zinc-200">
                changesets
              </strong>{" "}
              for review and submit, and Git compatibility at the boundary—built
              for humans and coding agents.
            </p>
            <div className="mt-9 flex flex-col gap-3 min-[380px]:flex-row">
              <Link
                className="inline-flex min-h-11 items-center justify-center rounded-full bg-zinc-950 px-5 py-2.5 text-sm font-semibold text-white outline-none transition hover:bg-zinc-800 active:-translate-y-px focus-visible:ring-2 focus-visible:ring-sky-500 focus-visible:ring-offset-2 focus-visible:ring-offset-slate-50 dark:bg-white dark:text-zinc-950 dark:hover:bg-zinc-200 dark:focus-visible:ring-sky-400 dark:focus-visible:ring-offset-zinc-950"
                to="/login"
              >
                Get started
                <svg
                  aria-hidden="true"
                  className="ml-2 h-4 w-4"
                  fill="none"
                  stroke="currentColor"
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  strokeWidth="1.8"
                  viewBox="0 0 24 24"
                >
                  <path d="M5 12h14" />
                  <path d="m13 6 6 6-6 6" />
                </svg>
              </Link>
              <Link
                className="inline-flex min-h-11 items-center justify-center rounded-full border border-slate-300 bg-white/70 px-5 py-2.5 text-sm font-semibold text-zinc-700 outline-none transition hover:border-slate-400 hover:bg-white active:-translate-y-px focus-visible:ring-2 focus-visible:ring-sky-500 focus-visible:ring-offset-2 focus-visible:ring-offset-slate-50 dark:border-white/10 dark:bg-white/[0.04] dark:text-zinc-200 dark:hover:border-white/20 dark:hover:bg-white/[0.08] dark:focus-visible:ring-sky-400 dark:focus-visible:ring-offset-zinc-950"
                to="/login"
              >
                Sign in
              </Link>
            </div>
          </div>

          <figure className="relative lg:translate-y-8">
            <div
              aria-hidden="true"
              className="absolute -inset-5 -z-10 rounded-[2rem] bg-sky-500/[0.06] blur-2xl"
            />
            <div className="overflow-hidden rounded-2xl border border-white/10 bg-zinc-900/90 shadow-2xl shadow-zinc-950/60 ring-1 ring-white/[0.04]">
              <div className="flex h-11 items-center border-b border-white/10 bg-white/[0.025] px-4">
                <div aria-hidden="true" className="flex gap-1.5">
                  <span className="h-2.5 w-2.5 rounded-full bg-zinc-600" />
                  <span className="h-2.5 w-2.5 rounded-full bg-zinc-600" />
                  <span className="h-2.5 w-2.5 rounded-full bg-zinc-600" />
                </div>
                <span className="mx-auto -translate-x-5 font-mono text-[11px] text-zinc-500">
                  ~/work/checkout
                </span>
              </div>
              <div className="min-h-[23rem] overflow-x-auto p-5 font-mono text-[12px] leading-6 sm:p-6 sm:text-[13px]">
                <p>
                  <span className="text-sky-400">$</span>{" "}
                  <span className="text-zinc-200">gs auth login</span>
                </p>
                <p className="text-zinc-500">Authenticated as mira@orbit.dev</p>
                <p className="mt-4">
                  <span className="text-sky-400">$</span>{" "}
                  <span className="text-zinc-200">
                    gs workspace init orbit/checkout
                  </span>
                </p>
                <p className="text-zinc-500">
                  Hydrated 318 files from slice orbit/checkout
                </p>
                <p className="mt-4">
                  <span className="text-sky-400">$</span>{" "}
                  <span className="text-zinc-200">gs status</span>
                </p>
                <p className="text-zinc-500">
                  <span className="text-amber-300">M</span>{" "}
                  services/checkout/authorize.go
                </p>
                <p className="mt-4">
                  <span className="text-sky-400">$</span>{" "}
                  <span className="text-zinc-200">
                    gs cs create --title &quot;Tighten authorization&quot;
                  </span>
                </p>
                <p className="text-zinc-500">Created orbit/checkout@184</p>
                <p className="mt-4">
                  <span className="text-sky-400">$</span>{" "}
                  <span className="text-zinc-200">gs cs submit</span>
                </p>
                <p className="text-zinc-500">
                  <span className="text-emerald-300">Checks 4/4</span> ·
                  submitted to global
                </p>
              </div>
            </div>
            <figcaption className="sr-only">
              An example Gitslice CLI session authenticating, creating a
              workspace, reviewing changes, and submitting a changeset.
            </figcaption>
          </figure>
        </section>

        <section
          aria-labelledby="features-heading"
          className="mx-auto w-full max-w-7xl px-4 pb-24 sm:px-6 sm:pb-32 lg:px-8"
        >
          <div className="border-t border-slate-200 pt-20 dark:border-white/10 sm:pt-24">
            <div className="grid gap-6 md:grid-cols-[0.7fr_1.3fr] md:items-end">
              <p className="font-mono text-xs font-medium uppercase tracking-[0.18em] text-sky-400">
                Native by design
              </p>
              <div>
                <h2
                  className="text-3xl font-semibold tracking-[-0.035em] text-zinc-950 dark:text-white sm:text-4xl"
                  id="features-heading"
                >
                  Small surfaces. One coherent system.
                </h2>
                <p className="mt-4 max-w-2xl text-base leading-7 text-slate-600 dark:text-zinc-400">
                  Source, policy, review, and automation share the same
                  graph—without making every developer carry the whole thing.
                </p>
              </div>
            </div>

            <div className="mt-12 grid gap-4 md:grid-cols-2 lg:gap-5">
              {features.map((feature, index) => (
                <article
                  className={`group rounded-2xl border border-slate-200 bg-white/75 p-6 shadow-[inset_0_1px_0_rgba(255,255,255,0.85)] transition duration-300 hover:-translate-y-1 hover:border-slate-300 hover:bg-white dark:border-white/10 dark:bg-white/[0.035] dark:shadow-[inset_0_1px_0_rgba(255,255,255,0.06)] dark:hover:border-white/20 dark:hover:bg-white/[0.06] sm:p-7 ${
                    index === 0 || index === 3 ? "md:translate-y-5" : ""
                  }`}
                  key={feature.title}
                >
                  <div className="flex h-11 w-11 items-center justify-center rounded-xl border border-sky-300 bg-sky-50 text-sky-700 transition group-hover:border-sky-400 group-hover:bg-sky-100 dark:border-sky-400/20 dark:bg-sky-400/[0.08] dark:text-sky-300 dark:group-hover:border-sky-400/30 dark:group-hover:bg-sky-400/[0.12]">
                    <FeatureIcon name={feature.icon} />
                  </div>
                  <h3 className="mt-6 text-lg font-semibold tracking-tight text-zinc-900 dark:text-zinc-100">
                    {feature.title}
                  </h3>
                  <p className="mt-3 max-w-xl text-sm leading-6 text-slate-600 dark:text-zinc-400">
                    {feature.description}
                  </p>
                </article>
              ))}
            </div>
          </div>
        </section>
      </main>

      <footer className="relative border-t border-slate-200 dark:border-white/10">
        <div className="mx-auto flex min-h-20 w-full max-w-7xl flex-col items-start justify-between gap-4 px-4 py-6 text-sm text-zinc-500 min-[380px]:flex-row min-[380px]:items-center sm:px-6 lg:px-8">
          <div className="flex items-center gap-2.5">
            <BrandMark className="h-5 w-5 opacity-80" />
            <span>© 2026 Gitslice</span>
          </div>
          <Link
            className="rounded-md text-slate-600 outline-none transition hover:text-zinc-950 focus-visible:ring-2 focus-visible:ring-sky-500 focus-visible:ring-offset-2 focus-visible:ring-offset-slate-50 dark:text-zinc-400 dark:hover:text-white dark:focus-visible:ring-sky-400 dark:focus-visible:ring-offset-zinc-950"
            to="/login"
          >
            Sign in
          </Link>
        </div>
      </footer>
    </div>
  );
}
