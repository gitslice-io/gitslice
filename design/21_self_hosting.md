# 21. Self-Hosting: Gitslice Source On Gitslice

Status (2026-10-01):

- Phase 0 is live.
- The code for Phases 1–3 is merged or in review (see "Work Items").
- Each operational step below is a runbook, and each phase has an exit check.

## Goal

Develop Gitslice on Gitslice. The canonical source of this repository moves
from GitHub to the slice `gitslice/gitslice`. Every change becomes a native
changeset gated by required checks, and GitHub becomes a read-only mirror fed
by Gitslice.

Requirements:

- **Dogfood the daily loop.** Humans and agents write through `gs`
  (workspaces, changesets, submit), and checks run from `.gitslice/checks.yaml`.
- **`go install` keeps working at every step.** In the end state it is served
  by Gitslice's own Git endpoint, not GitHub.
- **Public docs and the website use only `gitslice.io` names.** That covers the
  Go import path, the install script, release downloads, source links and
  clone URLs.
- **The deploy path never depends on the system being deployed.** If Gitslice
  production is down, a fix must still be shippable.

Non-goals:

- Moving the deploy triggers (Cloud Build, Workers Builds) off GitHub. They
  keep reading the mirror.
- Two-way sync. Exactly one side is writable at any time.

## Decisions

- **Location.** Organization account `gitslice`, path `/gitslice/gitslice`,
  public slice `gitslice/gitslice`, mirroring `gitslice-io/gitslice`.
- **Names before hosting.** Public names point at `gitslice.io`, and
  `gitslice.io` decides where the bytes come from:
  - **Go:** import path `gitslice.io/gitslice`, resolved by a `go-import` meta
    tag the web Worker serves (`web/src/lib/goImport.ts`).
  - **Releases:** downloads under `https://gitslice.io/releases/…`, redirected
    to the current store (`web/src/lib/releases.ts`).
  - **Source:** links to `https://gitslice.io/slices/gitslice/gitslice`.
- **One writable side.** GitHub is the source of truth until the flip, then
  Gitslice is. The mirror workflows share a concurrency group and each refuses
  to run while the other direction is enabled.
- **Deploys read the mirror.** Cloud Build and Workers Builds keep their
  GitHub triggers. Break-glass: a hotfix can land on the mirror and be
  re-applied as a changeset afterwards.

## Work Items

| Gap | Change | Where |
|---|---|---|
| Module path | `github.com/gitslice-io/gitslice` → `gitslice.io/gitslice` | #385 |
| Anonymous Git reads of public slices | `internal/gitcompat/http.go` | #386 |
| Vanity `go-import`, `/releases` redirects, install docs | `web/` | #387 |
| Stable, deterministic per-slice Git history; stale-base and empty-slice push | `internal/gitcompat/history.go` | #388 |
| Organization accounts and members (`gs account`) | `service/account_members.go` | #389 |
| Import keeps original authors, dates, messages (`Commit.git_import`) | `service/repository.go` | #390 |
| CI daemon runs in-slice checks nobody bundled | `service/check_dispatch.go` | #391 |
| Immutable slice tags (`gs tag`), published in the projection | `service/tags.go`, `history.go` | #392 |
| Checks files, mirror workflows, exporter (`ops/mirror`) | `.gitslice/`, `.github/workflows/mirror-*.yml` | #393 |
| Projection keeps imported authorship (`Git-Commit` trailer) | `history.go` | #394 |
| Projection keeps symlinks (mode 120000) | `history.go` | #396 |
| Faster cold projection builds; no body deadlines for Git | `history.go`, `http.go` | #397 |
| Gzipped fetch negotiations reach http-backend | `http.go` | #398 |
| Workspaces keep symlinks | `internal/cli/cli.go` | #399 |
| Phase 1 script, operator subject in `cloudbuild.yaml` | `ops/selfhost/phase1.sh` | this change |
| Phase 1 docs (source links, build from source) | `web/` | #395 (draft until Phase 1 is live) |
| Phase 2 workflow docs (CLAUDE.md, AGENTS.md, README) | — | #400 (applied at cutover in Gitslice) |

