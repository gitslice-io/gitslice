# Gitslice Product Overview

Gitslice is a source graph platform for teams that need one coherent codebase
without forcing every workflow through one physical Git repository.

The product should feel familiar to Git users, but it should make large-scale
work easier by treating slices, virtual workspaces, changesets, and submit
validation as first-class product concepts.

The MVP should be a CLI-first product. The first complete user experience should
be `gs`: authentication, workspace setup, slice hydration, local edit capture,
changeset creation, and submit from the command line. Web UI, IDE
plugins, and richer dashboards can build on the same backend later, but they
should not be required for the initial product to be usable.

## 1. Product Thesis

Modern codebases are often split across many repositories for reasons that are
operational rather than conceptual:

- Git repositories get too large.
- Teams need different visibility and write policies.
- CI and submit rules differ by code area or team.
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
- The MVP is CLI-first; `gs` should be the first complete product surface.
- Slices are product views, not storage shards.
- Changesets are the review and submission unit.
- A changeset has exactly one authoring slice.
- Cross-slice changesets are not supported.
- Submit requirements are explicit slice settings and are enforced server-side.
- Workspaces hydrate only what the user or agent needs.
- Caches and watchers improve performance, but server validation decides
  correctness.
- Git users should be productive without learning the native internals.

## 4. Account And Auth Model

Accounts are both namespace owners and collaboration containers. Every
source-path root belongs to exactly one account:

```text
/{account}/...
```

An account can represent:

- a user account, such as `/nicholas`
- an organization account, such as `/acme`
- a service-owned account, if the product later needs managed system code

Account kind is metadata. It is not encoded in the path. Gitslice should not use
`/users` or `/orgs` prefixes. Because paths are rooted directly under account
slugs, the account service must maintain a globally unique slug registry.

### 4.1 Identity Types

Gitslice should support these identities:

- human users
- organization groups or teams
- service accounts for CI and automation
- agent identities for AI coding agents
- short-lived sessions created by the CLI or Git gateway

Every authenticated request has:

```text
subject_id
subject_type
session_id
account_memberships[]
scopes[]
issued_at
expires_at
```

The subject may be a human, service account, or agent. Authorization should not
depend on a display name or email address.

### 4.2 CLI Authentication

The MVP authentication flow should be optimized for the CLI:

```text
gs auth login
  -> browser or device-code login
  -> exchange identity-provider session for Gitslice refresh token
  -> store refresh token in the OS credential store where available
  -> mint short-lived access tokens for API calls
```

The CLI should expose:

```bash
gs auth login
gs auth status
gs auth logout
gs auth token
```

Access tokens should be short-lived. Refresh tokens should be revocable per
device/session. The CLI should never store long-lived tokens in workspace files.

### 4.3 Account Memberships And Roles

Users can belong to multiple accounts. A request against a slice resolves:

```text
authenticated subject
  -> account memberships
  -> slice visibility
  -> slice roles
  -> submit requirements
```

Core account roles:

- owner: manages account settings, billing, admins, and destructive operations
- admin: manages slices, teams, service accounts, and policy overrides
- member: can see account-visible resources
- guest: limited access to explicitly shared slices

Slice roles:

- owner: manages slice definition, visibility, and roles
- admin: manages slice settings and reviewers
- writer: can create changesets from the slice
- reader: can read the slice

Submit settings can add required approvals and checks, but they do not grant
read access by themselves.

### 4.4 Authorization Rules

Authentication answers who the caller is. Authorization answers what that caller
can do.

Read authorization:

- public slices can be read without authentication, subject to publicability
  policy
- account-visible slices can be read by account members
- private slices require explicit slice reader access
- overlapping slices can expose the same path to different audiences; access is
  evaluated through the slice being read

Write authorization:

- a user must have writer access on the authoring slice
- every changed path must be included by the authoring slice
- the user must have read access to every changed path
- submit must pass slice submit requirements, required checks, and any active
  path locks

Admin authorization:

- account admins can manage account-level settings, teams, service accounts,
  and override policy according to account rules
- slice owners/admins can manage slice definitions, submit settings, and role
  assignments
- weakening submit settings requires the same administrative review flow as
  other protected slice-definition changes

### 4.5 Git Authentication

Git clone/fetch/push should authenticate through the Git gateway, then map the
caller to the same account, slice, and policy model used by the CLI.

Supported MVP options:

- HTTPS Git credentials backed by Gitslice access tokens
- generated Git credentials from `gs auth login`
- service-account tokens for CI checkout

SSH keys can be added later, but they should map to the same subject and session
model. Git authentication must not bypass changesets or submit validation.

### 4.6 Agent And Service Account Auth

Agents and CI should use explicit service or agent identities rather than
borrowing a human user's long-lived token.

Agent/service credentials should be:

- scoped to accounts, slices, and operations
- revocable without deleting the owning user or account
- auditable in changeset, patchset, and submit logs
- optionally bound to an external workload identity provider

For the MVP, agent identities can use service-account tokens with clear audit
metadata. Later, agents can get richer delegation rules such as "act on behalf
of user X for slice Y until time Z."

## 5. Core Product Objects

