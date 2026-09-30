-- Agent self-registration and long-lived API keys.
-- See design/20_agent_signup_and_claim.md.

-- One row per self-registered agent subject: the personal account it created
-- and the (unverified) email of the human allowed to claim co-ownership.
create table if not exists agent_registrations(
	subject_id text primary key references subjects(id),
	account_id text not null references accounts(id),
	owner_email text not null,
	created_at timestamptz not null default now(),
	claimed_at timestamptz,
	claimed_by_subject_id text references subjects(id)
);
create index if not exists agent_registrations_unclaimed_email_idx
	on agent_registrations(owner_email) where claimed_at is null;

-- Long-lived bearer credentials ("gsk_..."), stored as sha256 hashes. Kept apart
-- from sessions so they can be named, listed, and revoked independently.
create table if not exists api_keys(
	id text primary key,
	subject_id text not null references subjects(id),
	name text not null,
	token_hash text unique not null,
	created_at timestamptz not null default now(),
	last_used_at timestamptz,
	expires_at timestamptz,
	revoked_at timestamptz
);
create index if not exists api_keys_subject_idx on api_keys(subject_id);
