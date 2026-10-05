-- Organizations (design/12_account_auth.md 9.3): who created each one, so the
-- organizations a user created can be counted, a public profile per account, and
-- invitations, which become memberships only when the invitee accepts.
alter table accounts add column if not exists created_by_subject_id text;
alter table accounts add column if not exists display_name text not null default '';
alter table accounts add column if not exists description text not null default '';
alter table accounts add column if not exists website text not null default '';

create index if not exists idx_accounts_created_by on accounts(created_by_subject_id) where created_by_subject_id is not null;

create table if not exists account_invitations(
	account_id text not null references accounts(id),
	subject_id text not null references subjects(id),
	role text not null,
	invited_by_subject_id text not null references subjects(id),
	created_at timestamptz not null default now(),
	primary key(account_id, subject_id)
);

create index if not exists idx_account_invitations_subject on account_invitations(subject_id);
