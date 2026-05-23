# Gitslice Web Interface Design

This document defines the web interface for Gitslice. The web UI is a
review-and-management console that sits on the same gRPC API as the CLI, via
grpc-gateway JSON endpoints. The CLI remains the primary workspace/edit surface;
the web excels at code browsing, changeset review, slice administration, and
account management.

Related documents:

- [00_product.md](00_product.md): product overview and primary workflows
- [01_gitslice_architecture_design.md](01_gitslice_architecture_design.md): top-level architecture
- [03_core_api.md](03_core_api.md): gRPC APIs used by the web client
- [04_cli_design.md](04_cli_design.md): native CLI design (edit surface)

## 1. Positioning

The web interface is a later client of the same account, auth, changeset,
submit, and storage APIs used by the CLI. It is not a replacement for `gs`; it
is a complementary surface for:

- Browsing the global source graph
- Reviewing changesets with inline diffs and comments
- Managing slices, roles, visibility, and submit settings
- Managing accounts, memberships, service accounts, and tokens
- Monitoring submit status, checks, and conflicts

The web UI should not be the primary tool for creating or editing file content.
That remains the CLI's job. The web can create changesets from uploaded diffs
or small inline edits, but local workspaces and the edit-build-test cycle belong
to `gs`.

## 2. Navigation Architecture

The layout uses a persistent sidebar and top bar:

```text
┌──────────────────────────────────────────────┐
│  TopBar: [search bar]        [user menu v]   │
├────────┬─────────────────────────────────────┤
│ Sidebar│                                     │
│        │        Page Content Area            │
│  ──────│                                     │
│  Dashboard                                   │
│  ──────│                                     │
│  Source│                                     │
│  Browser                                     │
│  ──────│                                     │
│  Changesets                                  │
│  ──────│                                     │
│  Slices │                                     │
│  ──────│                                     │
│  Path   │                                     │
│  Locks  │                                     │
│  ──────│                                     │
│ Settings│                                     │
└────────┴─────────────────────────────────────┘
```

When the user navigates into a slice context, the sidebar shows a breadcrumb and
contextual sub-navigation: Overview, Code, Changesets, Settings.

### 2.1 Sidebar

- **Account switcher** at the top for users with multiple account memberships.
- **Nav items**: Dashboard, Source Browser, Changesets, Slices, Path Locks,
  Settings.
- **Quick slice list**: recently viewed or pinned slices for fast access.
- **Contextual sub-nav**: when inside a slice, replace main nav with
  `{account}/{slice}` breadcrumb and Overview / Code / Changesets / Settings
  links.

### 2.2 Top Bar

- **Global search**: search changesets by title, author, or affected path.
  Full-text search over the source tree is not in MVP scope.
- **User menu**: profile link, account settings, logout.

## 3. Page-by-Page Design

### 3.1 Dashboard (`/`)

The landing page after login. Shows a summary of the user's work across all
accounts they belong to.

```text
┌──────────────────────────────────────────────────┐
│  Welcome back, nicholas                    [acme v]│
├──────────────────────┬───────────────────────────┤
│                      │                           │
│  My Changesets       │  Pending Reviews          │
│  ┌──────────────────┐│  ┌───────────────────────┐│
│  │ #42 Fix auth      ││  │ #38 from alice       ││
│  │  Review . acme/.. ││  │  Needs your approval  ││
│  │ #39 Add API       ││  │ #35 from bot-agent   ││
│  │  Draft . nicho../ ││  │  Checks failing       ││
│  └──────────────────┘│  └───────────────────────┘│
│                      │                           │
│  Recent Activity     │  Your Slices              │
│  ┌──────────────────┐│  ┌───────────────────────┐│
│  │ alice submitted   ││  │ acme/payment    public││
│  │   #38 to acme/.. ││  │ acme/backend  account ││
│  │ #41 opened by bot ││  │ nicholas/identity ..  ││
│  │ #40 checks passed ││  │ [+ new slice]         ││
│  └──────────────────┘│  └───────────────────────┘│
└──────────────────────┴───────────────────────────┘
```

Widgets:

- **My Changesets**: open changesets authored by the current user across all
  slices. Shows changeset number, title, status badge, and authoring slice.
