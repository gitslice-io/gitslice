<h1 align="center">Gitslice</h1>

<p align="center"><strong>Many agents. One codebase. Land in parallel.</strong></p>

<p align="center">
  <a href="https://gitslice.io">Website</a> ·
  <a href="https://gitslice.io/doc">Docs</a> ·
  <a href="#benchmarks">Benchmarks</a> ·
  <a href="https://gitslice.io/llms.txt">For agents</a> ·
  <a href="#install">Install</a> ·
  <a href="LICENSE">Apache-2.0</a>
</p>

Gitslice is Git-compatible source infrastructure for fleets of coding agents
and the people who work with them. Agents get a workspace for the task,
land changes without stepping on each other, and the server, not the agent,
decides what merges. Slices give each agent its own part of one global
codebase.

```console
$ gs auth register-agent --username build-bot --email mira@orbit.dev
$ gs init --lazy orbit/checkout work          # mounts now, fetches files as they are read
$ cd work && $EDITOR orbit/checkout/authorize.go
$ gs create --all --message "Tighten authorization" --submit
created 8b4ee46085 patchset 1
waiting for required check "orbit/unit" to pass...
submitted 8b4ee46085
```

## Why

Git was built for people who merge now and then. A fleet of agents lands
constantly, and every landing moves the branch under all the others.

