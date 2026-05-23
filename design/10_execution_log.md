# Gitslice Execution Log

This log captures implementation notes, decisions, and important learnings while
turning the design docs into the first Go prototype.

## 2026-05-23: Split Service Implementation Files

Request:

- break up `service/service.go` into multiple files

Implemented:

- kept `service/service.go` focused on shared construction and the object-store
  interface
- moved service methods into files that match API boundaries:
  - `auth.go`
  - `blob.go`
  - `changeset.go`
  - `repository.go`
  - `slice.go`
  - `workspace.go`
- moved shared gRPC error mapping to `errors.go`
- kept repository tree-entry helpers with repository read behavior, and kept
  changeset validation helpers with changeset submit/update behavior

Important decisions and learnings:

- This is a code-organization-only change; service behavior and public gRPC
  registrations remain unchanged.
- The split mirrors the proto file boundaries introduced in the same API layer.

Verification:

```bash
gofmt -w service/*.go
go test -mod=readonly ./service ./server
go test -mod=readonly ./...
go build -mod=readonly ./cmd/...
```

## 2026-05-23: Split Core Proto Files

Request:

- break down `proto/core/v1/core.proto` into multiple files

Implemented:

- replaced the monolithic `core.proto` with service-scoped proto files:
  - `auth.proto`
  - `blob.proto`
  - `changeset.proto`
  - `repository.proto`
  - `slice.proto`
  - `workspace.proto`
- added `common.proto` for cross-service primitives (`Empty`, `SliceRef`,
  `EntryKind`, and `TreeEntry`)
- regenerated Go protobuf and gRPC stubs from all `proto/core/v1/*.proto`
  inputs
- updated proto regeneration instructions to use the full proto file set

Important decisions and learnings:

- Kept the protobuf package and Go package unchanged so existing Go call sites
  continue to use the same `corev1.*` symbols.
- Kept submit-validation types in `changeset.proto` and imported them from
  `workspace.proto`, matching their shared use by changeset submit and
  workspace diff validation.
- Removed the stale generated `core.pb.go` and `core_grpc.pb.go`; generated
  output now tracks the proto file boundaries.

Verification:

```bash
protoc --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative --go-grpc_opt=require_unimplemented_servers=false proto/core/v1/*.proto
go test ./...
go build ./cmd/...
```

## 2026-05-22: Start Go Prototype

Request:

- start implementing the MVP
- keep implementation in Go
- use one server binary and one CLI binary
- use a fake account service and all core gRPC services in the same server
- use real PostgreSQL
- use filesystem object storage only for the prototype
- keep `server/` wiring-only and put service behavior under `service/`

Initial repo state:

- repository only contained design docs and no Go module
- current branch: `codex/single-slice-workspaces`
- open PR: #6
- untracked `.antigravitycli/` is unrelated and should remain untouched

Implementation decision:

- create a new Go module in this repo
- add `cmd/gitslice-server` for the server binary
- add `cmd/gs` for the CLI binary
- add top-level `server/` for process wiring only
- add top-level `service/` for fake account and core gRPC implementations
- add `proto/core/v1` with hand-written gRPC bindings for the prototype so the
  repo can compile without adding a proto generation step yet
- use a JSON gRPC codec for the prototype service boundary; this keeps the first
  pass focused on service behavior and CLI/server integration
- add PostgreSQL-backed metadata storage and migrations
- add filesystem object-store package for prototype blob/object bytes

Implemented in the first pass:

- created the Go module and dependency set for gRPC and PostgreSQL
- added hand-written `proto/core/v1` service bindings and JSON-encoded message
  structs for the prototype
- added `cmd/gitslice-server` and `cmd/gs`
- added `server/` with config loading, gRPC listener setup, auth interceptor,
  health service registration, migration startup, and dependency wiring only
- added top-level `service/` implementing fake account login plus repository,
  blob, slice, workspace, and changeset gRPC services
- added `internal/postgres` with migrations, development fixture seeding, fake
  sessions, accounts, slices, refs, commits, commit file snapshots, blobs,
  changesets, and patchsets