## Go Install

The module path is `gitslice.io/gitslice`, so the command is:

    go install gitslice.io/gitslice/cmd/gs@latest

- **v0.2.0** is the first release under this path. `v0.1.x` declare the old
  path.
- **Stage 1 (live).** The Worker answers `GET /gitslice/…?go-get=1` with:

      <meta name="go-import" content="gitslice.io/gitslice git https://github.com/gitslice-io/gitslice">

  Verified on 2026-10-01 through `proxy.golang.org` and with
  `GOPROXY=direct`.
- **Stage 2.** Change the `goModules` entry to:

      <meta name="go-import" content="gitslice.io/gitslice git https://gitslice.io/git/gitslice/gitslice.git gitslice/gitslice">

  - The fourth field (Go 1.25+) puts the module root in the slice's
    subdirectory.
  - Version tags then need that prefix. The projection publishes it
    automatically for semver tags on single-prefix slices (for example
    `gitslice/gitslice/v0.3.0`).
  - Users on the default proxy are unaffected. `GOPROXY=direct` users need
    Go 1.25+.

Before switching, every version already published under `gitslice.io/gitslice`
must have identical content on both hosts, because `sum.golang.org` has
recorded its hash. Identical Git tree ids imply identical module files, so
compare the trees:

    git -C <github clone> rev-parse vX.Y.Z^{tree}
    git -C <gitslice clone> rev-parse gitslice/gitslice/vX.Y.Z:gitslice/gitslice

## Phase 0: Names (Done)

Done:

- the module rename;
- the Worker `go-get` handler and `/releases` redirects;
- `install.sh`, `llms.txt` and the CLI install docs use gitslice.io names;
- `v0.2.0` tagged and released.

Exit check (passed 2026-10-01):

- `go install gitslice.io/gitslice/cmd/gs@latest` prints `gs version v0.2.0`.
- `curl -fsSL https://gitslice.io/install.sh | sh` installs v0.2.0.

## Staging Rehearsal (2026-10-01)

Every phase was rehearsed on staging (agenttools.dev, R2-backed) against the
real GitHub history.

Phase 1:

- **Organization and slice.** Created the reserved `gitslice` organization
  through an operator, plus the member, the folder and the public slice.
- **Import.** A `--deep` import of all 446 commits took 8 minutes, about one
  commit per second.
- **Cold clone.** An anonymous clone that builds the whole projection took
  26 s with an empty object cache: 427 native commits, 2,868 blob versions,
  108 MB. A warm fetch took 0.5 s.
- **Drift check.** The projected `gitslice/gitslice` tree is identical to
  GitHub `main`.

Phase 3:

- A native `v0.2.0` tag on the commit imported from GitHub's `v0.2.0` projects
  as both `v0.2.0` and `gitslice/gitslice/v0.2.0`.
- The module tree at that tag is identical to GitHub's (`c0f593c…`), so the Go
  checksums will match.

Phase 2:

- **Exporter.** A native change exported to a scratch copy of the GitHub
  repository. `ops/mirror` found the sync point through the `Git-Commit`
  trailer and replayed one commit with an identical tree. A second run was a
  no-op.
- **Workspace layout.** `gs init gitslice/gitslice` materializes the module
  under `gitslice/gitslice/`.

The rehearsal found four bugs, all fixed before production:

| Bug | Fix |
|---|---|
| Symlinks projected as regular files | #396 |
| Gzipped fetch negotiations failed with HTTP 500 | #398 |
| Workspaces turned symlinks into regular files on the next changeset | #399 |
| Cold projection builds outran the gateway write deadline and Cloudflare's proxy timeout | #397 |

Production preparation:

- **Operator:** agent `gitslice-operator` (`agent_0644c3a2c20a390ca6142f0973dbdcf4`).
  Its key is kept by the maintainer, not in CI, and it is the default
  `_OPERATOR_SUBJECTS`.