**1. Land in parallel.** The server admits a change by the paths it changes.
Agents editing different files never wait on each other, and agents editing the
same file are merged on submit (`gs slice update --merge-on-submit on`) instead
of being sent back to pull, rebase and push again. With 16 agents landing at
once, Git needed 120 retries; Gitslice needed none
([benchmark](#landing-in-parallel)).

**2. A workspace for the task, not the repository.** A slice is a repo-like
view of one global source graph, so an agent opens only its part. On Linux and
macOS `gs init --lazy` mounts it and fetches files when they are read; edits
stay on the machine until `gs create`. `gs checkpoint` saves work in progress
there, `gs rewind` goes back to a checkpoint (and can be undone), and
`gs fork` opens one as a second workspace to try another approach.

**3. The server decides.** An agent can claim anything. What lands is decided
where the commit is made: path by path, against the slice's required checks and
approvals, with every iteration kept as an immutable patchset. CI runs on any
machine with Docker (`gs agent start --ci`), with build images cached between
runs.

**4. Built for agents.** Agents register themselves with no browser
(`gs auth register-agent`), work through the `gs` CLI or `gs mcp`, a Model
Context Protocol server for agents that work without a checkout (read, search,
blame, history, and changesets you can submit), and report what went wrong with
`gs feedback`. `GS_TRACE=<file>` logs every server call as JSON lines.

**5. Git compatible.** `git clone https://gitslice.io/git/<account>/<slice>.git`
reads a slice, and `git push HEAD:refs/changes/new` creates a changeset. The
native graph is the source of truth; Git is the boundary.

## Benchmarks

Everything here comes from a benchmark in this repository, with its method,
numbers and caveats in [design/28_benchmarks.md](design/28_benchmarks.md). Where
Git is faster, that document says so.

### Landing in parallel

Agents each in their own workspace or clone, all landing at the same moment;
time is to the last landing, retries are extra rounds of pull and push (Git) or
sync and submit (Gitslice), summed over the agents. Medians, 2-vCPU VM, Git
remote on the same disk (the best case for Git).

| Scenario | Agents | Git | Gitslice |
| --- | ---: | ---: | ---: |
| Each agent edits its own file | 8 | 1.4 s, 28 retries | **1.2 s, 0 retries** |
| | 16 | 5.3 s, 120 retries | **2.4 s, 0 retries** |
| One file, a block each (merge on submit) | 8 | 1.4 s, 28 retries | 1.8 s, **0 retries** |
| | 16 | 5.4 s, 120 retries | **3.5 s, 0 retries** |

Git's retries grow with the square of the number of agents, and its time grows
faster than the agents do (1.4 s to 5.3 s for twice as many). Without merge on
submit, agents on the same file are slower than Git (3.9 s and 35 retries at 8
agents), which is what the setting is for.

### Opening a workspace

Ryujinx_Neo (4,508 files, 58.6 MB) from GitHub next to the same content as a
slice, from a cold start; seconds, median of 3 (range). The Gitslice API was
35-195 ms from the test machine over the day, so ranges are wide.

| | to tree | read 20 files | search the whole tree |
| --- | ---: | ---: | ---: |
| `git clone --depth 1` | 4.3 | 0.00 | 0.09 |
| `git clone` (full history) | 8.5 | 0.00 | 0.11 |
| `gs init` | 8.0 (6.9-12.4) | 0.00 | 0.09 |
| `gs init --lazy` | **2.6** (2.5-5.9) | 4.8 (4.0-10.1) | 107 (92-232) |
| `gs init --lazy` + `gs fetch` | 12.6 (5.7-50.8) | 0.01 | 1.5 |

A full checkout costs what a full clone costs. A lazy mount is up before any
content is fetched and pays a server round trip for each file it reads for the
first time: fine for a task that opens a few files, slow for one that reads the
whole tree, where `gs fetch` first is the answer. Faster first reads are the
next thing to work on.

Reproduce: `go test ./tests/cli -run TestBenchmarkParallelAgents -v` with
`GITSLICE_BENCH=1` and a test database, or `bench/workspace_bench.py`. See
[design/28_benchmarks.md](design/28_benchmarks.md).

## How it works

```text
  agent ── gs / gs mcp / git ──►  ┌─────────────── Gitslice server ───────────────┐
  (a workspace of one slice)      │  changeset ─► admit by path ─► merge on submit │
                                  │       │              │                         │
                                  │   patchsets      required checks ◄── CI daemon │
                                  │       │          and approvals    (any machine)│
                                  │       ▼                                        │
                                  │      one global source graph (Postgres +       │
                                  │      object store); slices are views of it     │
                                  └────────────────────────────────────────────────┘
```

- **Slice:** a named set of paths in the global graph, with its own visibility,
  required checks and approvals. A workspace is bound to one.
- **Changeset:** the unit of review and submit. Each iteration is an immutable
  **patchset**, so every version an agent tried stays auditable.
- **Submit:** the server validates the patchset's paths against the head,
  checks the slice's requirements, merges where it is allowed to, and publishes
  a commit to the global graph.
- **CI daemon:** the agent on your machine runs the slice's checks in a
  container and reports the results; the server enforces them.

## Git, and what else

| | Git + a host | Gitslice |
| --- | --- | --- |
| Many writers on one branch | each push races; losers rebase and retry | disjoint paths land independently; same-file edits merge on submit |
| Workspace for a task | clone, partial clone or sparse checkout | a slice, or a lazy mount of one |
| Rules for landing | branch protection and CI on the host | slice requirements enforced where the commit is made |
| Agent identity | tokens issued by a person | agents register themselves; humans claim them |
| Tooling for agents | a shell and `git` | `gs`, `gs mcp`, checkpoints, `GS_TRACE` |
| Small repo, one developer | **faster and simpler: use Git** | more machinery than you need |

Gitslice is a server you run or use at [gitslice.io](https://gitslice.io); it is
not a Git replacement for a repository one person works on. It is for the
point where many agents write at once.

## Where the source lives

**This GitHub repository is a read-only mirror.** The canonical source is the
public slice `gitslice/gitslice` on Gitslice itself:

- Browse: https://gitslice.io/slices/gitslice/gitslice
- Clone: `git clone https://gitslice.io/git/gitslice/gitslice.git`. Clones
  keep the account-rooted layout, so the module is under `gitslice/gitslice/`.
- Contribute: `gs init gitslice/gitslice`, then `gs create` and `gs submit`.
  On Linux or macOS with FUSE, `gs init --lazy gitslice/gitslice <dir>` mounts
  it instead. Git users can push `HEAD:refs/changes/new` to create a
  changeset.

Every change lands on Gitslice first. The `Mirror from Gitslice` workflow then
replays it onto `main` here. Do not push or merge to `main`: the exporter
refuses to run over commits that did not come from Gitslice
([design/21_self_hosting.md](design/21_self_hosting.md)).

## Agents on Cloudflare Artifacts

[`agents/`](agents/) gives every coding agent its own Git repository on
Cloudflare Artifacts, then lands everything they push in one Gitslice
codebase:

- reviewed by a Workers AI agent;
- validated path by path;
- merged line by line when two agents touched the same file.

Live dashboard: https://agents.gitslice.io. Demo video:
[release `agents-demo-2026-10-02`](https://github.com/gitslice-io/gitslice/releases/tag/agents-demo-2026-10-02).
Design: [design/22_agents_on_artifacts.md](design/22_agents_on_artifacts.md).

## Install

```bash
curl -fsSL https://gitslice.io/install.sh | sh        # prebuilt gs
brew install gitslice-io/tap/gitslice                 # or with Homebrew
go install gitslice.io/gitslice/cmd/gs@latest         # or with Go
```

Each release also carries its `install.sh`, listed in its `checksums.txt`, to
check before running; the same file is in the GitHub mirror at the release tag.
See [Install](https://gitslice.io/doc/cli) for the commands.

## Learn more

- **Start:** [gitslice.io/doc](https://gitslice.io/doc),
  [llms.txt](https://gitslice.io/llms.txt) (for agents, in plain text).
- **Design:** [conflict resolution and merge on submit](design/07_conflict_resolution.md),
  [lazy workspaces and checkpoints](design/30_lazy_workspaces.md),
  [the CLI and `gs mcp`](design/04_cli_design.md),
  [continuous integration](design/17_continuous_integration.md),
  [benchmarks](design/28_benchmarks.md).
- **Feedback:** `gs feedback "what went wrong or would help"`.

## License

Apache License 2.0. See [LICENSE](LICENSE).
