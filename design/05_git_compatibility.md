# Gitslice Git Compatibility Design

This document explains how Gitslice exposes Git-compatible repositories while
keeping the native source graph, changesets, submit validation, and storage
model as the source of truth.

Related documents:

- [00_product.md](00_product.md): product overview and Git-facing workflows
- [01_gitslice_architecture_design.md](01_gitslice_architecture_design.md): top-level architecture
- [02_storage.md](02_storage.md): commits, refs, trees, blobs, and projection inputs
- [03_core_api.md](03_core_api.md): gRPC APIs used by the Git gateway
- [04_cli_design.md](04_cli_design.md): native CLI behavior
- [07_conflict_resolution.md](07_conflict_resolution.md): path-level conflicts and batched submit
- [08_mvp_implementation.md](08_mvp_implementation.md): Go MVP implementation shape and test harness
- [09_execution_plan.md](09_execution_plan.md): rollout phases and Git workflow validation

## 1. Core Principle

Git compatibility is a boundary layer.

```text
Native global source graph first.
Git compatibility at the boundary.
```

Gitslice should not be implemented internally as a traditional Git server. Git
clients see ordinary Git repositories, but the native system stores global
commits, trees, refs, slices, changesets, and submit metadata.

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
- A push to a slice repository must create or update a changeset for that slice
  only. If the diff touches paths outside the URL's slice projection, the push is
  rejected instead of becoming a cross-slice changeset.
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

### 4.1 Projection Cache And Packfiles

Synthetic Git objects and packfiles should be cached. This is required for
operational correctness under load: clone and fetch must not depend on
recomputing large projected histories from scratch for every client.

Projection cache keys must include every input that changes Git-visible output:

- slice id
- slice definition hash
- native target ref and commit id
- projection algorithm version
- Git protocol capabilities requested by the client
- large-blob projection mode

Cache entries are derived artifacts. They must never decide authorization,
policy, or submit correctness. Every request still authenticates the caller and
checks slice visibility before serving cached bytes.

Packfile generation should use stable checkpoints. A large clone can stream from
a cached base pack for `(slice, definition, commit)`, while fetch computes only
the incremental pack relative to the client's advertised commits. Cache entries
must register reachability leases or expiration timestamps so storage GC can
delete obsolete synthetic Git objects and packfiles safely.

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
submit validation, and workspace behavior aligned with the native model.

## 8. Push Into Changesets

Protected targets must not allow ordinary Git pushes to write directly to the
accepted global ref. All pushes to protected refs are intercepted and routed
through the changeset merge path.

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
  -> reject if the diff would require more than one authoring slice
  -> create or update changeset
  -> create patchset
  -> run validation
```

Direct push to a protected branch must be translated into a changeset that
follows the same submit validation as native writes. The Git gateway must never
allow a direct ref update to bypass the changeset pipeline. The server should
return a message informing the user that their push was converted to a changeset
and is awaiting validation and submit.

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
- path base predicate recording
- submit requirement resolution
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

### 11.1 Large Blob Handling

Git LFS support is a confirmed non-goal. Native Gitslice storage stores blobs
and immutable tree nodes in the filesystem object store, with authoritative
commit roots, blob metadata, and reachability metadata in PostgreSQL. The Git
gateway projects those native blobs as ordinary Git blob objects for
clone/fetch/push. This avoids unnecessary architecture complexity such as
pointer rewriting and binary blob redirection.

## 12. Non-Goals

The Git compatibility layer should not:

- define the native storage model
- make every slice an independent Git repository internally
- allow Git pushes to bypass the changeset merge path or submit validation
- create cross-slice changesets from one Git push
- expose paths outside the authorized slice projection
- use Git object ids as native commit, tree, or blob ids
- provide external VCS-specific interop behavior in the MVP