- **Mirror bot:** agent `gitslice-mirror` (`agent_16a3fb4d536b2042ffa9b3f02b695674`).
  Its key is the `GITSLICE_MIRROR_TOKEN` repository secret.
- **Ownership:** both agents were registered with the maintainer's email, so
  the maintainer can claim them at https://gitslice.io/claims.

## Production Run (2026-10-01 – 2026-10-02)

Phase 1:

- **Deploys.** Merged code shipped through the Cloud Build trigger, ahead of
  the daily schedule:
  - `14061ca`: Phase 1 code;
  - `c9b4a74`: #404;
  - `ec79b88`: #408;
  - `c38a1b3`: #410.
- **Organization and slice.** `ops/selfhost/phase1.sh` created:
  - the `gitslice` organization, owned by `gitslice-operator`;
  - `gitslice-mirror` as a writer;
  - the public slice `gitslice/gitslice`.
- **Import.** A `--deep` import of all 451 commits took about 45 minutes,
  3–7 s per commit, versus 1 s on staging. Each commit is a sequence of
  database round trips from Cloud Run to Neon, and staging's Postgres is
  local. Native submits pay the same per-commit cost.
- **Drift check.** GitHub `main` moved twice during the import. A re-run took
  19 s, including a cold projection build on a fresh revision. It imported
  the two new commits, and the trees then matched.
- **Tags.** A native `v0.2.0` was created on the commit imported from
  `b00271e`. It projects as both `v0.2.0` and `gitslice/gitslice/v0.2.0`, and
  its module tree is `c0f593c…` on both hosts.
- **Scheduled import.** `GITSLICE_IMPORT_ENABLED` is on. The first run, on
  the #395 merge, imported that commit in 4 s.
- **Docs.** #395 went live through Workers Builds; `llms.txt` and the CLI
  docs now point at the slice.
- **Workspace.** `gs init gitslice/gitslice` hydrated 503 files in 2.5
  minutes from a cold cache, and the file list matches the Git tree exactly.
- **CI host.**
  - The daemon `gitslice-ci` runs as a PM2 process on the staging host, under
    the agent `gitslice-checks` (`agent_c4c59942329c74d3303cfacac41154d2`).
    `gitslice-ci` itself is a reserved username.
  - The Docker container `gitslice-ci-pg` provides the e2e database.
  - Runbook: "CI daemon" in the deployment skill.

Exit-check and CI dry run:

- `gs ci` ran in a workspace with a Go edit and a web edit, so that all six
  checks applied.
- A draft changeset then went to the CI daemon and was never submitted.
- Every issue below was fixed before the cutover:

| Finding | Fix |
|---|---|
| A required check from a subdirectory's checks file (`gitslice/web/build`) errored for every change outside that directory | #404 |
| `gs ci` failed `test` inside a workspace: `TestBrowsePrintsWebURL` found the enclosing workspace | #405 |
| The daemon's Go builds failed on VCS stamping, from a stray empty `/tmp/.git` on the CI host | #406 |
| Workspaces ignored `.gitignore`. After `gs ci`, `gs status` hashed 600 MB of `node_modules` for minutes, and `gs create --all` would have committed it | #407 |
| The CI daemon re-registered under a new id once its agent joined the `gitslice` org, so the slice's `ci_daemon_id` went stale | #408 |
| `TestCLISliceCRUD` flaked under load on the 2-CPU CI host | #409 |
| After one `gs sync` of a `gs create` draft, the next sync or modify failed with a base mismatch, and restacked patchsets were never checked | #410 |
| The e2e database filled its tmpfs, because default WAL settings let WAL grow toward 1 GB | `gitslice-ci-pg` now runs with bounded WAL and no fsync |
| Check logs kept only their first 256 KiB, which dropped test failures and summaries | #411 |
| Concurrent checks on 2 CPUs made the e2e tests flake | #412, plus `GITSLICE_CHECK_CONCURRENCY=1` on the daemon |
| #393 committed a 2.8 MB build of `ops/mirror` at the repository root | removed by a Gitslice changeset |

