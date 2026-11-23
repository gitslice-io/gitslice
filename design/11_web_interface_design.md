# 11. Gitslice Web Interface Design

This document describes the production web application in `web/`. The CLI (`gs`)
remains the primary workspace and edit surface; the web UI provides source
browsing, rich file rendering, changeset review and diffing, slice and account
administration, agent claim/conversation flows, and documentation/blog hosting.

This document supersedes the abandoned `14_web_style_guide.md` by folding the
visual design system directly into Section 4.

Related documents:

- [01_gitslice_architecture_design.md](01_gitslice_architecture_design.md): top-level architecture
- [03_core_api.md](03_core_api.md): Core gRPC / ConnectRPC API
- [12_account_auth.md](12_account_auth.md): Accounts, Clerk auth, CLI login, claims, and authorization
- [16_bring_your_own_agent.md](16_bring_your_own_agent.md): Agent daemon and conversations
- [18_analytics.md](18_analytics.md): PostHog analytics and `/ingest/*` proxy

## 1. Architecture & Technology Stack

| Layer | Choice | Notes |
|-------|--------|-------|
| SSR & Edge Runtime | TanStack Start + Nitro on Cloudflare Workers | Configured via `web/wrangler.jsonc` (`compatibility_flags: ["nodejs_compat"]`, static `ASSETS` binding, `RELEASES` R2 bucket binding) |
| Framework | React 18 + TypeScript | Strict TypeScript build (`tsc -b && vite build`) |
| Bundler | Vite 7 | Route-level code splitting via `lazyRouteComponent` |
| Routing | TanStack Router (`web/src/routes/router.tsx`) | Typed routes, validated search params, SSR loaders, intent preloading, scroll restoration |
| Server State | TanStack Query | SSR dehydration/hydration, caching, infinite queries, polling |
| RPC Transport | ConnectRPC (`@connectrpc/connect-web`, `@bufbuild/protobuf`) | Generated TypeScript descriptors from `proto/core/v1/*.proto` talking to the Go server's Connect HTTP API (`PUBLIC_API_BASE_URL`) |
| Authentication | Clerk (`@clerk/tanstack-react-start`, `@clerk/clerk-react`) | SSR session inspection + client auth provider; bearer token attached to ConnectRPC calls |
| Styling | Tailwind CSS + `@tailwindcss/typography` | Class-based dark mode (`darkMode: "class"`), Geist + Geist Mono typography |
| Syntax & Rich Viewers | Shiki, `marked` + `dompurify`, `mermaid`, `@viz-js/viz`, `hyparquet`, `read-excel-file` | Plus server-rendered Markdown via `RepositoryService.RenderFile` |

### 1.1 Worker Entry & Edge Responsibilities (`web/src/server.ts`)

Before invoking the TanStack Start SSR handler, the Cloudflare Worker entry
handles edge routes directly:

1. **First-party analytics proxy (`/ingest/*`)** via `handleIngestProxy`
   (`web/src/analytics/ingestProxy.ts`) — forwards to PostHog without app
   cookies (see [18_analytics.md](18_analytics.md)).
2. **LLM docs aliases** (`llmsAliasResponse`).
3. **`go get` vanity import discovery** (`goImportResponse`,
   `sliceGoImportResponse`) backed by the API slice lookup.
4. **Release binaries** (`releaseResponse`) served from the `RELEASES` R2 bucket
   under the `releases/` prefix.
5. **Social link-preview images and account avatars** (`socialImageResponse`)
   proxied from the API and cached at the edge.
6. **Git Smart HTTP proxy** (`gitProxyTarget`) forwarding `/git/...` requests to
   the Go server.
7. **Edge page cache for anonymous visitors** (`servePage` in
   `web/src/lib/pageCache.ts`) — caches unauthenticated SSR HTML in the Workers
   edge cache keyed by build ID; authenticated requests always render fresh.

### 1.2 Data Flow