- **Pending Reviews**: changesets where the user is a required approver and has
  not yet approved. Shows status of checks.
- **Recent Activity**: chronological feed of submits, new changesets, check
  results, and review requests across the user's accounts.
- **Your Slices**: quick list of slices the user has write or admin access to,
  with a "New Slice" button.

Each widget row links to the relevant detail page.

### 3.2 Source Browser (`/source/{account}/[...path]`)

The global file explorer. Navigates the canonical path tree rooted at account
slugs. Supports directory listing, file viewing, blame, and covering-slice
visibility.

```text
┌──────────────────────────────────────────────────┐
│  / acme / payment / api / handler.go              │
│                                [ref: main v] [🔍] │
├──────────────┬───────────────────────────────────┤
│ Tree         │  1 │ package api                   │
│ ┌──────────┐ │  2 │                               │
│ │ payment/  │ │  3 │ import (                      │
│ │ ├─ api/   │ │  4 │   "context"                   │
│ │ │  ├─ ha..│ │  5 │   "net/http"                  │
│ │ │  └─ mi..│ │  6 │ )                             │
│ │ ├─ proto/ │ │  7 │                               │
│ │ └─ READ.. │ │  8 │ func Handler(w http.Respon.  │
│ └──────────┘ │    │ ...                            │
│              │                                   │
│  Covering    │                                   │
│  Slices:     │                                   │
│   acme/back..│                                   │
│   acme/pay..│                                   │
│   [Blame]    │                                   │
└──────────────┴───────────────────────────────────┘
```

**Directory view**: sortable table with columns for name, kind (file, directory,
symlink), mode, size, last commit message, and last commit author. Entries link
into deeper paths.

**File view**: syntax-highlighted content via Shiki, with a line-number gutter.
Read-only by default.

**Ref selector**: dropdown to pick a branch, tag, or commit SHA. The view
resolves paths against that ref.

**Blamable sidebar**: toggle-able panel that shows the commit and author for
each line.

**Covering slices badge**: lists every slice whose `included_paths` cover the
current path. Each slice name links to its detail page.

The source browser is read-only. Users are directed to the CLI for edits.

### 3.3 Changeset List (`/changesets`)

A filterable, sortable table of changesets.

```text
┌──────────────────────────────────────────────────┐
│  Changesets                                      │
│  [Slice: all v] [Status: open v] [Author: ..v]  │
│  [Search by title or path...          ] [🔍]     │
├──────────────────────────────────────────────────┤
│  ID   │ Title            │ Slice       │ Status  │
│  #42  │ Fix auth token.. │ acme/payment │ Review │
│  #41  │ Add health check │ acme/backend │ Draft  │
│  #39  │ Update proto     │ nicholas/id..│ Submit.│
│  #38  │ Refactor handler │ acme/payment │ Submit.│
│       │                  │             │        │
│  Showing 4 of 12              [← 1 2 3 →]       │
└──────────────────────────────────────────────────┘
```

Columns:

- ID (linked to detail page)
- Title
- Authoring slice
- Author
- Status badge
- Updated timestamp

Filters:

- **Slice**: dropdown scoped to slices the user can see.
- **Status**: multi-select checkboxes for Draft, Review, Pending Publish,
  Submitted, Abandoned, Failed, Needs Rebase, Merge Conflict, Needs Requirement
  Refresh.
- **Author**: free-text or user picker.
- **Search**: free-text match against title and affected paths.

Status badges are color-coded: Draft (gray), Review (blue), Pending Publish
(yellow), Submitted (green), Abandoned (red), Failed (red), Needs Rebase
(orange), Merge Conflict (red), Needs Requirement Refresh (orange).

The default view shows open changesets (not Submitted or Abandoned) for the
currently selected account, ordered by most recently updated.

### 3.4 Changeset Detail / Code Review (`/changesets/{id}`)

The primary code review surface. Shows everything about a changeset: metadata,
diffs, approvals, checks, coverage, and activity.

