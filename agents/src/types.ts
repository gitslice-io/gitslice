// Shared shapes for sessions, baselines and the events the dashboard streams.

export type Status =
  | "forked" // the agent has its Artifacts repo
  | "pushed" // a push is being turned into a changeset
  | "reviewing" // the changeset exists; the review agent is reading it
  | "approved" // review passed; submitting
  | "landing" // submitted; waiting for Gitslice to publish
  | "landed" // part of the slice's history
  | "merging" // another agent landed on the same file; merging line by line
  | "conflict" // overlapping edits; the agent must rework on a newer base
  | "needs-human" // escalated by policy or the reviewer
  | "changes-requested" // the reviewer asked the agent for changes
  | "failed";

export interface Baseline {
  repo: string; // Artifacts repo the sessions fork
  gitCommit: string; // projected Git commit at its head
  nativeCommit: string; // the Gitslice commit it stands for
  tree: string; // Git tree of that commit
  createdAt: number;
}

export type Json = string | number | boolean | null | Json[] | { [key: string]: Json };

export interface Signal {
  at: number;
  kind: "info" | "overlap" | "review" | "merged" | "conflict" | "escalated" | "landed" | "error";
  message: string;
  data?: Json;
}

export interface ReviewSummary {
  verdict: string;
  summary: string;
  concerns: string[];
  model: string;
  ms: number;
}

export interface Session {
  id: string; // also the Artifacts repo name
  agent: string;
  task: string;
  account: string;
  slice: string;
  remote: string;
  baseline: Baseline;
  intent: string[]; // paths the agent said it would touch
  touched: string[]; // paths its latest push changed (global, "/acct/...")
  status: Status;
  changesetId?: string;
  handle?: string;
  patchsetId?: string;
  pushes: number;
  lastCommit?: string;
  landedCommit?: string;
  autoMerged?: boolean;
  review?: ReviewSummary;
  signals: Signal[];
  resumedFrom?: string; // the conflicted session this one reworks
  supersededBy?: string;
  fixerFor?: string; // the session this fixer agent repairs (it forks that session's repo)
  fixedBy?: string; // the fixer session assigned to this one ("pending" until it forks)
  fixAt?: number; // when a fixer is sent if the agent has not pushed again
  retries?: number; // times a stalled landing was queued again
  createdAt: number;
  updatedAt: number;
  pushedAt?: number;
  landedAt?: number;
}

export interface Stats {
  agents: number;
  sessions: number;
  forks: number;
  baselines: number;
  pushes: number;
  changesets: number;
  reviewed: number;
  landed: number;
  autoMerged: number;
  conflicts: number;
  escalated: number;
  fixes: number;
  inFlight: number;
  medianLandMs: number | null;
}

export interface HubEvent {
  at: number;
  session?: string;
  agent?: string;
  kind: string;
  message: string;
}