```text
Browser / Cloudflare Worker SSR
  -> ConnectRPC over HTTPS (with Clerk JWT or anonymous for public slices)
    -> Go Core API (server/ + service/)
      -> PostgreSQL metadata + Object store
```

Route loaders prefetch queries on the server during SSR via
`createServerApiClient()`, dehydrate TanStack Query state into the HTML payload,
and hydrate seamlessly on the client. All pages are lazy-loaded with
`lazyRouteComponent` so a visitor reading a slice does not download docs, blog,
diff viewer, or settings bundles.

## 2. Layout & Navigation Architecture

The app uses a **single-column top-bar shell** with **no persistent sidebar**:

```text
+----------------------------------------------------------------------+
| TopBar: BrandMark  Home | Doc | Blog   [AccountSwitcher] [Theme] [U] |
+----------------------------------------------------------------------+
| Optional Banner (e.g. VisitorBanner, ClaimsNotice)                   |
+----------------------------------------------------------------------+
| <main> (max-w-[100rem] centered content area)                        |
|   PageHeader (Breadcrumb + Title + Actions)                          |
|   Page Content                                                       |
+----------------------------------------------------------------------+
```

- **`AppShell.tsx`** (`web/src/components/AppShell.tsx`): Pure layout wrapper
  rendering `<TopBar />`, an optional banner slot, and `<main>` with responsive
  padding (`px-2 py-6 sm:px-4 md:px-8 md:py-8`) over `bg-slate-50` /
  `dark:bg-zinc-950`.
- **`TopBar.tsx`** (`web/src/components/TopBar.tsx`): Sticky/translucent header
  (`border-b border-slate-200 dark:border-zinc-800`) containing:
  - `BrandMark` + Gitslice home link (`useHomeTarget`).
  - Primary nav items: **Home**, **Doc**, **Blog** (plus **Conversations** when
    `conversationsEnabled` feature flag is on).
  - Right controls: `AccountSwitcher` (persisted via `localStorage` and URL
    context), `ClaimsNotice` badge when pending agent claims exist,
    `ThemeToggle`, and Clerk's `<UserButton />` (or Sign In / Sign Up links for
    visitors). If a visitor page without Clerk UI later detects an active
    session from another tab, `reloadForClerkUi()` reloads cleanly.
- **Route layouts (`web/src/routes/layouts.tsx`)**:
  - `rootRoute` (`web/src/routes/root.tsx`): HTML shell, Clerk provider,
    `PostHogProvider`, QueryClient provider, and global error/not-found
    handling.
  - `appRoute`: Authenticated shell requiring sign-in (and username onboarding
    when needed).
  - `publicAppRoute`: Publicly accessible shell that renders for both signed-in
    users and anonymous visitors (showing `VisitorBanner` where appropriate).

## 3. Route Table (`web/src/routes/router.tsx`)

