-- Blame snapshots (design/27_blame.md): the blame of a file's version at the
-- commit that last changed it, gzipped JSON. Derived data, safe to delete.
create table if not exists blame_snapshots(
	commit_id text not null,
	path text not null,
	data bytea not null,
	created_at timestamptz not null default now(),
	primary key (commit_id, path)
)
