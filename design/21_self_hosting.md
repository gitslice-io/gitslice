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
| Projection uses imported authors and adds a `Git-Commit` trailer | `history.go` | follow-up |
| Checks files, mirror workflows, exporter (`ops/mirror`) | `.gitslice/`, `.github/workflows/mirror-*.yml` | this change |

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

## Phase 1: Read-Only Copy In Gitslice

Runbook, after the Phase 1 code is deployed:

1. **Operator.** Put the operator's subject id (`gs auth status --json`) in
   the `_OPERATOR_SUBJECTS` Cloud Build substitution, and deploy.
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

2. **Protect GitHub `main`.** Add a ruleset that restricts updates, with
   GitHub Actions as the only bypass actor, so only the export workflow can
   push.
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
  1. Set `GITSLICE_EXPORT_ENABLED=false` and remove the ruleset.
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
