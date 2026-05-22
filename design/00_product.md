# Gitslice Product Overview

Gitslice is a source graph platform for teams that need one coherent codebase
without forcing every workflow through one physical Git repository.

The product should feel familiar to Git users, but it should make large-scale
work easier by treating slices, virtual workspaces, changesets, policy, and
submit queues as first-class product concepts.

## 1. Product Thesis

Modern codebases are often split across many repositories for reasons that are
operational rather than conceptual:

- Git repositories get too large.
- Teams need different visibility and write policies.
- CI and submit rules differ by folder or team.
- Developers and agents need only a small working set.
- Cross-repository refactors are hard to review and land safely.

Gitslice should provide one native global source graph with repository-like
slices at the boundary. Users work in small, understandable projections, while
the system keeps paths, history, policy, indexing, and submit validation
consistent.

## 2. Target Users

Primary users:

- engineers working in large multi-team codebases
- platform teams that own build, CI, policy, and source infrastructure
- organizations that want monorepo-style consistency without one giant Git repo
- AI coding agents that need scoped, policy-aware workspaces

Secondary users:

- open-source maintainers who want repository-like public slices
- CI systems and code review tools that need Git-compatible checkouts
- migration teams moving from many Git repositories toward one source graph

## 3. Product Principles

Gitslice product behavior should follow these principles:

- Native source graph first; Git compatibility at the boundary.
- Slices are product views, not storage shards.
- Changesets are the review and submission unit.
- A changeset has exactly one authoring slice.
- Cross-slice changesets are not supported.
- Folder policy and queue rules are source-controlled but cannot authorize their
  own weakening.
- Workspaces hydrate only what the user or agent needs.
- Caches and watchers improve performance, but server validation decides
  correctness.
- Git users should be productive without learning the native internals.

## 4. Core Product Objects

Account
: A globally unique user or organization namespace. Paths are rooted directly
  under account slugs, for example `/acme/payment`.

Slice
: A repository-like view over one account's source graph. A slice owns
  visibility, roles, included paths, and Git URL identity.

Workspace
: A local working area that can contain one or more slices and hydrates files on
  demand.

Changeset
: The unit of review and submission. It contains immutable patchsets, review
  state, policy requirements, queue requirements, and submit status.

Patchset
: An immutable version of a changeset's proposed file edits.

Folder policy
: Source-controlled metadata at `{folder}/.gitslice/policy.yaml` that adds
  approvals, checks, publicability, locks, or large-file requirements for paths
  below that folder.

Submit queue
: Source-controlled account policy that determines when a changeset can land.
  Queues define eligibility; target-ref sequencers serialize final ref updates.

Git projection
: A Git-compatible repository view generated from a slice. It supports clone,
  fetch, CI checkout, and push-to-changeset workflows without making Git storage
  the source of truth.

## 5. Primary Workflows

### 5.1 Native CLI Workflow

```text
gs workspace init
gs slice add acme/payment
edit files
gs status
gs cs create
gs cs submit
```

The user works in a sparse workspace. The CLI snapshots local edits into a
changeset patchset, uploads missing blobs, and submits through server-side
policy, queue, and conflict validation.

### 5.2 Git Compatibility Workflow

```text
git clone https://gitslice.io/git/acme/payment.git
edit files
git commit
git push origin HEAD:refs/changes/new
```

The Git gateway converts the pushed Git diff into a native changeset and
patchset. Direct writes to protected accepted refs are rejected or translated
into changeset workflows.

### 5.3 Policy Workflow

```text
/acme/payment/.gitslice/policy.yaml
```

Teams express folder-level requirements as versioned files. Policy changes are
reviewed through changesets, but weakening a policy requires approval under the
previous policy and relevant parent or account administration rules.

### 5.4 Submit Workflow

```text
changeset
  -> resolve changed paths
  -> resolve covering slices
  -> resolve folder policies
  -> resolve required queues
  -> run checks
  -> target-ref landing sequencer
  -> CAS ref update
```

The product should prefer clear blocked states over implicit best-effort submit.
If policy, queues, checks, indexes, or ref freshness are stale, submit should
block or retry rather than land under uncertain requirements.

## 6. Product Scope

MVP scope:

- account-rooted global paths
- slice creation and projection
- sparse native workspaces
- native `gs` changeset flow
- folder policy files
- versioned account queues
- per-target-ref landing sequencer
- Git clone/fetch from slice URLs
- Git push into changesets
- derived indexes for path coverage, policy, queue selection, and search
- correctness-first storage lifecycle and GC

Later scope:

- richer branch support
- advanced query language
- richer migration tooling from existing Git repositories
- Git LFS protocol compatibility for configured large-file paths
- IDE integrations
- hosted review UI
- organization analytics and policy dashboards

## 7. Product Non-Goals

The product should not:

- expose cross-slice changesets
- provide atomic multi-slice submission
- use `/users` or `/orgs` path prefixes
- make Git sparse checkout a core workflow
- make Git object ids the native object ids
- allow policy or queue files to weaken themselves
- make client-side file watchers authoritative for correctness
- require users to understand internal storage objects for normal workflows

## 8. Document Map

- [01_gitslice_architecture_design.md](01_gitslice_architecture_design.md): architecture and system model
- [02_storage.md](02_storage.md): storage, object model, refs, hashing, GC, and replication
- [03_core_api.md](03_core_api.md): gRPC services, proto messages, and gateway behavior
- [04_cli_design.md](04_cli_design.md): native `gs` CLI and workspace behavior
- [05_git_compatibility.md](05_git_compatibility.md): Git gateway, projections, and push behavior
- [06_indexing.md](06_indexing.md): derived indexes, events, freshness, and rebuilds
- [07_execution_plan.md](07_execution_plan.md): implementation phases and workflow validation