After the fixes, all six checks passed on the daemon for the dry-run
patchset. `e2e` needed one rerun, before #412.

Measured on the CI host, which has 2 CPUs and 3.3 GiB of memory:

| Operation | Time |
|---|---|
| `gs ci`, all six checks | 8.5 min |
| `web/build` on the daemon | 8 min |
| e2e | 2–5 min |
| `gs init` | 2.5 min |
| `gs sync` of two paths | 3 min |

Phase 2 (cutover at 05:51 UTC on 2026-10-02):

- **Required checks.**
  - `gitslice/gofmt`
  - `gitslice/vet`
  - `gitslice/test`
  - `gitslice/build`
  - `gitslice/e2e`
  - `gitslice/web/build`

  These are the server's account-relative check names.
- **GitHub `main`.** The ruleset blocks deletion and force pushes. Updates
  stay open; see step 2 of the runbook.
- **Final import.** It covered 462 commits.
- **Flip.** `GITSLICE_IMPORT_ENABLED=false`, `GITSLICE_EXPORT_ENABLED=true`.
- **First export.** "mirror head 6f125a7cb7a6, 0 new commit(s), 0 new tag(s)".
- **First native changeset.** This one: the workflow docs from #400 and this
  record.

Follow-ups:

- `gs ci` names checks from global paths (`gitslice/gitslice/gofmt`), and the
  server from account-relative ones (`gitslice/gofmt`). Results bundled with
  `gs cs capture` therefore never satisfy required checks, and the daemon
  runs them again.
- `TestSliceTagsInGitProjection` flaked once under load.
- Cold `gs init` and `gs sync` are slow. Both fetch objects one at a time.

## Phase 1: Read-Only Copy In Gitslice

Runbook, after the Phase 1 code is deployed. `ops/selfhost/phase1.sh`
automates steps 3–4 and the exit check, and is safe to re-run.

1. **Operator.** `_OPERATOR_SUBJECTS` in `cloudbuild.yaml` names the
   `gitslice-operator` agent. Replace it with a human's subject id
   (`gs auth status --json`) once one is set up.
2. **Mirror bot.** Register it with an isolated home, so your own CLI config
   is untouched:

   ```bash
   HOME=~/.config/gitslice-mirror gs auth register-agent --username gitslice-mirror --email <owner email>
   ```

3. **Organization and slice.**

   ```bash
   gs account create-org gitslice --owner <your username>
   gs account set-member gitslice gitslice-mirror --role writer
   printf 'mkdir /gitslice/gitslice\nquit\n' | gs shell --slice gitslice/home --no-color
   gs slice create gitslice/gitslice --include /gitslice/gitslice --visibility public
   ```

4. **First import.** The workflow only imports new commits afterwards.

   ```bash
   gs import gitslice-io/gitslice --deep --mount /gitslice/gitslice --slice gitslice/gitslice
   ```

5. **Scheduled import.** Turn on the workflow (`mirror-import.yml`):

   ```bash
   gh secret set GITSLICE_MIRROR_TOKEN < <bot API key>
   gh variable set GITSLICE_IMPORT_ENABLED --body true
   ```

6. **Native tags for published versions.** For example, `v0.2.0` on the
   native commit whose `git_import.git_commit_id` is `b00271e…`:

   ```bash
   gs tag create v0.2.0 --slice gitslice/gitslice --commit <native id>
   ```

7. **Docs rows marked 1:** the "Source" link and build-from-source
   instructions point at gitslice.io.

Constraint: make no native writes under `/gitslice/gitslice` in this phase.
Import applies Git deltas on top of head without conflict detection.

Exit check:

- A projected clone diffs clean against GitHub `main`:

  ```bash
  git clone https://gitslice.io/git/gitslice/gitslice.git
  git rev-parse HEAD:gitslice/gitslice   # equals GitHub main^{tree}
  ```