```text
┌─────────────────────────────────────────────────────────────────┐
│  #42 Fix auth token refresh      [Review] [Rebase] [Abandon]    │
│  Author: nicholas . Slice: acme/payment . Target: main          │
│  Base: a1b2c3d . Created: 2h ago . Updated: 10m ago             │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  Description:                                                    │
│  │ The token refresh was failing because the grant type was...  │
│                                                                 │
│  ┌─ Patchsets ────┬──────────────┬──────────────────────────┐  │
│  │ PS1  PS2 [PS3] │ 8 files      │ Diff view: [Unified|Split]│  │
│  │                │              │                           │  │
│  │ Files changed  │ ┌ diff ────┐│  ┌────────────────────────┐│  │
│  │ ┌────────────┐ │ │- old line ││  │ Submit Requirements    ││  │
│  │ │ handler.go │ │ │+ new line ││  │ ✓ payment-owner (bob)  ││  │
│  │ │ auth.go    │ │ │+ new line ││  │ ✓ payment-ci           ││  │
│  │ │ auth_test. │ │ │          ││  │                        ││  │
│  │ │ go.mod    │  │ │          ││  │ Covering Slices        ││  │
│  │ │ go.sum    │  │ │          ││  │ ┌────────────────────┐ ││  │
│  │ └────────────┘ │ └───────────┘│  │ │ acme/backend      │ ││  │
│  │                │              │  │ │ acme/payment (auth)│ ││  │
│  └────────────────┴──────────────│  │ └────────────────────┘ ││  │
│                                  │  └────────────────────────┘│  │
│  ┌─ Activity ────────────────────┤                           │  │
│  │ nicholas  created #42         │                           │  │
│  │ nicholas  uploaded PS1        │                           │  │
│  │ alice     approved PS2        │                           │  │
│  │ bot-ci    payment-ci: passed  │                           │  │
│  │ nicholas  uploaded PS3        │                           │  │
│  │                              │                           │  │
│  │ [_________________comment__] │                           │  │
│  └──────────────────────────────┘                           │  │
└─────────────────────────────────────────────────────────────────┘
```

#### 3.4.1 Header

Shows changeset number, title, status badge, and action buttons (Submit, Rebase,
Abandon — context-sensitive based on current status and user permissions).
Below: author, authoring slice, target ref, base commit, creation and update
timestamps. The description block renders markdown.

#### 3.4.2 Patchset Tabs

Horizontal tabs to switch between patchset versions (PS1, PS2, PS3, ...). The
current patchset is highlighted. Tabs show an icon if the patchset has approvals
or completed checks. Switching patchsets reloads the diff view, changed files
tree, and approval/check annotations for that version.

#### 3.4.3 Changed Files Tree

Collapsible tree of changed files with +/- line count indicators. Clicking a
file scrolls the diff viewer to that file. Files are shown with canonical
absolute paths (e.g. `/acme/payment/api/handler.go`).

#### 3.4.4 Diff Viewer

Full-width unified or side-by-side diff view of the selected patchset.
Syntax-highlighted where applicable.

Inline commenting: click a line number (or drag-select a range) in the diff to
open an inline comment form. Comments are threaded — replies nest under the
original comment. Each comment thread shows author, timestamp, and resolved
state. Resolved threads collapse but remain visible.

#### 3.4.5 Submit Requirements Panel

Right sidebar panel showing:

- **Required approvals**: list of teams or individuals who must approve. Each
  shows status (pending, approved, waived). An approval is recorded against a
  specific patchset id and slice definition hash.
- **Required checks**: list of CI checks with status (pending, running, passed,
  failed). Each links to the check run details.
- **Admin override**: whether the authoring slice allows admin overrides.
- **Active path locks**: any path locks intersecting the changed paths.

This panel updates when the user switches patchset tabs, since approvals and
checks are tied to specific patchsets.

#### 3.4.6 Covering Slices Panel

Right sidebar panel (below submit requirements) showing each changed path and
its covering slices. The authoring slice is highlighted. Other covering slices
are listed for visibility and overlap awareness but do not add approval
requirements.

#### 3.4.7 Activity Timeline

Chronological log of every event on this changeset:

- Created
- Patchset uploaded
- Approval granted / revoked
- Check started / passed / failed
- Status transitions (Draft → Review, Review → Submitted, etc.)
- Comments added

Each entry shows actor, timestamp, and a brief description.

#### 3.4.8 General Comments

Thread at the bottom of the page for discussion not tied to a specific diff
line. Markdown input with preview. Comments are dated and attributed.

