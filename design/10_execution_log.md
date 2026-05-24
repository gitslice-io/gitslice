# Gitslice Execution Log

This log captures implementation notes, decisions, and important learnings while
turning the design docs into the first Go prototype.

## 2026-05-24: Server-Side Interactive File Shell

Request:

- add a `gs shell` command for interactive file and folder inspection
- ensure the shell inspects server files, not local workspace files

Implemented:

- added `gs shell` as a local REPL that uses the current workspace only for
  auth, slice scope, and relative path mapping
- defaulted shell inspection to latest `refs/global/main`, with `--commit` for
  pinned native commit inspection
- added read-only shell commands: `pwd`, `ls`, `cd`, `cat`, `stat`, `ref`,
  `help`, `exit`, and `quit`
- mapped shell paths to the bound slice root and rejected paths outside the
  slice before issuing server reads
- added functional coverage proving `cat` reads a submitted server file even
  after the local workspace copy is deleted
- expanded functional coverage for shell navigation, absolute canonical paths,
  slice-boundary rejection, and `--commit` pinned historical reads

Important decisions and learnings:

- The shell is deliberately read-only in the MVP. Local edits still happen in
  the hydrated workspace and go through `gs status`, `gs cs create`, and submit
  validation.
- Keeping the shell server-backed avoids confusing local dirty state with the
  authoritative committed tree the user wants to inspect.
- Leading slash paths can mean shell-rooted paths such as `/nested/file.go`.
  Canonical paths under the current account such as `/acme/backend/file.go`
  must still be interpreted as canonical so cross-slice escapes are rejected.

Verification:

```bash
go test ./internal/cli
GITSLICE_TEST_DATABASE_URL=postgres://nic@localhost/gitslice_dev?sslmode=disable go test -count=1 ./tests/functional -run 'TestServerShell' -v
git diff --check
go test ./...
go build ./cmd/...
GITSLICE_TEST_DATABASE_URL=postgres://nic@localhost/gitslice_dev?sslmode=disable go test -count=1 ./tests/functional -v
```

## 2026-05-24: Global Client Object Cache

Request:

- add a global client-side object cache shared across workspaces
- make workspace initialization use the same cache for hydrated files

Implemented:

- added `internal/clientcache` as a content-addressed local cache rooted at the
  user cache directory, keyed by `sha256:<digest>`
- made workspace scans write file bytes through the global cache before status
  or changeset creation
- changed changeset create/update to call `BlobService.GetBlobStatus` for
  changed content hashes and upload only server-missing blobs from the cache
- made `gs workspace init <account>/<slice>` hydrate the slice through the
  global cache by default
- added `gs workspace hydrate <path>` for targeted follow-up hydration through
  the same cache

Important decisions and learnings:

- Workspace `.gs` state remains workspace-local. Cached bytes are local
  performance data and are not authoritative for authorization, path
  containment, or submit correctness.
- Init-time hydration resolves server metadata first. If the content hash is
  already present locally, the CLI writes workspace bytes from the cache without
  reading the blob from the server.
- The cache is safe to share across workspaces because content hashes are
  verified before storage and server blob identity is revalidated with
  `GetBlobStatus` before submit.

Verification:

```bash
go test ./internal/clientcache ./internal/cli
go test ./...
go build ./cmd/...
GITSLICE_TEST_DATABASE_URL=postgres://nic@localhost/gitslice_dev?sslmode=disable go test -count=1 ./tests/functional -v
```

## 2026-05-24: Split PostgreSQL Stores

Request:

- break up the broad Postgres `Store` object
- remove `internal/postgres/store.go`
- have callers depend on specific store objects instead of a monolithic store

Implemented:

- replaced the broad `postgres.Store` type with a lifecycle-only `postgres.DB`
  handle for opening, closing, migrations, tree-store configuration, and
  integrity verification
