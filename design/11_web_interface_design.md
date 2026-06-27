# Gitslice Web Interface Design

This document defines the first web interface for Gitslice against the
capabilities implemented by the current Go prototype. It intentionally avoids
review, account-management, policy, search, and realtime features that are
described in broader product docs but are not yet exposed by the concrete
services under `proto/core/v1`.

The CLI remains the primary workspace and edit surface. The web UI is a thin
inspection and control console for:

- developer login through the current fake account service
- source browsing by known account path and ref
- slice listing, details, and the currently supported definition edits
- changeset creation from uploaded file edits
- changeset lookup by id, patchset metadata inspection, submit, and abandon

Related documents:

- [00_product.md](00_product.md): product overview and broader future workflows
- [01_gitslice_architecture_design.md](01_gitslice_architecture_design.md): top-level architecture
- [03_core_api.md](03_core_api.md): target gRPC API design
- [04_cli_design.md](04_cli_design.md): native CLI design
- [08_mvp_implementation.md](08_mvp_implementation.md): concrete MVP implementation shape

## 1. Current Support Boundary

The web MVP may use only the currently implemented public services:

```text
FakeAccountService.Login
FakeAccountService.ApproveSignup

RepositoryService.ResolvePath
RepositoryService.ListDirectory
RepositoryService.ReadFile
RepositoryService.GetCommit
RepositoryService.GetRef

BlobService.GetBlobStatus
BlobService.UploadBlob

SliceService.ResolveSlice
SliceService.GetSlice
SliceService.ListSlices
SliceService.UpdateSliceDefinition

ChangesetService.CreateChangeset
ChangesetService.GetChangeset
ChangesetService.UpdateChangeset
ChangesetService.SubmitChangeset
ChangesetService.AbandonChangeset
```

The server exposes these methods to browsers through the HTTP API handler
enabled on the combined server listener and, when configured, the optional HTTP
listener from `GITSLICE_HTTP_ADDR` or `--http-addr`. The web app uses ConnectRPC
with TypeScript service descriptors generated from `proto/core/v1/*.proto`,
using Connect procedure paths such as:

```text
POST /gitslice.core.v1.FakeAccountService/Login
POST /gitslice.core.v1.FakeAccountService/ApproveSignup
POST /gitslice.core.v1.SliceService/ListSlices
POST /gitslice.core.v1.ChangesetService/GetChangeset
```

The Connect HTTP path calls the same service implementations as the native gRPC
server, so service behavior stays shared across CLI and web callers. Browser
clients served from a different origin can use
`GITSLICE_HTTP_ALLOWED_ORIGIN` or
`--http-allowed-origin` to enable CORS for local development.

The repository's current `web/` directory implements only the signup approval
page from this design. It is a static browser app and is not mounted by the Go
server.

### 1.1 Explicit Non-Scope

The following are not in the first web UI because the current prototype does not
support them end to end:

- OAuth, SSO, invitations, browser refresh tokens, or production session
  management
- account member management, service-account management, token revocation, or
  account creation
- changeset list/search feeds; the API can get a changeset by id but cannot list
  or search changesets
- code search, global search, or source-tree full-text search
- inline review comments, general comments, review threads, approvals, review
  requests, or changes-requested state
- CI check-run ingestion, check-run details, or mutable check status
- path-lock creation, release, or management
- source blame or per-line commit attribution
- rebase actions
- persisted patchset diff rendering from server state; patchsets expose file
  edit metadata and blob ids, but the public API does not expose staged blob
  reads
- slice roles, submit settings, default branch, display name, delete, transfer,
  or audited definition history in the concrete `SliceDefinition`
- reviewed control-plane changesets for slice-definition changes
- Git push into changesets

## 2. Navigation Architecture

Use a compact app shell with a persistent sidebar and top bar:

```text
+--------------------------------------------------+
| TopBar: account/ref inputs            user menu  |
+----------+---------------------------------------+
| Sidebar  |                                       |
|          | Page Content Area                     |
| Home     |                                       |
| Source   |                                       |
| Slices   |                                       |
| Changeset|                                       |
+----------+---------------------------------------+
```