| Path | Parent Layout | Access | Page Component | Purpose |
|------|---------------|--------|----------------|---------|
| `/` | `rootRoute` | Public / Auth | `IndexPage` | Marketing landing page for visitors; authenticated home / onboarding entry for signed-in users (SSR prefetches `authStatusQuery`) |
| `/login`, `/login/$` | `rootRoute` | Public | `LoginPage` | Clerk sign-in flow |
| `/signup`, `/signup/$` | `rootRoute` | Public | `SignupPage` | Clerk sign-up flow |
| `/cli-login` | `rootRoute` | Auth | `CliLoginPage` | Device-code CLI login approval (`CompleteCliLogin`, see [12_account_auth.md](12_account_auth.md)) |
| `/claims` | `appRoute` | Auth | `ClaimsPage` | Pending & completed agent ownership claims (`ListPendingAgentClaims`, `ClaimAgent`) |
| `/conversations` | `appRoute` | Auth (feature-gated) | `ConversationsPage` | Cross-slice agent conversations list (redirects to `/` when `conversationsEnabled` is false) |
| `/slices` | `appRoute` | Auth | `HomePage` | Account slices, shared slices, owned agents, and agent daemons dashboard |
| `/slices/new` | `appRoute` | Auth | `SliceCreatePage` | Create a new slice with included paths and visibility |
| `/organizations/new` | `appRoute` | Auth | `NewOrganizationPage` | Create an organization account |
| `/accounts/$account/secrets` | `appRoute` | Auth | `AccountSecretsPage` | Manage folder-scoped account secrets (`AccountSecretsPanel`, `SecretForm`) |
| `/slices/$account/$slice/settings` | `appRoute` | Auth | `SliceSettingsPage` | Slice administration panels (SSR prefetches slice + global ref) |
| `/accounts/$account` | `publicAppRoute` | Public | `AccountPage` | Public/authenticated account profile, avatar, bio, slices, members/groups, and account CI daemon panel (SSR prefetches `accountProfileQuery`) |
| `/slices/$account/$slice` | `publicAppRoute` | Public | `SliceDetailPage` | Slice file browser, README, rich viewers, blame, history drawer, folder policy & automations drawer, Git clone URL (SSR via `loadSliceDetail`, OpenGraph `sliceHead`) |
| `/slices/$account/$slice/agents` | `publicAppRoute` | Public (feature-gated) | `SliceAgentsPage` | Slice agent conversations view |
| `/slices/$account/$slice/agents/$conversationId` | `publicAppRoute` | Public (feature-gated) | `SliceAgentsPage` | Specific agent conversation transcript (SSR prefetches `conversationQuery`) |
| `/changesets` | `publicAppRoute` | Public | `ChangesetsPage` | Filterable, searchable, infinite-scrolling changesets list (`?slice=&status=&q=`, SSR prefetches `changesetsListQuery`) |
| `/cs/$id` | `publicAppRoute` | Public | `ChangesetDetailPage` | Changeset review, patchset timeline, diff viewer, approvals, comments, checks, submit/abandon (SSR via `loadChangesetDetail`, OpenGraph `changesetHead`) |
| `/doc`, `/doc/$section` | `publicAppRoute` | Public | `DocPage` | Built-in product and CLI documentation |
| `/blogs`, `/blogs/$slug` | `publicAppRoute` | Public | `BlogListPage` / `BlogPostPage` | Product blog listing and articles |

Not-found boundaries are specialized per resource (`AccountNotFound`,
`SliceNotFound`, `ChangesetNotFound`, and fallback `PageNotFound`).

## 4. Visual Design System

The UI follows a crisp, high-density engineering aesthetic built with Tailwind
CSS:

- **Typography**:
  - Sans-serif: `Geist`, falling back to system UI fonts (`tailwind.config.*`).
  - Monospace: `Geist Mono`, falling back to `ui-monospace` / `SFMono-Regular` /
    `Menlo` — used for paths, commit/changeset handles, diffs, blame gutters,
    and code blocks.
  - Antialiased rendering (`-webkit-font-smoothing: antialiased`,
    `text-rendering: optimizeLegibility`) and `scrollbar-gutter: stable` to
    prevent horizontal layout shift across views of varying heights.
- **Color Palette (Light & Dark Mode)**:
  - **Light mode** (`:root`): slate/zinc neutral surfaces (`bg-slate-50` page
    canvas `#f8fafc`, `bg-white` cards/panels, `text-zinc-900` / `text-zinc-950`
    primary text, `text-slate-600` secondary text, `border-slate-200` borders).
  - **Dark mode** (`:root.dark`, toggled via `ThemeToggle.tsx` with `class`
    strategy): zinc neutral surfaces (`bg-zinc-950` page canvas `#09090b`,
    `bg-zinc-900` elevated cards, `text-zinc-100` / `text-zinc-50` primary text,
    `text-zinc-400` secondary text, `border-zinc-800` borders).
  - **Accent**: sky-blue (`sky-500` / `sky-600`, selection highlight
    `rgb(14 165 233 / 0.22)` in light mode and `rgb(56 189 248 / 0.28)` in dark
    mode) for interactive links, focus rings, active tabs, and primary actions.
  - **Borders & Elevation**: 1px crisp borders (`border-slate-200` /
    `dark:border-zinc-800`) define structure rather than heavy drop shadows.
