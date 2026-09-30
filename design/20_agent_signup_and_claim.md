# 20. Agent Sign-Up And Human Claim

Status: phase 1 (agent registration + API keys) shipped in #372. Phase 2
(human claim) is implemented; phase 3 is design only.

## Goal

An agent should be able to start using Gitslice with **no human in the loop**:
it registers itself, receives a long-lived credential, and gets its own
personal account and home slice. When the agent registers it names an
**owner email**. Later, the human who controls that email can sign in through
the normal Clerk flow and **claim co-ownership** of everything the agent
created, without the agent losing access.

Non-goals for now:

- Agents acting on behalf of an existing human account (delegated tokens).
  That is a separate feature: a signed-in human mints a scoped key for an agent
  inside their own account.
- Per-key scopes. Phase 1 keys carry the full authority of the agent subject.
- Email delivery. The design leaves room for a notification email, but nothing
  in phase 1 sends mail.

## Current State

See `12_account_auth.md` for the full picture. The relevant facts:

- Every credential today traces back to a human Clerk sign-in. The CLI device
  flow (`StartCliLogin` → browser `/cli-login?code=` → `CompleteCliLogin` →
  `PollCliLogin`) requires a signed-in browser to approve.
- `StartCliLogin` and `PollCliLogin` are the only unauthenticated RPCs
  (`server/server.go` `isPublicMethod`).
- `subjects.kind` exists but only `'user'` is written.
- `account_memberships.role` exists and `AccountRole` already ranks
  `owner > admin > writer > member > reader`, so multiple owners of one account
  are representable today.
- Subjects have no stored email; a Clerk user's email only lands in
  `display_name`, and the Clerk verifier reads the `email` claim best-effort
  without checking verification status.
- The service-token path (`internal/auth/servicetoken`) is a testing affordance
  that can impersonate any subject; it is not a sign-up mechanism.

## Model

### Agent subject

An agent is a row in `subjects` with `kind = 'agent'` and an id of the form
`agent_<random>`. It gets a normal personal account (`accounts.kind =
'personal'`), an `admin` membership on that account, and a home slice, exactly
like a human who ran `ChooseUsername`. Agents and humans share one username
namespace.

### Agent registration

`agent_registrations` records, per agent subject, which account it created and
which email may claim it:

```sql
create table if not exists agent_registrations(
	subject_id text primary key references subjects(id),
	account_id text not null references accounts(id),
	owner_email text not null,          -- normalized: trimmed, lowercased
	created_at timestamptz not null default now(),
	claimed_at timestamptz,
	claimed_by_subject_id text references subjects(id)
);
create index if not exists agent_registrations_unclaimed_email_idx
	on agent_registrations(owner_email) where claimed_at is null;
```

The owner email is **unverified at registration time**. It does not grant
anything by itself; it only names who is allowed to claim, and a claim requires
a verified email (phase 2).

### API keys

Agents need a credential that does not come from a browser. API keys live in
their own table rather than `sessions`, so they can be listed, named, revoked,
and audited independently of login sessions:

```sql
create table if not exists api_keys(
	id text primary key,                -- "key_<random>"
	subject_id text not null references subjects(id),
	name text not null,
	token_hash text unique not null,    -- sha256 hex, same as sessions
	created_at timestamptz not null default now(),
	last_used_at timestamptz,
	expires_at timestamptz,             -- null = no expiry
	revoked_at timestamptz
);
create index if not exists api_keys_subject_idx on api_keys(subject_id);
```

Key format: `gsk_<random>` from `objectid.RandomID("gsk")`. The plaintext is
returned exactly once, at creation. Only the hash is stored.

`SubjectForToken` resolves `gsk_`-prefixed tokens against `api_keys` (not
revoked, not expired) and everything else against `sessions` as today. Because
the Clerk resolver already tries `SubjectForToken` first, API keys work under
both `AUTH_PROVIDER=clerk` and the local provider with no resolver change.
`last_used_at` is updated at most once per minute per key to avoid a write on
every request.

## Phase 1: Registration

### RPC