- added `internal/objectstore/filesystem` as the prototype content-addressed byte
  store
- added `internal/objectid`, `internal/paths`, and `internal/authctx` helpers
- added the minimal CLI journey:
  - `gs auth login`
  - `gs workspace init acme/payment`
  - `gs status`
  - `gs cs create`
  - `gs cs submit`
  - `gs cs status`
- added a functional smoke test that starts the real server and runs the CLI
  against it when `GITSLICE_TEST_DATABASE_URL` points at a disposable PostgreSQL
  database

Important implementation decisions and learnings:

- The filesystem object store is only a prototype adapter. PostgreSQL is already
  the source of truth for object metadata and reachability.
- The first schema includes `commit_files` so submit can create a real file
  snapshot instead of moving a ref without corresponding file state.
- Workspace metadata starts as JSON files under `.gs/` (`slice.json` and
  `state.json`) instead of YAML. This avoids adding another parser dependency
  before the CLI shape stabilizes and gives tests deterministic fixtures.
- The hand-written gRPC package is a bootstrapping shortcut. A generated proto
  step should replace it once the API stabilizes.
- The first real smoke run found that the hand-written gRPC unary helper was not
  populating `grpc.UnaryServerInfo.FullMethod`, which caused the auth
  interceptor to treat `FakeAccountService.Login` as protected. The binding now
  passes canonical full method names to the interceptor.
- `go test ./...` passes without a local database by skipping the real-Postgres
  functional smoke test. Running that smoke requires `GITSLICE_TEST_DATABASE_URL`.
- Verified the functional smoke against local PostgreSQL with:

```bash
GITSLICE_TEST_DATABASE_URL=postgres://nic@localhost/gitslice_dev?sslmode=disable go test -count=1 ./tests/functional -run TestMinimalCLIJourney -v
```

## 2026-05-22: Correctness And Test Hardening

Request:

- continue until the implementation plan is finished
- finish the functional tests and load tests

Implemented:

- changed CLI status from "all files are edits" to a workspace base snapshot:
  - `.gs/base_snapshot.json` records the last accepted local file snapshot
  - `gs status` compares the working tree against that base
  - `gs cs submit` refreshes the base snapshot after a successful submit
  - file deletes now produce delete edits
- added `gs cs update` so a draft changeset can receive a new patchset before
  submit
- changed submit validation from whole-ref freshness to per-path entry
  fingerprints:
  - path bases record whether the path existed at patchset creation
  - file bases record mode, blob id, content hash, and an entry fingerprint
  - submit allows stale target refs when every changed path still matches its
    base predicate
  - disjoint stale changesets can now submit; same-path stale changesets are
    rejected
- moved PostgreSQL schema DDL out of Go string literals into
  `internal/postgres/migrations/0001_init.sql`
- expanded functional tests to cover:
  - minimal edit/create/submit/status journey
  - clean status after submit
  - changeset update
  - delete detection and submit
  - outside-slice edit rejection
  - disjoint stale changesets submitting successfully
  - same-path conflict rejection
  - restart persistence against the same PostgreSQL schema and filesystem object
    root
- added opt-in load tests under `tests/load` with the `load` build tag:
  - concurrent disjoint submit through the real CLI and server
  - repeated concurrent status calls over a dirty workspace
  - load tests report operation count, wall time, throughput, p50, p95, and p99

Important decisions and learnings:

- The base snapshot is local cache only. Server-side path containment and
  submit validation still make the authoritative decision.
- Path-base predicates are intentionally based on file fingerprints instead of
  commit equality. This matches the design goal that unrelated changesets can
  publish even when their original base commit is stale.
- The submit path still serializes final publication with the target ref row
  lock and CAS update. The scalability improvement here is that stale disjoint
  work no longer fails only because another path moved first.
- Explicit SQL migration files are easier to review and test than a large Go
  string slice. The Go migrator now embeds and applies those SQL files.
