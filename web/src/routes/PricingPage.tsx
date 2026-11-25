import { Link } from "@tanstack/react-router";

import {
  CONTACT_EMAIL,
  GITHUB_MIRROR_URL,
  SiteFooterLinks,
  SitePageHeader,
  SiteSection
} from "./site/SitePageChrome";

interface PlanTier {
  name: string;
  badge: string;
  price: string;
  period: string;
  summary: string;
  highlights: string[];
  ctaLabel: string;
  ctaTo?: "/signup" | "/doc";
  ctaHref?: string;
  featured?: boolean;
}

const tiers: PlanTier[] = [
  {
    name: "Personal & Open Source",
    badge: "Available now",
    price: "$0",
    period: "free during public beta",
    summary:
      "Everything individuals, coding agents, and open-source maintainers need to work in parallel.",
    highlights: [
      "Unlimited public slices and automatic Git mirrors",
      "Private personal slices and delegated agent accounts",
      "Lazy FUSE workspaces and local checkpoints",
      "Self-hosted CI runners, folder checks, and encrypted secrets",
      "Folder automations, reusable templates, and pipelines"
    ],
    ctaLabel: "Create free account",
    ctaTo: "/signup",
    featured: true
  },
  {
    name: "Team",
    badge: "Public beta — free",
    price: "$0",
    period: "during public beta",
    summary:
      "Shared organizations, fine-grained slice roles, human approval gates, and fleet-scale agent governance.",
    highlights: [
      "Organization accounts, teams, and slice-level RBAC",
      "Path owners (.gitslice/owners.yaml) and human-only approvals",
      "Shared account CI runner pools and required submit checks",
      "Audited manual runbooks with role & confirmation policies",
      "Automatic GitHub, GitLab, and Forgejo mirroring"
    ],
    ctaLabel: "Start with your team",
    ctaTo: "/signup"
  },
  {
    name: "Self-Hosted & Enterprise",
    badge: "Apache 2.0",
    price: "Custom",
    period: "or self-host free forever",
    summary:
      "Run Gitslice inside your own VPC with full source access, or talk to us about dedicated deployments and SLAs.",
    highlights: [
      "100% open-source server, CLI, and web UI (Apache 2.0)",
      "Zero vendor lock-in: standard Git clone and real-time mirrors",
      "Dedicated support, custom quotas, and security review assistance",
      "Bring your own container/VM CI runner pools"
    ],
    ctaLabel: "Contact us",
    ctaHref: `mailto:${CONTACT_EMAIL}?subject=Gitslice%20Enterprise`
  }
];

const faqs: Array<{ q: string; a: string }> = [
  {
    q: "Will free accounts lose access when paid Team plans launch?",
    a: "No. Personal and open-source usage will remain free, and we will give at least 60 days' notice before introducing paid billing on Team workspaces. You can also mirror any slice to GitHub or export via plain git clone at any time."
  },
  {
    q: "Are there hard lock-in constraints if we leave Gitslice?",
    a: "None. Every slice is cloneable with standard Git, and Slice Git Mirrors push every landing and tag to GitHub or another Git host within seconds as standard commits."
  },
  {
    q: "How are CI runners billed?",
    a: "CI checks run on your own Docker or Podman runner daemons (gs agent start --ci), so you never pay per-minute CI markups on your own hardware."
  },
  {
    q: "Can we self-host Gitslice on our own infrastructure?",
    a: "Yes. The entire Gitslice stack is licensed under Apache 2.0 and available in gitslice/gitslice and our GitHub mirror."
  }
];