### 2.1 Sidebar

- **Home**: starting point with dev login state, account input, and common
  supported actions.
- **Source**: opens the source browser for an account, path, and known ref.
- **Slices**: opens the slice list for the selected account.
- **Changeset**: opens changeset lookup and create flows.

Do not include Path Locks, Account Settings, global Settings, or review queues in
the first navigation; those depend on unsupported APIs.

### 2.2 Top Bar

- **Account input**: current account slug, persisted in the URL or local app
  state. There is no account-list endpoint yet.
- **Ref input**: default `main`; users may enter another known ref name or a
  commit id where a page supports it.
- **User menu**: show the logged-in subject id and a logout action that clears
  the local bearer token.

No global search box in the first version.

## 3. Page-by-Page Design

### 3.1 Login (`/login`)

The web MVP uses the current development login only:

```text
+--------------------------------------+
| Gitslice Dev Login                   |
| Dev user: [alice________________]    |
| Server:   [same-origin__________]    |
|                                      |
| [Login]                              |
+--------------------------------------+
```

Behavior:

- Calls `FakeAccountService.Login` through the web adapter with `dev_user`.
- Stores the returned bearer token in app state, with optional development-only
  session storage for reloads.
- Does not model refresh tokens, OAuth callbacks, device metadata, invitations,
  or token revocation.

### 3.2 Home (`/`)

The landing page is a supported-action launcher, not an activity dashboard.

```text
+------------------------------------------------+
| Logged in as user_alice                         |
| Account: [acme____________]                     |
|                                                |
| [Browse Source] [List Slices] [New Changeset]   |
|                                                |
| Open Changeset                                 |
| Changeset: [acme/payment@42________] [Open]    |
+------------------------------------------------+
```

Do not show "my changesets", pending reviews, recent activity, or check status
widgets until list/search/review APIs exist.

### 3.3 Source Browser (`/source/{account}/[...path]`)

Read-only source browsing by known ref or commit.

```text
+----------------------------------------------------------+
| / acme / payment / app.go       ref: [main________]      |
+----------------------+-----------------------------------+
| Directory            | File                              |
| app.go      file     | 1 package payment                 |
| README.md   file     | 2                                |
| api         dir      | 3 func App() string { ... }       |
+----------------------+-----------------------------------+
| Covering slices: acme/payment, acme/backend              |
+----------------------------------------------------------+
```

Supported behavior:

- Resolve `ref=main` through `RepositoryService.GetRef`, or accept an explicit
  `commit` query parameter.
- Use `RepositoryService.ResolvePath` to decide whether the path is a file or
  directory.
- Use `RepositoryService.ListDirectory` for directories. When browsing in the
  context of a selected slice, pass the optional `slice` projection so directory
  entries are filtered to that slice's `included_paths`.
- Use `RepositoryService.ReadFile` for file contents.
- Show only fields available on `TreeEntry`: name, path, kind, mode, size,
  content hash, blob id, tree id, and symlink target.
- Calculate covering slices client-side by calling `SliceService.ListSlices` for
  the current account and matching `included_paths`.

Not supported in this page:

- branch/tag dropdowns from server-side ref listing
- last commit author/message per directory entry
- blame
- search
- inline editing

### 3.4 Slice List (`/slices?account={account}`)

Lists slices for a known account slug through `SliceService.ListSlices`.

```text
+----------------------------------------------------------+
| Slices for acme                                          |
+----------------+------------+---------+------------------+
| Slice          | Visibility | Version | Included paths   |
| payment        | account    | 3       | /acme/payment    |
| backend        | account    | 2       | /acme/backend    |
+----------------+------------+---------+------------------+
```

Columns:

- slice slug
- visibility
- definition version
- definition hash
- included paths count and preview

No "new slice" action until a create-slice API exists.

### 3.5 Slice Detail (`/slices/{account}/{slice}`)

Shows the current slice definition and a source browser scoped by its included
paths.

