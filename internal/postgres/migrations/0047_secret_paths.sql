-- A secret applies to folders and everything below them, instead of to
-- slices (design/33_secrets.md). secret_paths copies the secret's name so that
-- the database itself keeps a folder from having two secrets of one name: a
-- name never changes, so the copy cannot drift.
create table if not exists secret_paths(
	secret_id text not null references secrets(id) on delete cascade,
	path text not null,
	name text not null,
	primary key (secret_id, path)
);

create unique index if not exists secret_paths_path_name on secret_paths(path, name);

-- Convert the old scopes. Migrations run at every start, so each insert only
-- touches secrets with no path yet: those written before this change, since
-- a secret written since always has one. An all-slices secret gets its
-- account root, and a slice-listed one gets the included paths of its slices.
-- secrets.all_slices and secret_slices stay, unused, so the previous server
-- can still run after a rollback.
insert into secret_paths(secret_id, path, name)
select secrets.id, '/' || accounts.slug, secrets.name
from secrets join accounts on accounts.id = secrets.account_id
where secrets.all_slices
  and not exists (select 1 from secret_paths existing where existing.secret_id = secrets.id)
on conflict do nothing;

insert into secret_paths(secret_id, path, name)
select distinct secret_slices.secret_id, slice_included_paths.prefix, secret_slices.name
from secret_slices
join secrets on secrets.id = secret_slices.secret_id
join slices on slices.id = secret_slices.slice_id and slices.deleted_at is null
join slice_included_paths on slice_included_paths.slice_id = secret_slices.slice_id
where not secrets.all_slices
  and not exists (select 1 from secret_paths existing where existing.secret_id = secrets.id)
on conflict do nothing