Account
: A globally unique user or organization namespace. Paths are rooted directly
  under account slugs, for example `/acme/payment`.

Slice
: A repository-like view over one account's source graph. A slice owns
  visibility, roles, included paths, and Git URL identity.

Workspace
: A local working area bound to exactly one slice. It hydrates files for that
  slice on demand.

Changeset
: The unit of review and submission. It contains immutable patchsets, review
  state, authorization requirements, submit requirements, and submit status.

Patchset
: An immutable version of a changeset's proposed file edits.

Submit settings
: Slice-level settings that define required approvals and checks for changes
  authored from that slice. Target-ref sequencers serialize final ref updates.

Git projection
: A Git-compatible repository view generated from a slice. It supports clone,
  fetch, CI checkout, and push-to-changeset workflows without making Git storage
  the source of truth.

## 6. MVP Product Shape

The MVP should be usable end-to-end from the CLI before any web UI is required.

CLI-first means:

- onboarding starts with `gs auth login`
- a user can create or select a workspace from the CLI
- slice discovery and hydration work from the CLI
- local edits become changesets from the CLI
- submit status, authorization failures, check state, and conflicts are visible
  from the CLI
- path lookup is available from the CLI
- Git compatibility exists for clone/fetch/push workflows, but `gs` remains the
  primary product surface

Minimum CLI journey:

```text
gs auth login
gs workspace init acme/payment
gs status
gs cs create
gs cs submit
gs cs status
```

Web and IDE surfaces should be treated as later clients of the same account,
auth, changeset, submit, and storage APIs.

## 7. Primary Workflows

### 7.1 Native CLI Workflow

```text
gs workspace init acme/payment
edit files
gs status
gs cs create
gs cs submit
```

The user works in a sparse workspace bound to one slice. The CLI snapshots local
edits into a changeset patchset for that slice, uploads missing blobs, and
submits through server-side submit and conflict validation. To work on another
slice, the user creates a separate workspace.

### 7.2 Git Compatibility Workflow

```text
git clone https://gitslice.io/git/acme/payment.git
edit files
git commit
git push origin HEAD:refs/changes/new
```

The Git gateway converts the pushed Git diff into a native changeset and
patchset. Direct writes to protected accepted refs are rejected or translated
into changeset workflows.

### 7.3 Submit Settings Workflow

```yaml
submit:
  required_approvals:
    - team: payment-owners
  required_checks:
    - payment-ci
```

Teams express submit requirements as part of slice definitions. Submit-setting
changes are reviewed through changesets or an equivalent administrative flow.
Weakening submit requirements requires the same protected slice-administration
path as changing included paths, roles, or visibility.

### 7.4 Submit Workflow

```text
changeset
  -> resolve changed paths
  -> resolve covering slices
  -> resolve submit requirements
  -> run checks
  -> target-ref landing sequencer
  -> CAS ref update
```

The product should prefer clear blocked states over implicit best-effort submit.
If submit requirements, checks, indexes, or ref freshness are stale, submit
should block or retry rather than land under uncertain requirements.

## 8. Product Scope

MVP scope:

- account-rooted global paths
- CLI-first onboarding and daily workflow
- account, membership, session, and token management for CLI/API/Git access
- slice creation and projection
- sparse native workspaces
- native `gs` changeset flow
- slice-level submit settings
- per-target-ref landing sequencer
- Git clone/fetch from slice URLs
- Git push into changesets
- PostgreSQL metadata storage and Cloudflare R2 object storage
- derived indexes for path coverage and history
- per-path conflict detection and safe batched target-ref updates
- correctness-first storage lifecycle and GC

Later scope:

- richer branch support
- advanced query language
- richer migration tooling from existing Git repositories
- advanced large-file transfer optimizations if native blob storage plus partial
  clone is not enough
- IDE integrations
- hosted review UI
- organization analytics and policy dashboards

## 9. Product Non-Goals

The product should not:

- expose cross-slice changesets
- auto-link multiple changesets into one product-level submission
- provide atomic multi-slice submission
- bind multiple slices into one workspace
- use `/users` or `/orgs` path prefixes
- support per-directory policy files
- include code search in the MVP
- add a separate submit scheduling abstraction in the MVP
- make Git sparse checkout a core workflow
- make Git object ids the native object ids
- make client-side file watchers authoritative for correctness
- require users to understand internal storage objects for normal workflows

## 10. Document Map

- [01_gitslice_architecture_design.md](01_gitslice_architecture_design.md): architecture and system model
- [02_storage.md](02_storage.md): storage stack, Postgres schema, R2 layout, refs, hashing, GC, and replication
- [03_core_api.md](03_core_api.md): gRPC services, proto messages, and gateway behavior
- [04_cli_design.md](04_cli_design.md): native `gs` CLI and workspace behavior
- [05_git_compatibility.md](05_git_compatibility.md): Git gateway, projections, and push behavior
- [06_indexing.md](06_indexing.md): derived indexes, events, freshness, and rebuilds
- [07_conflict_resolution.md](07_conflict_resolution.md): per-path conflict detection and batched submit
- [08_execution_plan.md](08_execution_plan.md): implementation phases and workflow validation
