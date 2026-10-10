# Account And Auth Current State

This document describes the account and authentication system implemented in the
current Go MVP prototype. It is a current-state map, not the final product auth
design.

## 1. Scope

The current account system exists to support local development, functional
tests, CLI workflows, service authorization checks, and Git read compatibility.
It is intentionally small:

- one fake development login service
- one fake browser-approved signup flow
- PostgreSQL-backed accounts, subjects, memberships, and sessions
- bearer-token authentication for native gRPC and ConnectRPC requests
- bearer-token or Basic-password authentication for Git smart HTTP reads
- simple account-membership authorization where implemented

The system does not implement real OAuth, browser login, device-code login,
refresh tokens, invitations, billing, organization administration, or a full
role/permission matrix.

## 2. Database Model

The account and auth metadata lives in PostgreSQL:

`subjects`
: Actors that can authenticate or be recorded as authors. The seed data uses
  `kind = user` for humans and `kind = service_account` for the CI bot.

`accounts`
: Account containers addressed by unique `slug`. The seed data currently has
  one organization account, `acme`.

`account_memberships`
: Subject membership in accounts. Rows include a `role`, but the current service
  checks generally treat membership as boolean authorization rather than
  applying role-specific permissions.

`sessions`
: Development login sessions. Session rows store only a token hash, not the raw
  bearer token. They also include `expires_at` and nullable `revoked_at`.

Important relationships:

- slices belong to accounts through `slices.account_id`
- changesets and patchsets store `author_subject_id`
- imports store the importing `subject_id`
- commits can store `author_subject_id`

## 3. Seed Fixture

Server startup runs migrations and seeds a deterministic development fixture.
The current fixture creates:

- subjects:
  - `user_alice`
  - `user_bob`
  - `ci_bot`
- account:
  - `acct_acme` with slug `acme`
- memberships:
  - `user_alice` as `admin`
  - `user_bob` as `writer`
  - `ci_bot` as `writer`
- slices:
  - `acme/payment`
  - `acme/backend`

The fixture also creates the initial empty commit and `refs/global/main`.

## 4. Login And Sessions

The public development login surface is `FakeAccountService.Login`.

CLI usage:

```bash
gs auth login --server 127.0.0.1:50051 --dev-user alice
```

Implementation behavior:

- the dev user is normalized to a subject id such as `user_alice`
- login fails if that subject is not present in `subjects`
- a random `devtok...` bearer token is generated
- a random `sess...` session id is generated
- only `token_hash(token)` is stored in PostgreSQL
- sessions expire after 24 hours
- the CLI stores the raw token in `~/.gitslice/config.json`

`gs auth logout` clears the saved token and subject id from local CLI config.
It does not currently revoke the server-side session. The schema supports
revocation through `sessions.revoked_at`, but no user-facing revoke flow exists.

## 5. Browser-Approved Signup

The current signup surface is a fake browser approval flow. It does not contact
a real identity provider.

CLI usage:

```bash
gs auth signup --username nico
```

Implementation behavior:

1. the CLI starts a temporary localhost callback listener
2. the CLI opens or prints a URL under the static web UI at `/signup`
3. the web page asks the user to approve signup for the requested username
4. the web page calls `FakeAccountService.ApproveSignup` through the generated
   HTTP API
5. approval creates or reuses a user subject and personal account
6. approval creates or reuses the personal account's default `home` slice
7. approval creates a 24-hour bearer-token session
8. the web page redirects to the returned CLI callback URL with the token and
   subject id
9. the CLI validates the callback state and stores the token in
   `~/.gitslice/config.json`

The signup web page is intentionally simple. It exists to exercise the device
flow shape without a production identity provider. Callback URLs must point to a
loopback host so the gRPC signup service does not issue callback redirects that
send bearer tokens to arbitrary remote origins.

Username normalization:

- usernames are lowercased
- underscores are converted to hyphens for the account slug
- normalized usernames must be between 4 and 63 ASCII characters
- normalized usernames may contain only lower-case letters, numbers, and
  hyphens, and may not start or end with a hyphen
- self-service signup rejects names in the maintained reservation list in
  `internal/usernames`; the list covers Gitslice/system identities, well-known
  brands and platforms, and well-known public figures
- the user subject id is `user_<username>` with hyphens converted to underscores
- the personal account id is `acct_<username>` with hyphens converted to
  underscores
- the personal account slug is the normalized username
- the signed-up subject is added as `admin` on that personal account
- the default slice is `<username>/home`

Existing organization slugs, such as `acme`, cannot be claimed through this fake
signup flow.

## 5.1 Home Slice and Slice Slugs

Every signed-up personal account owns a default home slice:

```text
nico/home
```

The slice slug is always `home`. The included path is the user's account root:

```text
nico/home -> /nico
```

This is deliberate. The home slice represents the user's personal source root,
not a nested `/nico/home` folder. Existing changeset validation then limits files
created through `nico/home` to `/nico`.

Outside a workspace, `gs shell` resolves the signed-in user's `home` slice when
it exists and labels the shell with that slice. The empty account-root folder is
visible from slice metadata, so a newly signed-up user can run `gs shell` and
see `nic/` from `ls` before creating any files.

`gs fs` and mutating `gs shell` commands use the same personal home slice as
their authoring slice. Paths must resolve under the home slice included path,
for example `/nico`. Client-side checks reject absolute paths outside that root
before submit, and the changeset service revalidates the same path containment
against the `nico/home` slice definition.

Custom personal slices may use other URL-safe slugs such as `tools`,
`dotfiles`, or `blog`, but their included paths must stay under `/nico`.
Organization slices use the same `<account>/<slice-slug>` identity shape, such
as `acme/payment`, and may include paths under their owning account root.

## 6. Auth Status And Token

The current authenticated status surface is `AuthService.GetAuthStatus`.

CLI usage:

```bash
gs auth status
gs auth status --json
gs auth token
```

`gs auth status` reads the local server address and bearer token from
`~/.gitslice/config.json`, then calls `AuthService.GetAuthStatus`. A saved token
is reported as signed in only if the server accepts it. Missing local config,
incomplete local config, or an unauthenticated server response are reported as
signed out without printing the token.

JSON output exposes only non-secret fields:

```json
{
  "signed_in": true,
  "server_addr": "127.0.0.1:50051",
  "subject_id": "user_alice"
}
```

`gs auth token` is the explicit secret-bearing CLI command for scripts and
Git-compatible flows that need the bearer token. It reads
`~/.gitslice/config.json`, validates the saved token with
`AuthService.GetAuthStatus`, and prints the raw token only if the server accepts
it. The non-secret `gs auth status` command remains the preferred inspection
surface for humans and diagnostics.

## 7. Native Request Authentication

The gRPC server installs unary and stream auth interceptors.

Public methods:

- `FakeAccountService.Login`
- `FakeAccountService.ApproveSignup`
- gRPC health checks

All other native methods require metadata:

```text
authorization: Bearer <token>
```

The interceptor:

1. extracts a bearer token from request metadata
2. looks up an unrevoked, unexpired session by token hash
3. loads the session subject
4. attaches `subject_id` to the request context

Service handlers call `requireSubject(ctx)` to read that authenticated subject.
If metadata is missing, malformed, expired, revoked, or unknown, the request
fails as unauthenticated.

The ConnectRPC HTTP middleware accepts the `Authorization` HTTP header, so
browser callers use the same bearer token path.

## 8. Git HTTP Authentication

The Git smart HTTP compatibility layer authenticates independently from the gRPC
interceptor because it is a plain HTTP handler.

Accepted credentials:

- `Authorization: Bearer <token>`
- Basic auth where the password is the token

The Git handler resolves the subject by token hash through the same
`AuthStore.SubjectForToken` path used by gRPC authentication.