- moved Postgres behavior into focused store files:
  - `auth_store.go`
  - `blob_store.go`
  - `changeset_store.go`
  - `repository_store.go`
  - `slice_store.go`
- moved shared types, errors, migrations, fixture seeding, and helpers into
  focused files and removed `internal/postgres/store.go`
- removed compatibility forwarding methods such as `Store.GetRef`,
  `Store.GetChangeset`, and `Store.PublishPending`
- updated service construction, gRPC auth interceptors, the publisher loop, and
  Git compatibility to accept the specific Postgres stores they use

Important decisions and learnings:

- `postgres.DB` remains as the ownership/lifecycle boundary for the shared SQL
  connection and migrations, but product behavior now hangs off `AuthStore`,
  `RepositoryStore`, `BlobStore`, `SliceStore`, and `ChangesetStore`.
- `ChangesetStore` keeps explicit references to `RepositoryStore` and
  `SliceStore` because create/submit/publish need ref reads, slice resolution,
  and tree access inside the same Postgres-backed boundary.
- Integrity verification stays on `postgres.DB` because it intentionally spans
  refs, commits, blobs, path heads, and tree reachability.

Verification:

```bash
gofmt -w internal/postgres/*.go server/*.go service/*.go internal/gitcompat/*.go tests/load/load_test.go
go test -mod=readonly ./internal/postgres ./service ./server ./internal/gitcompat
go test -mod=readonly ./cmd/gitslice-server ./internal/authctx ./internal/gitcompat ./internal/objectstore/filesystem ./internal/objectid ./internal/paths ./internal/postgres ./internal/treestore ./proto/core/v1 ./server ./service
go build -mod=readonly ./cmd/gitslice-server
```

## 2026-05-23: Add HTTP JSON Gateway For Core gRPC Services

Request:

- add grpc-gateway in the server so the web app can call the API

Implemented:

- generated grpc-gateway stubs for all public `proto/core/v1` service files
  using unbound method routes
- added optional server HTTP gateway listener configured by `GITSLICE_HTTP_ADDR`
  or `--http-addr`
- added optional CORS origin handling through `GITSLICE_HTTP_ALLOWED_ORIGIN` or
  `--http-allowed-origin`
- wired the gateway through the existing gRPC listener with insecure local
  transport so the current auth interceptor and service behavior remain shared
  by CLI and web callers
- forwarded the HTTP `Authorization` header into gRPC metadata for bearer-token
  protected methods
- normalized wildcard gRPC listen addresses to loopback before the gateway dials
  the in-process gRPC server
- added a real HTTP gateway functional test covering dev login and authenticated
  `ListSlices`
- updated proto regeneration instructions to include grpc-gateway output

Important decisions and learnings:

- The current protos do not have `google.api.http` annotations, so the first
  gateway uses generated unbound POST routes such as
  `/gitslice.core.v1.SliceService/ListSlices`.
- `grpc-gateway` v2.29 currently requires a newer Go version than this module,
  so the dependency is pinned to v2.22.0 to preserve the module's Go 1.22 line
  while keeping existing grpc/protobuf versions stable.
- The gateway is optional to avoid introducing a new mandatory listen port for
  CLI-only and test server runs.

Verification:

```bash
protoc --grpc-gateway_out=. --grpc-gateway_opt=paths=source_relative --grpc-gateway_opt=generate_unbound_methods=true proto/core/v1/*.proto
gofmt -w cmd/gitslice-server/main.go server/*.go tests/functional/cli_smoke_test.go
go test ./...
go build ./cmd/...
git diff --check
GITSLICE_TEST_DATABASE_URL=postgres://nic@localhost/gitslice_dev?sslmode=disable go test -count=1 ./tests/functional -run TestHTTPGatewayLoginAndListSlices -v
```

The targeted real-Postgres functional run could not complete in this
environment because local PostgreSQL rejected password authentication for
`user=nic database=gitslice_dev` (`SQLSTATE 28P01`). The default test suite
still passes and skips real-Postgres functional coverage when the database URL
is unavailable.