```text
+----------------------------------------------------------+
| acme/payment                         visibility: account |
| version: 3                            hash: def_...      |
+----------------------------------------------------------+
| Included paths                                           |
| /acme/payment                                            |
| /acme/proto/payment                                      |
|                                                          |
| Git clone                                                |
| http://{git-http-host}/git/acme/payment.git              |
+----------------------------------------------------------+
```

Supported behavior:

- Load the slice with `SliceService.ResolveSlice` using the public
  `account/slice` route params. The internal `slice_...` id may be used in RPC
  payloads that require it, but must not be the browser URL identifier.
- Browse the selected slice through `RepositoryService.ListDirectory` with the
  optional `slice` projection, so custom slices show only included folders.
- The history drawer uses slice-scoped `RepositoryService.ListCommits` and
  `ChangesetService.ListChangesets`. For public slices, commit history and
  changeset associations are visible without authentication.
- Link each included path to the source browser.
- Show a Git clone URL only when the deployment config exposes the optional Git
  smart HTTP server. The current Git layer supports clone and fetch, not push.

Do not show roles, submit settings, reviewers, or check summaries; those require
APIs or fields that do not exist yet.

### 3.6 Slice Settings (`/slices/{account}/{slice}/settings`)

Allows direct edits to the currently supported slice definition fields:
visibility and included paths.

```text
+----------------------------------------------------------+
| Slice Settings: acme/payment                             |
| Current hash: def_...                                    |
|                                                          |
| Visibility                                               |
| ( ) private   (x) account   ( ) public                   |
|                                                          |
| Included paths                                           |
| /acme/payment                                      [x]   |
| /acme/proto/payment                                [x]   |
| [Add path]                                               |
|                                                          |
| [Save definition]                                        |
+----------------------------------------------------------+
```

Behavior:

- Calls `SliceService.UpdateSliceDefinition` with the current
  `expected_definition_hash`.
- Handles hash conflicts by reloading the slice and asking the user to retry.
- Validates path shape in the client, then relies on the server for authoritative
  validation.

Do not include display name, default branch, role editors, submit settings,
definition-history tables, delete, transfer, or reviewed-control-plane
changeset dialogs in this version.

### 3.7 Changeset Lookup (`/changesets`)

Because there is no changeset list endpoint, `/changesets` is a lookup page.

```text
+----------------------------------------------------------+
| Open Changeset                                           |
| Changeset: [acme/payment@42__________________] [Open]    |
|                                                          |
| [Create Changeset]                                       |
+----------------------------------------------------------+
```

No changeset table, filters, pagination, author picker, or search.

### 3.8 Create Changeset (`/changesets/new`)

Creates a draft changeset and initial patchset from explicit file edits.

```text
+----------------------------------------------------------+
| New Changeset                                            |
| Authoring slice: [acme/payment____________]              |
| Target ref:      [main____________________]              |
| Title:           [________________________]              |
| Description:     [________________________]              |
|                                                          |
| File edits                                               |
| /acme/payment/app.go        modify     [content...]      |
| /acme/payment/new.go        add        [content...]      |
| /acme/payment/old.go        delete                     |
|                                                          |
| [Validate] [Create Draft]                                |
+----------------------------------------------------------+
```

Supported flow:

1. Resolve the authoring slice with `SliceService.ResolveSlice`.
2. Resolve `target_ref` with `RepositoryService.GetRef` when `base_commit_id` is
   not explicitly supplied.
3. For add/modify/rename edits, upload pasted file content through
   `BlobService.UploadBlob`.
4. Call `ChangesetService.CreateChangeset`.
5. Call `ChangesetService.UpdateChangeset` with the uploaded file edits.
6. Navigate to `/changesets/{id}`.

The page may show a client-side diff preview while the pasted content is still in
the browser. Once the changeset is loaded later from the server, the public API
only guarantees file-edit metadata, not staged blob contents.

Do not include "request review" or reviewer selection.

### 3.9 Changeset Detail (`/cs/{id}`)