- **Credentials are optional for clone and fetch.** A request without them
  reads as anonymous, which slice authorization admits only for public
  slices.
- **Credentials that are present but invalid** return `401` with a Basic
  challenge, as does any push without credentials.
- **Anonymous callers cannot probe for private slices:** they get the same
  challenge for a missing slice as for a private one.
- **Pushes** to `refs/changes/new` or `refs/changes/<changeset>` create or
  update changesets after write authorization
  (`05_git_compatibility.md` §8).

## 9. Authorization Today

Authorization is currently account-membership based and incomplete by design.

Implemented membership checks include:

- resolving and listing slices by account
- workspace state and workspace diff validation
- changeset create, list, get, diff, update, submit, and abandon through the
  changeset's authoring slice account
- repository import into an authoring slice
- Git projection and push rejection paths

Current broad authenticated-only surfaces include:

- repository read APIs such as ref, commit, path, directory, and file reads
- blob status and upload APIs
- some slice lookup and definition update paths
- workspace hydration and operation recording helpers

The practical implication is that the MVP proves authentication and selected
account boundaries, but it is not a complete access-control system. Repository
read authorization and role-specific write authorization need tightening before
the model is suitable for production use.

Short commit id resolution is a repository read surface. When added, it must be
scoped before uniqueness is decided: the server should search only commits
visible through the caller's target ref, account memberships, path filter, and
slice projection. Ambiguity and not-found errors must not reveal candidate ids
or commit existence outside that visible scope. Possession of a full native
commit id is not authorization to read that commit's metadata or changed paths.

## 9.1 Organization Accounts (2026-10)

An organization is an account (`accounts.kind = 'organization'`) that belongs
to its members rather than to one subject. It has a private `home` slice
covering `/<slug>` and an account root directory, like a personal account.

**Creation by operators.** (Since 9.3 anyone with a username may also create
an organization, without the operator powers below.) An operator's subject id is listed in
`GITSLICE_OPERATOR_SUBJECTS`, separated by commas, semicolons or spaces. In
production that is the `_OPERATOR_SUBJECTS` Cloud Build substitution.
Operators may use names reserved for self-service sign-up, such as
`gitslice`, because organization slugs are checked with
`usernames.NormalizeSyntax` rather than `Normalize`. The initial owners
default to the caller.

```text
gs account create-org gitslice [--owner <username>]...
```

**Membership** is managed through `AuthService` RPCs and `gs account`:

| RPC | Who may call it | Effect |
|---|---|---|
| `ListAccountMembers` | members, operators | lists members, owners first; for anyone else the account does not exist (NotFound) |
| `SetAccountMember` | owners, admins, operators | makes a role a member's only role; only operators add a non-member (others invite, 9.3) |
| `RemoveAccountMember` | owners, admins, operators | removes every membership of the member |

Rules:

- Only owners or operators grant, revoke or change the `owner` role.
- An organization always keeps at least one owner. The account row is locked
  during membership changes so concurrent requests cannot both remove the
  last owner.
- Personal accounts reject membership changes (FailedPrecondition). Their
  memberships come from sign-up and agent claims.
- Users are named by username: the slug of their personal account, which for
  agents is the agent's own account (`SubjectIDForUsername`).

Authorization is unchanged. Slices under an organization authorize through
`AccountRole`: owner, admin, writer and member can write; reader can only
read.

## 9.2 Accounts In The Web App (2026-10)

The web app works in one account at a time, as GitHub does: the viewer's
personal account, an organization they belong to, or an agent they claimed.

- **Auth status** returns `memberships` next to `accounts`, in the same order:
  each account's kind (`personal`, `organization`, or `agent` for a claimed
  agent's personal account) and the caller's role in it.
- **Switcher.** The top bar's account menu lists those memberships, grouped by
  kind with the role. The choice is the *active account*, remembered in the
  browser (`localStorage`, `gitslice.activeAccount`); one the viewer has left
  falls back to their personal account. Home lists the active account's
  slices (in the personal context, also the slices of agents they own), new
  conversations start on its slices, and "New slice" creates there.
