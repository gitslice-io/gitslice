# Gitslice Git Compatibility Design

This document explains how Gitslice exposes Git-compatible repositories while
keeping the native source graph, changesets, queues, and storage model as the
source of truth.

Related documents:

- [gitslice_architecture_design.md](gitslice_architecture_design.md): top-level architecture
- [storage.md](storage.md): commits, refs, trees, blobs, and projection inputs
- [core_api.md](core_api.md): gRPC APIs used by the Git gateway
- [cli_design.md](cli_design.md): native CLI and optional Jujutsu interop
- [execution_plan.md](execution_plan.md): rollout phases and Git workflow validation

## 1. Core Principle

Git compatibility is a boundary layer.

```text
Native global source graph first.
Git compatibility at the boundary.
```

Gitslice should not be implemented internally as a traditional Git server. Git
clients see ordinary Git repositories, but the native system stores global
commits, trees, refs, slices, changesets, queues, and policy metadata.

The Git gateway translates between Git protocol operations and native APIs.

## 2. Git Repository Identity

Each slice can be exposed as a Git repository.

Canonical Git URL format:

```text
https://gitslice.io/git/{account}/{slice}.git
```

Examples:

```bash
git clone https://gitslice.io/git/nicholas/identity.git
git clone https://gitslice.io/git/acme/payment.git
```

Resolution flow:

```text
Git URL
  -> account slug
  -> slice slug
  -> SliceService.ResolveSlice
  -> slice definition
  -> projected Git repository view
```

Slice identity, visibility, and roles are evaluated before serving Git data.

## 3. Supported Git Operations

Initial supported operations:

- `git clone`
- `git fetch`
- `git push`
- partial clone
- Git refs
- Git branches projected from native refs
- Git commits projected from global commits

Unsupported or constrained operations:

- Git sparse checkout is not a required compatibility feature. Slice projection
  is Gitslice's primary sparsity mechanism.
- Direct writes to protected accepted refs should be rejected or translated into
  changesets.
- Git-native repository administration should not mutate native storage objects.
- Git object ids are compatibility artifacts, not native object ids.

## 4. Clone And Fetch

Clone and fetch are read projections.

```text
git clone/fetch
  -> authenticate user
  -> resolve slice from URL
  -> resolve native target refs
  -> project native commits and trees into Git commits and trees
  -> stream Git pack data
```

The Git gateway reads through the core APIs:

```text
ResolveSlice
RepositoryService.GetRef
RepositoryService.GetCommit
RepositoryService.ResolvePath
RepositoryService.ListDirectory
RepositoryService.ReadFile
```

The native source of truth remains the global commit graph. Git clients receive
only the slice projection they are authorized to read.

Slices already define the visible working set, so clients should not need Git
sparse checkout to avoid cloning unrelated repository content. Partial clone can
still be useful to avoid downloading large blob contents inside a slice until
needed.

## 5. Projected Git Refs

When a slice is exposed as a Git repository, the Git gateway projects native refs
into Git refs.

Example:

```text
native target ref: refs/global/main
git ref:           refs/heads/main
```

Future branch support can project additional target refs:

```text
refs/global/branches/{branch}
refs/accounts/{account}/branches/{branch}
```

Projected Git refs are compatibility views. The native refs remain authoritative.

## 6. Synthetic Git Commits

Git commits exposed to clients are synthetic projections.

Mapping:

```text
GitCommit(slice=nicholas/identity, hash=A)
  -> GlobalCommit(G123)
  -> SliceDefinitionHash(D456)
```

One global commit may map to many synthetic Git commits because each slice sees
a different projected tree.

```text
GlobalCommit(G123)
  -> nicholas/identity Git commit A
  -> acme/payment Git commit B
```

Synthetic Git commit IDs must be stable for the same projection inputs:

```text
slice_id
slice_definition_hash
global_commit_id
projected_parent_git_commit_ids
projected_tree_id
author
message
timestamp policy
```

The projection cache key is:

```text
(slice_id, slice_definition_hash, global_commit_id)
```

Git clients must treat a slice definition change as a projection epoch change.
A slice's projected Git history may gain or lose commits when `included_paths`
change.

## 7. Path Projection

Git repositories expose a slice projection of canonical global paths.

Gitslice canonical paths are account-rooted:

```text
/nicholas/services/identity/auth.go
```

Inside a cloned slice repository, the checkout preserves the canonical path
layout minus the leading slash:

```text
identity/
  nicholas/
    services/
      identity/
        auth.go
```

There are no custom mount aliases. This keeps diffs, authorization, review,
policy resolution, and workspace behavior aligned with the native model.

## 8. Push Into Changesets

Protected targets should not allow ordinary Git pushes to write directly to the
accepted global ref.

Instead:

```bash
git push origin HEAD:refs/changes/new
```

or an equivalent server-supported push target should create or update a
changeset.

Server behavior:

```text
Git push
  -> authenticate user
  -> resolve slice from Git URL
  -> convert Git diff to global absolute paths
  -> verify every changed path is inside the authoring slice
  -> create or update changeset
  -> create patchset
  -> run validation
```

Direct push to a protected branch should either be rejected or translated into a
changeset according to the matching folder policy files for the pushed paths.

## 9. Changeset Refs

Changeset patchsets can be addressed with refs:

```text
refs/changes/{changeset_id}/{patchset_number}
```

These refs make it possible to integrate with Git tooling, CI systems, and
review systems without making changesets ordinary branches.

`refs/changes/new` can be supported as a Git push alias that asks the server to
allocate a new changeset id.

## 10. Validation Rules For Git Push

Git-originated writes must go through the same native validation as CLI or API
writes.

Validation includes:

- authoring slice containment
- read and write authorization
- covering slice resolution
- matching folder policy files
- queue selection
- required approvals
- required checks
- conflict detection against latest target ref

The Git gateway should not create native commits directly for normal users. It
creates or updates changesets and patchsets.

## 11. CI And Tooling Compatibility

The Git layer should support common ecosystem expectations:

- refs suitable for CI checkout
- stable synthetic commit ids for the same projection inputs
- fetchable changeset patchset refs
- partial clone for large trees

CI should run against projected Git commits but report status back to the native
changeset and patchset objects.

## 12. Jujutsu Interop

Jujutsu can interoperate through the Git-compatible slice projection. It should
remain optional: selected local jj/Git commits can be converted into Gitslice
changesets by `gs`, but jj must not bypass Gitslice folder policies, queues, or
submit validation.

The native CLI behavior and interop shape are defined in
[cli_design.md](cli_design.md#13-optional-jujutsu-interop).

## 13. Non-Goals

The Git compatibility layer should not:

- define the native storage model
- make every slice an independent Git repository internally
- allow Git pushes to bypass changeset, queue, or folder policy validation
- expose paths outside the authorized slice projection
- use Git object ids as native commit, tree, or blob ids
