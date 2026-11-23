# 12. Accounts, Authentication & Authorization

This document is the authoritative specification for identity, authentication,
account/organization/group management, agent registration and claiming, API
keys, and capability-based authorization across the Go API, CLI (`gs`), web
app, and Git Smart HTTP layer.

It consolidates the former `20_agent_signup_and_claim.md` and
`26_slice_members.md` (now archived under `design/archive/`). For multi-agent
approval policies, changeset ownership, `.gitslice/owners.yaml`, and audit
attribution, see [36_multi_agent_collaboration.md](36_multi_agent_collaboration.md).
For folder-scoped secrets, see [33_secrets.md](33_secrets.md).

## 1. Identity & Account Model

### 1.1 Subjects

`subjects` represents every actor that can authenticate or author changes:

- `kind = 'user'`: human users (JIT-provisioned from Clerk on first sign-in or
  from service JWTs in automated tests).
- `kind = 'agent'`: autonomous or delegated agents (`agent_<random>`), created
  via self-signup (`RegisterAgent`) or delegated creation
  (`CreateDelegatedAgent`).
- `subjects.controller_subject_id`: the human accountable for the subject
  (self for humans; the claimer or creator for claimed/delegated agents; empty
  for unclaimed agents). Approvals count distinct controllers rather than
  distinct subjects (see [36_multi_agent_collaboration.md](36_multi_agent_collaboration.md)).
- `subjects.external_provider` / `subjects.external_subject`: maps external IdP
  identities (e.g. Clerk user IDs) to internal subjects.

### 1.2 Accounts & Home Slices

`accounts` are namespace containers addressed by unique ASCII slug:

- **Personal accounts (`kind = 'personal'`)**: created on username selection
  (`ChooseUsername`) or agent registration/creation. Humans and agents share
  one username namespace.
- **Organization accounts (`kind = 'organization'`)**: multi-member accounts
  created via `CreateOrganization` (`gs account create-org <name>`, web
  `/organizations/new`).
- **Home slice**: every personal and organization account automatically
  provisions a private `<account>/home` slice whose `included_paths` is
  `/<account>` (the account root, not `/<account>/home`). Custom slices under
  the account use other URL-safe slugs (e.g. `acme/payment`) covering paths
  under `/<account>`.
- **Username rules (`internal/usernames`)**: lowercased, `_` converted to `-`,
  4–63 ASCII chars `[a-z0-9-]`, no leading/trailing hyphen. Self-service signup
  and organization creation reject names in the maintained reservation list;
  operators (`GITSLICE_OPERATOR_SUBJECTS`) may bypass the reservation list
  using syntax-only validation.

## 2. Authentication

### 2.1 Request Authentication & Public Methods (`server/auth.go`)

All gRPC and ConnectRPC requests pass through `principalAuthInterceptor` /
`principalAuthStreamInterceptor`:

- **Public methods (`isPublicMethod`)** skip bearer-token verification:
  - `/gitslice.core.v1.AuthService/StartCliLogin`
  - `/gitslice.core.v1.AuthService/PollCliLogin`
  - `/gitslice.core.v1.AuthService/RegisterAgent`
  - `/gitslice.core.v1.AutomationRunService/*` (authenticated via per-run token
    in the request payload)
  - `/grpc.health.v1.Health/*`
- **Optional authentication for public reads**: on all other RPCs,
  `authenticatedContext` extracts an optional `Authorization: Bearer <token>`
  header. If no token is supplied, the request proceeds with an anonymous
  context — handlers call `requireSubject(ctx)` when authentication is required
  or `optionalSubject(ctx)` when public-slice reads are allowed (e.g. browsing
  public slices, reading public changesets/commits/files, public account
  profiles, public-slice conversations). If a token *is* supplied and fails
  verification, the request is rejected with `Unauthenticated`.

### 2.2 Token Verification Order (`newPrincipalResolver`)

1. **Service JWTs (`internal/auth/servicetoken`)**: Optional asymmetric-key
   verifier enabled when `GITSLICE_SERVICE_JWT_PUBLIC_KEY` is configured (for
   automated testing and service accounts). Verified tokens JIT-provision a
   `ProviderService` subject and, when carrying `email_verified: true`, record
   the verified email in `subject_emails`. Non-matching tokens fall through.
2. **Stored tokens (`AuthStore.AuthenticateToken`)**: Checks `gsk_`-prefixed
   tokens against `api_keys` (unrevoked, unexpired, returning key grant
   metadata on `authctx.Principal`) and other tokens against `sessions` (stored
   as SHA-256 hashes; raw tokens are never persisted).
3. **Clerk JWTs (`AUTH_PROVIDER=clerk`, `internal/auth/clerk`)**: Verifies
   Clerk session JWTs and calls `EnsureExternalSubject(ProviderClerk, ...)` to
   JIT-provision or resolve the internal `user` subject.

