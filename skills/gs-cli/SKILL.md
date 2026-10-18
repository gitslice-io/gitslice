---
name: gs-cli
description: Operate and automate the Gitslice `gs` CLI. Use when an agent needs to inspect auth, context, config, slices, server files, one-slice workspaces, status, diffs, native commit history, changesets, dependent changesets, structured CLI output, diagnostic RPC calls, or `gs` command failures.
---

# Gitslice `gs` CLI

## Core Rules

Use `gs` as a Gitslice-native CLI, not as a Git wrapper. Preserve these invariants while operating or scripting it:

- Treat workspaces as bound to exactly one slice.
- Use canonical account-rooted server paths such as `/nic/notes/readme.md`.
- Expect workspace files to materialize below the workspace root as `nic/notes/readme.md`.
- Create another workspace when work belongs to another slice.
- Let server-side submit validation be authoritative; do not bypass it with direct storage writes.
- Prefer current command behavior from `gs schema` over older design examples when they disagree.

For automation, use `--json`, `--jq`, `--template`, `--non-interactive`, and `--no-color` instead of parsing human text. Diagnostics and progress go to stderr. In JSON mode, failures use:

```json
{
  "error": {
    "code": "stable_snake_case_code",
    "message": "human-readable message",
    "hint": "optional next action",
    "retriable": false
  }
}
```

A dropped connection is retried only for calls that give the same result when repeated: reads, validation and content uploads, three tries in all. When that still fails, the error is `server_unavailable` with `retriable: true` and nothing changed. For anything that writes (creating a changeset, a stack entry, a submit) gs does not retry: the error is `connection_dropped` with `retriable: false`, because the server may have applied the change before the connection dropped. Check what exists (`gs status`, `gs cs list`) before running the command again, so the change is not made twice.

Never print or summarize bearer tokens unless the user explicitly needs a secret-bearing command. Use `gs auth status` for non-secret auth inspection; reserve `gs auth token` for scripts that truly need the token.

## Orientation

Find the executable first:

```bash
command -v gs
./bin/gs version --json
go run ./cmd/gs version --json
```

Then inspect the local command contract:

```bash
gs schema
gs help environment
gs help formatting
gs help paths
gs help slices
gs auth status --json
gs context --json
```

Useful environment defaults:

- `GS_SERVER_ADDR`: gRPC endpoint. The built-in default is the production API
  at `api.gitslice.io:443`; staging and local development should override it
  explicitly (local commonly uses `127.0.0.1:50051`).
- `GS_WEB_URL`: web UI base for `gs browse`.
- `GS_GATEWAY_URL`: HTTP JSON gateway base.
- `GS_CLIENT_CACHE_DIR`: shared content-addressed object cache root.
- `NO_COLOR=1` or `TERM=dumb`: disable color and interactive shell headers.

The user config normally lives at `~/.gitslice/config.json`. Workspace metadata lives under `.gs/` and must not contain auth tokens.

## Scriptable Patterns

Use structured output fields from `gs schema`:

```bash
gs status --json
gs context --json=server_addr,active_slice,active_slice_source
gs auth status --jq '{signed_in, server: .server_addr, reason}'
gs version --template '{{.version}} {{.go_version}}'
```

When a command may prompt, pass enough flags to make it deterministic and add `--non-interactive`:

```bash
gs create --message "update notes" --all --non-interactive --json
gs submit --no-watch --non-interactive --json
```

If a command fails, inspect stderr in JSON mode and follow `error.hint`. Baseline exit codes are `0` success, `1` general failure, `2` canceled, and `4` authentication failure.

## Auth And Context

Start with:

```bash
gs auth status --json
gs context --json
gs config list --json
```

If auth is missing or invalid, prefer the active project's documented login path. Current CLI builds expose `gs auth login` with Clerk-oriented flags:

```bash
gs auth login --server 127.0.0.1:50051
gs auth login --clerk-token - --server 127.0.0.1:50051
```

An agent with no human available to sign in can register itself, when the server enables agent sign-up. This creates the agent's own personal account and saves a long-lived API key as the CLI credential. The human who owns `--email` can later claim co-ownership:

```bash
gs auth register-agent --username release-bot --email owner@example.com --server 127.0.0.1:50051
```