- **Account pages** (`/accounts/<account>`): avatar, kind and the viewer's
  role; the slices the viewer may read (`ListSlices`: every slice for members,
  public ones for anyone else, with `account_kind`); for an organization's
  members, its people. Owners and admins add people by username, change roles
  and remove people there, through the RPCs in 9.1. Slice breadcrumbs link the
  owner to this page. Your own personal page also lists the organizations you
  belong to and the agents you have claimed, each with your role, plus a "New
  organization" link. Others do not see your memberships.
- **Role-aware UI.** The slice page offers editing to roles that can write
  (owner, admin, writer, member) and Settings to owners and admins; "New
  slice" offers the accounts the viewer owns or administers. The server still
  decides; the UI only stops offering what it would refuse. A role an older
  server did not report is given the benefit of the doubt.

## 9.3 Self-Service Organizations, Invitations And Profiles (2026-10)

- **Anyone can create an organization.** A signed-in user with a username
  calls `CreateOrganization` (web: "New organization" in the account menu,
  `/organizations/new`; CLI: `gs account create-org <name>`). They are its only
  owner. Names reserved for sign-up are refused, and a user may have created at
  most 20 organizations (`accounts.created_by_subject_id`). Operators keep the
  old powers: reserved names and naming other owners.
- **Invitations.** Owners and admins invite people by username with a role
  (`InviteAccountMember`, `gs account invite`); only owners invite owners. The
  invitation (`account_invitations`) becomes a membership only when the invitee
  accepts it (`RespondToInvitation`; web: the card on Home and the badge in the
  account menu; CLI: `gs account invitations`, `accept`, `decline`). Owners and
  admins see and cancel pending ones (`ListAccountInvitations`,
  `CancelAccountInvitation`). Inviting again replaces the role.
  `SetAccountMember` now only changes the role of existing members; adding a
  non-member directly is left to operators, for server automation such as
  `ops/selfhost/phase1.sh`.
- **Profiles.** Each account has a display name (64 characters), description
  (280) and website (an http or https URL), public through `GetAccountProfile`
  and shown on its account page. An organization's owners and admins edit it,
  as does a person their own (`UpdateAccountProfile`, `gs account set-profile`).
  There is no avatar image; pages draw initials.
- **CLI active account.** `gs account use <account>` stores `active_account` in
  `~/.gitslice/config.json`; commands that default an account (`gs slice list`)
  use it while the user still belongs to it. `gs account use --clear` goes back
  to the personal account, `gs account current` shows which applies, and
  `gs auth status` reports it.
- **Home follows the active account** for conversations too; agent claims and
  owned agents appear only in the personal context.

## 10. Subject Propagation And Audit Fields

Authenticated subject ids flow through service methods and are stored on
user-visible writes:

- changesets store `author_subject_id`
- patchsets store `author_subject_id`
- published commits store the changeset author's subject id
- Git imports store the importing `subject_id`

The server does not trust local CLI state for the subject id. The subject comes
from the validated bearer token on each server request.

## 11. Current Invariants

- Raw bearer tokens are never stored in PostgreSQL.
- Native service methods are authenticated by default unless explicitly listed
  as public.
- The CLI token is global user config, not workspace metadata.
- Signup approval only redirects tokens to loopback callback URLs.
- Workspaces still bind to exactly one slice; auth does not change that model.
- Account membership is the current coarse authorization boundary where checks
  are implemented.
- Git compatibility is an authenticated projection layer, not the source of
  truth for identity or authorization.

## 12. Known Gaps

- no production identity provider
- no refresh-token lifecycle
- no server-side session revocation command
- no organization avatars or invitation expiry
- incomplete path/read authorization on repository and blob APIs
- no implemented auth-aware short commit id resolver yet
- no per-slice or per-path ACLs
- no audit event stream beyond persisted author fields