- **Timestamps (`web/src/components/Timestamp.tsx`, `web/src/lib/time.ts`)**:
  - Rendered in the viewer's local time zone and locale after hydration (UTC
    date during SSR to avoid hydration mismatch), with full timestamp + zone on
    hover.

## 5. Key Page & Feature Designs

### 5.1 Slice Detail & Rich File Viewers (`/slices/$account/$slice`)

`SliceDetailPage` displays the slice's directory tree, inline README, file
viewers, commit history drawer, blame view, and folder policy/automations
drawer. Search params (`?path=...&at=...`) address any file, directory, or
commit inside the slice.

#### Rich File Viewers (`web/src/components/source/`)

| Viewer | File(s) | Capabilities |
|--------|---------|--------------|
| **Source Code** | `SourceCodeViewer.tsx`, `highlight.ts` | Shiki syntax highlighting with dual light/dark themes (`--shiki-dark` CSS vars), line numbers, line-range selection, copy/raw actions |
| **Markdown** | `MarkdownViewer.tsx`, `useRepoImageLoader.ts` | Server-rendered READMEs via `RepositoryService.RenderFile` (`internal/markdown`: goldmark GFM + bluemonday sanitization) for instant SSR without shipping DOMPurify to the Worker; client-side `marked` + `DOMPurify` for opened `.md` files; `<picture>` theme resolution and authenticated repo-relative image loading (`data-repo-image`, `data-repo-srcset`); Preview / Raw toggle |
| **Diagrams** | `DiagramViewer.tsx` | Mermaid (`mermaid`) and Graphviz DOT (`@viz-js/viz`) rendering with Preview / Raw toggle |
| **Parquet** | `ParquetViewer.tsx`, `DataTable.tsx` | Client-side Parquet inspection via `hyparquet` rendered in a sortable/filterable data table |
| **Excel / Spreadsheets** | `SheetViewer.tsx`, `TableViewer.tsx` | `.xlsx` via `read-excel-file` and CSV/TSV tabular views with header-row toggle and row/column counts |
| **Structured Data & Notebooks** | `JsonViewer.tsx`, `JsonTree.tsx`, `JsonLinesViewer.tsx`, `StructuredViewer.tsx`, `NotebookViewer.tsx` | Interactive JSON tree, JSONL, YAML/TOML structured inspection, and Jupyter `.ipynb` rendering |
| **Logs, Media & Binary** | `LogViewer.tsx`, `ImageViewer.tsx`, `MediaViewer.tsx`, `BinaryViewer.tsx` | Log formatting, images, audio/video media, and binary metadata fallback |
| **Blame** | `BlameView.tsx`, `BlameGutter.tsx` | Line-level commit and changeset attribution backed by `RepositoryService.BlameFile` |

#### Folder Policy & Automations Modal (`web/src/routes/slice-detail/`)

- **`FolderPolicyDrawer.tsx`** displays effective folder policies (required
  checks, secrets visibility, automations) for the currently viewed folder.
- **`RunAutomationModal.tsx`** opens when triggering manual automations (such
  as bash automations requiring typed `AutomationInputSpec` inputs, secret
  visibility disclosure, and explicit confirmation) before dispatching the
  automation run.

#### History Drawer

- Uses slice-scoped `RepositoryService.ListCommits` and
  `ChangesetService.ListChangesets` (publicly readable on public slices).
- Commits linked to a changeset (`Commit.changeset_id`) open directly to
  `/cs/<id>?file=<path>`.

#### Mobile Folding (`web/src/components/MobileDetails.tsx`)

On phones (below the `lg` breakpoint), an open file or folder starts with its
chrome folded so the content appears first on screen:

- A sticky bar shows `← Files`, the file/folder name, and a **Details** toggle.
- Folded until toggled open: breadcrumb, slice links, full path, commit SHA,
  History and file action menus, viewer metadata (path/type/line count), and
  table row/column counts with header switch.
