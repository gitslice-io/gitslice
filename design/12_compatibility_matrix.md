# Gitslice Compatibility Matrix

This matrix records what the current MVP actually supports across the native
API, HTTP JSON gateway, Git compatibility layer, and web app. It exists to keep
designs, tests, and UI work from promising GitHub-clone behavior or future
Gitslice features before the backend supports them end to end.

Related documents:

- [03_core_api.md](03_core_api.md): public gRPC service contract
- [05_git_compatibility.md](05_git_compatibility.md): Git compatibility target
  design
- [08_mvp_implementation.md](08_mvp_implementation.md): current Go prototype
  shape
- [11_web_interface_design.md](11_web_interface_design.md): browser UI support
  boundary

## Status Legend

| Status | Meaning |
|--------|---------|
| Supported | Implemented and covered by normal or functional tests. |
| Partial | Implemented for the MVP, with known behavior or protocol limits. |
| Prototype | Available for development only; not a production contract. |
| Planned | Product/design intent, but not implemented yet. |
| Deferred | Not implemented and not committed until a concrete workflow needs it. |
| Non-goal | Intentionally outside the current product direction. |

## Native Core API

| Area | Surface | Status | Notes |
|------|---------|--------|-------|
| Auth | `FakeAccountService.Login` | Prototype | Development login only. No OAuth, SSO, refresh tokens, or token revocation API. |
| Repository reads | `GetRef`, `GetCommit`, `ListCommits`, `ResolvePath`, `ListDirectory`, `ReadFile` | Supported | Reads the native global source graph through PostgreSQL metadata and object storage. |
| Repository import | `ImportGitRepository`, `ImportGitRepositoryStream` | Supported | Imports a Git repository into native storage under an explicit mount path and authoring slice. |
| Blob upload/status | `UploadBlob`, `GetBlobStatus` | Supported | Staged blob readback is not exposed as a public API. |
| Slice reads | `ResolveSlice`, `GetSlice`, `ListSlices` | Supported | Account slug must be known by the caller; there is no account listing API. |
| Slice writes | `UpdateSliceDefinition` | Partial | Only visibility and included paths exist in the concrete `SliceDefinition`. |
| Workspace helpers | `GetWorkspaceState`, `HydratePaths`, `RecordWorkspaceOperation`, `ValidateWorkspaceDiff` | Supported | Workspaces remain bound to exactly one slice. |
| Changesets | `CreateChangeset`, `GetChangeset`, `UpdateChangeset`, `SubmitChangeset`, `AbandonChangeset` | Supported | There is no list/search API, review API, comments API, or rebase API. |

## HTTP JSON Gateway

| Area | Surface | Status | Notes |
|------|---------|--------|-------|
| Browser transport | `GITSLICE_HTTP_ADDR`, `--http-addr` | Supported | Optional listener; disabled when no address is configured. |
| Routes | Generated unbound grpc-gateway paths | Partial | Current routes are method paths such as `/gitslice.core.v1.SliceService/ListSlices`. Human REST paths require future `google.api.http` annotations. |
| Auth | HTTP `Authorization: Bearer ...` forwarded to gRPC metadata | Supported | Shares the same gRPC auth interceptor as CLI calls. |
| CORS | `GITSLICE_HTTP_ALLOWED_ORIGIN`, `--http-allowed-origin` | Supported | Development-oriented fixed allow-origin. |
| Streaming RPCs | `ImportGitRepositoryStream` over HTTP gateway | Planned | The current generated gateway stubs do not expose streaming RPCs to browsers. |

## Git Smart HTTP Compatibility

| Area | Surface | Status | Notes |
|------|---------|--------|-------|
| Listener | `GITSLICE_GIT_HTTP_ADDR`, `--git-http-addr` | Supported | Optional listener separate from the gRPC and HTTP JSON listeners. |
| URL shape | `/git/{account}/{slice}.git` | Supported | Resolves an account slug and slice slug before projection. |
| Auth | Bearer token and Basic password token | Supported | Unauthenticated or invalid-token reads return `401` with a Basic challenge. |
| Authorization | Account membership plus slice resolution | Supported | Missing slices return `404`; unauthorized accounts return `403`. |
| Clone/fetch | `git-upload-pack` | Partial | Projects the latest accepted native ref as `refs/heads/main`. Current MVP does not synthesize full historical Git ancestry. |
| Push | `git-receive-pack` | Planned | Authenticated pushes are rejected with guidance to use native changesets. Push-to-changeset translation is future work. |
| Direct protected ref writes | Branch push to accepted refs | Non-goal | Git writes must not bypass changeset submit validation. |
| Git object ids | Synthetic compatibility ids | Partial | Native commit, tree, and blob ids remain authoritative. |
| Git LFS | LFS protocol and pointer rewriting | Non-goal | Native blobs are projected as ordinary Git blobs. |

## Web MVP

| Area | Surface | Status | Notes |
|------|---------|--------|-------|
| Login | Dev login | Prototype | Uses `FakeAccountService.Login`; no production session model. |
| Source browsing | Known account/path/ref | Supported | Directory and file reads only. No search, blame, branch listing, or inline editing. |
| Slices | List, detail, supported definition edit | Supported | No create/delete/transfer, roles, submit settings, or audited history. |
| Changesets | Create from explicit file edits, lookup by id, inspect metadata, submit, abandon | Supported | No dashboard, list/search, comments, approvals, review requests, checks, or rebase. |
| Git clone display | Optional clone URL | Partial | Show only when Git HTTP is configured; clone/fetch only. |

## GitHub-Compatible Surface

Gitslice is not trying to be a broad GitHub API clone in the MVP. The
`agent-git-service` project is a useful reference for compatibility-matrix
discipline and Git HTTP operational details, but its core model makes Git
repositories authoritative. Gitslice must preserve the native source graph,
slices, changesets, and object storage as the source of truth.

| GitHub-style area | Status | Notes |
|-------------------|--------|-------|
| REST v3 `/api/v3` | Deferred | Only add narrow endpoints when a concrete client workflow needs them. |
| GraphQL v4 | Deferred | No GraphQL schema exists today. |
| OAuth device flow | Deferred | Current auth is fake account login only. |
| Issues, PRs, reviews, comments | Deferred | Changesets are native objects and do not yet expose review/comment APIs. |
| Actions/check runs | Deferred | Submit requirements carry raw ids, but check ingestion/details are not implemented. |
| Webhooks | Deferred | No public webhook delivery surface exists. |
| GitHub search APIs | Non-goal for MVP | Search should be designed around native source graph needs first. |
| GitHub repository administration APIs | Non-goal for MVP | Slice administration must use native APIs and preserve storage rules. |

## Functional Test Anchors

The current functional suite should remain the compatibility gate for supported
behavior:

- HTTP JSON gateway auth, CORS, blob, slice, and changeset flows
- direct gRPC repository read APIs
- slice definition optimistic concurrency
- changeset lifecycle, abandon, idempotent submit, and conflict behavior
- workspace helper APIs
- Git Smart HTTP unauthenticated and invalid-token rejection
- Git Smart HTTP Basic and Bearer authenticated clone/fetch reads
- Git Smart HTTP authenticated push rejection
- final projected Git contents after submit and concurrency scenarios

When a row moves from `Planned` to `Supported`, add or update functional tests
in the same change.
