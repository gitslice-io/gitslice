export type DocSection =
  | "start"
  | "concepts"
  | "agents"
  | "lazy"
  | "checks"
  | "webhooks"
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
    id: "webhooks",
    title: "Webhooks",
    description: "Send a slice's events to your own service: tags, landed commits, reviews, checks."
  },
  {
    id: "git-users",
    title: "For Git Users",
    description: "Map Git habits to Gitslice without learning internals first."
  },
  {
    id: "cli",
    title: "CLI Reference",
    description: "A compact command lookup for daily `gs` work."
  }
];

export const startSteps = [
  {
    title: "Install gs",
    description:
      "One command for Linux and macOS: downloads the latest prebuilt binary, verifies its checksum, and installs it to ~/.local/bin. For Windows, go install, or building from source, see the CLI Reference.",
    command: "curl -fsSL https://gitslice.io/install.sh | sh\ngs version"
  },
  {
    title: "Sign in",
    description: "Authenticate once, then use the same identity for CLI and web.",
    command: "gs auth login\ngs auth status"
  },
  {
    title: "Open your home slice",
    description:
      "Your personal account has a `home` slice rooted at /<you>. Use it for a first workflow. For a big slice, `gs init --lazy <slice> <dir>` mounts it instead of downloading everything (Linux and macOS, with FUSE); see Lazy Workspaces.",
    command: "gs init <you>:home\n# example: gs init nic:home"
  },
  {
    title: "Make a local edit",
    description: "Edit files normally, then inspect what the workspace will send.",
    command: "gs status\ngs diff"
  },
  {
    title: "Create and submit a changeset",
    description:
      "A changeset is the review and submit unit. When it lands, it is included in the accepted main tree.",
    command: 'gs create --message "my first change"\ngs submit\ngs status'
  }
];

export const conceptCards = [
  {
    title: "Accounts and paths",
    description:
      "Every source path starts under a globally unique account slug, such as /nic or /acme. Account kind is metadata, not part of the path."
  },
  {
    title: "Slices",
    description:
      "A slice is a repository-like projection over the global source graph. It defines included paths, visibility, roles, and submit settings."
  },
  {
    title: "Workspaces",
    description:
      "A workspace is bound to exactly one slice and hydrates the files needed for that scope instead of cloning the whole graph."
  },
  {
    title: "Changesets",
    description:
      "A changeset contains one or more patchsets for one authoring slice. Cross-slice changesets are intentionally not supported."
  },
  {
    title: "Submit validation",
    description:
      "Approvals, checks, path locks, and slice rules are enforced server-side before work is included in main."
  },
  {
    title: "Native commits",
    description:
      "Commits exist as immutable accepted snapshots. Users normally create changesets; the system creates a native commit when a changeset lands."
  }
];

export const gitTranslations = [
  ["Git repository", "Slice"],
  ["Pull request", "Changeset"],
  ["Commit you make locally", "Patchset/change content in a changeset"],
  ["Merged commit on main", "Native commit created when a changeset lands"],
  ["main branch", "Latest accepted tree at refs/global/main"],
  ["Working tree", "Workspace"],
  ["Clone URL", "Per-slice Git endpoint when Git HTTP is enabled"]
];

export const gitQuestions = [
  {
    question: "Do I still make commits?",
    answer:
      "In normal Gitslice work, no. You edit files, create a changeset, and submit it. The system creates the accepted native commit after submit validation passes."
  },
  {
    question: "Can I use a slice as a Go module?",
    answer:
      "Yes. A slice with one included path is the module gitslice.io/<account>/<slice>, rooted at that path: declare that module path in its go.mod, tag releases with gs tag create v1.0.0, and go get gitslice.io/<account>/<slice>@v1.0.0 works. For a private slice, put a Gitslice token in ~/.netrc for gitslice.io and set GOPRIVATE=gitslice.io/<account>/*."
  },
  {
    question: "What replaces a pull request?",
    answer:
      "A changeset. It is scoped to one authoring slice, carries patchsets, and is the unit that submit validation accepts or rejects."
  },
  {
    question: "Where is main?",
    answer:
      "The accepted tree is the native ref refs/global/main. The web UI usually calls it latest or main tree."
  },
  {
    question: "Can one change touch multiple repositories?",
    answer:
      "A changeset can only touch paths included by its authoring slice. If you need broader work, define a slice that includes the intended paths."
  },
  {
    question: "Can I clone with Git?",
    answer:
      "Yes when the deployment enables the Git smart-HTTP gateway. Use the Clone dropdown on a slice page for the concrete URL."
  }
];