- `gs ci` in a `gs init gitslice/gitslice` workspace agrees with GitHub
  Actions.

## Phase 2: Flip The Source Of Truth

Runbook:

1. **Checks.**

   ```bash
   gs agent start --name gitslice-ci       # on the CI host
   gs slice set-ci-daemon gitslice/gitslice <daemon id>
   gs slice secret set gitslice/gitslice GITSLICE_TEST_DATABASE_URL <url>
   gs slice update gitslice/gitslice --required-check <id> ...   # ids from gs ci --json
   ```

2. **Protect GitHub `main`.** Add the ruleset "main is a Gitslice mirror":
   no deletion, no force pushes.
   - Restricting ordinary updates needs a bypass actor for the export job.
     GitHub refuses the Actions app as one, and the organization disables
     deploy keys.
   - Two ways to enforce it:
     - Enable deploy keys for the organization, then push the export with a
       write deploy key (the approach in closed #413).
     - Create an organization GitHub App for the export job.
   - Until one of those exists, the exporter is the guard. It refuses to run
     over commits that did not come from Gitslice.
3. **Final import.** Dispatch `mirror-import.yml`, wait for it to finish, then:

   ```bash
   gh variable set GITSLICE_IMPORT_ENABLED --body false
   gh variable set GITSLICE_EXPORT_ENABLED --body true
   ```

4. **First export.** Dispatch `mirror-export.yml`. It must find GitHub's head
   through the `Git-Commit` trailer and report 0 new commits.
5. **Workflow docs.** Land the CLAUDE.md, AGENTS.md and deployment-skill
   updates as the first Gitslice-native changeset (worktrees → `gs init`
   workspaces, PRs → `gs create` / `gs submit`), plus a GitHub README marking
   the repository as a mirror.

How the exporter works (`ops/mirror`):

- It replays each projected commit after the sync point onto `main`: the
  slice subdirectory's tree, with the projected author, committer and
  message.
- It refuses to run when `main` has commits that did not come from Gitslice,
  or when its tree differs from the matching projected commit.
- It pushes new tags. Version tags start `release.yml` through
  `workflow_dispatch`.

Exit check:

- A week of changes lands only through `gs submit`.
- Cloud Build and Workers Builds deploy from the mirror unchanged.

## Phase 3: Gitslice Serves Git And Go

1. Create releases natively. The exporter pushes the tag and starts
   `release.yml`:

   ```bash
   gs tag create v0.3.0 --slice gitslice/gitslice -m "..."
   ```

2. Run the tree-equality check for every published `gitslice.io/gitslice`
   version.
3. Switch the `go-import` meta tag to stage 2.

Exit check: with Go 1.25+,

    GOPROXY=direct go install gitslice.io/gitslice/cmd/gs@<new tag>

resolves against `https://gitslice.io/git/gitslice/gitslice.git`.

## Risks And Rollback

- **Echo loop.** If the importer and the exporter both run, exported commits
  are imported again. Both workflows check both variables and share one
  concurrency group. Keep the one-writable-side rule.
- **A Gitslice outage blocks development.** Break-glass:
  1. Commit the fix to the mirror and deploy from it.
  2. Once Gitslice is back, re-apply the fix as a changeset.
  3. Reset `main` to the exporter's output.
- **Rolling back the flip.**
  1. Set `GITSLICE_EXPORT_ENABLED=false`.
  2. GitHub is current as of the last export. Re-apply by hand any changesets
     submitted after it.
- **Cold projection builds.** Each Cloud Run instance builds a slice's history
  once, then extends it. Watch first-clone latency after deploys. If it
  matters, cache projection bundles in object storage; the deterministic ids
  allow it.

## Open Questions

- Should release assets stay on GitHub behind `/releases`, or move to R2?
- The repository has no `LICENSE`. pkg.go.dev will not render the module's
  documentation without one.