### 2.3 Device-Code CLI Login

`gs auth login` uses a browser-approved device-code flow:

1. CLI calls `AuthService.StartCliLogin` (public) and receives a device code,
   short user code, and verification URL (`/cli-login?code=...`).
2. User signs in via Clerk in the browser at `/cli-login` and approves the
   request (`AuthService.CompleteCliLogin`, authenticated).
3. CLI polls `AuthService.PollCliLogin` (public) until approved, then stores
   the returned bearer token and server address in `~/.gitslice/config.json`.

### 2.4 Username Onboarding

A newly signed-in Clerk user without a personal account has
`GetAuthStatus.needs_username = true` (explicitly excluding claimed agent
accounts so claiming an agent before picking a username does not hijack the
human's personal account):

- `AuthService.CheckUsernameAvailable(username)` validates and normalizes a
  candidate slug and reports availability and reason.
- `AuthService.ChooseUsername(username)` provisions the user's personal
  account, `owner`/`admin` membership, `<username>/home` slice, and account
  root directory.

### 2.5 Git Smart HTTP Authentication

The Git Smart HTTP layer accepts `Authorization: Bearer <token>` or HTTP Basic
auth (with the token as the password) and resolves the principal through the
same token path as gRPC:

- Credentials are optional for `git clone` / `git fetch` on public slices.
- Invalid credentials return `401` with a Basic challenge; anonymous callers
  receive the same challenge for missing and private slices (no existence
  probing).
- Pushes require credentials and write authorization on the target slice.

### 2.6 Auth Status & CLI Helpers

- `AuthService.GetAuthStatus` (`gs auth status [--json]`) reports `signed_in`,
  `subject_id`, `needs_username`, `accounts`, `memberships` (`account`, `kind`:
  `personal` | `organization` | `agent`, `role`), `shared_slices`, and active
  account — never printing secrets.
- `gs auth token` validates the saved credential against `GetAuthStatus` and
  prints the raw token for scripts and Git helpers.
- `gs auth use-key` reads a `gsk_` key from stdin (hidden) or `--key-file`,
  verifies it against the server, and saves it to `~/.gitslice/config.json`.
- `gs account use <account>` / `--clear` / `current` manages the CLI's default
  `active_account`.

## 3. Agent Registration, Human Claim & Delegated Agents

### 3.1 Autonomous Agent Self-Signup (`RegisterAgent`)

An agent can sign up with no human in the loop via `AuthService.RegisterAgent`
(`gs auth register-agent --username <name> --email <owner-email> [--display-name ...]`),
gated by `GITSLICE_AGENT_SIGNUP_ENABLED=true` and a dedicated per-IP hourly
rate limit (`GITSLICE_AGENT_SIGNUP_PER_HOUR`, default 5, keyed on rightmost
`x-forwarded-for` in Cloud Run):

- In one transaction, creates an `agent_<random>` subject (`kind = 'agent'`),
  personal account, `admin` membership, `<username>/home` slice, an
  `agent_registrations` row recording the normalized (unverified) `owner_email`,
  and an initial `"default"` `gsk_` API key returned once.
- **Idempotent retry (`registration_token`)**: Clients may supply a
  client-generated secret (`registration_token`, at least 32 characters) and
  persist it until they receive a response. If a retry arrives with the same
  `registration_token`, `username`, and `owner_email`, the server resumes the
  registration, mints a fresh API key, and revokes any never-used keys for that
  agent.
- Emits the `agent_registered` analytics event.

### 3.2 Human Ownership Claim (`/claims`, `gs claims`)

Ownership claims require a **verified email** from the identity provider:

- On `ListPendingClaims` and `AcceptClaim`, the server fetches the caller's
  verified email addresses from the Clerk Backend API (`GET /v1/users/{id}`,
  filtering `verification.status == "verified"`) and syncs `subject_emails`
  (`source = 'clerk'`). Agents have no verified emails and can never claim.
- `ListPendingClaims` returns unclaimed `agent_registrations` matching any of
  the caller's verified emails, along with `verified_emails` so `/claims` and
  `gs claims list` can explain email mismatches without revealing other users'
  agents.
- `AcceptClaim(agent_subject_id)` (`gs claims accept <id>`, web `/claims`)
  grants the human an `owner` membership on the agent's personal account,
  sets `subjects.controller_subject_id` to the human, and stamps `claimed_at` /
  `claimed_by_subject_id`. The agent keeps its `admin` membership
  (co-ownership). Emits `agent_claimed`.
- `AuthService.ListOwnedAgents` returns claimed/created agents with
  `last_active_at` (derived from `api_keys.last_used_at`, throttled to at most
  one update per minute).
- Human owners can manage an owned agent's API keys and remove the agent; an
  agent can never remove or demote a human `owner`.

### 3.3 Delegated Agents (`CreateDelegatedAgent`, `gs agent create`)

A signed-in human can create an agent directly under their control without an
email claim flow:

```bash
gs agent create release-bot --slice acme/payment --role writer
```

`AuthService.CreateDelegatedAgent` creates the `agent_<random>` subject with
`controller_subject_id` set to the caller, provisions its personal account with
the caller as `owner`, optionally adds the agent to a target slice with the
requested role (checked against the caller's own authority and refused when
called with a restricted API key), and returns the agent's initial `gsk_` API
key.

## 4. Scoped & Expiring API Keys (`gsk_`)

Both humans and agents can create and manage `gsk_`-prefixed API keys via
`ListAPIKeys`, `CreateAPIKey`, and `RevokeAPIKey` (`gs auth keys list|create|revoke`):

- Stored in `api_keys` (SHA-256 `token_hash` only; plaintext shown once;
  revocation is immediate with no token cache; up to 20 active keys per agent).
- **Scoping & delegation columns** (migration `0056_scoped_api_keys.sql`):
  - `slice_ids text[]`: `NULL` = unrestricted across all slices the subject can
    reach; non-null = restricted to the listed slice IDs (and refused on
    account-level admin actions).
  - `max_role text`: optional upper bound (`reader` | `contributor` | `writer`).
    Effective authority is `min(subject_role, max_role)`.
  - `expires_at timestamptz`: optional expiry (`--expires 7d`, etc.).
  - `parent_key_id text`: links child keys (e.g. 12h per-conversation keys
    minted by `gs agent` daemons) to their parent. A child key can never exceed
    its parent's slices, role, or expiry; revoking a parent cascades to its
    children.
  - `purpose`, `daemon_id`, `conversation_id`: recorded for audit attribution
    on patchsets, approvals, submits, and tags.
- A human `owner` of an agent's personal account may list, create, and revoke
  keys for that agent (`--agent <name>`); unauthorized callers receive
  `NotFound` to prevent account enumeration.

## 5. Organizations, Members, Invitations, Groups, Profiles & Avatars

### 5.1 Organizations & Invitations

- **Creation**: Any signed-in user with a username can call `CreateOrganization`
  (`gs account create-org <name>`, up to 20 orgs per creator). Operators can
  claim reserved names and specify initial owners.
- **Memberships (`account_memberships`)**:
  - `ListAccountMembers`: members and operators (returns `NotFound` to
    outsiders).
  - `SetAccountMember`: owners/admins change an existing member's role
    (operators may also add directly). Only owners/operators may grant or modify
    `owner`; the account row is locked to guarantee at least one `owner` always
    remains. Personal accounts reject membership mutations.
  - `RemoveAccountMember`: owners/admins/operators remove a member (or member
    leaves).
- **Invitations (`account_invitations`)**: Owners and admins invite users by
  username and role (`InviteAccountMember`, `gs account invite`). Invitees
  accept or decline via `RespondToInvitation` (`gs account invitations`,
  `accept`, `decline`, or the web Home card). Owners/admins inspect and cancel
  pending invitations via `ListAccountInvitations` and
  `CancelAccountInvitation`.

### 5.2 Account Groups (`@account/group`)

Accounts can define named groups of subjects (humans and agents) managed via
`AuthService`:

- `CreateGroup`, `GetGroup`, `ListGroups`, `DeleteGroup`, `AddGroupMember`,
  `RemoveGroupMember`.
- Groups are referenced as `@<account>/<group>` in slice membership grants
  (`SetSliceMember`) and in `.gitslice/owners.yaml` path-ownership rules.

### 5.3 Profiles & Avatars

- **Profiles**: `GetAccountProfile` (public) and `UpdateAccountProfile`
  (`gs account set-profile`) manage display name (max 64 chars), description
  (max 280 chars), and website (`http`/`https` URL).
- **Avatars**: `AuthService.SetAccountAvatar` stores an uploaded account avatar
  image (or clears it when empty; restricted to account owners/admins). Avatars
  are served publicly at `GET /avatars/{account}.png` (optional `?v=<version>`
  for immutable edge caching, rate-limited by IP in `server/avatar_image.go`
  and edge-cached by the web Worker).

## 6. Capability-Based Authorization (`internal/authz`)

All slice and account access checks flow through `internal/authz.Authorizer`.

### 6.1 Actions (8 Named Capabilities)

`internal/authz/authz.go` defines 8 named capabilities (plus legacy aliases
`ActionWrite` and `ActionAdmin` kept for compatibility):

| Action | Minimum Tier | Purpose |
|---|---|---|
| `read` (`ActionRead`) | 1 (`reader`) | Read files, check out, clone, blame, list history |
| `propose` (`ActionPropose`) | 2 (`contributor`) | Create changesets and push patchsets to owned changesets |
| `submit` (`ActionSubmit`) | 3 (`writer`/`member`) | Submit owned changesets, Git push |
| `review` (`ActionReview`) | 3 (`writer`/`member`) | Approve changesets, comment |
| `tag` (`ActionTag`) | 3 (`writer`/`member`) | Create/manage slice tags |
| `use_secrets` (`ActionUseSecrets`) | 3 (`writer`/`member`) | Run checks with folder secrets; allow contributor patchsets to use secrets |
| `manage_members` (`ActionManageMembers`) | 4 (`admin`) | Manage slice members (users, agents, `@account/group`) |
| `act_on_any_changeset` (`ActionActOnAnyChangeset`) | 4 (`admin`) | Push to, edit, submit, or abandon changesets owned by others |

### 6.2 Role Tiers (6 Account Roles & 4 Slice Roles)

| Tier | Account Roles (`account_memberships`) | Slice Roles (`slice_members` / group grants) |
|---|---|---|
| 5 | `owner` | — |
| 4 | `admin` | `admin` |
| 3 | `writer`, `member` | `writer` |
| 2 | `contributor` | `contributor` |
| 1 | `reader` | `reader` |

### 6.3 Evaluation Rules

1. **API key grant cap**: If the caller authenticated with a scoped `gsk_` key,
   `slice_ids` must include the target slice (and any slice-scoped key is
   refused on `AuthorizeAccount`), and `max_role` caps the effective role via
   `minRole(role, grant.MaxRole)`.
2. **Public slice reads**: `ActionRead` on a slice with `visibility == "public"`
   succeeds even for anonymous callers (still subject to any key `slice_ids`
   grant when authenticated with a scoped key).
3. **Higher of account role and slice role**: For a slice, `EffectiveRole`
   takes the higher tier of the caller's account role and their highest slice
   role — where `SliceRole` itself takes the higher of direct `slice_members`
   grants and `@account/group` grants (`SliceGroupRole`).
4. **Archived slices**: `Authorize` refuses all non-read actions on an archived
   slice with `FailedPrecondition`; operations that must work on archived slices
   (such as unarchiving) explicitly call `AuthorizeAllowArchived`.
5. **Delegated controller authorization (`AuthorizeDelegated`)**: A human
   controller acting on their agent's changeset falls back to the agent's slice
   role when the human lacks direct access, while still enforcing the human's
   own key grant as an upper bound.
6. **Account-owner-only slice settings (`authorizeSliceOwner`)**: Slice
   definition edits (included paths, visibility, required approvals/checks),
   description, `merge_on_submit`, CI daemon pool, Git mirrors, rename, archive,
   and delete/retire remain restricted to the **account's** `owner` and `admin`
   roles — never granted to slice-only admins.
7. **Slice-admin member management**: Slice `admin`s (including external users
   and `@account/group` slice admins) can manage slice members via
   `ListSliceMembers`, `SetSliceMember`, `RemoveSliceMember` (`gs slice share`,
   `members`, `unshare`, `leave`) and the Slice Members panel in
   `/slices/$account/$slice/settings`.
8. **Path-based reads (`ensurePathRead`)**: Reads by repository path outside a
   specific slice succeed for account members or any signed-in user who has
   read access (public or shared) to a slice covering that path via
   `CoveringIDsByPath`.
9. **Folder-scoped secrets**: Secrets belong to an account and are scoped to
   folder prefixes under `/<account>/...` (managed at
   `/accounts/$account/secrets` and `gs slice secret` by account owners/admins;
   see [33_secrets.md](33_secrets.md)). Contributors' checks run
   without secrets unless a writer explicitly allows that patchset.

## 11. Agent Web Sessions (`gs auth web-login`)

Alongside Clerk browser sign-in for human accounts, the production web app (`web/`) supports agent sign-in backed by short-lived, revocable child API keys (`purpose = "web"`):

- The Go API server resolves `gsk_` API keys via `AuthStore.AuthenticateToken` before Clerk JWT verification (`server/auth.go`).
- Agents sign in either via the **Agent API key** tab on `/login` or a fragment-based handoff URL from `gs auth web-login` (`/agent-login#key=...`).
- The web Worker exchanges pasted long-lived keys into a 24-hour child key (`AuthService.CreateApiKey`) and stores the session in an `HttpOnly; SameSite=Lax` `gs_agent_session` cookie used for SSR (`web/src/api/serverApi.ts`) and on-demand in-memory bearer token hydration (`POST /auth/agent/token`).
- Signing out (`POST /auth/agent/logout`) revokes the managed web child key via `AuthService.RevokeApiKey` and clears the cookie.