## 2026-05-23: Align Web Interface Design With Current Prototype

Request:

- review `design/11_web_interface_design.md`
- remove or re-scope features and functions that are not supported yet

Implemented:

- rewrote the web interface design around the currently implemented public
  service surface in `proto/core/v1`
- removed first-version UI scope for OAuth, account administration, changeset
  lists/search, review comments, approvals, check-run details, path-lock
  management, blame, rebase, and unsupported slice policy fields
- changed the changeset section from a full review UI to id-based lookup,
  patchset metadata display, add-patchset, submit, and abandon
- changed slice administration to direct edits of only visibility and included
  paths through `SliceService.UpdateSliceDefinition`
- documented that the current repository still needs a browser-facing web
  adapter because grpc-gateway/gRPC-Web endpoints are not implemented

Important decisions and learnings:

- The current concrete `SliceDefinition` only carries version, visibility, and
  included paths; roles, submit settings, default branch, display name, and
  definition history remain future API work.
- The current changeset API supports `GetChangeset` by id but no list/search
  endpoint, so the web MVP should not include dashboards, review queues, or
  filterable changeset tables.
- Persisted patchsets expose file edit metadata and blob ids, but there is no
  public staged-blob read API. The web can show a client-side diff while content
  is being pasted/uploaded, but should not promise full server-reconstructed
  diffs yet.

Verification:

```bash
sed -n '1,260p' design/11_web_interface_design.md
sed -n '260,620p' design/11_web_interface_design.md
rg -n "OAuth|approval|approve|comment|review|check-run|Path Locks|path lock management|Account Settings|service account|token revocation|dashboard|activity|blame|search|rebase|default branch|display name|roles|submit settings|grpc-gateway|WebSocket|Monaco|diff-review|inline comments|pending reviews|reviewer" design/11_web_interface_design.md -S
git diff -- design/11_web_interface_design.md
```

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

## 2026-05-23: Incremental Deep Import And CLI Progress

Request:

- make deep import practical by avoiding full snapshot-per-commit work
- improve CLI UX so imports show progress
- confirm that deep import remains server-side

Implemented:

- added `RepositoryService.ImportGitRepositoryStream`, a server-streaming import
  RPC that sends clone, commit listing, per-commit read/upload/submit/publish,
  and final result events
- added gRPC stream authentication so streaming service methods get the same
  subject context as unary methods
- changed the CLI text path to use the streaming RPC and print progress to
  stderr while keeping `--json` as a final-response-only unary call
- changed deep import after the first commit to use `git diff-tree` between the
  previously imported Git commit and the next Git commit, then read only changed
  blobs with `git cat-file --batch`
- kept the first deep commit as a mounted-tree materialization so importing onto
  an existing mount still replaces stale mounted contents

Important decisions:

- Deep import stays server-side. The CLI only submits source, mount, slice, and
  mode, then renders server progress.
- The MVP native history remains linear. For each Git commit in topo-order, the
  importer computes the tree delta from the previously imported Git commit to
  the next Git commit, so each emitted native commit represents that Git tree in
  the chosen linear import sequence without re-reading the full tree.
- Text progress goes to stderr so stdout can remain a clean summary. JSON output
  remains stable and does not include progress events.

Verification:

```bash
go test ./service
go test ./server
go test ./internal/cli
go test ./internal/treestore
GITSLICE_TEST_DATABASE_URL=postgres://nic@localhost/gitslice_dev?sslmode=disable go test -count=1 ./tests/functional -run 'TestGitHubImport' -v
go test ./...
go build ./cmd/...
GITSLICE_TEST_DATABASE_URL=postgres://nic@localhost/gitslice_dev?sslmode=disable go test -count=1 ./tests/functional -v
GITSLICE_TEST_DATABASE_URL=postgres://nic@localhost/gitslice_dev?sslmode=disable GITSLICE_LOAD_WORKERS=8 GITSLICE_LOAD_STATUS_ITERATIONS=4 go test -count=1 -tags load ./tests/load -v
```

