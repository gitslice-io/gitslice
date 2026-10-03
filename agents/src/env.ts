import type { Hub } from "./hub";

export interface Env {
  ARTIFACTS: Artifacts;
  AI: Ai;
  HUB: DurableObjectNamespace<Hub>;
  EVENTS: Queue<unknown>;
  // Where Gitslice lives.
  GITSLICE_API: string;
  GITSLICE_GIT: string;
  GITSLICE_WEB: string;
  ARTIFACTS_NAMESPACE: string;
  // Slices agents may open sessions on ("account/slice", comma separated).
  SLICES: string;
  // Path patterns that always need a human approval ("*" wildcards).
  PROTECTED_PATHS: string;
  // Optional shallow depth for baseline imports; empty imports full history.
  BASELINE_DEPTH?: string;
  // Slices whose agents push straight to Gitslice (refs/changes/new); the
  // Worker reviews those changesets and runs fixers for them too.
  NATIVE_SLICES?: string;
  // "off" stops fixer agents from taking over rejected changes.
  FIXER?: string;
  // Secrets.
  GITSLICE_TOKEN: string; // bridge identity: authors changesets
  GITSLICE_REVIEWER_TOKEN: string; // review identity: approves them
  AGENTS_API_KEY: string; // comma-separated keys agents present to open sessions
}
