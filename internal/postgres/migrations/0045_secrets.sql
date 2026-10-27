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

-- Each slice secret becomes a secret scoped to its one slice, once: migrations
-- run at every start, and a secret deleted later must stay deleted. Values
-- are copied as stored (sealed, or legacy plaintext). slice_secrets stays,
-- unread, so that a rollback still finds its data.
create table if not exists secret_migrations(
	name text primary key,
	done_at timestamptz not null default now()
);

insert into secrets(id, account_id, name, value, all_slices, created_by, updated_by, created_at, updated_at)
select 'sec_' || md5(ss.slice_id || '/' || ss.name), s.account_id, ss.name, ss.value, false, 'migration', 'migration', ss.created_at, ss.updated_at
from slice_secrets ss
join slices s on s.id = ss.slice_id and s.deleted_at is null
where not exists (select 1 from secret_migrations where name = 'slice_secrets')
on conflict do nothing;

insert into secret_slices(secret_id, slice_id, name)
select 'sec_' || md5(ss.slice_id || '/' || ss.name), ss.slice_id, ss.name
from slice_secrets ss
join slices s on s.id = ss.slice_id and s.deleted_at is null
where not exists (select 1 from secret_migrations where name = 'slice_secrets')
on conflict do nothing;

insert into secret_migrations(name) values ('slice_secrets') on conflict do nothing
