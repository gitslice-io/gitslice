export type DocSection =
  | "start"
  | "concepts"
  | "agents"
  | "lazy"
  | "checks"
  | "secrets"
  | "triggers"
  | "mirrors"
  | "git-users"
  | "cli";

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
    description: "Sign in, open your home slice, make a first changeset."
  },
  {
    id: "concepts",
    title: "Concepts",
    description: "How accounts, slices, workspaces, changesets, and commits fit."
  },
  {
    id: "agents",
    title: "Agents",
    description: "Let an agent sign itself up, and claim it, without a browser."
  },
  {
    id: "lazy",
    title: "Lazy Workspaces",
    description: "Mount a slice in seconds, fetch files as they are read, checkpoint and rewind."
  },
  {
    id: "checks",
    title: "CI Checks",
    description: "Define build/test checks that run on every patchset and gate submit."
  },
  {
    id: "secrets",
    title: "Secrets",
    description: "Give checks tokens and keys without committing them, and fill a local env file."
  },
  {
    id: "triggers",
    title: "Triggers",
    description: "Run an HTTP call or a script when files in a folder change, and report checks that gate submit."
  },
  {
    id: "mirrors",
    title: "Git Mirrors",
    description: "Back up a slice to GitHub or any Git host: pushed after every landing and tag."
  },
  {
    id: "git-users",
    title: "For Git Users",
    description: "Map Git habits to Gitslice without learning internals first."
  },
  {
    id: "cli",
    title: "CLI Reference",
    description: "A compact command lookup for daily gs work."
  }
];

export function docPath(section: DocSection) {
  return section === "start" ? "/doc" : `/doc/${section}`;
}

export function normalizeSection(value: string | undefined): DocSection {
  if (
    value === "concepts" ||
    value === "agents" ||
    value === "lazy" ||
    value === "checks" ||
    value === "secrets" ||
    value === "triggers" ||
    value === "mirrors" ||
    value === "git-users" ||
    value === "cli"
  ) {
    return value;
  }
  return "start";
}
