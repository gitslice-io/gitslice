create table if not exists slice_definition_versions(
	slice_id text not null references slices(id) on delete cascade,
	version bigint not null,
	definition_hash text not null,
	visibility text not null,
	included_paths jsonb not null,
	required_approvals integer not null default 0,
	required_checks jsonb not null default '[]'::jsonb,
	created_at timestamptz not null default now(),
	created_by text,
	primary key(slice_id, version)
);

create index if not exists idx_slice_definition_versions_newest
	on slice_definition_versions(slice_id, version desc);

insert into slice_definition_versions(
	slice_id,
	version,
	definition_hash,
	visibility,
	included_paths,
	required_approvals,
	required_checks,
	created_at,
	created_by
)
select
	id,
	version,
	definition_hash,
	visibility,
	included_paths,
	required_approvals,
	required_checks,
	updated_at,
	null
from slices
on conflict do nothing;