An API key is shown once, so a lost one cannot be read back. It is replaced: the human who claimed the agent (`gs claims accept <agent-subject-id>`) runs `gs auth keys create --agent release-bot` (the key is the only thing printed on stdout), hands it to the agent privately, the agent runs `printf %s "$KEY" | gs auth use-key`, and the human then runs `gs auth keys revoke <key-id> --agent release-bot` for the lost key (`gs auth keys list --agent release-bot` shows the ids and when each was last used). An agent that still has a working key can rotate it the same way with no `--agent`. A human who has not claimed the agent gets `agent_not_found`.

To share a link, use `gs browse --print <account:slice | /account/path | workspace-path>` rather than hand-building URLs: it maps to the web app's real `/slices/<account>/<slice>?path=` route, checks that the slice and path exist, and warns on stderr when the slice is private.

Keep gs current with `gs upgrade` (`gs upgrade --check` only reports whether a newer release exists). Once a day gs also prints a one-line notice on stderr when a newer release is out (not with `--json` or `--quiet`; `GS_NO_UPDATE_CHECK=1` turns it off). It downloads the release for this platform from `https://gitslice.io/releases`, verifies its checksum, and replaces the running gs only if the new one starts and reports the expected version.

To share a changeset, use the `view:` link that `gs create`, `gs cs update` and `gs submit` print, `changeset_url` from their `--json` output, or `gs cs link [<changeset>]`, which checks that the changeset exists first. Do not build `/cs/...` URLs yourself, and never from a commit id: `gs submit` also reports the native commit the changeset landed as (`landed as commit sha256:...`, `commit_id` in JSON), and a commit id is not a changeset id, so `/cs/<commit>` is a dead link. `gs cs link` refuses commit ids.

A human signed in with that verified email lists and accepts pending agents with `gs claims list` and `gs claims accept <agent-subject-id>`. Accepting makes them a co-owner; the agent keeps its access.

For local tests, fixtures may write `~/.gitslice/config.json` directly with a service token; do that only inside test harnesses or when the user explicitly asks for test setup.

Use `gs context --json` before workspace-sensitive operations. It reports the cwd, config path, signed-in subject, nearest workspace, active slice, and source of that slice resolution.

## Server Files

Use `gs fs` for direct reads and small mutations in the signed-in user's home slice. Paths must be absolute server paths and must stay under the signed-in account root:

```bash
gs fs ls /nic --json
gs fs cat /nic/notes/readme.md --json
gs fs mkdir /nic/notes --json
gs fs write /nic/notes/readme.md --text "hello" --json
gs fs write /nic/notes/readme.md --stdin --json
gs fs upload ./notes /nic/notes --recursive --json
gs fs mv /nic/notes/readme.md /nic/notes/today.md --json
gs fs rm /nic/notes/today.md --json
```

Use `gs shell` for exploratory server-side browsing. It is text-only; run it with `--no-color` and piped input for repeatable output:

```bash
printf 'pwd\nls\nstat /nic/notes\nquit\n' | gs shell --slice nic/home --no-color
```

`gs shell --commit <commit-id>` pins inspection to a commit and is read-only. Mutating shell commands without `--commit` create and submit changesets behind the scenes, so prefer explicit `gs fs` or workspace flows when an auditable operation sequence matters.

## Workspaces

Initialize a workspace only in an empty directory:

```bash
mkdir work-notes
cd work-notes
gs init nic/home --json
gs status --json
```

To work in a large slice without checking it all out, mount it lazily (Linux and macOS with FUSE). The listing arrives in about a second at any size, and a file's content is fetched on first read, with the small files beside it. Edit with any tool: edits stay in the workspace's local layer, on this machine, until `gs create` or `gs modify` uploads them.

```bash
gs init --lazy acme/payment ~/w/payment   # creates and mounts it. gs unmount ~/w/payment; gs mount ~/w/payment again after that or a reboot; gs mount list | status | discard
cd ~/w/payment/acme/payment
gs status; gs diff                      # as in a gs init workspace; nothing is uploaded
gs restore path/to/file                 # drop a local edit (or --all)
gs create --all --message "..." --submit
gs sync                                 # moves the base; files nobody changed cost nothing
gs fetch                                # fetch everything now, for offline work or a build
```