- Replaced the hand-written gRPC binding layer with `proto/core/v1/core.proto`
  and generated Go stubs (`core.pb.go` and `core_grpc.pb.go`). The runtime now
  uses normal protobuf gRPC transport instead of the prototype JSON codec.

Verification:

```bash
go test ./...
go build ./cmd/...
GITSLICE_TEST_DATABASE_URL=postgres://nic@localhost/gitslice_dev?sslmode=disable go test -count=1 ./tests/functional -v
GITSLICE_TEST_DATABASE_URL=postgres://nic@localhost/gitslice_dev?sslmode=disable GITSLICE_LOAD_WORKERS=8 GITSLICE_LOAD_STATUS_ITERATIONS=4 go test -count=1 -tags load ./tests/load -v
```

## 2026-05-22: Git Read Compatibility Layer

Request:

- add the Git layer

Implemented:

- added `internal/gitcompat` with:
  - a projector that reads native refs, commits, slice definitions, commit file
    snapshots, and filesystem object-store blobs
  - a per-slice projection cache rooted at `GITSLICE_GIT_CACHE_ROOT`
  - a synthetic bare Git repository per slice at `{cache_root}/{account}/{slice}.git`
  - stable projection metadata in `gitslice_projection.json`
  - a smart HTTP handler that authenticates bearer/basic tokens, projects the
    latest native ref, and delegates Git wire protocol handling to
    `git http-backend`
- added optional Git HTTP runtime wiring to the single server binary:
  - `GITSLICE_GIT_HTTP_ADDR`
  - `GITSLICE_GIT_CACHE_ROOT`
  - `--git-http-addr`
  - `--git-cache-root`
- implemented read compatibility for `git clone` and `git fetch`
- explicitly reject Git pushes in this first layer. Git-originated changesets
  still need a dedicated push-to-changeset translator.
- added functional coverage that:
  - logs in through the fake account service
  - submits a file through the native CLI
  - clones `http://{git_addr}/git/acme/payment.git`
  - verifies the projected checkout contains `acme/payment/...`
  - verifies `git push` is rejected

Important decisions and learnings:

- The Git layer is a boundary adapter. It projects from Postgres plus filesystem
  object storage; it does not introduce Git as the native storage model.
- The first projection implementation exposes the latest accepted native ref as
  `refs/heads/main`. It does not yet synthesize full historical Git ancestry.
- Paths inside the Git checkout preserve canonical account-rooted layout without
  the leading slash, matching the design.
- The smart HTTP implementation uses the system `git` binary for repository
  creation, commit projection, and `git http-backend`. This is acceptable for
  the MVP layer and keeps protocol details delegated to Git.

Verification:

```bash
go test ./...
go build ./cmd/...
GITSLICE_TEST_DATABASE_URL=postgres://nic@localhost/gitslice_dev?sslmode=disable go test -count=1 ./tests/functional -v
GITSLICE_TEST_DATABASE_URL=postgres://nic@localhost/gitslice_dev?sslmode=disable go test -count=1 ./tests/functional -run TestGitCloneProjection -v
GITSLICE_TEST_DATABASE_URL=postgres://nic@localhost/gitslice_dev?sslmode=disable GITSLICE_LOAD_WORKERS=8 GITSLICE_LOAD_STATUS_ITERATIONS=4 go test -count=1 -tags load ./tests/load -v
```

## 2026-05-22: Conflict And Concurrency Coverage

Request:

- add more comprehensive conflict-resolution cases
- add more concurrency tests to verify system correctness

Implemented:

- added functional conflict coverage for stale disjoint updates:
  - seed a file
  - create a stale update changeset for that file
  - land a separate stale disjoint changeset first
  - submit the original stale update
  - clone the Git projection and verify both final files are present with the
    expected contents
- added delete/update conflict coverage in both orders:
  - delete lands first, stale update is rejected
  - update lands first, stale delete is rejected
- added concurrent same-new-path submit coverage:
  - create multiple changesets from the same missing path base
  - submit them concurrently
  - assert exactly one succeeds and all others fail with conflict semantics
