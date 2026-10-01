# Claude Code Working Agreement

This repository is worked by a **single orchestrating Claude agent** that
**delegates implementation to the `codex` CLI running in Gitslice
workspaces**. The main agent stays free to plan, integrate, verify, and pick
up new tasks while codex does the heavy editing in the background.

**The source lives on Gitslice.** The canonical source is the public slice
`gitslice/gitslice` (https://gitslice.io/slices/gitslice/gitslice). GitHub
`gitslice-io/gitslice` is a read-only mirror that only the export workflow
writes. Never push to it or open pull requests there
(`design/21_self_hosting.md`).

See [AGENTS.md](AGENTS.md) for project architecture, rules, build/test commands,
and design docs. This file governs *how the work is divided*.

## Core directive

**Default to delegation.** When a task involves writing or changing more than a
trivial amount of code, do **not** implement it yourself in the main working
tree. Instead:

1. Decompose the task and write a precise prompt (with the exact API/contracts).
2. Create a Gitslice **workspace** per parallel unit of work.
3. Launch `codex exec` in each workspace, **in the background**.
4. While codex runs, stay available for the user's next request.
5. When codex finishes, **integrate, build, test, and submit** the changeset
   yourself.

The main agent's job is orchestration + verification, not typing implementation.
Reserve direct edits in the main tree for: tiny one-liners, wiring that must
match contracts you control, and integration/conflict resolution.

**Picking an executor.** Use **codex** (workspace flow below) for substantial,
multi-file, parallelizable, or contract-sensitive work. Use **opencode** with
the **Z.AI** provider for small, well-scoped, low-risk tasks (see "Delegating
simple tasks to opencode"). Either way, the main agent owns build, test and
submit.

## Delegating to codex

Run codex non-interactively, scoped to a workspace, in the background:

```bash
mkdir ../gs-<task> && (cd ../gs-<task> && gs init gitslice/gitslice)
codex exec --dangerously-bypass-approvals-and-sandbox --skip-git-repo-check \
  --cd /abs/path/to/gs-<task>/gitslice/gitslice "$(cat /tmp/<task>_prompt.md)" \
  > /tmp/<task>.log 2>&1            # run with run_in_background so you stay free
```

The module root is `gitslice/gitslice/` inside the workspace. A workspace has
no `.git`, hence `--skip-git-repo-check`.

Prompt-writing rules (this is where quality comes from):

- State the **exact files** the agent may touch and the files it must **not**
  touch (so parallel agents stay on disjoint paths and don't conflict).
- Give the **precise contract**: function/type signatures, request/response
  shapes, route/file conventions, the env/flags involved. Have the agent **read**
  the relevant existing files and design docs first.
- Require the agent to **validate** before finishing: `gofmt`, `go build ./...`,
  `go vet`, `go test ./...` for Go; `npm ci && npm run build` for `web/`.
- Ask it to print a summary of files changed and confirm the checks passed.

## Delegating simple tasks to opencode (Z.AI)

Use **opencode** for **small, well-scoped, low-risk** units of work — a single
short prompt, no parallel fan-out, no shared-contract foundation: a one-file
edit, a localized refactor, a focused question about the code, a small test, a
docstring/comment pass. Reach for the codex workspace flow above when the work is
substantial, multi-file, parallelizable, or contract-sensitive.

The binary is **not on `PATH`** — invoke it by absolute path and pin the Z.AI
provider. Run it non-interactively in the background so you stay free:

```bash
# `glm-4.7` is a good coding default; `glm-4.5-air` is faster/lighter for
# trivial edits. Run from the dir (or workspace) you want it to operate in.
~/.opencode/bin/opencode run "$(cat /tmp/<task>_prompt.md)" \
  --model zai-coding-plan/glm-4.7 \
  --print-logs --log-level INFO \
  > /tmp/<task>.log 2>&1            # run with run_in_background so you stay free
```

opencode rules:

- **Always pass `--print-logs`.** With stdout piped (non-TTY), the compact
  renderer hides the assistant text; the reply is only emitted reliably with
  logs on. The model's answer is the final line(s) after the `INFO` log block.
- **Pin `--model zai-coding-plan/<model>`** every time. Don't rely on the
  default provider. Authenticated Z.AI models: `glm-4.5-air`, `glm-4.7`,
  `glm-5-turbo`, `glm-5.1`, `glm-5.2`.
- opencode **edits in place** in its working directory (no sandbox). For any
  task that writes code, run it inside a throwaway **workspace**, just like
  codex. Read-only and analysis tasks can run anywhere.
- Same ownership rules apply: **you** still build, test, lint, and submit.
  Don't trust opencode's word that checks passed — re-run them yourself.

## Parallelism & integration

- **Foundation first, then fan out.** If pieces share contracts (an API client,
  generated types, a scaffold), have one codex agent build that foundation and
  verify it builds; then launch the page/feature agents in parallel against it.
- Keep each parallel agent on **disjoint files**. Each workspace becomes one
  changeset (`gs create --message "area: summary" --all`).
  - Chain dependent work with `gs create --base <changeset>`.
  - Land a chain with `gs submit <changeset> --with-dependencies`.
  - Reconcile shared files (e.g. `go.mod`) in one changeset with
    `go mod tidy`.
- Run `gs sync` before submitting when the base may have moved. Overlapping
  edits fail at submit with a path-base conflict.
- **Delete workspaces** once their changesets are submitted or abandoned
  (`rm -rf ../gs-<task>`).

## The main agent always owns

- **Final build, test and lint** in the workspace. Don't trust codex's word
  alone: re-run. `gs ci` runs the slice's checks. For e2e tests, export the DB
  URL directly (`GITSLICE_TEST_DATABASE_URL=...`), because the Makefile's
  `-include env.local` does not parse quoted values.
- **Changesets, submits and deploys.**
  - Submit only after the required checks pass; the slice's CI daemon runs
    them on every patchset.
  - Title changesets like commit subjects (`area: summary`).
  - Deploys build from the GitHub mirror, which trails Gitslice by about 10
    minutes.
- **Verification of behavior**, not just compilation, when feasible.

## Don't

- Don't hand codex an entire app with no contract — decompose and specify.
- Don't run many parallel agents over the same files; they will clobber each other.
- Don't skip the post-integration build/test because "codex said it passed."
