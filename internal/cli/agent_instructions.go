package cli

import (
	"path/filepath"
	"strings"
)

// agentWorkspaceInstructions returns the runtime-agnostic guidance injected into
// every agent conversation. It is delivered per-runtime (codex receives it as
// `developerInstructions` on thread start/resume, so nothing is written into the
// workspace and `gs cs capture` never picks it up); a future runtime that reads
// an on-disk AGENTS.md can write this same text to that file.
//
// includedPaths is the slice's editable scope (from .gs/slice.json); the agent
// may only edit files under those prefixes.
func agentWorkspaceInstructions(includedPaths []string) string {
	var b strings.Builder
	b.WriteString(agentWorkspaceInstructionsPreamble)
	b.WriteString("\n")
	b.WriteString(agentWorkspaceEditableScope(includedPaths))
	b.WriteString("\n")
	b.WriteString(agentWorkspaceInstructionsCommands)
	b.WriteString("\n")
	b.WriteString(agentWorkspaceInstructionsFileLinks)
	return b.String()
}

// agentWorkspaceEditableScope renders the editable-paths section. Only files
// under the slice's included paths are part of the workspace; edits anywhere
// else are out of scope and will not be captured.
func agentWorkspaceEditableScope(includedPaths []string) string {
	if len(includedPaths) == 0 {
		return "- You may only edit files under this slice's included paths. Run " +
			"`gs status` to see the workspace scope."
	}
	var b strings.Builder
	b.WriteString("- You may only edit files under this slice's included path(s):\n")
	for _, p := range includedPaths {
		b.WriteString("    ")
		b.WriteString(strings.TrimRight(p, "/"))
		b.WriteString("/\n")
	}
	b.WriteString("  Files outside these paths are not part of the workspace; do not " +
		"create or edit files elsewhere, as those changes are out of scope and will " +
		"not be captured.")
	return b.String()
}

// conversationIncludedPaths reads the editable scope for a conversation's
// hydrated workspace from its .gs/slice.json. Best-effort: a missing or
// unreadable config yields nil, and the instructions fall back to generic
// scope guidance.
func conversationIncludedPaths(workdir string) []string {
	var cfg WorkspaceConfig
	if err := readJSONFile(filepath.Join(workdir, ".gs", "slice.json"), &cfg); err != nil {
		return nil
	}
	return cfg.IncludedPaths
}

const agentWorkspaceInstructionsPreamble = `You are working inside a Gitslice workspace, NOT a git repository.

- This directory is a gitslice workspace bound to a single slice. It is not a git
  repo: do not run ` + "`git`" + ` commands. Git is not the source of truth here, and
  git operations will not reflect or persist your work.
- Edit files directly with your normal tools. Your changes are captured
  automatically as a patchset at the end of each turn — you do not need to commit
  or run any capture command yourself.
- NEVER create or update a changeset yourself. The Gitslice agent daemon monitors
  this workspace and creates and updates the changeset automatically. Do not run
  ` + "`gs cs create`" + `, ` + "`gs cs update`" + `, ` + "`gs cs capture`" + `, ` + "`gs cs submit`" + `, or
  similar changeset-mutating commands — read-only inspection like ` + "`gs cs status`" + `
  and ` + "`gs cs show`" + ` is fine.`

// agentWorkspaceInstructionsFileLinks tells the agent how to reference workspace
// files in its replies. The conversation is read in a web UI that has no access
// to the agent's local filesystem, so absolute/`file://` paths are dead links.
// Instead the agent emits a stable slice-relative marker under the `gsfile:`
// scheme; the server rewrites it at read time to the correct web URL (the file
// in the changeset's patchset when that turn changed it, otherwise the slice's
// current file view). The agent needs no knowledge of accounts, IDs, or URLs.
const agentWorkspaceInstructionsFileLinks = `- When you mention a workspace file in your reply, link to it with a
  repository path under the ` + "`gsfile:`" + ` scheme, as a Markdown link:
    ` + "`[internal/cli/agent.go](gsfile:internal/cli/agent.go)`" + `
  Optionally pin a line or range with a fragment:
    ` + "`[agent.go:42](gsfile:internal/cli/agent.go#L42)`" + ` or ` + "`#L42-L60`" + `.
  Use the account-rooted repository path, without the leading slash. If the
  editable scope is ` + "`/nic/File`" + ` and the file is
  ` + "`nic/File/Lol.txt`" + ` in the workspace, link
  ` + "`[Lol.txt](gsfile:nic/File/Lol.txt)`" + `, not
  ` + "`gsfile:Lol.txt`" + `. This is the same path ` + "`gs status`" + ` shows, minus the
  leading slash. Never use an absolute or ` + "`file://`" + ` path and never prefix the
  path with ` + "`./`" + `. The web UI cannot open paths on this machine; the
  ` + "`gsfile:`" + ` link is resolved to the right file in the UI for you. Use it only
  for files inside this workspace.`

const agentWorkspaceInstructionsCommands = `- Use the ` + "`gs`" + ` CLI for source-control operations. Useful commands:
    gs status            show the workspace status (pending edits)
    gs log               show slice history
    gs diff              show changes against the slice base
    gs show <commit>     show a specific commit
    gs sync              pull the latest slice state into the workspace
    gs cs status         show the current changeset status
    gs cs show           show the current changeset details
  Run ` + "`gs --help`" + ` or ` + "`gs <command> --help`" + ` for anything else.
- If a ` + "`gs`" + ` command fails with an authentication error (e.g. "not logged in",
  "invalid token", or an Unauthenticated/Unauthorized response), do NOT try to
  re-authenticate or work around it yourself — you run with the agent daemon's
  credentials and cannot complete a login from here. Tell the user to run
  ` + "`gs auth login`" + ` on the machine running the agent daemon and approve the
  sign-in URL it prints in their browser, then retry.
- Do NOT create AGENTS.md, CLAUDE.md, or other agent-instruction files in the
  workspace. These instructions are provided out-of-band; writing such a file
  would pollute the slice's changeset.`
