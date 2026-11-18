export const conceptCards = [
  {
    title: "Accounts and paths",
    description:
      "Every path lives under a globally unique account slug, such as /nic or /acme. Personal accounts and organizations share the same flat namespace — account kind is metadata, not a path prefix."
  },
  {
    title: "Slices",
    description:
      "A slice is a named projection over one or more folders in the global source graph. It defines included paths, visibility (private or public), member roles, required checks, approver groups, and optional Git mirrors. CLI commands accept account/slice and account:slice interchangeably."
  },
  {
    title: "Workspaces",
    description:
      "A workspace is bound to one slice and places files at their full account-rooted paths (for example, ~/work/payments/acme/payments/...). Use gs init for a full checkout or gs init --lazy for an on-demand FUSE mount."
  },
  {
    title: "Changesets and patchsets",
    description:
      "A changeset is the review and landing unit for one authoring slice (cross-slice changesets are intentionally disallowed). Each update via gs modify uploads a new immutable patchset, keeping revision history and re-running applicable checks."
  },
  {
    title: "Dependent changesets",
    description:
      "When work splits cleanly into stages, changesets can stack on one another inside a slice. Inspect and move through a stack with gs deps, gs up, gs down, gs switch, or start a fresh independent changeset with gs create --root."
  },
  {
    title: "Submit validation and native commits",
    description:
      "Before a changeset lands, the server enforces required CI checks, approval counts, human-approval rules, and slice path boundaries. Once accepted, Gitslice publishes an immutable native commit on refs/global/main."
  }
];

export const lifecycleSteps: Array<[string, string]> = [
  ["Draft", "Iterate locally and upload patchsets with gs create and gs modify."],
  ["Pending publish", "Submit validation passed; queued to land on main."],
  ["Submitted", "Included in the accepted tree as a native commit."],
  ["Abandoned", "Closed without landing via gs cs abandon; history stays inspectable."]
];

export const sliceRoles: Array<[string, string]> = [
  ["reader", "Read files, inspect history, and clone via Git HTTP when enabled."],
  [
    "contributor",
    "Propose changesets and upload patchsets. CI checks run without secrets until a writer or admin allows the patchset."
  ],
  ["writer", "Submit changesets, approve reviews, create tags, and run CI with secrets."],
  ["admin", "Everything a writer can do, plus manage slice members (slice settings, CI daemon, and secrets remain with account owners and admins)."]
];