#### 3.4.9 Action Buttons

Context-sensitive actions shown in the header:

- **Approve**: record an approval against the current patchset. Available to
  users listed in required approvals.
- **Request Changes**: record a changes-requested review. Available to required
  approvers.
- **Submit**: trigger `SubmitChangeset`. Available when the user has writer
  access and all requirements are met (or admin override is available).
- **Abandon**: close the changeset without submitting. Requires writer access.
- **Rebase**: update the base commit. Creates a new patchset. Requires writer
  access.

### 3.5 Create Changeset (`/changesets/new`)

A form for creating a changeset from the web. Not the primary edit path (the
CLI is), but available for small changes or control-plane changes.

```text
┌──────────────────────────────────────────────────┐
│  New Changeset                                   │
│                                                  │
│  Authoring Slice: [acme/payment           v]     │
│  Target Ref:      [main                   v]     │
│  Title:           [________________________]     │
│  Description:                                    │
│  ┌────────────────────────────────────────────┐  │
│  │                                            │  │
│  └────────────────────────────────────────────┘  │
│                                                  │
│  File Edits:                                     │
│  ┌────────────────────────────────────────────┐  │
│  │ [+ Add file edit]                          │  │
│  │ /acme/payment/api/handler.go  [modify ✕]   │  │
│  │ /acme/payment/api/auth.go     [add    ✕]   │  │
│  └────────────────────────────────────────────┘  │
│                                                  │
│  Diff Preview:                                   │
│  ┌────────────────────────────────────────────┐  │
│  │ [rendered diff of selected file edits]     │  │
│  └────────────────────────────────────────────┘  │
│                                                  │
│  ⚠ Every path must be included by acme/payment   │
│                                                  │
│  [Create as Draft]  [Create and Request Review]  │
└──────────────────────────────────────────────────┘
```

**Slice picker**: dropdown of slices where the user has writer access.

**Target ref picker**: dropdown of accepted refs for the selected slice.

**File edit uploader**: users add file edits by specifying the canonical path,
operation (add, modify, delete, rename), and pasting/uploading file content.
The web validates that every changed path is included by the authoring slice
before allowing creation.

**Diff preview**: renders the proposed diff before submission. Shows covering
slices per changed path.

The changeset is created in Draft status. The user can then request review or
continue editing via `gs cs update` from the CLI.

### 3.6 Slice Detail (`/slices/{id}`)

The landing page for a specific slice. Shows overview, code browser, changeset
list, and settings as tabs.

```text
┌─────────────────────────────────────────────────────────────────┐
│  acme/payment                                    [Settings ⚙]   │
│  org.acme/payment . Public . 3 included paths                  │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  [Overview] [Code] [Changesets] [Settings]                       │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  Included Paths:                                                 │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │ /acme/payment                                            │   │
│  │ /acme/proto/payment                                      │   │
│  │ /acme/README.md                                          │   │
│  └──────────────────────────────────────────────────────────┘   │
│                                                                 │
│  Clone:  git clone https://gitslice.io/git/acme/payment.git      │
│                                                                 │
│  Recent Changesets:                                              │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │ #42  Fix auth token refresh          Submitted . 2h ago  │   │
│  │ #38  Refactor handler                Review . 1d ago     │   │
│  │ #35  Add payment endpoint            Submitted . 3d ago  │   │
│  └──────────────────────────────────────────────────────────┘   │
│                                                                 │
│  Roles Summary:                                                  │
│  │ Owner: nicholas                                              │
│  │ Admins: alice                                                │
│  │ Writers: bob, payment-team                                   │
│  │ Readers: everyone-in-acme                                    │
│                                                                 │
│  Submit Settings:                                                │
│  │ Required Approvals: payment-owners                           │
│  │ Required Checks: payment-ci, payment-lint                    │
│  │ Admin Override: allowed                                      │
└─────────────────────────────────────────────────────────────────┘
```

**Overview tab**: included paths, clone URL with copy button, recent changesets
list, roles summary, submit settings summary.

**Code tab**: embedded source browser scoped to this slice's included paths.
The tree view shows only the paths under `included_paths`. Same behavior as the
global source browser but filtered.

**Changesets tab**: changeset list pre-filtered to this slice.

