# Gitslice Execution Log

This log captures implementation notes, decisions, and important learnings while
turning the design docs into the first Go prototype.

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
