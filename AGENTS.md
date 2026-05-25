# Gitslice Agent Guide

This file gives coding agents the project-level context and execution rules for
working in this repository.

## Project Overview

Gitslice is a Git-compatible source graph system built around native slices,
changesets, patchsets, and a global content-addressed object model.

The current MVP prototype is Go-based and uses:

- `cmd/gitslice-server`: one server binary for fake account auth, core gRPC
  services, and optional Git smart HTTP read compatibility.
- `cmd/gs`: native CLI for login, single-slice workspaces, server file shell,
  status, changesets, submit, and changeset status.
- `proto/core/v1`: protobuf API contract and generated Go gRPC stubs.
- `server/`: process wiring only. Keep product rules out of this package.
- `service/`: public gRPC service implementations and fake account service.
- `internal/postgres`: PostgreSQL metadata store, SQL migrations, and seed data.
- `internal/objectstore/filesystem`: prototype-only filesystem object store.
- `internal/treestore`: immutable tree-node storage on top of the object store.
- `internal/gitcompat`: Git read compatibility layer and projection cache.
- `tests/cli`: real server plus CLI e2e tests.
- `tests/rpc`: real server plus direct RPC e2e tests.
- `tests/load`: opt-in load and contention tests behind the `load` build tag.

The design source of truth is under `design/`, especially:

- `design/00_product.md`
- `design/01_gitslice_architecture_design.md`
- `design/02_storage.md`
- `design/03_core_api.md`
- `design/04_cli_design.md`
- `design/05_git_compatibility.md`
- `design/07_conflict_resolution.md`
- `design/08_mvp_implementation.md`
- `design/09_execution_plan.md`
- `design/10_execution_log.md`
- `design/11_web_interface_design.md`

## Architecture Rules

- Preserve the native storage model. Git is a compatibility layer, not the
  internal source of truth.
- Keep workspaces bound to exactly one slice.
- Do not introduce cross-slice changesets.
- Use PostgreSQL as metadata source of truth.
- Store commit tree payloads in object storage; PostgreSQL commit rows should
  store the root tree hash, not per-commit file snapshots.
- Treat filesystem object storage as prototype-only.
- Keep `server/` wiring-only: config, listeners, dependency construction,
  interceptors, health, and shutdown.
- Put service behavior in `service/` and shared primitives in `internal/...`.
- Put SQL schema changes in `internal/postgres/migrations/`, not embedded Go
  string literals.
- Keep path validation and canonicalization in `internal/paths`.
- Keep object id rules in `internal/objectid`.
- Do not bypass changeset submit validation for user-visible writes.

## Execution Rules

- Before changing behavior, read the relevant design doc and nearby code.
- Keep changes scoped to the requested behavior.
- Do not revert or modify unrelated user changes. Leave unrelated untracked
  files alone, including `.antigravitycli/`.
- Use `apply_patch` for manual edits.
- Run `gofmt` on edited Go files.
- Prefer generated protobuf stubs from `proto/core/v1/*.proto`; do not
  reintroduce hand-written gRPC bindings.
- If changing generated protobuf output, regenerate with:

```bash
protoc --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative --go-grpc_opt=require_unimplemented_servers=false proto/core/v1/*.proto
protoc --grpc-gateway_out=. --grpc-gateway_opt=paths=source_relative --grpc-gateway_opt=generate_unbound_methods=true proto/core/v1/*.proto
```

## Logging Rule

Append important implementation decisions, tradeoffs, test findings, and
surprising behavior to `design/10_execution_log.md`.

When updating the log:

- Add a dated section.
- Include the request or goal.
- Record meaningful decisions and why they were made.
- Record important learnings or bugs found during verification.
- Record the exact verification commands that matter.

Small mechanical edits do not need long entries, but anything that changes
architecture, data model, validation, concurrency behavior, API shape, or test
coverage should be logged.

## Verification

Default local gate:

```bash
go test ./...
go build ./cmd/...
```

Real PostgreSQL functional gate:

```bash
GITSLICE_TEST_DATABASE_URL=postgres://nic@localhost/gitslice_dev?sslmode=disable go test -count=1 ./tests/cli ./tests/rpc -v
```

Opt-in load gate:

```bash
GITSLICE_TEST_DATABASE_URL=postgres://nic@localhost/gitslice_dev?sslmode=disable GITSLICE_LOAD_WORKERS=8 GITSLICE_LOAD_STATUS_ITERATIONS=4 go test -count=1 -tags load ./tests/load -v
```

If a change touches submit, conflict detection, concurrency, storage, or the Git
projection layer, prefer running the real PostgreSQL functional gate. Run the
load gate when changing contention or performance-sensitive paths.
