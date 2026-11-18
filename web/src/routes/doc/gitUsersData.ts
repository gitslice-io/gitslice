export const gitTranslations: Array<[string, string]> = [
  ["Git repository", "Slice (a named projection over one or more folders)"],
  ["git clone <url>", "gs init <account>/<slice> [dir] (or gs init --lazy for on-demand FUSE mounts)"],
  ["Working tree", "Workspace (bound to one slice, with account-rooted paths)"],
  ["Pull request", "Changeset (the review and submit unit for one slice)"],
  ["Local commit / push to PR branch", "Patchset created by gs create or updated with gs modify"],
  ["Merged commit on main", "Immutable native commit created automatically on submit"],
  ["main branch", "Latest accepted tree at refs/global/main"],
  ["git pull / git rebase", "gs sync (or enable --merge-on-submit on for non-overlapping auto-merges)"],
  ["Stacked branches", "Dependent changesets (gs deps, gs up, gs down, gs switch, gs create --root)"],
  ["git stash / git reset", "gs checkpoint, gs rewind, and gs restore (in lazy workspaces)"],
  ["git tag", "gs tag create <name> / gs tag list"],
  ["git log / git blame", "gs log / gs blame <path>"],
  ["GitHub Actions / CI config", ".gitslice/checks.yaml and .gitslice/triggers.yaml"],
  ["Clone URL", "https://gitslice.io/git/<account>/<slice>.git when Git HTTP is enabled"]
];

export const gitQuestions = [
  {
    question: "Do I still make local commits?",
    answer:
      "In normal Gitslice workflows, no. You edit files, create a changeset (gs create), update it with new patchsets if needed (gs modify), and submit it (gs submit). Gitslice creates the accepted native commit once validation passes."
  },
  {
    question: "How do I import an existing Git repository?",
    answer:
      "Run gs import <git-url-or-local-path> --mount /<account>/<project>. By default it imports the latest snapshot (--mode shallow); pass --deep (and optionally --max-commits) to preserve commit history with automatic resume if the connection drops."
  },
  {
    question: "What happens if main moves while I'm working?",
    answer:
      "Run gs sync to bring your workspace onto the latest base and merge local edits. Slice owners can also enable --merge-on-submit on so non-overlapping stale changesets are merged server-side as a fresh patchset (re-running checks and approvals) when submitted."
  },
  {
    question: "Can I use a slice as a Go module?",
    answer:
      "Yes. Any slice with one included folder is served as the Go module gitslice.io/<account>/<slice>, rooted at that folder. Declare that module path in go.mod, tag releases with gs tag create v1.0.0, and consumers can run go get gitslice.io/<account>/<slice>@v1.0.0. For private slices, add a Gitslice token to ~/.netrc for gitslice.io and set GOPRIVATE=gitslice.io/<account>/*."
  },
  {
    question: "What replaces a pull request?",
    answer:
      "A changeset. It is scoped to one authoring slice, holds immutable patchsets as you iterate, shows inline diffs, check runs, and trigger logs, and is the unit submit validation accepts or rejects."
  },
  {
    question: "Where is main?",
    answer:
      "The accepted tree lives at the native ref refs/global/main. In the web UI you will see it labeled latest or main tree."
  },
  {
    question: "Can one change touch multiple repositories?",
    answer:
      "A changeset can touch any path included in its authoring slice. Instead of coordinating multi-repo PRs, define a slice that includes all the folders you want to change atomically."
  },
  {
    question: "Can I still clone with Git or keep a GitHub backup?",
    answer:
      "Yes. Read-only Git smart-HTTP cloning works via the Clone dropdown on any slice page (git clone https://gitslice.io/git/<account>/<slice>.git), and Git Mirrors automatically fast-forward pushes to GitHub or any Git host after every landing and tag."
  }
];