**Settings tab**: full slice administration panel (see 3.7).

### 3.7 Slice Settings (`/slices/{id}/settings`)

Administration panel for slice owners and admins. All definition changes that
require review (visibility, included paths, submit settings) create a
control-plane changeset rather than applying immediately.

```text
┌─────────────────────────────────────────────────────────────────┐
│  Slice Settings: acme/payment                                   │
│                                                                 │
│  ┌─ General ─────────────────────────────────────────────────┐  │
│  │  Display Name:  [Payment Service          ]               │  │
│  │  Default Branch: [main v]                                  │  │
│  └───────────────────────────────────────────────────────────┘  │
│                                                                 │
│  ┌─ Visibility ──────────────────────────────────────────────┐  │
│  │  o Private   o Account-visible   . Public                 │  │
│  │  ! 3 paths are exposed by overlapping public slices       │  │
│  └───────────────────────────────────────────────────────────┘  │
│                                                                 │
│  ┌─ Included Paths ──────────────────────────────────────────┐  │
│  │  ┌─────────────────────────┐  ┌──────────────────────┐    │  │
│  │  │ /acme/payment       [✕] │  │ Path tree picker     │    │  │
│  │  │ /acme/proto/payment  [✕] │  │ /acme/              │    │  │
│  │  │ /acme/README.md      [✕] │  │  ├─ payment/   [✓]   │    │  │
│  │  │ [+ Add Path]            │  │  ├─ proto/           │    │  │
│  │  └─────────────────────────┘  │  │  └─ ...           │    │  │
│  │                                │  └──────────────────────┘   │  │
│  │  ! Changing included paths requires a reviewed changeset     │  │
│  └───────────────────────────────────────────────────────────┘  │
│                                                                 │
│  ┌─ Roles ───────────────────────────────────────────────────┐  │
│  │  Owner:  nicholas                                         │  │
│  │  Admins: [nicholas] [alice  ✕] [ + ]                     │  │
│  │  Writers: [bob ✕] [payment-team v] [ + ]                │  │
│  │  Readers: [everyone-in-acme v] [ + ]                     │  │
│  └───────────────────────────────────────────────────────────┘  │
│                                                                 │
│  ┌─ Submit Settings ─────────────────────────────────────────┐  │
│  │  Required Approvals:                                      │  │
│  │    [team: payment-owners ✕] [+ Add]                        │  │
│  │  Required Checks:                                         │  │
│  │    [payment-ci ✕] [payment-lint ✕] [+ Add]               │  │
│  │  ☑ Allow admin override                                   │  │
│  │  ! Changes require a reviewed control-plane changeset     │  │
│  └───────────────────────────────────────────────────────────┘  │
│                                                                 │
│  ┌─ Definition History ──────────────────────────────────────┐  │
│  │  v3  2026-05-20  Added /acme/README.md   nicholas        │  │
│  │  v2  2026-05-15  Added payment-lint check  alice         │  │
│  │  v1  2026-05-01  Initial definition       nicholas       │  │
│  └───────────────────────────────────────────────────────────┘  │
│                                                                 │
│  ┌─ Danger Zone ─────────────────────────────────────────────┐  │
│  │  [Delete this slice]  [Transfer ownership]                │  │
│  └───────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────┘
```

**General**: display name, default branch.

**Visibility**: radio buttons for private, account-visible, public. When
changing to public, the UI shows a warning listing any overlapping private
slices and the paths they share. Changing visibility creates a reviewed
changeset.

**Included Paths**: list of current paths with remove buttons. "Add Path" opens
a path tree picker. Added/removed paths are staged as a control-plane changeset
for review.

**Roles**: owner (read-only display), admins, writers, readers. Each is an
editable list of subject IDs, teams, or account-level groups. Changes go
through a reviewed changeset.

**Submit Settings**: required approvals (teams or individuals), required checks
(CI check names), admin override toggle. Changes go through a reviewed
changeset.

**Definition History**: list of all accepted slice definition versions with
version number, date, description of change, and author.

Changeset-gated changes show a warning banner: "This change requires a reviewed
control-plane changeset." Clicking "Save" opens a "Create Slice Definition
Changeset" dialog that pre-fills the changeset with the proposed definition
diff.