## 2026-05-23: Bounded Resumable Linux Deep Import

Request:

- prove deep import against Linux in a bounded way
- add resumability before attempting large history imports

Implemented:

- added `max_commits` and `resume` to `ImportGitRepositoryRequest`
- added CLI flags:
  - `--max-commits N` for bounded deep import of the most recent N commits
  - `--resume` enabled by default to reuse completed imports
- added server-side `git_imports` and `git_import_commits` tables to persist
  Git-to-native commit mappings by source, mount path, authoring slice, target
  ref, and mode
- changed bounded deep imports to use `git clone --depth N` so large-repo tests
  do not clone full history
- added functional coverage for bounded deep import and resume skipping

Linux bounded test:

```bash
gs repo import github torvalds/linux \
  --mount /acme/payment/imported/linux-deep \
  --slice acme/payment \
  --mode deep \
  --max-commits 5
```

Result:

```text
first run: 108.578s, imported 5 commits, pending=0, object_store=1.8G
resume run: 29.734s, skipped all 5 commits
```

The first selected commit materialized 93,689 paths. Subsequent selected commits
used incremental Git diffs and changed 1, 1, 1, and 1,852 paths respectively.

Verification:

```bash
go test ./service
go test ./internal/postgres
go test ./internal/cli
GITSLICE_TEST_DATABASE_URL=postgres://nic@localhost/gitslice_dev?sslmode=disable go test -count=1 ./tests/functional -run 'TestGitHubImport' -v
go test ./...
go build ./cmd/...
GITSLICE_TEST_DATABASE_URL=postgres://nic@localhost/gitslice_dev?sslmode=disable go test -count=1 ./tests/functional -v
GITSLICE_TEST_DATABASE_URL=postgres://nic@localhost/gitslice_dev?sslmode=disable GITSLICE_LOAD_WORKERS=8 GITSLICE_LOAD_STATUS_ITERATIONS=4 go test -count=1 -tags load ./tests/load -v
```

## 2026-05-23: Linux Deep Import 5000-Commit Attempt

Request:

- measure deep import time for `torvalds/linux` with `--max-commits 5000`

Result:

- The import did not reach native import work. Both attempts failed during the
  server-side GitHub clone.
- Attempt 1 used the default Git HTTP behavior:
  - duration: 323.043s
  - failure: `curl 92 HTTP/2 stream 5 was not closed cleanly: CANCEL`
  - imported commits: 0
  - published commits: 0
- Attempt 2 forced Git HTTP/1.1 through inherited Git config:
  - duration: 326.322s
  - failure: `curl 18 transfer closed with outstanding read data remaining`
  - imported commits: 0
  - published commits: 0

Important learning:

- At `--depth 5000`, the current server-side `git clone --depth N` path is not
  reliable enough against GitHub for Linux-sized repositories. The next fix
  should make import clone/fetch resumable, probably by using a server-side Git
  cache and fetch retries instead of cloning into a throwaway directory for every
  import attempt.

Verification command shape:

```bash
gs repo import github torvalds/linux \
  --mount /acme/payment/imported/linux-deep5000 \
  --slice acme/payment \
  --mode deep \
  --max-commits 5000
```

## 2026-05-23: Linux Deep Import 1000-Commit Run

Request:

- try a bounded Linux deep import with `--max-commits 1000`

Command:

```bash
gs repo import github torvalds/linux \
  --mount /acme/payment/imported/linux-deep1000 \
  --slice acme/payment \
  --mode deep \
  --max-commits 1000
```

Result:

```text
status: success
duration: 1155.289s
native commits published: 1000
pending publishes: 0
object store size: 2.9G
object store files: 147060
observed temporary Git checkout size: 8.0G
```

