alter table slices
	add column if not exists required_approvals integer not null default 0;

alter table slices
	add column if not exists required_checks jsonb not null default '[]'::jsonb;

alter table patchsets
	add column if not exists submit_requirements jsonb not null default '{}'::jsonb;

alter table changesets
	add column if not exists submit_blocked_reason text not null default '';

create table if not exists approvals(
	changeset_id text not null references changesets(id),
	patchset_id text not null references patchsets(id),
	subject_id text not null references subjects(id),
	created_at timestamptz not null default now(),
	primary key(changeset_id, patchset_id, subject_id)
);

create table if not exists check_results(
	changeset_id text not null references changesets(id),
	patchset_id text not null references patchsets(id),
	check_name text not null,
	status text not null check (status in ('pass', 'fail')),
	reported_by text not null references subjects(id),
	created_at timestamptz not null default now(),
	updated_at timestamptz not null default now(),
	primary key(changeset_id, patchset_id, check_name)
);

create index if not exists idx_approvals_current_patchset
	on approvals(changeset_id, patchset_id);

create index if not exists idx_check_results_current_patchset
	on check_results(changeset_id, patchset_id);