### 3.8 Account Settings (`/accounts/{account}/settings`)

Account-level administration for account owners and admins.

```text
┌─────────────────────────────────────────────────────────────────┐
│  Account Settings: acme                                         │
│                                                                 │
│  [Profile] [Members] [Service Accounts] [Sessions & Tokens]     │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  Members:                                                       │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │ nicholas   owner    [✕] [v change role]                   │   │
│  │ alice      admin    [✕] [v change role]                   │   │
│  │ bob        member   [✕] [v change role]                   │   │
│  │ ci-bot     guest    [✕] [v change role]                   │   │
│  │                     [ + Invite member ]                    │   │
│  └──────────────────────────────────────────────────────────┘   │
│                                                                 │
│  Account roles: owner, admin, member, guest                     │
└─────────────────────────────────────────────────────────────────┘
```

**Profile tab**: display name, account slug (read-only after creation), account
kind.

**Members tab**: list of account members with role badges. Owner can change
roles and remove members. Invite button for adding new members.

**Service Accounts tab**: create, list, and manage service accounts for CI and
automation. Each service account has scoped tokens. Tokens can be revoked
individually without deleting the service account.

**Sessions & Tokens tab**: list of active sessions and refresh tokens for the
current user. Each can be revoked individually. Shows device info, IP, issued
at, and expires at.

### 3.9 Path Locks (`/locks`)

Simple management page for high-risk path locks.

```text
┌─────────────────────────────────────────────────────────────────┐
│  Path Locks                                                     │
│  [ + New Lock ]                                                 │
├─────────────────────────────────────────────────────────────────┤
│  Path                              │ Owner     │ Created │      │
│  /acme/infra/prod                  │ nicholas  │ 2d ago  │ [✕]  │
│  /acme/releases/2026-Q2.yaml      │ nicholas  │ 5d ago  │ [✕]  │
└─────────────────────────────────────────────────────────────────┘
```

**New Lock dialog**: path input (validated against account namespace), reason
textarea.

**Lock list**: path, owner, creation date, release button. Releasing a lock
requires the lock owner or admin permission.

## 4. URL Structure

```text
/                                          Dashboard
/login                                     Login / OAuth callback
/source/{account}/[...path]?ref={ref}      Source browser
/changesets                                Changeset list
/changesets/{id}                           Changeset detail / review
/changesets/new                            Create changeset
/slices                                    Slice list
/slices/{id}                               Slice detail
/slices/{id}/settings                      Slice settings
/accounts/{account}                        Account detail
/accounts/{account}/settings               Account settings
/locks                                     Path locks
```

Query parameters:

- Source browser: `?ref={commit|branch|tag}` selects the ref for path resolution.
- Changeset list: `?slice={id}`, `?status={status}`, `?author={id}`, `?q={search}`.
- Changeset detail: `?patchset={number}` selects the patchset to display.

## 5. Component Tree

```text
<App>
  <AuthProvider>
    <Router>
      <Layout>
        <Sidebar>
          <AccountSwitcher />
          <NavSection label="Main">
            <NavItem to="/" icon={Home} label="Dashboard" />
            <NavItem to="/source" icon={FolderTree} label="Source" />
            <NavItem to="/changesets" icon={GitPullRequest} label="Changesets" />
            <NavItem to="/slices" icon={Layers} label="Slices" />
            <NavItem to="/locks" icon={Lock} label="Path Locks" />
          </NavSection>
          <SliceQuickNav />
        </Sidebar>
        <TopBar>
          <GlobalSearch />
          <UserMenu />
        </TopBar>
        <main>
          <Routes>
            <DashboardPage />
            <SourcePage>
              <PathBreadcrumb />
              <RefSelector />
              <DirectoryView /> | <FileView />
              <CoveringSlicesBadge />
              <BlamePanel />
            </SourcePage>
            <ChangesetListPage />
            <ChangesetDetailPage>
              <ChangesetHeader />
              <PatchsetTabs />
              <ChangedFileTree />
              <DiffViewer />
                <DiffLine />
                <InlineCommentThread />
              <SubmitRequirementsPanel />
              <CoveringSlicesPanel />
              <ActivityTimeline />
              <GeneralComments />
              <ActionBar />
            </ChangesetDetailPage>
            <CreateChangesetPage>
              <SlicePicker />
              <TargetRefPicker />
              <FileEditUploader />
              <DiffPreview />
            </CreateChangesetPage>
            <SliceListPage />
            <SliceDetailPage>
              <SliceOverview />
              <SliceSourceBrowser />
              <SliceChangesetList />
            </SliceDetailPage>
            <SliceSettingsPage>
              <VisibilitySetting />
              <IncludedPathsEditor />
              <RolesEditor />
              <SubmitSettingsEditor />
              <DefinitionHistory />
            </SliceSettingsPage>
            <AccountSettingsPage>
              <MembershipList />
              <ServiceAccountList />
              <TokenManager />
              <SessionList />
            </AccountSettingsPage>
          </Routes>
        </main>
      </Layout>
    </Router>
  </AuthProvider>
</App>
```

