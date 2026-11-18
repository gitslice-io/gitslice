export interface CommandGroup {
  commands: Array<[string, string]>;
  id: string;
  title: string;
}

export const globalFlags: Array<[string, string]> = [
  ["-C, --directory <dir>", "Run gs as if started in dir (like git -C); repeatable."],
  ["--json[=field,...]", "Emit structured JSON output (optionally selecting fields) for scripts and agents."],
  ["--jq <expr>", "Filter structured output directly with a jq expression."],
  ["--format <fmt>", "Output format: text, json, or command-specific formats."],
  ["--server <host:port>", "Target a different Gitslice server (or set GS_SERVER_ADDR)."],
  ["--non-interactive", "Fail instead of prompting for input in CI or scripts."]
];

export const commandGroups: CommandGroup[] = [
  {
    id: "auth",
    title: "Auth & accounts",
    commands: [
      ["gs auth login", "Sign in to an account in the browser."],
      ["gs auth status", "Show the active identity and session."],
      ["gs auth logout", "Clear local authentication credentials."],
      ["gs auth token", "Print an auth token for local tooling."],
      [
        "gs auth register-agent --username <name> --email <owner>",
        "Agents: sign up without a browser and save a long-lived API key."
      ],
      [
        "gs auth keys create --agent <name>",
        "Owners: mint a new API key for an agent (printed once); gs auth keys list and revoke manage keys."
      ],
      [
        "gs auth use-key",
        "Agents: sign in with an owner-created API key read from stdin or --key-file."
      ],
      ["gs claims list", "List agents registered with your verified email address."],
      ["gs claims accept <agent-id>", "Become a co-owner of an agent's account."],
      ["gs account create-org", "Create an organization account; gs account manages orgs, members, and active account."],
      [
        "gs group create|add|rm|list|show",
        "Manage account groups (@account/name) for slice sharing and --approvers policies."
      ]
    ]
  },
  {
    id: "setup",
    title: "Setup & troubleshooting",
    commands: [
      [
        "gs doctor [account/slice]",
        "Check version, server, sign-in, workspace and slice access; says how to fix each problem."
      ],
      ["gs context", "Show resolved CLI config, server, signed-in user, workspace, and active changeset."],
      [
        "gs upgrade",
        "Install the latest release (--check only reports). gs also checks once a day when a newer release is out."
      ],
      [
        "gs completion bash|zsh|fish",
        "Print a shell completion script; gs completion <shell> --help says how to load it."
      ],
      [
        'gs feedback "<message>" --category bug|idea|docs',
        "Tell the Gitslice team what went wrong or what would help; gs feedback list --mine shows their answers."
      ]
    ]
  },
  {
    id: "workspace",
    title: "Workspace & lazy mounts",
    commands: [
      [
        "gs init <account>/<slice> [dir]",
        "Create a full workspace checkout in dir (created if missing) or the current empty directory."
      ],
      ["gs -C <dir> <command>", "Run any gs command as if it started in dir, like git -C."],
      [
        "gs init --lazy <account>/<slice> [dir]",
        "Mount a slice on demand with FUSE (Linux and macOS). Fetches the listing up front and file contents on first read; supports --prefetch <glob> and --read-only."
      ],
      ["gs sync", "Sync the workspace to the latest remote base commit (--merge line|manual|ours|theirs)."],
      ["gs status", "Show pending local workspace edits against the base snapshot."],
      ["gs diff", "Inspect pending content changes, three lines of context around each (-U N to change)."],
      [
        "gs workspace delete [dir]",
        "Remove a workspace from this machine (unmounting a lazy one). Refuses if uncaptured edits exist unless --force is passed."
      ],
      ["gs mount <dir>", "Re-mount a lazy workspace after gs unmount or a reboot, preserving local edits."],
      ["gs unmount <dir>", "Unmount a lazy workspace while keeping its edits."],
      [
        "gs mount list",
        "List all lazy workspaces on this machine (gs mount status inspects fetches/errors; gs mount discard removes an unmounted one)."
      ],
      ["gs fetch [path...]", "Download a lazy workspace's files in parallel batches for full builds or offline work."],
      ["gs restore <path>... | --all", "Drop local edits in a lazy workspace so base content shows again."],
      [
        "gs checkpoint [-m <message>]",
        "Save local edits in a lazy workspace on this machine (nothing uploaded). gs checkpoint list shows them newest first."
      ],
      [
        "gs rewind [checkpoint]",
        "Restore local edits to a checkpoint (defaults to the newest that differs from now; automatically checkpoints first so it is undoable)."
      ],
      [
        "gs fork <checkpoint> <dir>",
        "Mount a new lazy workspace showing a checkpoint on its original base to explore two approaches side by side."
      ],
      ["gs cache status | prune", "Inspect or prune the shared local object cache (~/.cache/gitslice, bounded by GS_CACHE_MAX)."]
    ]
  },
  {
    id: "changesets",
    title: "Changesets & stacks",
    commands: [
      ["gs create --message <title>", "Create a changeset from pending edits and print its link."],
      [
        "gs create --all --message <title> --submit",
        "Create and land in one step: waits for required checks, then submits."
      ],
      [
        "gs create --message <title> --description-file <file>",
        "Attach a Markdown description while keeping the title one line (add --if-changed for no-op-safe scripts)."
      ],
      ["gs cs edit --title <title> --description <text>", "Update a draft changeset's title or description."],
      [
        "gs modify [--submit]",
        "Upload a new patchset to the active changeset; --submit also lands it once required checks pass."
      ],
      ["gs submit", "Validate and publish the active changeset (or dependent stack) to refs/global/main."],
      ["gs cs list | show | status | link", "List slice changesets, inspect details, or print a verified web link."],
      ["gs cs approve | abandon | handoff", "Approve a patchset, abandon a draft, or hand off authorship."],
      ["gs deps", "Inspect the active dependent changeset stack."],
      [
        "gs switch | up | down | top | bottom",
        "Move between changesets in a stack (gs move / gs detach restructure stacks; gs create --root starts a new one)."
      ],
      [
        "gs intent <glob> --for 2h -m <note>",
        "Declare an advisory path reservation so teammates and agents see overlap warnings (gs intent list / clear)."
      ]
    ]
  },
  {
    id: "history",
    title: "History, browse & files",
    commands: [
      ["gs log", "List recent accepted native commits."],
      ["gs blame <path>", "Show which commit and changeset last modified each line (--lines 10,20, --json)."],
      ["gs show <commit|changeset>", "Inspect an accepted native commit or a changeset by ID or handle."],
      ["gs browse [--print] [path|slice]", "Open or print a verified web URL for a slice or file (warns if private)."],
      ["gs import <source> --mount <path>", "Import an existing Git repository into Gitslice (--mode shallow|deep)."],
      ["gs tag create <name> | list", "Create or list immutable slice tags (also used for Go module releases)."],
      ["gs shell", "Open the interactive server-backed file shell."],
      ["gs fs ls | cat | mkdir", "List, read, or create paths directly on the server without a workspace."],
      ["gs fs upload <local> <path>", "Upload and land local files directly; prints the changeset link."],
      ["gs fs write <path> --text <t> --no-submit", "Open a direct server edit as a draft changeset for review."]
    ]
  },
  {
    id: "checks",
    title: "CI checks",
    commands: [
      ["gs ci", "Run applicable .gitslice/checks.yaml checks locally against pending workspace edits."],
      [
        "gs check list | logs <check> | rerun <check>",
        "Inspect a changeset's check runs, stream logs (--tail, --follow), or rerun a check."
      ],
      [
        "gs check allow [changeset]",
        "Allow a contributor patchset's checks to run with secrets and rerun them."
      ]
    ]
  },
  {
    id: "slices",
    title: "Slices",
    commands: [
      ["gs slice list", "List slices in your account and slices shared with you."],
      ["gs slice create <account>/<slice>", "Create a new slice definition over one or more folders."],
      [
        "gs slice update <slice>",
        "Change included paths, --visibility, --required-check, --required-approvals, --required-human-approvals, --approvers, --merge-on-submit, or --description."
      ],
      [
        "gs slice share <slice> <user|@account/group>",
        "Grant a person, agent, or group a role on one slice (--role reader|contributor|writer|admin)."
      ],
      ["gs slice members <slice>", "List slice members; gs slice unshare and gs slice leave remove access."],
      ["gs slice rename <slice> <new-name>", "Rename a slice; the old name keeps resolving and remains reserved."],
      ["gs slice archive <slice>", "Make a slice read-only and hide it from listings (gs slice unarchive restores it)."],
      [
        "gs slice set-ci-daemon <slice> <daemon-id>",
        "Designate a CI daemon for the slice (pass --add to add backup daemons to its pool)."
      ],
      ["gs slice history <slice>", "Inspect slice definition version history."]
    ]
  },
  {
    id: "secrets",
    title: "Secrets",
    commands: [
      [
        "gs secret create <NAME> --path <folder>",
        "Store an encrypted value (from stdin) for checks, triggers, and mirrors under the folder (--slice targets a slice's folders)."
      ],
      ["gs secret ls", "List account secrets and their attached folders; raw values are never shown."],
      ["gs secret update <secret-id>", "Rotate the value (--value) or change folders (--add-path, --remove-path)."],
      ["gs secret rm <secret-id>", "Delete a secret from every folder it applies to."],
      [
        "gs secret env <template> -o <file>",
        "Fill gitslice-secret: placeholders into a mode-0600 local env file (account owners and admins)."
      ],
      ["gs slice secret ls | set | rm <slice>", "List or manage secrets scoped strictly to one slice's folders."]
    ]
  },
  {
    id: "triggers",
    title: "Triggers",
    commands: [
      ["gs trigger ls [folder]", "List triggers that apply at a folder (its own and ancestor folders)."],
      ["gs trigger runs --changeset <id>", "List trigger runs for a changeset (or --trigger, --account), newest first."],
      ["gs trigger show <run-id>", "Inspect details and status for a single trigger run."],
      ["gs trigger logs <run-id>", "Print a trigger run's log output."],
      ["gs trigger run <trigger-id>", "Manually fire a trigger that listens on manual (--input key=value)."],
      ["gs trigger rerun | cancel <run-id>", "Rerun a finished trigger for the same event, or cancel an active run."]
    ]
  },
  {
    id: "mirrors",
    title: "Git mirrors",
    commands: [
      [
        "gs slice mirror set <slice> --url <https-url> --token-secret <NAME>",
        "Push the slice to an external Git host after every landing and tag (owners and admins)."
      ],
      ["gs slice mirror <slice>", "Show the mirror URL, last pushed commit, pending state, and any error."],
      ["gs slice mirror push <slice>", "Trigger an immediate push instead of waiting for retry."],
      ["gs slice mirror rm <slice>", "Stop mirroring; the remote repository keeps its history."]
    ]
  },
  {
    id: "agents",
    title: "Agents & MCP",
    commands: [
      [
        "gs mcp [--slice <account>/<slice>]",
        "Serve Gitslice over MCP (stdio) for coding agents without a local checkout (set GS_TRACE for JSONL logs)."
      ],
      [
        "gs agent create <name> [--slice <slice> --role contributor]",
        "Create a delegated agent owned by you and print its API key once."
      ],
      ["gs agent doctor --ci", "Verify Docker/Podman and host prerequisites for running a CI daemon."],
      ["gs agent install --ci --name <name>", "Install a CI-only agent daemon as a background user service (gs agent uninstall removes it)."],
      ["gs agent start [--ci]", "Run an agent daemon interactively in an empty directory."],
      ["gs agent status", "List your daemons and owned agents' daemons, status, and assigned slices."],
      [
        "gs agent expose <daemon> <slice>",
        "Allow a slice's owners to designate your daemon as its CI daemon (gs agent unexpose withdraws it)."
      ],
      ["gs agent rename <daemon> <name>", "Set a friendly name shown in the web UI, slice settings, and check runs (--clear removes it)."],
      ["gs agent stop", "Show how to stop a running local daemon."]
    ]
  }
];