```proto
rpc RegisterAgent(RegisterAgentRequest) returns (RegisterAgentResponse);

message RegisterAgentRequest {
  // Desired username; same rules as ChooseUsername.
  string username = 1;
  // Email of the human who may later claim co-ownership. Required.
  string owner_email = 2;
  // Optional human-readable name, e.g. "release-bot". Defaults to username.
  string display_name = 3;
}

message RegisterAgentResponse {
  string subject_id = 1;
  string account = 2;
  // Plaintext API key. Returned only once.
  string api_key = 3;
}
```

`RegisterAgent` is unauthenticated (added to `isPublicMethod`). In a single
transaction it:

1. normalizes and validates `username` (`normalizeSignupUsername`) and
   `owner_email` (trimmed, lowercased, exactly one `@` with a non-empty local
   part and a domain containing a `.`, no whitespace, at most 254 bytes);
2. inserts the `subjects` row (`kind = 'agent'`);
3. calls the existing `provisionAccountForSubject` to create the account,
   `admin` membership, home slice, and account root directory;
4. inserts the `agent_registrations` row;
5. inserts an `api_keys` row named `"default"`.

A taken or invalid username returns the same errors `ChooseUsername` does
(`ErrConflict` → `FailedPrecondition`, `ErrInvalid` → `InvalidArgument`).

### Abuse controls

Open, unauthenticated account creation is the main risk. Phase 1 ships with:

- **Opt-in.** Disabled unless `GITSLICE_AGENT_SIGNUP_ENABLED=true`. When
  disabled, `RegisterAgent` returns `FailedPrecondition` ("agent sign-up is
  disabled on this server").
- **A dedicated per-IP limit**, separate from and much tighter than the general
  per-subject gRPC limit: `GITSLICE_AGENT_SIGNUP_PER_HOUR` (default 5) with the
  same count as the burst. It keys on the same client IP the existing limiters
  use.

In production the `RegisterAgent` limiter keys gRPC calls on the rightmost
`x-forwarded-for` hop, like the HTTP limiter. Cloud Run's TCP peer is its own
front end, so keying on the peer would give every client one shared bucket.
Sign-up is enabled in prod through the `_AGENT_SIGNUP_ENABLED` /
`_AGENT_SIGNUP_PER_HOUR` Cloud Build substitutions (defaults `"true"` / `"5"`).
Set `_AGENT_SIGNUP_ENABLED=false` on the trigger to turn it off.

Still to do (phase 3); these were planned before enabling it in production:

- quotas for unclaimed agent accounts (stored bytes, slices, changesets/day);
- automatic expiry of agent accounts left unclaimed for N days;
- optionally a proof-of-work challenge or an invite code on `RegisterAgent`.

### CLI

```bash
gs auth register-agent --username release-bot --email me@example.com \
  [--display-name "Release bot"] [--server ADDR]
```

It calls `RegisterAgent` unauthenticated and saves the returned key to
`~/.gitslice/config.json` exactly like a completed `gs auth login`, so every
other command works immediately. `--json` prints `subject_id`, `account`, and
`server_addr`, but not the key. `gs auth token` can print the key when a script
really needs it.

## Phase 2: Human Claim

### Verified email

A claim must only succeed for an email the identity provider has verified. The
Clerk session JWT does not carry email or verification state by default, so the
claim path fetches the user from the Clerk Backend API (`GET /v1/users/{id}`)
and uses only `email_addresses[]` entries with `verification.status ==
"verified"`. This lookup happens only on the claim RPCs, not on every request.

The service-token path counts as verified only when the minted token carries
an explicit `email_verified: true` claim. It is a test affordance.

Verified addresses are stored so claims can be matched in SQL:

```sql
create table if not exists subject_emails(
	subject_id text not null references subjects(id),
	email text not null,               -- normalized like owner_email
	source text not null,              -- "clerk" | "service"
	verified_at timestamptz not null default now(),
	primary key(subject_id, email)
);
```

- **Clerk callers.** Both claim RPCs re-fetch the caller's verified emails from
  the Backend API and replace that subject's `source = 'clerk'` rows, so an
  address removed at Clerk stops matching. The provider user id comes from
  `subjects.external_subject`, which `EnsureExternalSubject` now records (with
  `external_provider`) on first sign-in. Without `CLERK_SECRET_KEY` the claim
  RPCs return `FailedPrecondition`; if Clerk is unreachable they return
  `Unavailable`.
- **Service-token callers.** The resolver records the token's email when it
  carries `email_verified: true`.
- **Agents** have no verified emails, so they can never claim.

### RPCs

```proto
rpc ListPendingClaims(ListPendingClaimsRequest) returns (ListPendingClaimsResponse);
rpc AcceptClaim(AcceptClaimRequest) returns (AcceptClaimResponse);

message PendingClaim {
  string agent_subject_id = 1;
  string agent_display_name = 2;
  string account = 3;
  string owner_email = 4;
  string created_at = 5;
}
message ListPendingClaimsRequest {}
message ListPendingClaimsResponse { repeated PendingClaim claims = 1; }
message AcceptClaimRequest { string agent_subject_id = 1; }
message AcceptClaimResponse { string account = 1; }
```

`ListPendingClaims` returns unclaimed registrations whose `owner_email` matches
one of the caller's verified emails. `AcceptClaim` re-checks that match, then
in one transaction:

- inserts an `owner` membership for the caller on the agent's account;
- sets `claimed_at` and `claimed_by_subject_id`.

The agent's own `admin` membership is untouched, so both keep access. That is
the co-ownership.

### Claimed accounts are not personal accounts

Every provisioned account has `kind = 'personal'`, so a claimed agent account
would otherwise be mistaken for the claimer's own. Personal-account detection
(`ChooseUsername`, `UsernamesForSubjects`, and the ownership check in
`provisionAccountForSubject`) excludes accounts registered by a *different*
agent. `ListSubjectAccountSlugs` sorts claimed agent accounts last.
`GetAuthStatus.needs_username` is now "has no personal account" rather than "has
no memberships", and the CLI's personal-account lookup honors it. Without this,
a human who claimed an agent before picking a username would have been handed
the agent's account as their own.

Claims are **explicit** (an Accept click), not a side effect of signing in.
A silent grant on login would be invisible to the human and would make the
resolver hot path do email work on every request.

### Web and CLI

- A card at the top of the signed-in home page: "2 agent accounts are waiting
  for you". It lists the agent name, account, and registration date, with an
  Accept button. It renders nothing when there is nothing to claim or the
  lookup fails. Users must pick their own username before they see it, because
  the username gate comes first.
- `gs claims list` and `gs claims accept <agent-subject-id>` for humans who
  live in the terminal.

## Phase 3: Co-Owner Management

`12_account_auth.md` notes there is no membership administration API. A claimed
account needs at least:

- `ListAccountMembers(account)`;
- `ListApiKeys(subject)`, `CreateApiKey(name)`, and `RevokeApiKey(key_id)`.
  Owners of an account can list and revoke the keys of agent members of that
  account;
- `RemoveAccountMember(account, subject)`, with the rule below.

Authority rules:

- An agent subject can never remove or demote a human `owner`.
- A human owner can revoke agent keys and remove the agent from the account.
  Removing the agent keeps its authored history, which stays attributed to the
  agent subject.

## Security Notes

- **Wrong or hostile owner email.** An agent can name any email. The worst case
  is that the email's owner is offered co-ownership of an account they did not
  ask for. They are never granted it without an explicit accept, and the agent
  gains nothing over the victim.
- **Key theft.** A leaked `gsk_` key is full access to the agent's account until
  revoked. Keys are only shown once, are stored hashed, and are revocable in
  phase 3. Scopes and expiry are future work.
- **Enumeration.** `RegisterAgent` reveals whether a username is taken, which
  `CheckUsernameAvailable` already does for signed-in users. The per-IP limit
  bounds this.
- **Tokens in logs.** API keys follow the existing rule: never printed except by
  `gs auth token`.

## Implementation Order

1. Migration `0024_agent_signup.sql` (`agent_registrations`, `api_keys`).
2. Storage: `RegisterAgent` on `storage.AuthStore` (postgres + memory);
   `SubjectForToken` understands `gsk_` keys.
3. Proto + service + public-method allowlist + signup limiter + config flags.
4. CLI `gs auth register-agent`.
5. Phase 2: migration `0025_subject_emails.sql`, claim RPCs, Clerk Backend API
   lookup, personal-account fixes, `gs claims`, and the web card.
6. Phase 3 member and key management.
