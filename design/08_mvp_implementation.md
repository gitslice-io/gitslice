# Gitslice MVP Implementation Design

This document defines the concrete MVP implementation shape. Product context is
in [00_product.md](00_product.md), architecture is in
[01_gitslice_architecture_design.md](01_gitslice_architecture_design.md), storage
is in [02_storage.md](02_storage.md), core APIs are in
[03_core_api.md](03_core_api.md), CLI behavior is in
[04_cli_design.md](04_cli_design.md), and rollout sequencing is in
[09_execution_plan.md](09_execution_plan.md).

## 1. Implementation Goals

The MVP implementation should optimize for correctness, local repeatability, and
end-to-end CLI validation.

The concrete implementation choices are:

- Go for the server, CLI, generated gRPC clients, test harnesses, and load tests
- one server binary for the fake account service and all core gRPC services
- one CLI binary, `gs`, that only talks to the server through gRPC
- PostgreSQL as source-of-truth metadata storage
- filesystem-based object storage for the local prototype
- functional and load tests that start the server locally and run the CLI against
  the gRPC API

The MVP should not start as a set of microservices. Service boundaries should be
expressed as Go interfaces and gRPC services inside one process.

## 2. Binaries

The MVP has two user-facing binaries.

```text
cmd/gitslice-server
cmd/gs
```

`gitslice-server` hosts:

```text
FakeAccountService
RepositoryService
BlobService
SliceService
ChangesetService
WorkspaceService
Internal submit/storage helpers
```

`cmd/gitslice-server` should stay thin. It should parse flags/env, call into the
top-level `server` package, and let that package wire the fake account service,
core gRPC services, PostgreSQL, filesystem object store, health checks, metrics,
and shutdown behavior.

The `server` package is wiring-only. It must not contain product business logic,
authorization rules, submit rules, path normalization, object hashing, or
storage semantics. Those belong in `service`, `internal/submit`,
`internal/paths`, `internal/objectid`, `internal/postgres`, and related packages.

`gs` is the native CLI and supports the MVP journey:

```bash
gs auth login
gs workspace init acme/payment
gs status
gs cs create
gs cs submit
gs cs status
```

The Git gateway can be added to the same server binary when Git compatibility
work begins. The core MVP should land the native gRPC API and CLI first.

## 3. Single Server Runtime

The server process owns all core product state transitions.

```text
gitslice-server
  -> gRPC listener
  -> auth interceptor
  -> fake account service
  -> core service implementations
  -> storage layer
  -> PostgreSQL
  -> filesystem object store
```

Required startup inputs:

```text
GITSLICE_GRPC_ADDR
GITSLICE_DATABASE_URL
GITSLICE_OBJECT_STORE_ROOT
GITSLICE_DEV_ACCOUNT_FIXTURE
```

The server should fail fast if the database URL or object-store root is missing.
The filesystem object store is prototype-only. It is used for local development,
functional tests, load tests, and early validation. A durable object-store
adapter is required before production-style deployment, horizontal scaling, or
multi-host writes.

The server should expose:

```text
gRPC core API
gRPC health check
structured logs
basic Prometheus-style metrics endpoint or in-process metrics collector
pprof in local/dev mode
```

## 4. Fake Account Service

The MVP account service is intentionally fake. It exists to unblock local
correctness testing without building production OAuth, SSO, invitations, billing,
or organization administration.

Responsibilities:

- load users, service accounts, accounts, memberships, and sessions from a local
  fixture or seed table
- issue development session tokens for `gs auth login`
- validate bearer tokens in a gRPC interceptor
- attach subject id and account membership context to each request
- enforce simple role checks used by slice and changeset services
- write audit fields such as `actor_subject_id`

Non-goals:

- real OAuth or device-code login
- browser login
- billing
- organization invitation flows
- public sign-up
- long-lived production refresh-token lifecycle

Development login can be explicit:

```bash
gs auth login --server 127.0.0.1:50051 --dev-user alice
```

The CLI stores the returned token in the user config directory, not in workspace
metadata.

Example fixture:

```yaml
subjects:
  - id: user_alice
    display_name: Alice
accounts:
  - id: acct_acme
    slug: acme
memberships:
  - account_id: acct_acme
    subject_id: user_alice
    role: admin
```

The fake account service must sit behind an interface so a real account service
can replace it later without changing the core services.

## 5. Core gRPC Services

The server should implement the public services defined in
[03_core_api.md](03_core_api.md):

```text
RepositoryService
BlobService
SliceService
ChangesetService
WorkspaceService
```