- View-switching controls (e.g. Preview / Raw) remain visible.
- Navigating to another path resets details to folded.

### 5.2 Slice Settings (`/slices/$account/$slice/settings`)

`SliceSettingsPage` enforces a clear permission split via
`useSlicePermissions`:

- **Account owners & admins** (`isAccountAdmin`) can edit:
  1. **Description** (`SliceDescriptionPanel.tsx` → `SetSliceDescription`)
  2. **Merge on Submit** (`SliceMergeOnSubmitPanel.tsx` → `SetSliceMergeOnSubmit`)
  3. **Slice Definition** (`SliceDefinitionSettingsPanel.tsx` → `UpdateSliceDefinition` with optimistic concurrency via `expected_definition_hash`, visibility, included paths, rename, archive/unarchive, delete/retire)
  4. **CI Daemon Pool** (`SliceCIDaemonPanel.tsx` → assign/remove CI daemons for the slice)
  5. **Folder Policy & Secrets** (link to `/accounts/$account/secrets`)
  6. **Git Mirror** (`SliceGitMirrorPanel.tsx` → mirror URL, token secret selector, branch, username, sync status, "Push now", "Stop mirroring")
- **Slice admins** (`canAdmin`, including external users or `@account/group`
  slice admins) can manage:
  - **Slice Members** (`SliceMembersPanel.tsx` → add/update/remove user, agent,
    and `@account/group` members with `reader`, `contributor`, `writer`, or
    `admin` roles).

### 5.3 Changeset Detail (`/cs/$id`)

`ChangesetDetailPage` is publicly readable when the authoring slice is public
and provides full review workflows for signed-in users:

- **URL resolution**: Accepts short 10-hex changeset handles, canonical IDs, or
  landed commit IDs (redirecting commit IDs to the canonical `/cs/<handle>`
  URL). Clear error states distinguish not-found, sign-in-required (private
  slice), and no-access (linking to `/claims` for unclaimed agents).
- **Patchset timeline**: Compact horizontal timeline with patchset dots and
  draggable `From` / `To` handles calling `DiffChangeset` to compare any two
  patchsets (or `Recorded base` vs target patchset).
- **Review & Checks**: Inline and general comments, approvals (controller-aware
  with human-approval requirements), check runs and automation logs, submit
  requirements, base-changeset links for stacked changesets, and submit /
  abandon / rerun actions.
- **Polling**: Polls `GetChangeset` while status is `pending_publish` and
  refreshes running check/automation logs.
- **Mobile folding**: Below `lg`, starts with only the one-line title, status,
  review actions, and a Details toggle above the diff; Details unfolds
  breadcrumb, handle/author, description/base, patchsets, checks, and
  submit-blocked notes. The changed-file tree acts as the primary diff
  navigator on mobile.

### 5.4 Accounts, Secrets, Claims & CLI Login

- **`/accounts/$account`** (`AccountPage`): Public/member profile with avatar
  (`AccountAvatar.tsx` backed by `GET /avatars/{account}.png`), bio/profile
  editing, account slices, members/invitations/groups (`AccountPeople.tsx`,
  `PendingInvitations.tsx`), and account-level CI daemon pool
  (`AccountCIDaemonPanel.tsx`).
- **`/accounts/$account/secrets`** (`AccountSecretsPage`): Account owners and
  admins create, rotate, and delete folder-scoped secrets
  (`AccountSecretsPanel.tsx`, `SecretForm.tsx`). Values are write-only and
  never returned to the browser.
- **`/claims`** (`ClaimsPage`): Displays pending agent ownership claims matched
  by the signed-in user's verified email, allowing one-click claiming (`ClaimAgent`)
  and showing owned agents (`OwnedAgents.tsx`).
- **`/cli-login`** (`CliLoginPage`): Device-code approval screen for `gs auth login`
  — validates the user code and calls `CompleteCliLogin` so the polling CLI
  receives a service JWT.
