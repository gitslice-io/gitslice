import { Link } from "@tanstack/react-router";

import {
  RELEASES_URL,
  SiteFooterLinks,
  SitePageHeader
} from "./site/SitePageChrome";
import type { DocSection } from "./doc/docData";

interface ChangelogEntry {
  date: string;
  category: string;
  title: string;
  summary: string;
  items: string[];
  blogSlug?: string;
  docSection?: DocSection;
}

const entries: ChangelogEntry[] = [
  {
    date: "2026-11-21",
    category: "Automations",
    title: "Reusable automation templates & multi-step pipelines",
    summary:
      "Publish versioned on:[call] automation templates from any folder and chain sequential steps with typed outputs.",
    items: [
      "action.uses calls reusable templates across folders or public slices (pinned by slice tag or commit SHA) with caller-bounded secret, reveal, egress, network, and timeout ceilings.",
      "action.steps runs up to 20 sequential steps with if: success|failure|always and ${{ steps.<id>.outputs.<key> }} interpolation.",
      "Added gs auto inspect <uses-ref>, gs.setOutput / gs-auto set-output, and expandable pipeline step logs in CLI and web UI."
    ],
    blogSlug: "folder-automations",
    docSection: "automations"
  },
  {
    date: "2026-11-19",
    category: "Automations",
    title: "Folder automations, manual runbooks & ephemeral shell containers",
    summary:
      "Run event-driven workflows and audited manual runbooks with typed inputs, folder-scoped sealed secrets, and isolated containers.",
    items: [
      "Added .gitslice/automations.yaml with http, worker, bash, and check actions.",
      "Added typed manual inputs, role/human-only/confirm guards, and the web UI RunAutomationModal.",
      "Added unified folder Policy drawer combining owners, checks, automations, secrets, and CI runners."
    ],
    blogSlug: "folder-automations",
    docSection: "automations"
  },
  {
    date: "2026-11-18",
    category: "Performance",
    title: "ABFS-inspired lazy workspaces: delta syncs, bounded cache & 6x faster deletes",
    summary:
      "Improved FUSE-mounted lazy workspaces for large repositories and concurrent agent runs.",
    items: [
      "gs sync now transfers only changed files instead of full manifests.",
      "Added bounded local object caches with gs cache prune --max <size>.",
      "Reduced bulk deletion of 10,000 mounted files from 15.9 s to 2.6 s."
    ],
    blogSlug: "what-we-learned-from-abfs",
    docSection: "lazy"
  },
  {
    date: "2026-11-14",
    category: "Git Compatibility",
    title: "Automatic Slice Git Mirrors",
    summary:
      "Mirror any slice to GitHub, GitLab, or any Git host automatically on every landing and tag.",
    items: [
      "Fast-forward-only pushes prevent accidental history overwrites; diverged remotes pause safely with clear status.",
      "Natural single-folder root mapping so mirrored repositories look native on GitHub.",
      "Automatic exponential retry schedule and gs slice mirror push for immediate sync."
    ],
    blogSlug: "slice-git-mirrors",
    docSection: "mirrors"
  },
  {
    date: "2026-11-03",
    category: "Performance",
    title: "Prebuilt Git covers: cold clones in 5 seconds & single-request CI checkouts",
    summary:
      "Dramatically accelerated Git clone performance and CI archive hydration for slices.",
    items: [
      "Prebuilt compressed covers bring cold clones of gitslice/gitslice down to 5.1 s.",
      "CI checkouts hydrate the slice tree in a single request."
    ],
    blogSlug: "pando-style-git-covers",
    docSection: "git-users"
  },
  {
    date: "2026-10-13",
    category: "Agents",
    title: "Agent accounts, stdio MCP server & advisory path intents",
    summary:
      "First-class source control workflows for fleets of autonomous coding agents.",
    items: [
      "Added headless agent signup (gs auth register-agent) and owner-minted delegated agent keys.",
      "Added gs mcp stdio server for workspace-less editing and gs intent for advisory path reservations."
    ],
    blogSlug: "agent-scale-source-control",
    docSection: "agents"
  }
];

export function ChangelogPage() {
  return (
    <article className="mx-auto w-full max-w-4xl pb-16">
      <SitePageHeader
        description="Recent product releases, performance milestones, and platform updates shipped on Gitslice."
        eyebrow="Changelog"
        title="What we've shipped"
      />

      <div className="mt-6 flex flex-wrap items-center gap-4 text-xs text-slate-600 dark:text-zinc-400">
        <span>Looking for downloadable CLI release binaries and SHA-256 checksums?</span>
        <a
          className="font-medium text-sky-700 underline underline-offset-4 dark:text-sky-300"
          href={RELEASES_URL}
        >
          Browse Gitslice CLI releases →
        </a>
      </div>

      <ol className="mt-8 border-t border-slate-200 dark:border-zinc-800">
        {entries.map((entry) => (
          <li
            className="grid gap-x-10 gap-y-2 border-b border-slate-200 py-8 dark:border-zinc-800 sm:grid-cols-[9rem_1fr]"
            key={`${entry.date}-${entry.title}`}
          >
            <div className="pt-1">
              <time
                className="block font-mono text-xs font-medium tabular-nums text-slate-500 dark:text-zinc-400"
                dateTime={entry.date}
              >
                {entry.date}
              </time>
              <span className="mt-2 inline-block rounded-full border border-slate-200 bg-slate-50 px-2.5 py-0.5 text-[11px] font-medium text-slate-700 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-300">
                {entry.category}
              </span>
            </div>
            <div className="min-w-0">
              <h2 className="text-lg font-semibold tracking-tight text-zinc-950 dark:text-zinc-50 sm:text-xl">
                {entry.title}
              </h2>
              <p className="mt-2 text-sm leading-6 text-slate-600 dark:text-zinc-400">
                {entry.summary}
              </p>
              <ul className="mt-3 list-disc space-y-1.5 pl-5 text-sm leading-6 text-slate-700 dark:text-zinc-300">
                {entry.items.map((item) => (
                  <li key={item}>{item}</li>
                ))}
              </ul>
              <div className="mt-4 flex flex-wrap items-center gap-4 text-xs font-medium">
                {entry.blogSlug ? (
                  <Link
                    className="text-sky-700 underline underline-offset-4 dark:text-sky-300"
                    params={{ slug: entry.blogSlug }}
                    to="/blogs/$slug"
                  >
                    Read announcement →
                  </Link>
                ) : null}
                {entry.docSection ? (
                  <Link
                    className="text-slate-600 underline underline-offset-4 hover:text-zinc-950 dark:text-zinc-400 dark:hover:text-zinc-100"
                    params={{ section: entry.docSection }}
                    to="/doc/$section"
                  >
                    Documentation →
                  </Link>
                ) : null}
              </div>
            </div>
          </li>
        ))}
      </ol>

      <SiteFooterLinks />
    </article>
  );
}
