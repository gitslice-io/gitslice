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
	b.WriteString(agentWorkspaceInstructionsChecks)
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
		"not be captured. These are canonical account-rooted repository paths; the " +
		"matching on-disk path omits only the leading slash (for example, /slices/io " +
		"is slices/io in the workspace). Follow that layout for new files and use " +
		"`gs status` to confirm their canonical paths.")
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
- Edit files directly with your normal tools. The daemon captures the complete
  workspace result as a patchset only after your turn completes — you do not need
  to commit or run any capture command yourself.
- ` + "`gs status`" + ` and ` + "`gs diff`" + ` compare the complete workspace result with the
  changeset base. Files captured by an earlier turn can remain listed while the
  draft is active; listed paths are not necessarily uncaptured changes from this
  turn.
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

const agentWorkspaceInstructionsCommands = `- Treat ` + "`gs`" + ` commands according to their side effects; discovering a command
  in ` + "`gs --help`" + ` does not make it safe inside an agent-managed draft.
- Safe inspection commands include:
    gs context           show resolved server, auth, workspace, and slice context
    gs status            show the complete workspace result against the base
    gs diff              show the complete workspace diff against the base
    gs log               show slice history
    gs show <commit>     show a specific commit
    gs cs status         show the current changeset status
    gs cs show           show the current changeset details
  ` + "`gs ci`" + ` is also safe: it runs applicable checks locally without capturing or
  submitting.
- Do not run other commands that mutate this draft or unrelated remote state as
  an ordinary implementation step. In particular, ` + "`gs sync`" + ` is a
  changeset-mutating rebase when a draft is active. Do not run mutating ` + "`gs fs`" + `
  or ` + "`gs shell`" + ` operations, slice mutations, or changeset mutations unless the
  user explicitly requests that exact native operation and its separate side
  effect is intended.
- ` + "`gs import`" + ` is a server-side native operation: the server performs the Git
  clone and publishes a native changeset/commit outside this daemon-managed
  draft. It does not copy files into the local workspace. Run it only when the
  user explicitly wants a native Gitslice import; if the request could instead
  mean copying source into this draft for review, clarify which result they want.
  A server/RPC import failure cannot be fixed by changing the local ` + "`git`" + `
  executable or ` + "`PATH`" + `. Never replace a failed native import with an archive
  copy or another semantically different workflow without the user's agreement.
- If a ` + "`gs`" + ` command fails with an authentication error (e.g. "not logged in",
  "invalid token", or an Unauthenticated/Unauthorized response), do NOT try to
  re-authenticate or work around it yourself — you run with the agent daemon's
  credentials and cannot complete a login from here. Tell the user to run
  ` + "`gs auth login`" + ` on the machine running the agent daemon and approve the
  sign-in URL it prints in their browser, then retry.
- Do not create an agent-instruction file merely to persist these injected
  Gitslice instructions; they are already provided out-of-band.
- Existing or imported ` + "`AGENTS.md`" + `, ` + "`CLAUDE.md`" + `, ` + "`.claude/**`" + `, and similar
  project files are ordinary repository content. Preserve and follow them. Edit,
  create, or delete them only when the user's task specifically requires it; do
  not remove them merely because they contain agent guidance.`

// agentWorkspaceInstructionsChecks tells the agent about CI checks: they run
// automatically when the turn is captured, the agent may maintain the
// committed checks files, and it should run `gs ci` to verify before ending a
// turn (but must never run capture — that is automatic).
const agentWorkspaceInstructionsChecks = `- CI checks: a slice can define checks in ` + "`.gitslice/checks.yaml`" + ` files
  (one per folder; they cascade from each changed path up to the repository root).
  Each check has a shell ` + "`run`" + ` command (e.g. build, test, or lint) and may
  declare ` + "`paths`" + ` globs that scope when it runs. When your turn is captured,
  the in-slice checks that match your changes run automatically and their
  pass/fail is recorded on the patchset; any check listed in the slice's required
  checks must pass before the changeset can submit.
- You MAY create or edit ` + "`.gitslice/checks.yaml`" + ` like any other workspace file
  to add or adjust checks for the code you change — it is committed with the slice.
- In a check definition, ` + "`paths`" + ` matches changed repository paths only. It
  cannot represent GitHub event conditions such as branch filters, schedules, or
  ` + "`workflow_dispatch`" + `. Report unsupported trigger semantics rather than
  approximating them with unrelated path filters.
- Check ` + "`paths`" + `, ` + "`include`" + `, and ` + "`working_dir`" + ` values use the logical
  repository-root namespace, not canonical ` + "`/<account>/...`" + ` paths. A leading
  slash means that logical root. Prefer omitting ` + "`working_dir`" + ` or using ` + "`.`" + `
  when the checks file is colocated with the code it tests.
- When replacing another CI system, keep the source CI definition until the
  native checks validate successfully. Remove it afterward only when replacement
  is part of the user's request.
- Before ending a turn, run ` + "`gs ci`" + ` to run all applicable checks for your
  changes and confirm they pass; fix failures rather than leaving a failing
  patchset. ` + "`gs ci`" + ` runs the checks locally and does NOT capture or submit, so
  it is safe to run — but do NOT run ` + "`gs cs capture`" + ` (capture is automatic).`