Shows the data returned by `ChangesetService.GetChangeset` and exposes supported
mutations to signed-in users. The page is publicly readable when the changeset's
authoring slice is public; anonymous users get a read-only detail and diff view.

```text
+----------------------------------------------------------+
| acme/payment@42  Fix payment app            status: draft |
| author: user_alice  slice: acme/payment  target: main    |
| base: cmt_abc                                             |
+----------------------------------------------------------+
| Patchsets                                                |
| PS1  base cmt_abc  author user_alice  created ...        |
|                                                          |
| File edits                                               |
| op      path                         blob/content hash    |
| modify  /acme/payment/app.go        blb_... sha256:...    |
|                                                          |
| Coverage                                                 |
| /acme/payment/app.go: slice_1, slice_2                   |
|                                                          |
| Submit requirements                                      |
| approvals: none returned                                 |
| checks: none returned                                    |
| path locks: none returned                                |
|                                                          |
| [Add Patchset] [Submit] [Abandon]                        |
+----------------------------------------------------------+
```

Supported behavior:

- Display handle, title, description, author, authoring slice, target ref, base
  commit, status, current patchset number, commit id, and pending publish id.
- Keep the primary title to one visible line on the review surface. Collapse
  secondary metadata and actions on mobile so the file list and diff begin high
  on the page.
- Keep canonical changeset and patchset ids available only in debug/details JSON,
  not as the primary visible label.
- Show the base changeset as a direct `Base changeset` link when the changeset
  is based on another changeset.
- Display each patchset's changed paths, file edits, coverage, path bases, read
  set, write set, and raw submit requirement ids.
- Display all patchsets as a compact horizontal timeline on the changeset detail
  page. The timeline should have patchset dots plus draggable `From` and `To`
  handles. `From = Recorded base` means the target patchset is compared against
  its stored materialization base.
- Allow comparing any two patchsets from the same changeset by calling
  `DiffChangeset` with `from_patchset` and `to_patchset`.
- On mobile, make the changed-file tree the primary diff view. The user can
  toggle diff bodies on or off, and selecting a file may reveal that file's
  diff.
- Poll `GetChangeset` while status is `pending_publish`.
- Call `SubmitChangeset` with `expected_current_patchset_id`.
- Call `AbandonChangeset` with a reason.
- Allow uploading another patchset with the same file-edit controls used by the
  create page.

Not supported:

- inline comments or general comments
- approve/request-changes buttons
- check-run status details
- rebase
- activity timeline beyond fields directly available on the changeset and
  patchsets

## 4. URL Structure

```text
/                                      Home
/login                                 Dev login
/source/{account}/[...path]            Source browser
/slices?account={account}              Slice list
/slices/{account}/{slice}              Slice detail
/slices/{account}/{slice}/settings     Slice settings
/changesets                            Changeset lookup
/changesets/new                        Create changeset
/changesets/{id}                       Changeset detail
```

Query parameters:

- Source browser: `?ref={known-ref}` or `?commit={commit-id}`.
- Slice list: `?account={account}`.
- Changeset detail: patchset focus is selected in-page through `Diff base` and
  `Target patchset`.

## 5. Component Tree

```text
<App>
  <DevAuthProvider>
    <Router>
      <Layout>
        <Sidebar>
          <NavItem to="/" label="Home" />
          <NavItem to="/source/:account" label="Source" />
          <NavItem to="/slices" label="Slices" />
          <NavItem to="/changesets" label="Changeset" />
        </Sidebar>
        <TopBar>
          <AccountInput />
          <RefInput />
          <UserMenu />
        </TopBar>
        <main>
          <Routes>
            <LoginPage />
            <HomePage />
            <SourcePage>
              <PathBreadcrumb />
              <RefOrCommitInput />
              <DirectoryView />
              <FileView />
              <CoveringSlicesList />
            </SourcePage>
            <SliceListPage />
            <SliceDetailPage>
              <SliceDefinitionSummary />
              <IncludedPathLinks />
              <GitCloneInfo />
            </SliceDetailPage>
            <SliceSettingsPage>
              <VisibilitySetting />
              <IncludedPathsEditor />
              <SaveDefinitionButton />
            </SliceSettingsPage>
            <ChangesetLookupPage />
            <CreateChangesetPage>
              <SliceRefInput />
              <TargetRefInput />
              <FileEditForm />
              <ClientSideDiffPreview />
            </CreateChangesetPage>
            <ChangesetDetailPage>
              <ChangesetHeader />
              <PatchsetTabs />
              <FileEditTable />
              <CoverageTable />
              <PathBaseTable />
              <SubmitRequirementIds />
              <ChangesetActionBar />
            </ChangesetDetailPage>
          </Routes>
        </main>
      </Layout>
    </Router>
  </DevAuthProvider>
</App>
```

