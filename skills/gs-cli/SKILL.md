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

Workspace-aware commands discover `.gs/` by walking up from subdirectories. Keep edits under the materialized path for the bound slice. For a slice that includes `/acme/payment`, edit files under `acme/payment/...` inside the workspace.

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
gs diff --name-only
gs create --message "describe the change" --all --json
gs diff --stat
gs submit --json
gs status --json
```

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

If a command accepts a slice reference, prefer canonical `<account>/<slice>` in scripts. Bare slice slugs are CLI sugar and require a signed-in account. The reserved `home` slice covers `/account`, not `/account/home`.

## History And Diff

Use native commit inspection commands, not removed `gs commit ...` compatibility forms:

```bash
gs log --slice acme/payment --limit 20 --json
gs log -- /acme/payment/app.go
gs show <commit-id-or-prefix> --name-only --json
gs diff
gs diff --name-only
gs diff <commit-id-or-prefix>
gs diff <old-commit> <new-commit> --stat
```

Commit prefixes are acceptable for interactive inspection, but scripts should keep full ids from JSON output when available.

## RPC Diagnostics

Use `gs rpc` as an escape hatch when there is no first-class CLI command:

```bash
gs rpc list --json
gs rpc call AuthService/GetAuthStatus --request '{}' --json
gs rpc call gitslice.core.v1.AuthService/GetAuthStatus --request '{}' --json=subject_id
```

`gs rpc call` supports unary generated core RPCs with protojson requests. It uses saved auth by default; add `--unauthenticated` only for intentionally public development methods. Do not replace product commands with RPC calls when a dedicated `gs` command exists.

## Troubleshooting

- `authentication failed` or exit code `4`: run `gs auth status --json`; re-run `gs auth login` if needed.
- `not in a gitslice workspace`: run `gs context --json`; initialize an empty directory with `gs init <account>/<slice>`.
- `workspace init requires an empty directory`: create a fresh empty directory.
- `outside_slice`: move the edit under the workspace's bound slice or create another workspace for the intended slice.
- `workspace has local changes` before sync: inspect `gs status --json`; create a changeset first if the sync policy requires it.
- `unresolved sync conflicts`: resolve markers/side files, then run `gs cs update --json` and retry submit.
- Unknown command or stale examples: run `gs schema` and prefer commands listed there.
