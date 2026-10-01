# Gitslice

Gitslice is Git-compatible source infrastructure for humans and coding agents:
one global source graph, repository-like slices, and changesets with
server-side submit validation. See https://gitslice.io.

## Where the source lives

**This GitHub repository is a read-only mirror.** The canonical source is the
public slice `gitslice/gitslice` on Gitslice itself:

- Browse: https://gitslice.io/slices/gitslice/gitslice
- Clone: `git clone https://gitslice.io/git/gitslice/gitslice.git`. Clones
  keep the account-rooted layout, so the module is under `gitslice/gitslice/`.
- Contribute: `gs init gitslice/gitslice`, then `gs create` and `gs submit`.
  Git users can push `HEAD:refs/changes/new` to create a changeset.

Every change lands on Gitslice first. The `Mirror from Gitslice` workflow then
replays it onto `main` here, so commits pushed straight to GitHub are refused
by both the branch rules and the exporter
([design/21_self_hosting.md](design/21_self_hosting.md)).

## Install

```bash
curl -fsSL https://gitslice.io/install.sh | sh        # prebuilt gs
go install gitslice.io/gitslice/cmd/gs@latest         # or with Go
```
