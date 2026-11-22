---
name: gitslice
description: Work with Gitslice source infrastructure via the gs CLI or hosted MCP — onboarding an agent account, checking out slices, creating and submitting changesets, running CI checks, syncing conflicts, and signing in to the Gitslice web UI.
---

# Gitslice Agent Skill

Gitslice is Git-compatible source infrastructure built around **slices** (repo-like
views over a global codebase), **changesets** (reviewable units of change), and
native CLI/MCP workflows.

## 1. Install & Onboard

Install the `gs` CLI (no Go toolchain required; checksum-verified):

```sh
curl -fsSL https://gitslice.io/install.sh | sh
```

Ensure `~/.local/bin` is on `PATH` in non-login shells:

```sh
export PATH="$HOME/.local/bin:$PATH"
```

### Option A — Human gave you a join token (`gsi_...`)
Registers you, links ownership to your human, and grants slice access in one step:

```sh
gs onboard --join "$GS_JOIN_TOKEN" --json
# or directly:
gs auth join "$GS_JOIN_TOKEN" --username <your-agent-name> --json
```

### Option B — Self-signup (no browser needed)
Use your human owner's email (and optionally request access to a target slice):

```sh
gs onboard --email <owner-email> --slice <account>/<slice> --role writer --json
# or directly:
gs auth register-agent --username <your-agent-name> --email <owner-email> \
  --request-slice <account>/<slice> --request-role writer --json
```

- Pass the returned `claim_instructions` / `human_handoff_message` to your human
  owner word for word. They claim you (and approve slice access in one click) at
  `https://gitslice.io/claims`.
- Never print or share raw `gsk_...` API keys.
- Run `gs doctor` (or `gs doctor <account>/<slice>`) to diagnose auth, network,
  or slice permissions.

## 2. Core Workspace Workflow

Every workspace is bound to **one slice** inside an empty directory:

```sh
mkdir work && cd work
gs init <account>/<slice>
```

> **Path layout:** Files live under their projected repository path (for example,
> `gs init gitslice/gitslice` puts files under `./gitslice/gitslice/`). Check
> the `files are under ...` and `found agent guide: ...` lines printed by `gs init`
> and always read any `AGENTS.md` inside the slice before making edits.

Day-to-day loop:

```sh
gs status                                       # inspect modified/added/deleted paths
gs diff                                         # review local changes
gs ci                                           # run the slice's configured checks locally
gs create --all --message "Short title" \
  --description "Markdown details"              # create a draft changeset
gs submit                                       # wait for checks & land to refs/global/main
```

Useful flags & commands:
- `gs create --all --message "..." --submit` creates and submits in one step.
- `gs create --if-changed ...` exits 0 as a no-op when nothing changed (for cron/CI).
- `gs sync` updates your workspace to the latest commit before submitting or
  after a landing. If a conflict occurs, resolve the marked files and re-run.
- `gs cs edit <id> --message "..." --description "..."` updates a draft's title/body.
- Most commands support `--json` for structured output (and structured errors
  include `code`, `message`, `hint`, `remediation_command`, and `remediation_url`).
- Use `gs schema [command]` to inspect exact flags and output fields for any
  command family without scraping prose help.

## 3. Sharing Links & Web Sign-In

- **Files & slices:** Always build links with `gs browse --print <path>` (verifies
  the path exists and warns if the slice is private).
- **Changesets:** Share the `view:` URL printed by `gs create`/`gs submit` (or
  `gs cs link <id>`). Do **not** construct `/cs/` URLs from a `sha256:...` commit ID.
- **Browser sign-in as an agent:** Mint a short-lived, revocable web session URL:
  ```sh
  gs auth web-login --expires 2h --slice <account>/<slice> --role reader
  ```
  The secret rides in the `#key=...` fragment (never sent in HTTP requests) and
  is revoked immediately on sign-out.

## 4. Zero-Checkout MCP (Local or Hosted)

Read, search, inspect history/blame, and create/submit changesets without
checking out a workspace:

- **Hosted Streamable HTTP MCP:** `https://gitslice.io/mcp` (optional query
  `?slice=<account>/<slice>`) with header `Authorization: Bearer <gsk_...>`.
- **Local stdio MCP:** `gs mcp --slice <account>/<slice>`.

Available MCP tools: `read_file`, `list_directory`, `search`, `history`, `blame`,
`create_changeset`, `get_changeset`, `submit_changeset`.

## 5. Collaboration & Feedback

- Share a slice with another agent or teammate:
  `gs slice share <account>/<slice> <username> --role writer|contributor|reader`
- Report CLI/API friction directly to the Gitslice team:
  `gs feedback "<what happened>" --category bug|idea|docs`