Every workspace command works in a mount: `gs sync`, `gs submit`, and switching changesets (`gs switch`, `gs up`, `gs down`) move the mount's layers without rewriting or fetching files; a changeset's files are fetched when something reads them. `gs init --lazy --prefetch 'acme/payment/**/*.go' <slice> <dir>` fetches matching files in the background. One background process serves all your lazy workspaces and exits when the last is unmounted. The directory must be empty and outside any other workspace. `gs agent start --lazy` mounts each conversation's workspace instead of checking it out (design/30_lazy_workspaces.md).

Workspace-aware commands discover `.gs/` by walking up from subdirectories. Keep edits under the materialized path for the bound slice. For a slice that includes `/acme/payment`, edit files under `acme/payment/...` inside the workspace.

Run commands from inside the workspace you made, not from a copy of it. A directory that has its own `.gs/` is another workspace: `gs create` and `gs status` skip it with a warning, and `.gs/` at any depth is never committed. A file's path in a changeset is its path in the workspace; nothing re-bases paths (`--root` starts a new dependency tree, it does not change paths).

## Concurrent Agent Workspaces

For concurrent agent work, treat a local `gs` workspace as a disposable per-task checkout bound to one slice. Do not let two active jobs mutate the same workspace directory, `.gs/` metadata, or active changeset state at the same time.

Use this pattern:

```bash
run_root="${TMPDIR:-/tmp}/gitslice-agent/${USER:-agent}/$(date +%Y%m%d%H%M%S)-$$"
task_dir="$run_root/acme-payment-task-1"
mkdir -p "$task_dir"
cd "$task_dir"
gs init acme/payment --json
gs context --json
```

Guidelines:

- Allocate one workspace directory per logical task, even when two tasks target the same slice.
- Use separate workspaces for separate slices; a workspace cannot be rebound or expanded to another slice.
- Keep concurrent task directories outside the source repo unless the user asks for artifacts in-repo.
- Use stable task names in paths so logs and failures are traceable.
- Always `cd` into the intended workspace, then run `gs context --json` before mutating.
- Pass `--json --non-interactive --no-color` on scripted commands.
- Let workspaces share the global object cache; do not make cache isolation part of correctness.
- Set `GS_CLIENT_CACHE_DIR` only when tests need isolation from the user's normal cache.
- Clean up disposable workspaces only after capturing command output, changed paths, changeset ids, and any failure diagnostics the user needs.

If multiple agents need to contribute to one review, have each agent create its own changeset or dependent changeset from its own workspace, then use `gs deps`, `gs update-dependents`, and `gs submit --with-dependencies` from a coordinating workspace. Avoid sharing one mutable active changeset across independent agents unless an external lock or orchestrator serializes `gs modify` and `gs submit`.

Normal workspace loop:

```bash
gs context --json
gs status --json
gs diff --name-only                     # gs diff, gs cs diff and gs show -p print 3 lines of context around each change; -U N changes it, --unified=-1 shows whole files
gs create --message "describe the change" --all --json
gs diff --stat
gs submit --json
gs status --json
```

To create and land in one step, use `gs create --message "describe the change" --all --submit --json`: it waits for required checks that have not reported (up to `--watch-timeout`, default 15m), submits, and waits for the changeset to land. If something else blocks it (a failing check, missing approvals), the changeset stays created, the command exits 1 with `submit_blocked`, and `submit.results[].blocked_reason` says why.

Before it uploads, `gs create` lists the new directories that get 20 or more added files (on stderr). It asks before adding 100 or more files to one new directory when a person is at the terminal, and with no one to ask it refuses only when the files sit under the slice's own path a second time (`repeated_slice_path`, for example `/acme/app/acme/app/...`, a slice copied into itself). Pass `--yes` to go ahead anyway.

When nothing changed, `gs create` creates nothing and fails with `nothing_to_change`. In scheduled jobs (a daily sync), add `--if-changed`: an unchanged workspace is then a no-op that exits 0 (`{"created": false, "reason": "nothing_to_change"}` with `--json`), so `gs create --all --message "daily sync" --if-changed --submit` lands only real changes. `--allow-empty` creates an empty changeset on purpose.

Long uploads (`gs fs upload`, `gs create`), syncs and hydrates print progress lines on stderr; `--json` and `--quiet` keep stderr free of them.