Important observations:

- `git clone --depth 1000` completed successfully, unlike the earlier
  `--depth 5000` attempts.
- The first selected commit materialized 93,693 paths.
- The importer completed all 1000 native publishes. Several merge commits still
  touched thousands of paths, including observed diffs of 9,511, 9,569, 25,935,
  and 25,824 changed paths.
- End-to-end time includes server startup, clone, commit listing, initial tree
  materialization, incremental Git diff reads, blob writes, native changeset
  submit, and publish for every selected commit.

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

## 2026-05-23: Service And Storage Struct Split

Request:

- replace the monolithic service implementation struct with per-service structs
- split the Postgres storage object into focused logic structs
- add targeted Postgres-backed storage tests

Implemented:

- replaced the single `service.Services` implementation with dedicated
  `FakeAccountService`, `RepositoryService`, `BlobService`, `SliceService`,
  `WorkspaceService`, and `ChangesetService` structs
- kept shared path diff validation in a small internal `diffValidator` helper
  used by workspace and changeset flows
- added repository read helpers so workspace hydration can use repository logic
  without depending on the repository gRPC handler object
- split `internal/postgres.Store` into lifecycle plus focused logic accessors:
  auth, blobs, repository, slices, and changesets
- kept compatibility wrapper methods on `Store` for callers such as the Git
  compatibility layer while new service code depends on narrower storage structs
- added Postgres-backed storage tests for:
  - publishing object-store tree nodes and reading files through `root_tree_id`
  - rejecting same-path submits through path-head CAS before root publish
  - accepting and publishing disjoint pending changesets in one batch

Verification:

```bash
go test ./...
GITSLICE_TEST_DATABASE_URL=postgres://nic@localhost/gitslice_dev?sslmode=disable go test -count=1 ./internal/postgres -v
GITSLICE_TEST_DATABASE_URL=postgres://nic@localhost/gitslice_dev?sslmode=disable go test -count=1 ./tests/functional -v
```

## 2026-05-23: Integrity Verifier And Production Future Work

Request:

- make the repo a stronger proof of concept for a large-scale monorepo system
- keep the POC on PostgreSQL plus filesystem object storage
- prove scalability and integrity
- add `future_work.md` for production work

Implemented:

- added a storage integrity verifier in `internal/postgres` that checks:
  - refs point at existing commits
  - commit ids recompute from canonical commit payloads
  - commit parents exist
  - reachable root tree objects exist and hash to their content-addressed ids
  - tree file entries reference existing blob rows with matching metadata
  - blob rows match filesystem object bytes by id, raw content hash, and size
  - path heads match the current accepted ref when no publish is pending
- added tree-object verification in `internal/treestore` so the verifier can
  traverse immutable tree nodes from a root tree id and detect missing or
  corrupt tree payloads
- fixed native object-id canonicalization so nil and empty tree/commit slices
  hash identically, and commit timestamps are normalized to UTC before hashing
- added Postgres-backed tests proving the verifier passes after publish and
  detects a missing filesystem blob object
- added the verifier to the hot-file load/projection test so a scale run ends
  with a native metadata plus filesystem-object integrity check
- added root `future_work.md` covering production storage, scalability,
  integrity, security, product completeness, and operations work

Important decisions and learnings:

- The integrity verifier intentionally treats PostgreSQL as the reachability
  source of truth and object-store directory listing as non-authoritative.
- Path heads may legitimately run ahead of the accepted ref while publish is
  pending, so the verifier only enforces path-head/current-ref equality when
  there are no pending publish rows.
- The first DB-backed integrity run exposed that object ids could vary based on
  empty slice representation and timestamp timezone formatting. Canonicalizing
  those inputs in `internal/objectid` makes commit and tree ids stable across
  storage round trips.

Verification:

```bash
go test ./...
go build ./cmd/...
GITSLICE_TEST_DATABASE_URL=postgres://nic@localhost/gitslice_dev?sslmode=disable go test -count=1 ./internal/postgres -v
GITSLICE_TEST_DATABASE_URL=postgres://nic@localhost/gitslice_dev?sslmode=disable go test -count=1 ./tests/functional -v
GITSLICE_TEST_DATABASE_URL=postgres://nic@localhost/gitslice_dev?sslmode=disable GITSLICE_LOAD_WORKERS=8 GITSLICE_LOAD_STATUS_ITERATIONS=4 GITSLICE_LOAD_HOT_WORKERS=12 GITSLICE_LOAD_HOT_OPERATIONS=12 GITSLICE_LOAD_HOT_MAX_ATTEMPTS=80 GITSLICE_LOAD_PROJECTION_WORKERS=4 go test -count=1 -tags load ./tests/load -v
```

Bounded load result:

```text
concurrent_disjoint_submit operations=8 wall=54.702958ms throughput=146.24/s p50=52.81425ms p95=53.591625ms p99=53.591625ms
same_path_submit_contention operations=8 wall=25.94375ms throughput=308.36/s p50=7.598833ms p95=8.043084ms p99=8.043084ms
repeated_status operations=32 wall=45.9465ms throughput=696.46/s p50=9.240542ms p95=20.643208ms p99=20.902375ms
hot_files_create_update_submit_accept operations=12 wall=140.116041ms throughput=85.64/s p50=77.477958ms p95=139.282084ms p99=139.282084ms
hot_files_contention successes=12 attempts=76 conflicts=64 conflict_rate=84.21%
integrity ref_count=1 commit_count=16 blob_count=79 tree_count=61 tree_file_count=42 path_head_count=3
```

## 2026-05-23: GitHub Repository Import CLI

Request:

- add CLI support to import a GitHub repository under a chosen Gitslice path
- support shallow import and deep import with every Git commit
- test listing and inspecting imported commits

Implemented:

- added `RepositoryService.ImportGitRepository`
  - accepts GitHub `owner/repo`, Git URLs, or local Git paths for tests
  - validates that the mount path is inside the authoring slice
  - clones shallow for `mode=shallow` and imports only `HEAD`
  - clones full history for `mode=deep` and imports commits in chronological
    order as native changesets
  - writes imported blobs through the object store and blob metadata path
  - submits through the existing changeset, path-head CAS, pending publish, and
    ref CAS flow rather than creating local commits in the CLI
- added `RepositoryService.ListCommits` for native commit listing by ref
- added CLI commands:
  - `gs repo import github <owner/repo-or-url> --mount <path> --slice <slice> --mode shallow`
  - `gs repo import github <owner/repo-or-url> --mount <path> --slice <slice> --mode deep`
  - `gs commit list --limit N`
  - `gs commit inspect <native-commit-id>`
- added functional tests that create a local Git repo, import it through the
  GitHub import command in shallow and deep modes, list native commits, inspect
  imported commits, and verify the final projected Git checkout contains the
  imported files

Important decisions and learnings:

- The CLI command is named for GitHub, but tests pass a local Git path through
  the same code path so CI does not depend on external network access.
- Deep import preserves one native commit per Git commit for linear histories by
  diffing each Git commit snapshot against the previous imported snapshot. Merge
  semantics can be made richer later, but the MVP creates a deterministic linear
  native history.
- Import is intentionally server-side. The CLI starts the operation and displays
  the Git-to-native commit mapping; the server owns validation, blob persistence,
  submit admission, and publication.

Verification:

```bash
go test ./...
GITSLICE_TEST_DATABASE_URL=postgres://nic@localhost/gitslice_dev?sslmode=disable go test -count=1 ./tests/functional -v
```

## 2026-05-23: Large GitHub Import Performance Fix

Request:

- fix the Linux-sized shallow GitHub import path after the benchmark showed it
  failed after about 24 minutes waiting for publish

