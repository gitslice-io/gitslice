# Gitslice Product Overview

Gitslice is a source graph platform for teams and AI coding agents that need one
coherent codebase without forcing every workflow through one physical Git
repository.

The product feels familiar to Git users, while treating slices, virtual
workspaces, changesets, folder-level policy/CI/automation configs, and
server-side submit validation as first-class concepts.

Gitslice ships as a cohesive multi-surface platform backed by one core server:

- **Native CLI (`gs`)**: authentication, full and lazy FUSE workspaces, local
  edit capture, stacked changesets, submit, and an embedded MCP server
  (`gs mcp`) for coding agents.
- **Web App (`web/`)**: TanStack Start SSR interface on Cloudflare Workers for
  browsing slices, reviewing changesets, inspecting blame/history, managing
  approvals/checks/automations/secrets, and chatting with BYO agent daemons.
- **Git Compatibility Gateway (`/git/`)**: Git Smart HTTP clone, fetch,
  push-to-changeset, and outbound Git mirrors.
- **CI & Automations**: BYO agent/CI daemon pools with a Cloudflare doorbell
  and hosted container runners, plus folder-scoped `.gitslice/` configs for
  owners, checks, merge strategies, and Worker/Container automations.

## 1. Product Thesis

Modern codebases are often split across many repositories for reasons that are
operational rather than conceptual:

- Git repositories get too large.
- Teams need different visibility and write policies.
- CI, ownership, and submit rules differ by code area or team.
- Developers and agents need only a small working set.
- Cross-repository refactors are hard to review and land safely.

Gitslice provides one native global source graph with repository-like slices at
the boundary. Users and agents work in small, understandable projections, while
the system keeps paths, history, policy, indexing, and submit validation
consistent.

## 2. Target Users

Primary users:

- engineers working in large multi-team codebases
- platform teams that own build, CI, policy, and source infrastructure
- organizations that want monorepo-style consistency without one giant Git repo
- AI coding agents that need scoped, policy-aware workspaces and MCP tools

Secondary users:

- open-source maintainers who want repository-like public slices and Git mirrors
- CI systems and code review tools that need Git-compatible checkouts
- migration teams moving from existing Git repositories toward one source graph

## 3. Product Principles

Gitslice product behavior follows these principles:

- Native source graph first; Git compatibility at the boundary.
- Slices are product views, not storage shards.
- Changesets are the review and submission unit.
- A changeset has exactly one authoring slice; cross-slice changesets are not
  supported.
- Submit requirements are enforced server-side from slice definitions and
  in-tree `.gitslice/` folder configs.
- Workspaces hydrate only what the user or agent needs (eagerly or lazily via
  FUSE).
- Caches and watchers improve performance, but server validation decides
  correctness.
- Git users can be productive without learning the native internals.

## 4. Account And Auth Model

Accounts are both namespace owners and collaboration containers. Every
source-path root belongs to exactly one account:

```text
/{account}/...
```

An account represents:

- a personal user account, such as `/nicholas`
- an organization account, such as `/acme`

Account kind is metadata, not encoded in the path. Gitslice does not use
`/users` or `/orgs` prefixes. Because paths are rooted directly under account
slugs, the auth service maintains a globally unique slug registry.

### 4.1 Identity Types

Gitslice supports:

- human users (authenticated via Clerk / OIDC in web and browser-assisted CLI
  login)
- organization groups (`@account/group`) for slice membership and approver gates
- agent identities (self-registered via `gs auth register-agent` and claimable
  by a human, or created as delegated agents)
- scoped API keys (`AuthService.CreateApiKey`) for CI, scripts, and automation
- per-run bearer tokens for isolated automation executions (`AutomationRunService`)

Authorization uses stable `subject_id` and `principal` metadata rather than
display names or email addresses.

### 4.2 CLI And Agent Authentication

Human CLI login uses a browser-backed polling flow:

```bash
gs auth login           # browser-backed CLI login
gs auth status          # show active subject, accounts, and server
gs auth logout          # clear local credentials
gs auth register-agent  # register an agent identity (claimable by a human)
```

Long-lived credentials are stored outside workspace directories (in the user's
CLI config/credential store). Agents and CI can also authenticate with scoped
API keys or delegated agent credentials.

### 4.3 Account Memberships And Roles

Users can belong to multiple accounts. A request against a slice resolves:

```text
authenticated principal
  -> account memberships & account groups
  -> slice visibility & slice roles
  -> submit requirements (slice definition + .gitslice/ folder configs)
```

Account roles:

- `owner`: manages account settings, organizations, members, groups, and
  destructive operations
- `admin`: manages slices, members, invitations, CI daemon pools, and secrets
- `member`: can access account-visible resources

Slice roles (assigned directly or via `@account/group`):

- `owner`: manages slice lifecycle, visibility, members, and all settings
- `admin`: manages slice definition, submit settings, members, CI daemons, and
  Git mirrors
- `writer`: can create, modify, and submit changesets from the slice
- `reader`: can read the slice and view changesets

### 4.4 Authorization Rules

Read authorization:

- `public` slices can be read without authentication
- `account` slices can be read by members of the owning account
- `private` slices require explicit slice reader (or higher) access
- overlapping slices can expose the same path to different audiences; access is
  evaluated through the slice being read

Write authorization:

- a caller must have `writer` (or higher) access on the authoring slice
- every changed path must be included by the authoring slice
- submit must pass slice submit settings, folder-level `.gitslice/owners.yaml`
  and `.gitslice/checks.yaml` requirements, and path-head freshness checks

### 4.5 Git Authentication

Git clone/fetch/push authenticates through the `/git/` Smart HTTP gateway using
the same bearer/basic token resolution as the native API. Git authentication
never bypasses changesets or submit validation.

## 5. Core Product Objects

Account
: A globally unique user or organization namespace. Paths are rooted directly
  under account slugs, for example `/acme/payment`.

Slice
: A repository-like view over one account's source graph. A slice owns
  visibility, roles, included paths, submit settings, CI daemon pool bindings,
  Git mirror configuration, and Git URL identity.

Workspace
: A local working area bound to exactly one slice. Workspaces support eager
  sparse hydration (`gs init`) or lazy on-demand FUSE hydration
  (`gs init --lazy`).

Changeset
: The unit of review and submission. It contains immutable patchsets, review
  state, collaborator/handoff metadata, submit requirements, and submit status.
  User-facing surfaces identify changesets by a shareable handle of the form
  `account/slice@number` (for example `acme/payment@42`). Multiple dependent
  changesets can be organized into a **Changeset Stack**.

Patchset
: An immutable version of a changeset's proposed file edits. Referenced as
  `<changeset-handle>.<patchset-number>`, for example `acme/payment@42.2`.

Submit settings & `.gitslice/` configs
: Slice-level submit settings (`SliceDefinition` and `merge_on_submit`) combined
  with hierarchical in-tree `.gitslice/` folder configs (`owners.yaml`,
  `checks.yaml`, `automations.yaml`, `merge.yaml`) govern approvals, CI checks,
  auto-merge strategies, and event-driven automations.

Git projection & mirrors
: A Git-compatible repository view generated from a slice for clone, fetch, and
  push-to-changeset workflows, plus optional automated outbound mirroring to
  external Git remotes.

## 6. Primary Workflows

### 6.1 Native CLI Workflow

```bash
gs auth login
gs init acme/payment            # or: gs init --lazy acme/payment
# edit files
gs status
gs diff
gs create -m "Add payment retry"
# iterate after review
gs modify
gs sync
gs submit
```

The CLI snapshots local edits into a changeset patchset, uploads missing blobs,
and submits through server-side validation and asynchronous batched commit
publication.

### 6.2 Agent & MCP Workflow

Coding agents can work through the same native workspace and changeset flow:

- `gs auth register-agent` registers a dedicated agent identity.
- `gs mcp` exposes workspace, diff, changeset, stack, check, and path-intent
  operations over the Model Context Protocol.
- BYO agent daemons (`AgentService.Connect`) let users converse with local or
  hosted agents directly from the web UI on slice/changeset context.
- Multi-agent collaboration uses explicit collaborators, handoffs, and advisory
  path intents (`CreatePathIntent` / `ListPathIntents`).

### 6.3 Git Compatibility Workflow

```bash
git clone https://api.gitslice.io/git/acme/payment.git
# edit files
git commit -m "Add payment retry"
git push origin HEAD:refs/changes/new
```

The Git gateway translates the pushed diff into a native changeset and patchset.
Direct pushes to protected accepted refs are rejected or routed through
changeset creation.

### 6.4 Submit Settings And `.gitslice/` Folder Configs

Slice-level submit settings live on the versioned `SliceDefinition` (and slice
metadata for `merge_on_submit`):

```yaml
# SliceDefinition fields
required_approvals: 1
required_human_approvals: 1
approvers: "@acme/payment-owners"
required_checks:
  - unit-tests
# Slice flag
merge_on_submit: true
```

In addition to slice-level settings, teams can place declarative configuration
files inside `.gitslice/` directories anywhere in the source tree. The server
walks ancestor directories from each changed path up to the account root
(`internal/gitslicecfg`):

- **`.gitslice/owners.yaml`**: path-scoped code owners and approval rules for
  files under that subtree.
- **`.gitslice/checks.yaml`**: declarative CI check definitions (commands,
  required flags, secret bindings, triggers) dispatched to slice/account CI
  daemon pools or Cloudflare-hosted runners.
- **`.gitslice/automations.yaml`**: event-driven and manual automations
  (JavaScript Dynamic Workers, bash containers, HTTP targets, and reusable
  pipelines).
- **`.gitslice/merge.yaml`**: per-path merge strategies used during server-side
  merge-on-submit and restack operations.

### 6.5 Submit Workflow

```text
changeset
  -> resolve changed paths & authoring slice containment
  -> resolve slice submit settings + .gitslice/owners.yaml + .gitslice/checks.yaml
  -> verify approvals & required checks
  -> optional server-side merge_on_submit if path bases drifted
  -> durable path-head CAS + enqueue pending_publish row
  -> async target-ref publisher (3-stage batched tree build & CAS ref update)
  -> outbox -> derived indexes, blame warmer, automations, Git mirrors
```

