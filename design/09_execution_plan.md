# Gitslice Execution Plan

This document tracks implementation phases and workflow validation for Gitslice.
Product context is in [00_product.md](00_product.md), architecture is in
[01_gitslice_architecture_design.md](01_gitslice_architecture_design.md), the
storage design is in [02_storage.md](02_storage.md), and the gRPC API boundary is
in [03_core_api.md](03_core_api.md). CLI behavior is in
[04_cli_design.md](04_cli_design.md). Git compatibility is in
[05_git_compatibility.md](05_git_compatibility.md). Indexing details are in
[06_indexing.md](06_indexing.md). Conflict resolution and batched submit are in
[07_conflict_resolution.md](07_conflict_resolution.md). Concrete Go
implementation details and test harness requirements are in
[08_mvp_implementation.md](08_mvp_implementation.md).

## 1. MVP Phases

### Phase 0: Go Runtime, Server Shell, And Test Harness

- Go module layout for `cmd/gitslice-server`, `cmd/gs`, top-level `server`,
  top-level `service`, `internal/...`, and generated proto packages
- gRPC server startup with health checks, auth interceptor, structured logging,
  and local/dev config
- fake account service with fixture-backed users, accounts, memberships, and
  development tokens
- PostgreSQL migration runner for dev/test
- prototype filesystem object-store adapter
- Go test harness that can start PostgreSQL, start `gitslice-server`, run `gs`,
  and clean up temp workspaces

Exit criteria:

- `gitslice-server` starts locally and exposes gRPC health.
- `cmd/gitslice-server` delegates process wiring to the top-level `server`
  package; product behavior lives outside `server`.
- `gs auth login --dev-user alice` can obtain and store a token from the fake
  account service.
- Functional tests can start a local server on a random port and run the CLI
  against it.
- The test harness uses isolated HOME, workspace, database, and object-store
  roots per test run.
- The default test target runs unit tests plus at least one local server/CLI
  smoke test.

### Phase 1: Native Object Model

- PostgreSQL schema and migrations for source-of-truth metadata
- prototype filesystem object-store layout rooted at `GITSLICE_OBJECT_STORE_ROOT`
- Content-addressed blob store
- Immutable tree metadata
- Canonical path rules
- Global commit graph
- Atomic refs
- Staged blob upload protocol
- Reachability roots for GC

Exit criteria:

- PostgreSQL tables, constraints, and critical indexes exist for refs, commits,
  trees, blobs, and outbox events.
- filesystem blob keys are content-addressed and uploads are verified by hash
  before metadata can reference them.
- Blobs can be uploaded, verified, and referenced by metadata.
- Trees and commits are immutable and content-addressed where required.
- Refs can be updated only through CAS.
- Canonical path validation is shared by storage and API layers.
- Storage can enumerate accepted refs, patchsets, staged blob leases, and cache
  leases as GC roots.
- Unit tests cover blob, tree, and commit id determinism.
- Functional tests verify upload, restart, path lookup, and ref CAS through the
  local server.
- The plan records that filesystem object storage is prototype-only and must be
  replaced by a durable object-store adapter before production-style deployment.

### Phase 2: Slice Definitions And Projection

- User and organization accounts with globally unique slugs
- Slice identity
- Slice definitions as versioned metadata
- Absolute included paths
- Slice-level visibility and roles
- Overlapping slice coverage
- Deterministic projection by latest definition

Exit criteria:

- A slice can project a deterministic tree from the global commit graph.
- Slice definition changes create new auditable definition versions.
- Overlapping slices resolve covering slices for changed paths.
- Projection cache keys include slice id, slice definition hash, and global commit.
- The fake account fixture can seed `acme/payment` and `acme/backend` slices.
- Functional tests verify that a user can resolve and hydrate a slice through the
  CLI without cloning the full global tree.

### Phase 3: Workspace And Native CLI

- Sparse workspace metadata
- On-demand hydration
- One slice binding per workspace
- Local status/diff
- Local operation log and undo
- Draft patchset snapshots
- Native `gs` workflows
- CLI config for server address and auth token
- stable JSON output for test assertions

Exit criteria:

- A user can initialize a workspace bound to exactly one slice.
- File hydration preserves canonical account-rooted paths.
- Local edits can be converted into canonical global path diffs.
- Changeset creation rejects local diffs that are not contained by one
  authoring slice.
- Workspace state does not require cloning the full global source graph.
- Local operations can be inspected through `gs op log`.
- `gs cs create` and `gs cs update` produce server patchsets from local snapshots.
- Functional tests cover `gs auth login`, `gs workspace init`, `gs status`, and
  clean/dirty workspace transitions against a local server.
- Workspace tests verify that edits outside the bound slice are rejected.

### Phase 4: Changesets And Direct Submit

- Changeset creation
- Patchsets
- Review state
- Conflict detection
- Per-path base predicates
- Patchset read/write sets
- Covering-slice refresh
- Submit requirement resolution
- Required approvals and checks
- Per-target-ref landing sequencer
- Batched submit for compatible read/write sets
- Atomic ref update
- server-side submit status explanations for CLI display

Exit criteria:

- A changeset is scoped to one authoring slice.
- Each update creates an immutable patchset.
- Patchsets record path base predicates, read sets, and write sets.
- Submit recomputes coverage, submit requirements, and approvals.
- Submit finalization is serialized per target ref after validation.
- Multiple compatible changesets can publish as one commit chain and one ref CAS.
- Submit publishes through CAS or fails without moving the target ref.
- Functional tests cover create, update, abandon, happy-path submit,
  stale-path-base rejection, and outside-slice rejection through the CLI.