Use `gs sync --json` before editing if the remote base may have advanced. Merge strategies are `line` (default), `manual`, `ours`, and `theirs`:

```bash
gs sync --merge line --json
```

If sync records conflicts, inspect `gs status --json`, then resolve conflict files in the workspace. In the current implementation, conflict-state cleanup still lives behind the hidden compatibility update command:

```bash
gs cs update --json
```

Use `gs modify --all --json` for normal patchset updates when no sync conflict state is present.

## Changesets And Dependents

Use the current top-level changeset commands for new work:

```bash
gs create --message "base change" --all --json
gs create --message "dependent change" --base <changeset> --all --json
gs modify --all --json
gs deps --json
gs update-dependents --children --json
gs submit --json
gs submit <changeset> --with-dependencies --json
```

Navigation and restructuring commands:

```bash
gs switch <changeset>
gs up
gs down
gs top
gs bottom
gs move <changeset> --onto <base-or-root>
gs insert --base <changeset> --message "new base"
gs detach <changeset>
```

Use product language such as changeset, base changeset, dependent changeset, dependency tree, and update dependents. Avoid introducing "stack" as user-facing terminology even if internal fields still use names such as `stack_id`.

The hidden `gs cs ...` namespace may exist for compatibility and low-level diagnostics, but it is not the preferred surface and is intentionally omitted from `gs schema`.

## Slices

Inspect and manage slices with:

```bash
gs slice list --json
gs slice info nic/home --json
gs slice paths nic/home --json
gs slice create nic/tools --include /nic/tools --visibility private --json
gs slice update nic/tools --include /nic/tools --required-approvals 1 --json
gs slice delete nic/tools --yes --json
```

To rename, hide or retire a slice:

```bash
gs slice rename nic/tools utils --json     # the old name keeps resolving and stays reserved
gs slice archive nic/utils --json          # hidden from `gs slice list`, read-only; history stays
gs slice list nic --archived --json        # include archived slices
gs slice unarchive nic/utils --json
```

A renamed slice keeps its history, members, settings and changesets. Git URLs, `go get` paths and workspaces made under the old name keep working. `gs slice delete` also removes a slice that has changesets when none is open and its paths are covered by another slice or hold no files: the changesets stay (`history_kept` in the JSON), and the name stays reserved.

If a command accepts a slice reference, prefer canonical `<account>/<slice>` in scripts. Bare slice slugs are CLI sugar and require a signed-in account. The reserved `home` slice covers `/account`, not `/account/home`.

## Organizations

Organization accounts hold slices that several people or agents share, such as
`/gitslice/...`. Anyone with a username, agents included, can create one and
becomes its owner (up to 20 each). Owners and admins invite others by username;
an invitation becomes a membership when the invitee accepts it:

```bash
gs account create-org acme-corp --json                       # you become its owner
gs account invite acme-corp bob --role writer --json         # owner|admin|writer|member|reader
gs account invitations --json                                # (as bob) your pending invitations
gs account accept acme-corp --json                           # (as bob) join
gs account members acme-corp --json
gs account set-member acme-corp bob --role admin --json      # change an existing member's role
gs account remove-member acme-corp bob --yes --json
```

Only owners invite or promote owners, and the last owner cannot be removed or
demoted. Writers can create and submit changesets in the organization's
slices; readers can only read them. `set-member` only changes the role of
someone who is already a member; new members are invited. `gs account use
acme-corp` makes the organization the default account for commands such as
`gs slice list`.

### Working With Another Agent

Share a slice with another agent or person by username; it takes effect at
once, without adding them to your account (design/26_slice_members.md):

```bash
gs slice share <you>/project <other-agent> --role writer --json   # reader|writer|admin
gs slice members <you>/project --json
gs slice unshare <you>/project <other-agent> --json
# the other agent
gs slice list --shared --json        # slices shared with it, and its role
gs auth status --json                # .memberships and .shared_slices: all its access at once
gs init <you>/project                # in an empty directory
gs slice leave <you>/project         # give it up
```

A slice covers only paths that hold files, so create `<you>/project` once
files exist under `/<you>/project` (or share `<you>/home`, which shares
everything you have). Files sit at their full path in the workspace
(`<you>/project/plan.md`). Roles apply to that slice only: readers read and
clone; writers also create, submit and approve changesets (which run the
slice's checks with its secrets, as an account writer's do); admins also
manage its members and webhooks. The slice's paths, visibility, policy, CI
daemon and secrets stay with the account's owners and admins.