## 6. Technology Stack

| Layer | Choice | Rationale |
|-------|--------|-----------|
| Framework | React 18 + TypeScript | Large ecosystem for diff/code-review UIs |
| Bundler | Vite | Fast dev builds, good SPA support |
| Routing | TanStack Router | Type-safe routing with search params |
| Server state | TanStack Query | Cache invalidation, polling for changeset status |
| Diff viewer | diff2html or Monaco diff editor | Side-by-side and unified, inline comments |
| Syntax highlighting | Shiki | Accurate highlighting, WASM, many languages |
| File tree | react-arborist | Handles deep trees, lightweight |
| Auth | OAuth2 PKCE + grpc-gateway interceptor | Same token model as CLI |
| HTTP client | @connectrpc/connect-web or raw fetch | Type-safe if using Connect |
| CSS | Tailwind CSS | Utility-first, fast development |

## 7. Data Flow

```text
Web UI (SPA)
  -> HTTP/JSON (grpc-gateway)
    -> gRPC Core Services
      -> PostgreSQL (metadata)
      -> Object Store (files)
```

The web app calls the same gRPC services as the CLI but through grpc-gateway
JSON endpoints. The service boundaries remain identical:

- `RepositoryService` for path resolution, directory listing, file reads
- `SliceService` for slice resolution, listing, and definition updates
- `ChangesetService` for create, read, update, submit, and abandon
- `WorkspaceService` for backend hydration helpers (limited web use)

The web client should not call internal commit services. Those remain behind the
trusted service boundary.

## 8. Auth Flow

The web UI uses OAuth2 with PKCE for SPAs:

1. User visits `/login`, redirected to the identity provider.
2. Identity provider authenticates the user and redirects back with an
   authorization code.
3. The SPA exchanges the code for access and refresh tokens.
4. Access tokens are stored in memory (not localStorage) and attached to every
   API request via `Authorization: Bearer` header.
5. Refresh tokens are stored in a secure, HTTP-only cookie (or handled via
   token rotation with the backend).
6. The `AuthProvider` component manages token lifecycle and exposes the current
   subject, account memberships, and scopes to the rest of the app.

The auth model should be the same as the CLI's model described in the product
doc (section 4): subject_id, subject_type, session_id, account_memberships,
scopes, issued_at, expires_at.

## 9. Real-Time Updates

The MVP should use polling for changeset status updates, new comments, and
check results. TanStack Query's `refetchInterval` handles this cleanly.

Later, WebSocket or server-sent events can push changeset events to connected
clients for instant review updates.

## 10. Web MVP Scope

Included:

- Dashboard with user activity and pending reviews
- Global source browser with directory/file view, blame, and covering slices
- Changeset list with filtering and search
- Changeset detail with inline diff review, comments, approvals, and checks
- Create changeset from uploaded file edits
- Slice detail with code browser, changeset list, and clone URL
- Slice settings (visibility, included paths, roles, submit settings)
- Account settings (profile, members, service accounts, sessions)
- Path lock management
- OAuth2 PKCE auth flow

Not in web MVP:

- Code search (not in product MVP scope)
- Inline file editor in source browser (edits belong to the CLI)
- Workspace management UI (workspaces are local, CLI-only)
- Repository migration tooling (later, separate UI surface)
- Organization analytics dashboards (later)
- IDE plugin surfaces (later)
