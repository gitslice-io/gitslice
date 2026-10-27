-- Secrets belong to an account and apply to some of its slices, or to all of
-- them (design/33_secrets.md). secret_slices copies the secret's name so that
-- the database itself keeps a slice from having two secrets of one name: a
-- name never changes, so the copy cannot drift.
create table if not exists secrets(
	id text primary key,
	account_id text not null references accounts(id),
	name text not null,
	value text not null,
	all_slices boolean not null default false,
	created_by text not null default '',
	updated_by text not null default '',
	created_at timestamptz not null default now(),
	updated_at timestamptz not null default now()
);

create index if not exists idx_secrets_account on secrets(account_id, name);

create unique index if not exists secrets_account_all_slices_name on secrets(account_id, name) where all_slices;

create table if not exists secret_slices(
	secret_id text not null references secrets(id) on delete cascade,
	slice_id text not null references slices(id) on delete cascade,
	name text not null,
	primary key (secret_id, slice_id)
);

create unique index if not exists secret_slices_slice_name on secret_slices(slice_id, name);