- Load tests cover concurrent changeset creation and concurrent submit against
  one local target ref.

### Phase 5: Git Read Compatibility

- Git smart HTTP endpoint
- Clone from slice URL
- Synthetic Git history
- Fetch
- Partial clone support
- Packfile/projection cache
- Native large-blob projection rules

Exit criteria:

- A slice can be cloned from its Git URL.
- Projected Git commits are stable for the same projection inputs.
- Fetch and partial clone operate through projected refs and trees.
- Cached projection artifacts are keyed by all Git-visible projection inputs.
- Git compatibility can run in the same `gitslice-server` binary without
  bypassing native storage or authorization.

### Phase 6: Git Push Into Changesets

- Convert Git diff to global path patchset
- Push to changeset refs
- Changeset creation/update from Git
- Protected branch push policy

Exit criteria:

- `refs/changes/new` creates a changeset.
- Pushing to an existing changeset ref creates a new patchset.
- Protected ref pushes are rejected or translated into changesets.
- Git-originated changes go through the same validation as native changes.
- Functional tests verify that Git-originated changes produce native changesets
  with the same path containment and submit validation as `gs`.

### Phase 7: Indexing, CI, And Scale

- Changed path index
- Slice coverage index
- Submit requirement provenance index
- Patchset read/write set index
- Build/test integration
- Regional reads
- Projection cache
- Distributed GC
- Advanced replication
- local load test suite for read, write, and submit contention scenarios

Exit criteria:

- Indexes can be rebuilt from committed source-of-truth objects.
- CI requirements can be selected from slice submit settings and build/test indexes.
- Batched submit candidate selection can use indexes, but final submit
  revalidates path predicates from source-of-truth state.
- GC can delete unreachable staged blobs and projection artifacts only after
  reachability recheck and grace periods.
- Regional read replicas can serve reads while preserving linearizable ref writes.
- Load tests report p50, p95, p99 latency, throughput, CAS retry rate, and
  conflict rates.
- Load tests are opt-in for normal development but required before MVP release.

## 2. MVP Test Gates

The MVP implementation must keep tests end-to-end enough to catch broken service
boundaries. Unit tests are necessary but not sufficient.

Default developer gate:

```bash
go test ./...
```

The default gate should include:

- unit tests for canonical paths, object ids, storage transactions, and submit
  predicates
- gRPC service tests using in-process clients where direct service behavior needs
  tight assertions
- at least one functional smoke test that starts `gitslice-server` locally and
  runs `gs` against the gRPC API

Functional gate:

```bash
go test ./tests/functional -run TestCLI
```

Functional tests must:

- start PostgreSQL and local object storage for the test run
- start the real `gitslice-server` binary on a random localhost port
- run the real `gs` binary with an isolated HOME and workspace
- assert the minimal CLI journey from auth through submit status
- verify persistence by restarting the server in at least one suite

Load gate:

```bash
go test ./tests/load -run TestLoad -tags load
```

Load tests must:

- start the same local server binary used by functional tests
- run CLI workers for end-to-end user journeys
- use direct gRPC workers only for targeted service-level measurements
- record latency, throughput, CAS retries, conflict reasons, and resource usage
- fail when measured thresholds regress beyond the current MVP budget

Release gate:

```text
unit + functional + load + race-enabled concurrency tests
```

The release gate must pass before the MVP is considered complete.

## 3. Example Native Workflow

### 3.1 Create Workspace

```bash
gs workspace init nicholas/identity
```

### 3.2 Edit Code

```bash
vim nicholas/services/identity/auth.go
```

### 3.3 Create Changeset

```bash
gs cs create
```

### 3.4 Update Changeset

```bash
gs cs update
```

### 3.5 Submit

```bash
gs cs submit
```

Server behavior:

```text
1. Resolve changed absolute paths.
2. Verify all changed paths are contained by the authoring slice.
3. Resolve covering slices for overlap metadata.
4. Record path base predicates, read set, and write set.
5. Resolve submit requirements from the authoring slice definition and path locks.
6. Refresh overlap and submit requirements.
7. Check authoring-slice roles and approvals.
8. Run required checks.
9. Hand off to the target-ref landing sequencer.
10. Revalidate path predicates, submit requirements, checks, and conflicts.
11. Rebase or apply onto latest target ref.
12. Create commit or batched commit chain.
13. Update ref with CAS.
14. Emit indexing events.
```

## 4. Example Git Workflow

```bash
git clone https://gitslice.io/git/nicholas/identity.git
cd identity
git checkout -b my-change
# edit files
git commit -am "Update auth flow"
git push origin HEAD:refs/changes/new
```

Server behavior:

```text
1. Resolve slice from URL.
2. Authenticate and authorize user.
3. Convert Git diff to global absolute paths.
4. Verify every changed path is inside the URL's authoring slice.
5. Resolve covering slices for overlap metadata.
6. Resolve submit requirements.
7. Create changeset.
8. Create patchset.
9. Run validation.
```

## 5. Initial Non-Goals

The initial implementation should not include:

- special `/shared` or `/system` namespaces
- custom mount aliases inside slices
- direct user-facing commit creation
- single-owner path model
- object-store participation in metadata transactions
- path-level ACLs as the primary access model
- Git-native storage internals
- cross-slice changesets
- multi-slice workspaces
- distributed atomic commits across slices or target refs

These can be revisited only if a concrete product requirement justifies the
additional complexity.