export const commandGroups = [
  {
    title: "Auth",
    commands: [
      ["gs auth login", "Sign in to an account."],
      ["gs auth status", "Show the active session."],
      ["gs auth logout", "Clear local authentication."],
      ["gs auth token", "Print an auth token for local tooling."],
      [
        "gs auth register-agent --username <name> --email <owner>",
        "Agents: sign up with no browser and save an API key."
      ],
      [
        "gs auth keys create --agent <name>",
        "Owners: make a new API key for an agent that lost its key (printed once). gs auth keys list and revoke manage the rest."
      ],
      ["gs auth use-key", "Agents: sign in with a key an owner created, read from stdin or --key-file."],
      ["gs claims list", "List agents registered with your verified email."],
      ["gs claims accept <agent-id>", "Become a co-owner of an agent's account."]
    ]
  },
  {
    title: "Setup and troubleshooting",
    commands: [
      [
        "gs doctor [account/slice]",
        "Check version, server, sign-in, workspace and slice access; says how to fix each problem."
      ],
      ["gs upgrade", "Install the latest release (--check only reports). gs also says once a day when one is out."],
      [
        "gs completion bash|zsh|fish",
        "Print a shell completion script; gs completion <shell> --help says how to load it."
      ],
      [
        "gs feedback \"<message>\" --category bug|idea|docs",
        "Tell the Gitslice team what went wrong or what would help; gs feedback list --mine shows their answers."
      ]
    ]
  },
  {
    title: "Workspace and source",
    commands: [
      ["gs init <account>:<slice>", "Create a workspace for one slice: checks every file out into the current empty directory."],
      [
        "gs init --lazy <account>:<slice> [dir]",
        "Create a lazy workspace instead (Linux and macOS, with FUSE): the slice is mounted at dir in seconds (2.6 s for 4,500 files, cold), a file's content is fetched the first time something reads it (each first read is a server round trip; a walk of the whole tree, such as grep or a build, is read ahead of, and gs fetch first is fastest for work that reads everything), and your edits stay on this machine until gs create. Every other command works as in a checkout; gs sync and changeset switching move the mount without fetching. --prefetch <glob> warms files in the background; --read-only refuses writes."
      ],
      ["gs mount <dir>", "Mount a lazy workspace again after gs unmount or a reboot, with its edits."],
      ["gs unmount <dir>", "Unmount a lazy workspace; its edits are kept. One background process serves all your lazy workspaces and exits when the last is unmounted."],
      ["gs mount list", "List your lazy workspaces, mounted or not (gs mount status shows fetches and errors, gs mount discard deletes one)."],
      ["gs fetch [path...]", "Fetch a lazy workspace's files ahead of time, for offline work or a build."],
      ["gs restore <path>... | --all", "Drop local edits in a lazy workspace."],
      ["gs checkpoint [-m <message>]", "Save a lazy workspace's edits on this machine, to come back to; nothing is uploaded. gs checkpoint list shows them, newest first. gs takes one by itself before sync, switch, restore and rewind."],
      ["gs rewind [checkpoint]", "Put a lazy workspace's edits back as they were at a checkpoint (by default the newest that differs from now). What it replaces is checkpointed first, so a rewind can be undone."],
      ["gs fork <checkpoint> <dir>", "Mount a new lazy workspace showing a checkpoint, on the base it was taken on, to try a second approach side by side."],
      ["gs shell", "Open the server-backed file shell."],
      ["gs status", "Show pending workspace edits."],
      ["gs diff", "Inspect pending content changes, three lines of context around each (-U N to change)."],
      ["gs ci", "Run the slice's checks against your pending edits, locally."],
      ["gs log", "Read recent accepted history."],
      ["gs blame <path>", "Which commit and changeset last changed each line (--lines 10,20, --json)."],
      ["gs show <commit>", "Inspect an accepted native commit."]
    ]
  },
  {
    title: "File operations",
    commands: [
      ["gs fs ls <path>", "List server files."],
      ["gs fs cat <path>", "Read a server file."],
      ["gs fs upload <local> <path>", "Upload and land local content; prints the changeset link."],
      ["gs fs write <path> --text <t> --no-submit", "Open the change as a changeset for review instead of landing it."],
      ["gs fs mkdir <path>", "Create a directory."]
    ]
  },
  {
    title: "Changesets",
    commands: [
      ["gs create --message <title>", "Create a changeset from pending edits; prints its link."],
      [
        "gs create --all --message <title> --submit",
        "Create and land in one step: waits for required checks, then submits."
      ],
      [
        "gs create --message <title> --description-file <file>",
        "Give the changeset a Markdown description; the title stays one line."
      ],
      ["gs cs edit --title <title> --description <text>", "Change a draft changeset's title or description."],
      ["gs modify", "Create a new patchset on the active changeset."],
      ["gs diff", "Review workspace or changeset content."],
      ["gs submit", "Submit for validation and publish."],
      ["gs deps", "Inspect dependent changesets."]
    ]
  },
  {
    title: "Slices",
    commands: [
      ["gs slice list", "List account slices, and those shared with you."],
      ["gs slice share <slice> <username>", "Give a person or agent a role on one slice (--role reader|writer|admin)."],
      ["gs slice members <slice>", "Who the slice is shared with; gs slice unshare and gs slice leave end it."],
      ["gs slice create", "Create a slice definition."],
      ["gs slice update", "Change included paths or policy (e.g. --required-check)."],
      ["gs slice rename <slice> <new-name>", "Rename a slice. The old name keeps resolving and stays reserved."],
      ["gs slice archive <slice>", "Hide a slice from listings and make it read-only (gs slice unarchive restores it)."],
      ["gs slice set-ci-daemon", "Designate the daemon that runs out-of-slice checks: one of yours, or one its owner exposed to the slice (gs agent status --slice)."],
      ["gs slice history", "Inspect slice definition versions."]
    ]
  },
  {
    title: "Webhooks",
    commands: [
      [
        "gs webhook create --url <https-url> --event <name>",
        "Send a slice's events to an endpoint; --secret-stdin signs deliveries."
      ],
      ["gs webhook list", "List a slice's webhooks and how their last delivery went."],
      ["gs webhook ping <webhook-id>", "Send a ping now and show the response."],
      ["gs webhook deliveries <webhook-id>", "Recent deliveries, newest first."],
      ["gs webhook redeliver <delivery-id>", "Send a delivery's event again."],
      ["gs webhook update <webhook-id>", "Change the URL, events, secret or --active."],
      ["gs webhook delete <webhook-id> --yes", "Delete a webhook and its history."]
    ]
  },
  {
    title: "Agents",
    commands: [
      [
        "gs agent start",
        "Run an agent daemon in an empty directory; with --ci it runs your slices' checks."
      ],
      [
        "gs agent status",
        "List your agent daemons, and those of agents you own, with their status and the slices that use each as their CI daemon."
      ],
      [
        "gs agent expose <daemon> <slice>",
        "Let a slice's owners choose your daemon as its CI daemon; gs agent unexpose withdraws it."
      ],
      [
        "gs agent rename <daemon> <name>",
        "Name a daemon; the name shows in the web app, slice settings and check runs. --clear drops it."
      ],
      ["gs agent stop", "How to stop a running daemon (Ctrl-C)."]
    ]
  }
];