export function PricingPage() {
  return (
    <article className="mx-auto w-full max-w-5xl pb-16">
      <SitePageHeader
        description="Gitslice is free to use during public beta, with zero lock-in and an Apache 2.0 open-source server you can self-host anytime."
        eyebrow="Pricing & Plans"
        title="Simple, transparent plans"
      />

      <section className="py-10">
        <div className="grid grid-cols-1 gap-5 lg:grid-cols-3">
          {tiers.map((tier) => (
            <div
              className={
                tier.featured
                  ? "flex flex-col justify-between rounded-2xl border-2 border-zinc-950 bg-white p-6 shadow-sm dark:border-zinc-100 dark:bg-zinc-900/70"
                  : "flex flex-col justify-between rounded-2xl border border-slate-200 bg-white p-6 dark:border-zinc-800 dark:bg-zinc-900/50"
              }
              key={tier.name}
            >
              <div>
                <div className="flex items-center justify-between gap-2">
                  <h2 className="text-base font-semibold text-zinc-950 dark:text-zinc-50">
                    {tier.name}
                  </h2>
                  <span className="rounded-full border border-slate-200 bg-slate-50 px-2.5 py-0.5 font-mono text-[11px] font-medium text-slate-700 dark:border-zinc-700 dark:bg-zinc-800 dark:text-zinc-300">
                    {tier.badge}
                  </span>
                </div>
                <div className="mt-4 flex items-baseline gap-2">
                  <span className="text-3xl font-semibold tracking-tight text-zinc-950 dark:text-zinc-50">
                    {tier.price}
                  </span>
                  <span className="text-xs text-slate-500 dark:text-zinc-400">
                    {tier.period}
                  </span>
                </div>
                <p className="mt-3 text-sm leading-6 text-slate-600 dark:text-zinc-400">
                  {tier.summary}
                </p>
                <ul className="mt-5 space-y-2 border-t border-slate-200/80 pt-5 text-sm text-slate-700 dark:border-zinc-800 dark:text-zinc-300">
                  {tier.highlights.map((item) => (
                    <li className="flex items-start gap-2" key={item}>
                      <span
                        aria-hidden="true"
                        className="mt-1.5 h-1.5 w-1.5 shrink-0 rounded-full bg-emerald-500"
                      />
                      <span>{item}</span>
                    </li>
                  ))}
                </ul>
              </div>

              <div className="mt-6 pt-2">
                {tier.ctaTo ? (
                  <Link
                    className={
                      tier.featured
                        ? "inline-flex min-h-11 w-full items-center justify-center rounded-full bg-zinc-950 px-4 py-2 text-sm font-semibold text-white transition hover:bg-zinc-800 dark:bg-white dark:text-zinc-950 dark:hover:bg-zinc-200"
                        : "inline-flex min-h-11 w-full items-center justify-center rounded-full border border-slate-300 bg-white px-4 py-2 text-sm font-semibold text-zinc-800 transition hover:bg-slate-50 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-100 dark:hover:bg-zinc-800"
                    }
                    reloadDocument={tier.ctaTo === "/signup"}
                    to={tier.ctaTo}
                  >
                    {tier.ctaLabel}
                  </Link>
                ) : (
                  <a
                    className="inline-flex min-h-11 w-full items-center justify-center rounded-full border border-slate-300 bg-white px-4 py-2 text-sm font-semibold text-zinc-800 transition hover:bg-slate-50 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-100 dark:hover:bg-zinc-800"
                    href={tier.ctaHref}
                  >
                    {tier.ctaLabel}
                  </a>
                )}
              </div>
            </div>
          ))}
        </div>
      </section>

      <SiteSection id="quotas" title="Public beta guidelines & portability">
        <p>
          During public beta we enforce generous fair-use rate limits rather than hard paywalls.
          If you are onboarding a large monorepo or running high-concurrency agent fleets, email{" "}
          <a
            className="font-medium text-sky-700 underline underline-offset-4 dark:text-sky-300"
            href={`mailto:${CONTACT_EMAIL}`}
          >
            {CONTACT_EMAIL}
          </a>{" "}
          or inspect the self-hosting docs on{" "}
          <a
            className="font-medium text-sky-700 underline underline-offset-4 dark:text-sky-300"
            href={GITHUB_MIRROR_URL}
            rel="noopener noreferrer"
            target="_blank"
          >
            GitHub
          </a>
          .
        </p>
      </SiteSection>

      <SiteSection id="faq" title="Frequently asked questions">
        <div className="divide-y divide-slate-200 rounded-xl border border-slate-200 bg-white dark:divide-zinc-800 dark:border-zinc-800 dark:bg-zinc-900/50">
          {faqs.map((faq) => (
            <details className="group px-5 py-4" key={faq.q} name="pricing-faq">
              <summary className="cursor-pointer list-none font-medium text-zinc-950 outline-none focus-visible:ring-2 focus-visible:ring-sky-500 dark:text-zinc-50">
                <span className="flex items-center justify-between gap-4">
                  <span>{faq.q}</span>
                  <span
                    aria-hidden="true"
                    className="shrink-0 font-mono text-xs text-slate-400 transition group-open:rotate-45 dark:text-zinc-500"
                  >
                    +
                  </span>
                </span>
              </summary>
              <p className="mt-3 text-sm leading-6 text-slate-600 dark:text-zinc-400">
                {faq.a}
              </p>
            </details>
          ))}
        </div>
      </SiteSection>

      <SiteFooterLinks />
    </article>
  );
}
