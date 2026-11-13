# 35. Git Mirrors

Status: built 2026-11-13, not deployed.

## Goal

Let any slice keep a copy of itself in a Git repository elsewhere, such as
GitHub, as a backup and for people who read code there. An owner sets it up
once, and every landing and every tag reaches the copy within moments.

Before this, the only mirror was ours: a GitHub Actions workflow in
`gitslice-io/gitslice` ran `ops/mirror` on a 10-minute schedule
(design/21, Phase 2). GitHub runs scheduled workflows late, often by hours, and
nobody else could set one up without copying the exporter, the workflow and
two tokens.

## Decisions

1. **A slice setting, not a trigger.** A slice is what `git clone` gives you:
   one repository with one main branch (the slice's default target) and its
   tags. A mirror maps one slice to one repository. The remote and its token
   are configuration, like the CI daemon or `merge_on_submit`, so they live
   outside the tree: a changeset cannot point the backup somewhere else.
2. **The server pushes.** It already keeps a Git projection of every slice for
   `git clone` (design/23), and its image has `git`. A Cloudflare Worker has
   neither `git` nor the CPU time to build packs.
3. **What is pushed is the slice's projection, rooted like `ops/mirror`.**
   When the slice has a single root folder (`/gitslice/gitslice`), that folder
   becomes the repository root, as `ops/mirror -subdir` does and as the Go tag
   copies already assume. A slice of several folders is pushed with full
   paths. History is the first-parent history of the default target; each
   commit keeps its author, committer, dates and message, so it is the same
   commit every time it is computed.
4. **Never force.** The mirror finds its place by the `Gitslice-Commit` and
   `Git-Commit` trailers, exactly as `ops/mirror` does, so an existing mirror
   (ours) is adopted where it stands. If the remote branch has commits that
   did not come from Gitslice, or its tree differs, the push stops and the
   error is shown. An empty or missing branch receives the whole history.
5. **Tags are pushed once.** New tags of the projection are pushed; a tag the
   remote already has is never moved. Go's `<folder>/vX.Y.Z` tag copies stay
   out of the mirror, because its modules live at its root.

## Setting it up

```
gs slice secret set acme/app GITHUB_MIRROR_TOKEN < token.txt
gs slice mirror set acme/app --url https://github.com/acme/app.git \
    --token-secret GITHUB_MIRROR_TOKEN
gs slice mirror acme/app                                   # status
gs slice mirror push acme/app                              # push now
gs slice mirror rm acme/app
```

- **The token** is a secret of the slice's account, visible at the slice's
  folders (design/33). For GitHub, a fine-grained token with Contents
  read/write on that one repository. The setting stores the secret's name,
  never the value.
- **The user name** sent with the token defaults to `x-access-token`
  (GitHub). GitLab and others take `--username`.
- **The branch** defaults to `main`.
- **The web app** has the same in slice settings ("Mirror to Git", under the
  Secrets panel where the token is added): the URL, the token secret (chosen
  from the slice's secrets), the branch, the user name, the status, "Push
  now" and "Stop mirroring".
- Owners and admins of the slice's account may see and change it, as for
  other slice settings.

## When it pushes

A row in `slice_git_mirrors` (migration 0051) holds the setting and its state.
Two counters make the queue: `pending_seq` grows whenever something needs
pushing, `pushed_seq` records what the last good push covered.

- **Marking.** The webhook dispatcher's drain (design/24) already sees every
  recorded event. For each `push` it marks the mirrors of the account whose
  slice includes a changed path; for each `tag.created`, the account's
  mirrors. Saving the setting and "Push now" mark it too.
- **Claiming.** The same drain claims mirrors with `pending_seq > pushed_seq`
  whose retry time has come and whose lease is free, with a 30-minute lease.
- **Running.** Like covers (design/23), the push is CPU work, and Cloud Run
  gives CPU only to a container serving a request, so the server asks itself
  (`POST /git/_internal/mirror`, signed with the internal key) and the push
  runs in that request. Without a self URL (tests, local servers) it runs in
  the drain.
- **Finishing.** A push reads the projection after it read `pending_seq`, so
  a good push sets `pushed_seq` to that value; anything marked during the push
  stays pending and runs next. A failure keeps it pending and retries after 1
  m, 5 m, 30 m, 2 h, 6 h, then every 12 h, with the error shown.

## How a push works

1. Bring the slice's projection up to date (the same code `git fetch` uses).
2. A work repository per slice sits beside the projection
   (`<git cache>/.mirrors/<account>/<slice>.git`). It borrows the
   projection's objects through `objects/info/alternates`, so nothing is
   copied, and it holds `refs/mirror/main` and `refs/mirror/tags/*` set to the
   projection's refs. The projection never sees the remote's objects, so they
   cannot end up in its covers.
3. `ls-remote` reads the remote branch and tags; the branch is fetched.
4. `internal/gitmirror` (the exporter, moved out of `ops/mirror`, which now
   calls it) finds the sync point and writes the new commits.
5. A lazily built projection has commits and trees but not every file
   (design/30). Before pushing, the files the new commits need and the remote
   lacks are listed (`rev-list --objects --missing=print`) and read from the
   object store into the projection, as a fetch would.
6. Push the branch (fast-forward only), then the new tags.

The work repository is a cache. A new instance builds it again by fetching
the remote branch; a failed push removes it, so the next attempt starts
clean.

## Code

| Part | Code |
| --- | --- |
| Exporter (shared with `ops/mirror`) | `internal/gitmirror` |
| Work repository, hydration, credentials | `internal/gitcompat/git_mirror.go` |
| Self request `POST /git/_internal/mirror` | `internal/gitcompat/mirror_http.go` |
| Queue, retries, RPCs | `service/git_mirror.go`; hooked into `internal/webhooks/dispatcher.go` (`Options.Mirrors`) |
| Storage | `storage.GitMirrorStore`; `internal/postgres/git_mirror_store.go`, migration 0051; `internal/storage/memory/git_mirrors.go` |
| CLI | `internal/cli/slice_mirror.go` (`gs slice mirror`) |
| Web | `web/src/components/slices/SliceGitMirrorPanel.tsx` |
| Tests | `internal/gitmirror`, `internal/gitcompat/git_mirror_test.go`, `service/git_mirror_test.go`, `internal/postgres/git_mirror_store_test.go`, `tests/cli/slice_git_mirror_test.go` |

## Security

- **Targets** are `https` URLs on public addresses, checked as for webhooks
  (design/24), unless the server allows private targets (tests, local). The
  host is resolved before each push, every address must be public, and `git`
  is pinned to the checked address (`http.curloptResolve`), so DNS cannot
  change in between.
  `git` runs with `GIT_ALLOW_PROTOCOL=https`, no redirects, no terminal
  prompt and no system or global config.
- **The token** reaches `git` as an `http.extraHeader` in the environment
  (`GIT_CONFIG_*`), never on the command line, and is removed from any error
  before it is stored or logged.
- **Who can push where.** Only an owner or admin can set the URL, and the
  token is theirs. The server pushes nothing a reader of the slice could not
  already clone.

## Our own repository

`gitslice-io/gitslice` has the pre-cutover history and is fed by the export
workflow. Switching:

1. Create `GITHUB_MIRROR_TOKEN` (Contents read/write on
   `gitslice-io/gitslice` only) on `/gitslice/gitslice`
   (`gs slice secret set gitslice/gitslice GITHUB_MIRROR_TOKEN`).
2. `gs slice mirror set gitslice/gitslice --url
   https://github.com/gitslice-io/gitslice.git --token-secret
   GITHUB_MIRROR_TOKEN`. The first push adopts the mirror through the
   trailers.
3. Pushes made with a personal token start workflows, unlike pushes made
   with `GITHUB_TOKEN`, so `ci.yml` (push to main) and `release.yml` (push of
   `v*`) start by themselves.
4. Set the repository variable `GITSLICE_EXPORT_ENABLED=false`. The workflow
   stays as a manual fallback.

The ruleset "main is a Gitslice mirror" forbids deletion and non-fast-forward
updates only, so the token's pushes are allowed.

## Not in v1

- Several mirrors per slice, or mirroring other targets than the default.
- SSH remotes and GitHub App installations (a token per repository is
  enough for a backup).
- Pulling changes made on the mirror back into Gitslice.