To share read-only with everyone, make the slice public (`gs slice update
<account>/<slice> --visibility public`); anyone can then `gs init` or
git-clone it. For a team that owns many slices together, use an organization
(above).

## History And Diff

Use native commit inspection commands, not removed `gs commit ...` compatibility forms:

```bash
gs log --slice acme/payment --limit 20 --json
gs log -- /acme/payment/app.go
gs blame app.go --json                          # who last changed each line, and in which changeset
gs blame app.go --lines 40,60
gs show <commit-id-or-prefix> --name-only --json
gs diff
gs diff --name-only
gs diff <commit-id-or-prefix>
gs diff <old-commit> <new-commit> --stat
```

Commit prefixes are acceptable for interactive inspection, but scripts should keep full ids from JSON output when available.

`gs blame --json` returns `ranges` (`start_line`, `line_count`, `commit_id`) covering every line and `commits` with each commit's `summary`, `author`, `changeset_id` and `changeset_url`. It follows the file across moves; `truncated` means the history walk stopped before the file's first commit.

## Tags

Tags are immutable release names for a slice. Git clones of the slice see
them as `refs/tags/<name>`:

```bash
gs tag create v1.2.0 -m "release notes" --json         # tags the current head
gs tag create v1.2.1 --commit <commit-id> --slice acme/payment --json
gs tag list --json
```

Creating an existing tag again succeeds only for the same commit.

## RPC Diagnostics

Use `gs rpc` as an escape hatch when there is no first-class CLI command:

```bash
gs rpc list --json
gs rpc call AuthService/GetAuthStatus --request '{}' --json
gs rpc call gitslice.core.v1.AuthService/GetAuthStatus --request '{}' --json=subject_id
```

`gs rpc call` supports unary generated core RPCs with protojson requests. It uses saved auth by default; add `--unauthenticated` only for intentionally public development methods. Do not replace product commands with RPC calls when a dedicated `gs` command exists.

## Feedback

When gs, the web app or the docs get in your way, or something would help, tell the Gitslice team directly; they read every piece and answer:

```bash
gs feedback "what happened, what you expected, what would help" --category bug|idea|docs|other --json
gs feedback send --stdin --category bug < notes.md
gs feedback list --mine --json
```

gs adds `gs_version`, `os`, `arch` and the workspace slice; add the failing command with `--context command="gs ..."`. One piece of feedback can hold several points; the limit is 30 per day. `gs feedback list --mine` shows each with its status and the team's `resolution`.

## Troubleshooting

- Anything about the setup (version, server, sign-in, workspace, access): run `gs doctor --json`, or `gs doctor <account>/<slice> --json` for access to one slice. Each failed check has a `hint`.
- `not signed in`, `authentication failed` or exit code `4`: run `gs auth status --json`; re-run `gs auth login` (or `gs auth register-agent`) if needed.
- `nothing_to_change`: the workspace matches its base, so `gs create` made nothing; pass `--if-changed` in scheduled jobs.
- `permission_denied`: the signed-in subject has no role on the slice (in its account, or shared with it), or lacks write access. The owner of an agent account gains access by claiming it at `https://gitslice.io/claims`; anyone else asks an owner to share the slice (`gs slice share <slice> <username>`).
- `not in a gitslice workspace`: run `gs context --json`; initialize an empty directory with `gs init <account>/<slice>`.
- `workspace init requires an empty directory`: create a fresh empty directory.
- `workspace_damaged`: a directory on the way up has a `.gs/` without `slice.json`, so that workspace lost its binding. Restore `.gs/slice.json`, or move your files out and `gs init` a new empty directory; do not run `gs init` or `gs sync` inside it.
- `outside_slice`: move the edit under the workspace's bound slice or create another workspace for the intended slice.
- `workspace has local changes` before sync: inspect `gs status --json`; create a changeset first if the sync policy requires it.
- `unresolved sync conflicts`: resolve markers/side files, then run `gs cs update --json` and retry submit.
- Unknown command or stale examples: run `gs schema` and prefer commands listed there.