Submit prefers explicit blocked states (`stale_path_base`,
`requirements_changed`, `approvals_missing`, `checks_missing`,
`blocked_on_parent`) over implicit best-effort landing.

## 7. Shipped Production Stack

Gitslice runs as a production cloud-native system:

- **Core server**: Single Go binary (`cmd/gitslice-server`) serving gRPC,
  ConnectRPC HTTP, `/git/` Smart HTTP, `/avatars/`, `/og/`, and
  `/automation-archive` on Cloud Run with scale-to-zero idle gating.
- **Storage**: PostgreSQL (Neon) for transactional metadata and derived indexes,
  plus Cloudflare R2 (`GITSLICE_REQUIRE_R2=1`) for immutable blobs and tree
  nodes, backed by a 256 MiB in-process object cache and an LRU decoded
  tree-node cache.
- **Web app**: TanStack Start SSR application deployed on Cloudflare Workers
  (`web/`).
- **CI & Automations**: BYO daemon pools, Cloudflare `doorbell/` Worker with
  hibernating WebSockets and container runners, and `trigger-dispatch/`
  Cloudflare Dynamic Workers + `BashRunner` Durable Object containers (`gs-exec`).
- **Workspaces & Agents**: Native `gs` CLI, lazy FUSE workspaces (`gs init --lazy`
  + `gs mountd`), `gs mcp`, BYO agent daemons, scoped secrets, and outbound Git
  mirrors.

## 8. Product Non-Goals

Gitslice intentionally does not:

- expose cross-slice changesets or atomic multi-slice submission
- bind multiple slices into one workspace
- use `/users` or `/orgs` path prefixes
- remap canonical global paths to custom mount aliases inside slices
- make Git object ids the native storage ids
- make client-side file watchers authoritative for correctness
- require users to understand internal storage objects for normal workflows

## 9. Document Map

Living design documents in `design/`:

- [00_product.md](00_product.md): product overview, users, workflows, and scope
- [01_gitslice_architecture_design.md](01_gitslice_architecture_design.md): system architecture, binaries, edge services, and invariants
- [02_storage.md](02_storage.md): content-addressed object model, PostgreSQL schema, R2 + caches, and 3-stage publish
- [03_core_api.md](03_core_api.md): gRPC and ConnectRPC API specification across all 13 services
- [04_cli_design.md](04_cli_design.md): native `gs` CLI, workspace layout, and `gs mcp`
- [05_git_compatibility.md](05_git_compatibility.md): Git Smart HTTP gateway, projections, and push-to-changeset
- [06_indexing.md](06_indexing.md): derived indexes, transactional outbox, freshness, and rebuilds
- [07_conflict_resolution.md](07_conflict_resolution.md): per-path conflict detection, `merge_on_submit`, `.gitslice/merge.yaml`, and batched submit
- [11_web_interface_design.md](11_web_interface_design.md): TanStack Start web app architecture and UI workflows
- [12_account_auth.md](12_account_auth.md): accounts, Clerk auth, CLI login, agent registration/claim, groups, and API keys
- [13_file_identity_and_move_history.md](13_file_identity_and_move_history.md): stable file/directory identity and move-following history
- [15_stacked_changesets.md](15_stacked_changesets.md): changeset stacks, restacking, and ordered stack submission
- [16_bring_your_own_agent.md](16_bring_your_own_agent.md): BYO agent daemons, conversations, and live streaming
- [17_continuous_integration.md](17_continuous_integration.md): `.gitslice/checks.yaml`, CI daemon pools, Cloudflare doorbell, and container runners
- [18_analytics.md](18_analytics.md): product analytics and telemetry
- [21_self_hosting.md](21_self_hosting.md): self-hosting, deployment topology, and configuration
- [22_agents_on_artifacts.md](22_agents_on_artifacts.md): Cloudflare Artifacts agent prototype (`agents/`)
- [25_feedback.md](25_feedback.md): in-product feedback collection and triage
- [27_blame.md](27_blame.md): file blame snapshots and background blame warming
- [28_benchmarks.md](28_benchmarks.md): performance benchmarks and latency modeling
- [29_slice_lifecycle.md](29_slice_lifecycle.md): slice rename, aliases, archive, and retirement
- [30_lazy_workspaces.md](30_lazy_workspaces.md): lazy FUSE workspaces (`gs init --lazy`, `gs mountd`)
- [33_secrets.md](33_secrets.md): encrypted secrets, slice/path scoping, and patchset secret allowances
- [34_automations.md](34_automations.md): `.gitslice/automations.yaml`, Dynamic Workers, `BashRunner` containers, and pipelines
- [35_git_mirrors.md](35_git_mirrors.md): outbound Git mirror synchronization
- [36_multi_agent_collaboration.md](36_multi_agent_collaboration.md): collaborators, handoffs, and advisory path intents

Historical and operational references:

- [`design/archive/`](archive/): superseded design drafts and historical phase plans
- [`docs/execution_log.md`](../docs/execution_log.md): chronological implementation and operational log