Implemented:

- replaced per-file `git show <commit>:<path>` import reads with one
  `git cat-file --batch` process per snapshot
- batched treestore edit application so large sibling file imports rewrite each
  touched directory once instead of path-copying the tree once per file
- made filesystem object-store writes idempotent when the content-addressed
  target already exists
- made import publish waiting scale with changed file count instead of using a
  fixed 30-second timeout

Important learnings:

- The first `torvalds/linux` shallow import reached 93,703 path heads and
  93,064 blob rows, but failed because the final publish took longer than the
  import wait timeout.
- The old treestore path copied the root-to-file directory chain for every file
  edit, producing 918,429 filesystem objects and a 7.6 GB object store for one
  Linux shallow import.
- After batching, the same shallow import completed successfully in 96.908s and
  produced a 1.7 GB object store with 99,205 files.

Verification:

```bash
go test ./internal/treestore
go test ./service
go test ./...
go build ./cmd/...
GITSLICE_TEST_DATABASE_URL=postgres://nic@localhost/gitslice_dev?sslmode=disable go test -count=1 ./tests/functional -run 'TestGitHubImport' -v
GITSLICE_TEST_DATABASE_URL=postgres://nic@localhost/gitslice_dev?sslmode=disable GITSLICE_LOAD_WORKERS=8 GITSLICE_LOAD_STATUS_ITERATIONS=4 go test -count=1 -tags load ./tests/load -v
```

## 2026-05-24: Functional Test Coverage Gaps

Request:

- add the missing functional coverage identified after reviewing the existing
  functional tests

Implemented:

- expanded HTTP gateway functional coverage beyond login/list slices to cover:
  unauthenticated and invalid-token rejection, CORS preflight, blob upload and
  status lookup, changeset create/update/submit, and polling a submitted
  changeset through the gateway
- added direct gRPC functional coverage for repository read APIs:
  `GetRef`, `GetCommit`, `ListDirectory`, `ResolvePath`, full file reads, and
  range file reads after a real CLI submit
- added slice definition update coverage, including stale
  `expected_definition_hash` conflict handling
- added changeset lifecycle coverage for submit idempotency, abandon, and
  rejected submit after abandon
- added workspace helper coverage for `GetWorkspaceState`, `HydratePaths`,
  `RecordWorkspaceOperation`, and invalid workspace ids
- expanded Git smart HTTP coverage to include unauthenticated clone rejection,
  authenticated clone, fetch after a new native submit, and push rejection
- fixed Git projection clone checkout by setting each bare projection repo's
  symbolic `HEAD` to `refs/heads/main` before pushing `main`

Important decisions and learnings:

- the new tests stay in the existing real-server functional harness so they
  exercise auth interceptors, service wiring, storage, gateway transcoding, and
  projection behavior together
- the Git unauthenticated clone assertion accepts several common Git stderr
  phrasings because clients can report the same 401 challenge differently
- running the real functional suite exposed that projection repos pushed
  `refs/heads/main` while bare repo `HEAD` still pointed at the default branch,
  causing clones to succeed without checking out files

Verification:

```bash
gofmt -w internal/gitcompat/projector.go tests/functional/cli_smoke_test.go
GITSLICE_TEST_DATABASE_URL=<local test database URL> go test -count=1 ./tests/functional -run 'Test(StaleDisjointUpdatePreservesFinalState|ConcurrentDisjointSubmitFinalProjection|GitCloneProjection)$' -v
GITSLICE_TEST_DATABASE_URL=<local test database URL> go test -count=1 ./tests/functional -v
GITSLICE_TEST_DATABASE_URL=<local test database URL> go test -count=1 ./internal/postgres -v
go test ./...
go build ./cmd/...
git diff --check
```

The default `go test ./...`, `go build ./cmd/...`, and `git diff --check`
gates passed. The real PostgreSQL functional and storage gates passed with a
local test database URL.