export const webhookEvents = [
  ["push", "A commit landed that changes paths this slice includes, whichever slice authored it."],
  ["tag.created", "A tag (release) was created. Re-creating the same tag is not an event."],
  ["changeset.created", "A changeset was opened in the slice."],
  ["changeset.updated", "A new patchset was uploaded."],
  ["changeset.approved", "A changeset was approved."],
  ["changeset.submitted", "A changeset landed; the commit and its changed paths are included."],
  ["changeset.abandoned", "A changeset was abandoned."],
  ["check_run.completed", "A check finished, with its name and status."],
  ["*", "Every event above."],
  ["ping", "Sent when you ask (Ping, or gs webhook ping), even to an inactive webhook."]
];

export const webhookHeaders = [
  ["X-Gitslice-Event", "The event name, such as tag.created."],
  ["X-Gitslice-Event-ID", "The event id. Retries and redeliveries keep it, so drop duplicates by it."],
  ["X-Gitslice-Delivery", "This delivery's id."],
  ["X-Gitslice-Hook-ID", "The webhook's id."],
  ["X-Gitslice-Signature-256", "sha256= and the hex HMAC-SHA256 of the raw body, keyed by the secret (only when one is set)."]
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
    value === "webhooks" ||
    value === "git-users" ||
    value === "cli"
  ) {
    return value;
  }
  return "start";
}