- added concurrent disjoint submit final-state coverage:
  - create multiple stale disjoint changesets
  - submit them concurrently
  - clone the projected Git repository
  - verify every submitted file is present in the final accepted state
- added opt-in load contention coverage:
  - `TestLoadSamePathSubmitContention` drives concurrent same-path submit
    attempts and asserts one winner plus deterministic conflicts

Important decisions and learnings:

- The tests intentionally prepare stale patchsets before concurrent submit. This
  verifies the path-base conflict predicates rather than simply testing fresh
  sequential work.
- Some delete/update tests need to simulate a hydrated workspace by copying the
  base snapshot and file contents from the seed workspace. The current CLI does
  not yet hydrate files during `workspace init`, so the test keeps the focus on
  submit correctness without adding a hydration feature in the same change.
- Git projection is useful as a black-box final-state assertion because it
  verifies server submit, Postgres metadata, filesystem object storage, and Git
  read projection together.

Verification:

```bash
go test ./...
go build ./cmd/...
GITSLICE_TEST_DATABASE_URL=postgres://nic@localhost/gitslice_dev?sslmode=disable go test -count=1 ./tests/functional -v
GITSLICE_TEST_DATABASE_URL=postgres://nic@localhost/gitslice_dev?sslmode=disable GITSLICE_LOAD_WORKERS=8 GITSLICE_LOAD_STATUS_ITERATIONS=4 go test -count=1 -tags load ./tests/load -v
```

## 2026-05-22: Agent Execution Guide

Request:

- add `AGENTS.md`
- give a high-level overview of the project
- document execution rules, including appending important decisions and thinking
  to the execution log

Implemented:

- added root `AGENTS.md` with:
  - current MVP package overview
  - design document map
  - architecture rules for native storage, single-slice workspaces, Postgres,
    prototype filesystem object storage, service boundaries, migrations, proto,
    and Git compatibility
  - execution rules for scoped edits, preserving unrelated user changes, using
    `apply_patch`, formatting Go code, and generated proto handling
  - explicit logging rule to append important decisions, tradeoffs, findings,
    and verification commands to `design/10_execution_log.md`
  - default, functional, and load verification commands

Decision:

- Use `design/10_execution_log.md` as the canonical execution log. If a future
  request says `execution_log.md`, agents should treat this numbered design log
  as the current log unless the repo intentionally introduces a new file.

## 2026-05-23: Hot-File Load And Projection Latency Test

Request:

- load test hundreds of threads creating and submitting changesets on one slice
- use slice A modifying files X, Y, and Z
- measure throughput and latency for those changes to be projected on the home
  slice and another slice containing those files

Implemented:

- added `TestLoadHotFilesCreateSubmitProjectionLatency` under `tests/load`
- the test uses direct gRPC clients against the real local server instead of
  shelling out to the CLI, so the measurement focuses on backend create,
  patchset update, and submit behavior
- slice A is `acme/payment`
- hot files are:
  - `/acme/payment/shared/x.go`
  - `/acme/payment/shared/y.go`
  - `/acme/payment/shared/z.go`
- `acme/backend` is used as the overlapping slice because the dev fixture covers
  `/acme/payment/shared`
- the test records:
  - create/update/submit throughput and latency
  - conflict/retry rate under three-path contention
  - home slice projection refresh latency
  - overlapping slice projection refresh latency
  - submit-to-visible latency for both projected slices
- the projection assertion checks that the projected native commit includes each
  submitted commit, then verifies final projected Git contents match the native
  object store for both `acme/payment` and `acme/backend`

Important decisions and learnings:

- Current Git projection is on-demand. There is no asynchronous projector yet,
  so "time to projected" is measured as submit completion to completion of a
  projection request.
- With 300 concurrent workers and only three hot files, contention dominates:
  300 successful submits required 4036 total attempts, with 3736 conflicts
  rejected by path-base validation.
- The home and overlapping projections both become visible through the same
  global ref movement, but each slice rebuilds its own Git projection cache.

Verification:

```bash
go test ./...
go build ./cmd/...
GITSLICE_TEST_DATABASE_URL=postgres://nic@localhost/gitslice_dev?sslmode=disable GITSLICE_LOAD_HOT_WORKERS=12 GITSLICE_LOAD_HOT_OPERATIONS=12 GITSLICE_LOAD_PROJECTION_WORKERS=4 go test -count=1 -tags load ./tests/load -run TestLoadHotFilesCreateSubmitProjectionLatency -v
GITSLICE_TEST_DATABASE_URL=postgres://nic@localhost/gitslice_dev?sslmode=disable GITSLICE_LOAD_HOT_WORKERS=200 GITSLICE_LOAD_HOT_OPERATIONS=200 GITSLICE_LOAD_HOT_MAX_ATTEMPTS=400 GITSLICE_LOAD_PROJECTION_WORKERS=16 go test -count=1 -tags load ./tests/load -run TestLoadHotFilesCreateSubmitProjectionLatency -v
GITSLICE_TEST_DATABASE_URL=postgres://nic@localhost/gitslice_dev?sslmode=disable GITSLICE_LOAD_HOT_WORKERS=300 GITSLICE_LOAD_HOT_OPERATIONS=300 GITSLICE_LOAD_HOT_MAX_ATTEMPTS=600 GITSLICE_LOAD_PROJECTION_WORKERS=16 go test -count=1 -tags load ./tests/load -run TestLoadHotFilesCreateSubmitProjectionLatency -v
```

300-worker result:

```text
create/update/submit: operations=300 wall=12.164s throughput=24.66/s p50=6.726s p95=11.952s p99=12.137s
contention: successes=300 attempts=4036 conflicts=3736 conflict_rate=92.57%
home projection refresh: p50=2.223ms p95=5.511s p99=6.783s
other projection refresh: p50=2.081ms p95=753.895ms p99=883.395ms
home submit-to-visible: p50=7.772s p95=12.255s p99=12.651s
other submit-to-visible: p50=7.857s p95=12.271s p99=12.714s
```

## 2026-05-23: Cobra CLI And Agent-Friendly Output

Request:

- apply agent-friendly CLI best practices to `gs`
- migrate CLI command parsing to Cobra

Implemented:

- replaced the hand-rolled `internal/cli` command switch with a Cobra command
  tree while preserving the MVP command names and default human-readable output
- added global flags for explicit machine and automation modes:
  - `--format text|json`
  - `--json`
  - `--quiet`
  - `--non-interactive`
  - `--no-color`
  - `--verbose`
  - `--debug`
  - `--trace`
- expanded JSON success output beyond status commands so implemented write
  commands return stable resource identifiers on stdout
- added structured JSON error output from `cmd/gs` when `--json` or
  `--format json` is requested; diagnostics stay on stderr
- added `gs schema` to expose supported commands, global flags, machine output
  fields, and the structured error shape without scraping help text
- added focused CLI tests for schema output and format validation

Important decisions and learnings:

- Default text output remains stable for existing functional tests and human
  workflows. Agent-facing behavior is opt-in through `--json`/`--format json`
  rather than changing the non-TTY default in this MVP pass.
- The new global flags are accepted consistently through Cobra even when some
  are no-ops today. This reserves the interface for future non-interactive,
  diagnostic, and color behavior without introducing prompts or terminal-only
  output now.
- During implementation the worktree already contained a split protobuf layout
  and additional design/test changes. The CLI migration used the current
  generated `corev1` package and did not revert those unrelated changes.

Verification:

```bash
go test ./internal/cli
go test ./...
go build ./cmd/...
go run ./cmd/gs schema
GITSLICE_TEST_DATABASE_URL=postgres://nic@localhost/gitslice_dev?sslmode=disable go test -count=1 ./tests/functional -v
```

## 2026-05-23: Async Path-Head Publish

Request:

- update the design for path-based CAS admission plus async batch root update
- implement the design
- test and benchmark it

Implemented:

- added durable `path_heads` and `pending_publish` tables
- changed `SubmitChangeset` to:
  - lock or initialize each touched path head
  - compare path-head fingerprints against the patchset's recorded bases
  - update accepted path heads to the post-patch fingerprints
  - append a `pending_publish` row
  - mark the changeset `pending_publish`
- added an in-process publisher loop in `server/` that calls storage-layer
  `PublishPending`, builds a commit chain from pending rows, moves the target
  ref once, and marks included changesets `submitted`
- updated CLI submit to preserve the existing synchronous user experience by
  waiting for the accepted changeset to publish before updating local base
  state
- added `status` and `pending_publish_id` fields to `SubmitChangesetResponse`
  and `commit_id` / `pending_publish_id` to `Changeset`
- bounded the Postgres connection pool at 32 open connections after the
  300-worker benchmark hit Postgres `too many clients`
- updated design docs for storage schema, conflict resolution, core API,
  architecture, and MVP implementation details
- updated the hot-file load benchmark to measure:
  - create/update/submit acceptance latency
  - accepted-to-published latency
  - projection refresh latency
  - accepted-to-visible latency for home and overlapping slices

Important decisions and learnings:

- `path_heads` stores tombstones instead of deleting rows for accepted deletes.
  This is required so a stale same-path update cannot pass while the delete is
  accepted but not yet root-published.
- The accepted path head is now the conflict boundary. The root/ref publisher
  still checks pending-row status and ref CAS, but it does not rediscover normal
  same-path conflicts.
- Under hot-file contention, faster acceptance increased retry pressure on the
  three path-head rows. That is expected: path CAS improves root/ref throughput
  and disjoint-write scaling, but same-path workloads still serialize at the
  touched path rows.
- The 300-worker benchmark improved accepted write throughput from the previous
  synchronous-root result of 24.66/s to 41.87/s on the same local Postgres setup.

Verification:

```bash
go test ./...
GITSLICE_TEST_DATABASE_URL=postgres://nic@localhost/gitslice_dev?sslmode=disable go test -count=1 ./tests/functional -v
GITSLICE_TEST_DATABASE_URL=postgres://nic@localhost/gitslice_dev?sslmode=disable GITSLICE_LOAD_WORKERS=8 GITSLICE_LOAD_STATUS_ITERATIONS=4 GITSLICE_LOAD_HOT_WORKERS=12 GITSLICE_LOAD_HOT_OPERATIONS=12 GITSLICE_LOAD_PROJECTION_WORKERS=4 go test -count=1 -tags load ./tests/load -v
GITSLICE_TEST_DATABASE_URL=postgres://nic@localhost/gitslice_dev?sslmode=disable GITSLICE_LOAD_HOT_WORKERS=12 GITSLICE_LOAD_HOT_OPERATIONS=12 GITSLICE_LOAD_PROJECTION_WORKERS=4 go test -count=1 -tags load ./tests/load -run TestLoadHotFilesCreateSubmitProjectionLatency -v
GITSLICE_TEST_DATABASE_URL=postgres://nic@localhost/gitslice_dev?sslmode=disable GITSLICE_LOAD_HOT_WORKERS=300 GITSLICE_LOAD_HOT_OPERATIONS=300 GITSLICE_LOAD_HOT_MAX_ATTEMPTS=600 GITSLICE_LOAD_PROJECTION_WORKERS=16 go test -count=1 -tags load ./tests/load -run TestLoadHotFilesCreateSubmitProjectionLatency -v
```

300-worker async result:

```text
create/update/submit accept: operations=300 wall=7.166s throughput=41.87/s p50=4.739s p95=7.010s p99=7.120s
contention: successes=300 attempts=13904 conflicts=13604 conflict_rate=97.84%
accepted-to-published: p50=3.690s p95=6.159s p99=6.453s
home projection refresh: p50=758us p95=2.871s p99=2.992s
other projection refresh: p50=772us p95=389.299ms p99=408.879ms
home accepted-to-visible: p50=4.025s p95=6.558s p99=6.847s
other accepted-to-visible: p50=4.083s p95=6.617s p99=6.900s
```

## 2026-05-23: Object-Store Tree Nodes

