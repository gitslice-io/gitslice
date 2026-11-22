import type { DocSection } from "../doc/docData";

// Gitslice is developed on Gitslice.
export const LANDING_SOURCE_SLICE = { account: "gitslice", slice: "gitslice" } as const;

export type FeatureIconName =
  | "slice"
  | "changeset"
  | "agent"
  | "check"
  | "git"
  | "terminal";

export const features: Array<{
  description: string;
  doc: DocSection;
  docLabel: string;
  icon: FeatureIconName;
  title: string;
}> = [
  {
    title: "Merge on submit",
    description:
      "When main moves while an agent works, the server merges non-overlapping edits onto the latest base and reruns CI instead of making the agent start over. Conflicting files are still rejected cleanly by path.",
    icon: "changeset",
    doc: "concepts",
    docLabel: "Submit lifecycle"
  },
  {
    title: "Slices",
    description:
      "Repository-like views over one global source graph. Give each team or agent only the folders it owns — with its own visibility, roles, approver groups, and rules — without splitting history apart.",
    icon: "slice",
    doc: "concepts",
    docLabel: "Core concepts"
  },
  {
    title: "Lazy workspaces",
    description:
      "Mount a slice with FUSE and fetch files on first read so bandwidth tracks the task, not repo size. Save local checkpoints to rewind or fork side by side before uploading a single edit.",
    icon: "terminal",
    doc: "lazy",
    docLabel: "Lazy workspaces"
  },
  {
    title: "Checks the server enforces",
    description:
      "Commit folder-scoped checks, triggers, and encrypted secrets next to your code. CI runs on your own Docker or Podman daemons, contributor patchsets run secretless until allowed, and submit blocks until required checks pass.",
    icon: "check",
    doc: "checks",
    docLabel: "CI checks & triggers"
  },
  {
    title: "Built for agents",
    description:
      "Agents register without a browser or receive owner-minted keys, work over the gs CLI or stdio MCP, reserve paths with advisory intents, and leave an immutable audit trail of patchsets.",
    icon: "agent",
    doc: "agents",
    docLabel: "Agents guide"
  },
  {
    title: "Git compatible",
    description:
      "Import existing repositories, clone slices with plain Git, serve single-path slices as native Go modules, and mirror every landing and tag out to external Git hosts automatically.",
    icon: "git",
    doc: "mirrors",
    docLabel: "Git mirrors & CLI"
  }
];

// Measured by tests/cli/bench_agents_test.go (design/28_benchmarks.md,
// "Landing in parallel"): agents landing at once, medians, 2-vCPU VM.
export const parallelRows: Array<{
  agents: number;
  gitRetries: number;
  gitSeconds: string;
  gsRetries: number;
  gsSeconds: string;
  scenario: string;
}> = [
  {
    scenario: "Each agent edits its own file",
    agents: 8,
    gitSeconds: "1.4 s",
    gitRetries: 28,
    gsSeconds: "1.2 s",
    gsRetries: 0
  },
  {
    scenario: "Each agent edits its own file",
    agents: 16,
    gitSeconds: "5.3 s",
    gitRetries: 120,
    gsSeconds: "2.4 s",
    gsRetries: 0
  },
  {
    scenario: "One file, a block each (merge on submit)",
    agents: 8,
    gitSeconds: "1.4 s",
    gitRetries: 28,
    gsSeconds: "1.8 s",
    gsRetries: 0
  },
  {
    scenario: "One file, a block each (merge on submit)",
    agents: 16,
    gitSeconds: "5.4 s",
    gitRetries: 120,
    gsSeconds: "3.5 s",
    gsRetries: 0
  }
];

export const pillars: Array<{
  body: string;
  doc?: DocSection;
  docLabel?: string;
  index: string;
  stat: string;
  statLabel: string;
  title: string;
}> = [
  {
    index: "01",
    title: "Land in parallel",
    stat: "0 retries",
    statLabel: "16 agents, one codebase",
    body: "Git moves the branch under every agent that lands, so each loser pulls, rebases, and pushes again. Gitslice admits changes by path, so agents on different files never wait on each other — and same-file edits merge on submit."
  },
  {
    index: "02",
    title: "A workspace for the task",
    doc: "lazy",
    docLabel: "How lazy workspaces work",
    stat: "2.6 s",
    statLabel: "to mount 4,508 files, cold",
    body: "Mount a slice and start. Only the files an agent reads are fetched, so cost follows the task instead of repository size; tree walks are read ahead automatically. Local checkpoints let you rewind or fork approaches cheaply."
  },
  {
    index: "03",
    title: "The server decides",
    doc: "checks",
    docLabel: "How server checks work",
    stat: "Enforced",
    statLabel: "checks and approvals, on the server",
    body: "An agent can claim tests passed. What lands is decided where the commit is made: path by path, against required checks, human approval gates, and contributor secret isolation, with every revision kept as an immutable patchset."
  }
];