Service responsibilities:

```text
RepositoryService
  ResolvePath
  ListDirectory
  ReadFile
  GetCommit
  GetRef

BlobService
  GetBlobStatus
  UploadBlob

SliceService
  ResolveSlice
  GetSlice
  ListSlices
  UpdateSliceDefinition

ChangesetService
  CreateChangeset
  GetChangeset
  UpdateChangeset
  SubmitChangeset
  AbandonChangeset

WorkspaceService
  GetWorkspaceState
  HydratePaths
  ValidateWorkspaceDiff
  RecordWorkspaceOperation
```

The public services should call shared internal packages rather than duplicate
logic in handlers. For example, both `ChangesetService.UpdateChangeset` and
`WorkspaceService.ValidateWorkspaceDiff` should use the same path normalization,
authoring-slice containment, path-base, and read/write-set code.

`ChangesetService.SubmitChangeset` should use the async MVP path:

```text
path-head CAS -> pending_publish append -> in-process batch publisher -> ref CAS
```

The server binary owns only the publisher wiring: interval, batch size,
lifecycle, logging, and shutdown. Publish correctness belongs in the storage
layer so crash recovery can resume from durable `pending_publish` rows.

## 6. Suggested Go Package Layout

The exact package names can change, but ownership should stay clear.

```text
cmd/
  gitslice-server/
  gs/

server/
  config/
  runtime/
  grpc/

service/
  accountfake/
  repository/
  blob/
  slice/
  changeset/
  workspace/

proto/
  core/v1/
  storage/v1/

internal/
  authctx/
  cli/
  objectid/
  objectstore/
    filesystem/
  paths/
  postgres/
  repository/
  slices/
  changesets/
  submit/
  workspace/
  testharness/
```

Important package rules:

- `paths` owns canonical path parsing and validation.
- `objectid` owns blob, tree, and commit id hashing.
- `server` owns process startup, config loading, gRPC listener setup,
  interceptors, health, metrics, dependency wiring, and graceful shutdown only.
  It must stay free of product business logic.
- `service` owns all public gRPC service implementations, including the fake
  account service used by the MVP.
- `postgres` owns SQL transactions and migrations.
- `objectstore/filesystem` owns MVP byte storage and verification, not metadata
  truth.
- `submit` owns target-ref sequencer behavior and CAS publication.
- `changesets` owns patchset creation, path-base predicates, and submit status.
- `cli` owns local filesystem and `.gs` workspace behavior.

## 7. Storage In The Prototype MVP

PostgreSQL is the metadata source of truth for the MVP. Functional and load
tests should use a real PostgreSQL instance, not an in-memory substitute. The
server should run migrations before tests start, either explicitly through a Go
test helper or through a server startup flag used only in dev/test.

Object storage should be an interface:

```go
type Store interface {
    Put(ctx context.Context, key string, r io.Reader) error
    Get(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error)
    Delete(ctx context.Context, key string) error
}
```

The prototype adapter is filesystem-based. It should use deterministic,
content-addressed paths under `GITSLICE_OBJECT_STORE_ROOT` and should be safe to
delete as a unit for test cleanup. Functional, load, restart, and persistence
tests must use this filesystem adapter so the local prototype exercises real
byte persistence without external infrastructure.

For the MVP, the same adapter stores both raw blob bytes and immutable tree-node
payloads:

```text
blobs/sha256/{hh}/{hh}/{hash}
trees/sha256/{hh}/{hh}/{hash}.json
```

PostgreSQL commit rows store `root_tree_id`; tree node contents are not
expanded into database rows. Publishing a changeset path-copies only the tree
nodes on the changed paths, writes those new immutable nodes to object storage,
then records the new commit and target-ref move in PostgreSQL.

The filesystem adapter is not a production object-store design. It assumes a
single server process or equivalent single-writer discipline over the object
root. A durable object-store adapter should replace it before production-style
deployment.

The server must still verify raw blob bytes against their content hash before
marking a blob available, even when the backing object store is local.

## 8. CLI Implementation

The CLI is a Go binary using generated gRPC clients.

Local state:

```text
user config:
  server address
  session token
  default output format

workspace .gs/:
  config.json
  slice.json
  state.json
  cache/
  overlay/
  op_log/
  draft_patchsets/
```

The CLI should not use local state as an authority for server-visible decisions.
Before creating or updating a patchset, it should call
`WorkspaceService.ValidateWorkspaceDiff` or equivalent core validation.

Command behavior:

```text
gs auth login
  -> call fake account login endpoint or auth RPC
  -> store token in user config

gs workspace init acme/payment
  -> ResolveSlice
  -> check read access
  -> write .gs/slice.json
  -> hydrate default metadata

gs status
  -> reconcile filesystem with local cache
  -> normalize changed paths
  -> validate against bound slice
  -> show changed paths and draft changeset state

gs cs create
  -> upload missing blobs
  -> CreateChangeset
  -> UpdateChangeset with patchset 1
  -> record current changeset id locally

gs cs submit
  -> SubmitChangeset
  -> show submitted commit id or exact blocker
```

The implementation should start with JSON workspace files instead of YAML. This
keeps the initial Go CLI small and makes functional tests easier to assert.
Those files are never the source of truth for authorization, slice membership,
or submit decisions.

The CLI should support stable JSON output for functional tests:

```bash
gs status --format json
gs cs status --format json
```

## 9. Functional Test Harness

Functional tests must exercise the real server process and the real CLI binary.
They should not call service handlers directly except in lower-level unit tests.

Test harness responsibilities:

```text
1. Build or locate gitslice-server and gs binaries.
2. Start PostgreSQL for the test run.
3. Create a temporary object-store root.
4. Start gitslice-server on a random localhost port.
5. Wait for gRPC health.
6. Create a temporary HOME and workspace directory.
7. Run gs commands against the local server.
8. Assert CLI output and server-side persisted state.
9. Stop the server and clean up temp resources.
```

The harness should be written in Go. Prefer helper APIs such as:

```go
server := testharness.StartServer(t, testharness.ServerOptions{})
cli := testharness.NewCLI(t, server.GRPCAddr)
cli.Run(t, "auth", "login", "--dev-user", "alice")
```

Baseline functional suites:

- auth login and token persistence
- workspace init for one slice
- hydrate file and read file contents
- status on clean workspace
- status after file create/modify/delete/rename
- changeset create
- changeset update creates a new patchset
- submit happy path moves target ref
- submit conflict when path base is stale
- rejection for path outside bound slice
- blob upload deduplication
- server restart preserves committed state
- abandoned changeset cannot submit
- overlapping slice coverage is recorded without creating multi-slice changesets

Each functional test should prefer CLI assertions first and direct database or
gRPC assertions only when the CLI cannot expose the invariant cleanly.

## 10. Load Test Harness

Load tests should also be written in Go and start a local server. They should
use the real gRPC API and include end-to-end CLI workers for the critical user
journeys.

Recommended modes:

```bash
go test ./tests/load -run TestLoad -tags load
go test ./tests/load -run TestLoadSubmitContention -tags load
```

Load scenarios:

- many concurrent `gs status` calls over populated workspaces
- many concurrent blob uploads with duplicate content
- many concurrent changeset creates for disjoint files
- concurrent submits to one target ref with disjoint write sets
- concurrent submits with intentional same-path conflicts
- repeated hydrate/read operations for a large slice projection
- server restart between batches to verify persisted metadata and object storage

Metrics to collect:

- operation throughput
- p50, p95, and p99 latency
- submit CAS retry rate
- conflict rate by reason
- PostgreSQL connection pool usage
- peak goroutine count
- peak RSS where available
- object-store bytes written and read

Load tests should have explicit thresholds for MVP regressions. The thresholds
can be modest at first, but they must be checked automatically so performance
does not degrade unnoticed.

## 11. Test Data

The test harness should seed deterministic data:

```text
accounts:
  acme

subjects:
  alice
  bob
  ci-bot

slices:
  acme/payment
  acme/backend

target ref:
  refs/global/main
```

Seed commits should include enough files to exercise path lookup, hydration,
diffing, and overlapping slice coverage. Large load fixtures should be generated
programmatically in Go rather than checked in as static files.

## 12. MVP Delivery Gates

The MVP is not done when individual handlers pass unit tests. It is done when
the CLI journey works against a local server:

```bash
gs auth login
gs workspace init acme/payment
gs status
gs cs create
gs cs submit
gs cs status
```

Required gates:

- unit tests for path, object id, storage, and submit primitives
- gRPC service tests for each core service
- functional CLI tests against a real local server
- restart/persistence tests using PostgreSQL plus filesystem object store
- load tests for read, write, and submit contention paths
- race-enabled Go tests for packages with concurrent submit or cache logic

The default developer test target should run unit and functional tests. Load
tests can be opt-in because they are slower, but they must remain part of the
MVP acceptance suite.