Request:

- remove full snapshot-per-commit storage and use object storage for tree nodes;
  PostgreSQL should store only the hash/root pointer

Implemented:

- added `internal/treestore` for immutable content-addressed tree-node payloads
  stored under `trees/sha256/...` in the prototype filesystem object store
- removed the `commit_files` table from the MVP schema
- changed `commits.root_tree_id` to be the only durable commit-to-tree pointer in
  PostgreSQL
- changed repository reads (`GetFile`, `ListFiles`, path-base validation, and
  projection) to traverse object-store tree nodes from the commit root
- changed `PublishPending` to path-copy only changed tree nodes, create commit
  metadata with the resulting `root_tree_id`, and update the target ref with CAS
- wired the tree store through the single server binary before migrations so the
  initial empty root tree object exists before the initial commit is seeded

Important decisions and learnings:

- Tree-node writes are content-addressed and idempotent. The publisher can write
  them before the PostgreSQL transaction commits; if the transaction fails, the
  object-store nodes are unreachable and can be garbage-collected later.
- PostgreSQL remains the source of truth for reachability and current state.
  Object-store directory listing is not authoritative.
- The async path-head design remains unchanged. `path_heads` is still the
  conflict boundary; tree-node publication is the storage representation of the
  accepted commit state.
- This removes the O(total files in repo) write amplification from commit
  publication. A one-file update now rewrites the leaf's ancestor directory
  nodes plus one commit row, rather than copying every file row into
  `commit_files`.

Verification:

```bash
go test ./...
GITSLICE_TEST_DATABASE_URL=postgres://nic@localhost/gitslice_dev?sslmode=disable go test -count=1 ./tests/functional -v
GITSLICE_TEST_DATABASE_URL=postgres://nic@localhost/gitslice_dev?sslmode=disable GITSLICE_LOAD_WORKERS=8 GITSLICE_LOAD_STATUS_ITERATIONS=4 GITSLICE_LOAD_HOT_WORKERS=12 GITSLICE_LOAD_HOT_OPERATIONS=12 GITSLICE_LOAD_PROJECTION_WORKERS=4 go test -count=1 -tags load ./tests/load -v
GITSLICE_TEST_DATABASE_URL=postgres://nic@localhost/gitslice_dev?sslmode=disable GITSLICE_LOAD_WORKERS=300 go test -count=1 -tags load ./tests/load -run TestLoadConcurrentDisjointSubmit -v
GITSLICE_TEST_DATABASE_URL=postgres://nic@localhost/gitslice_dev?sslmode=disable GITSLICE_LOAD_HOT_WORKERS=300 GITSLICE_LOAD_HOT_OPERATIONS=300 GITSLICE_LOAD_HOT_MAX_ATTEMPTS=600 GITSLICE_LOAD_PROJECTION_WORKERS=16 go test -count=1 -tags load ./tests/load -run TestLoadHotFilesCreateSubmitProjectionLatency -v
```

Benchmark results:

```text
concurrent_disjoint_submit operations=300 wall=748ms throughput=400.89/s p50=465ms p95=620ms p99=641ms

hot_files_create_update_submit_accept operations=300 wall=7.861s throughput=38.16/s p50=5.096s p95=7.682s p99=7.811s
hot_files_contention successes=300 attempts=14807 conflicts=14507 conflict_rate=97.97%
hot_files_accepted_to_published p50=3.471s p95=5.879s p99=6.158s
hot_files_home_projection_refresh p50=838us p95=2.184s p99=2.234s
hot_files_other_projection_refresh p50=900us p95=462ms p99=489ms
hot_files_home_submit_to_visible p50=3.779s p95=6.339s p99=6.588s
hot_files_other_submit_to_visible p50=3.826s p95=6.398s p99=6.645s
```

The hot-file benchmark remains dominated by path-head contention on only three
paths, so accepted throughput is not expected to improve materially there. The
main measured gain is that disjoint changes now publish without per-commit file
snapshot writes and can sustain roughly 400 CLI submits per second on the local
test setup.