## 6. Technology Stack

| Layer | Choice | Rationale |
|-------|--------|-----------|
| Framework | React 18 + TypeScript | Standard SPA stack for internal tooling |
| Bundler | Vite | Fast dev builds and simple static output |
| Routing | TanStack Router | Typed route and search-param handling |
| Server state | TanStack Query | Caching, mutation state, and polling publish status |
| Syntax highlighting | Shiki | Read-only source highlighting |
| HTTP transport | ConnectRPC over current gRPC services | Keeps the browser API generated from the public proto contract |
| CSS | Tailwind CSS | Fast, utilitarian styling for prototype UI |

Do not add a diff-review package, Monaco editor, OAuth client, WebSocket client,
or comment editor until the corresponding backend capabilities exist.

## 7. Data Flow

```text
Web UI (SPA)
  -> ConnectRPC over HTTP
    -> implemented Core service handlers
      -> PostgreSQL metadata
      -> filesystem object store
```

The web client must not call internal commit services or read the filesystem
object store directly. Browser-visible endpoints should correspond to public
service methods and should preserve the same authorization checks as the CLI.

## 8. Auth Flow

The first auth flow is development-only:

1. User enters a dev user such as `alice`.
2. The ConnectRPC HTTP API calls `FakeAccountService.Login`.
3. The app attaches the returned bearer token to subsequent requests.
4. Logout clears the local token.

The CLI signup approval page is also a development-only web surface. It is a
static browser page under `web/` that reads `username`, `callback_url`, and
`state` query parameters, calls
`FakeAccountService.ApproveSignup` through the generated Connect HTTP API, and
then redirects the browser to the returned loopback callback URL. The Go server
does not mount a bespoke signup HTTP handler.

Production OAuth, refresh tokens, session lists, token rotation, and service
account token management remain later work.

## 9. Polling

Use polling only for currently supported state:

- `ChangesetService.GetChangeset` while a submitted changeset is
  `pending_publish`
- explicit reloads for source, slice, and changeset pages after mutations

Do not poll for comments, review events, check runs, path locks, or account
activity because those resources are not implemented.

## 10. Web MVP Scope

Included:

- development login against `FakeAccountService.Login`
- source browser with directory and file views
- client-side covering-slice display based on `ListSlices`
- slice list for a known account
- slice detail with included paths, visibility, version, and definition hash
- direct slice definition update for visibility and included paths
- changeset lookup by id
- changeset creation from explicit uploaded file edits
- changeset detail with patchset metadata, coverage, path bases, and raw submit
  requirement ids
- changeset submit, abandon, and add-patchset actions
- optional display of Git clone URLs for deployments with Git HTTP enabled

Not in web MVP:

- OAuth or production account flows
- account, membership, service-account, session, or token management
- dashboards, activity feeds, pending-review queues, changeset list, or
  changeset search
- code search
- blame
- full persisted patchset diff rendering
- inline editing in the source browser
- review comments, approvals, reviewer assignment, or request-changes state
- check-run details or CI integration controls
- path lock management
- slice roles, submit settings, default branch, display name, delete, transfer,
  or audited history
- reviewed control-plane changesets
- workspace management UI
- repository migration tooling
- IDE plugin surfaces
