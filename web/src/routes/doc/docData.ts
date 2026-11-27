import type { DocSection } from "../../lib/docSections";

export type { DocSection };

export interface DocParams {
  section?: string;
}

export const docSections: Array<{
  description: string;
  id: DocSection;
  title: string;
}> = [
  {
    id: "start",
    title: "Start Here",
    description: "Install gs, sign in, open your home slice, and land a first changeset."
  },
  {
    id: "concepts",
    title: "Concepts",
    description: "Accounts, slices, workspaces, changesets, roles, and native commits."
  },
  {
    id: "agents",
    title: "Agents",
    description: "Self-signup, delegated keys, safe contributor roles, MCP, and CI daemons."
  },
  {
    id: "lazy",
    title: "Lazy Workspaces",
    description: "Mount a slice in seconds, fetch files on read, checkpoint, rewind, and fork."
  },
  {
    id: "checks",
    title: "CI Checks",
    description: "Folder-scoped build and test checks in source that run per patchset and gate submit."
  },
  {
    id: "secrets",
    title: "Secrets",
    description: "Encrypted folder-scoped secrets for checks, automations, mirrors, and local env files."
  },
  {
    id: "automations",
    title: "Automations",
    description: "Run webhooks, JS workers, Cloudflare Container bash scripts, or CI checks on events or manually."
  },
  {
    id: "mirrors",
    title: "Git Mirrors",
    description: "Automatically push a slice to GitHub or any Git host after every landing and tag."
  },
  {
    id: "git-users",
    title: "For Git Users",
    description: "Map Git habits to Gitslice, import repos, and use slices as Go modules."
  },
  {
    id: "cli",
    title: "CLI Reference",
    description: "Install options, global flags, and a complete command lookup for daily gs work."
  }
];

export function docPath(section: DocSection) {
  return section === "start" ? "/doc" : `/doc/${section}`;
}

export function normalizeSection(value: string | undefined): DocSection {
  if (value === "triggers") {
    return "automations";
  }
  if (
    value === "concepts" ||
    value === "agents" ||
    value === "lazy" ||
    value === "checks" ||
    value === "secrets" ||
    value === "automations" ||
    value === "mirrors" ||
    value === "git-users" ||
    value === "cli"
  ) {
    return value;
  }
  return "start";
}
