-- Immutable release tags per slice (design/21_self_hosting.md). The Git
-- projection publishes each as refs/tags/<name>.
create table if not exists slice_tags(
	slice_id text not null references slices(id),
	name text not null,
	commit_id text not null references commits(id),
	definition_version bigint not null,
	message text not null default '',
	created_by text not null,
	created_at timestamptz not null default now(),
	primary key(slice_id, name)
)
