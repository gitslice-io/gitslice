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

Live dashboard: https://agents.gitslice.io. Design:
[design/22_agents_on_artifacts.md](design/22_agents_on_artifacts.md).

## Install

```bash
curl -fsSL https://gitslice.io/install.sh | sh        # prebuilt gs
go install gitslice.io/gitslice/cmd/gs@latest         # or with Go
```

## License

Apache License 2.0. See [LICENSE](LICENSE).
