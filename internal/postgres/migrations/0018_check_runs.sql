create table if not exists check_runs(
	id text primary key,
	changeset_id text not null,
	patchset_id text not null,
	check_name text not null,
	daemon_id text,
	provenance text not null default 'self' check (provenance in ('self', 'ci')),
	attempt integer not null default 1,
	superseded_by_run_id text references check_runs(id),
	status text not null check (status in ('queued', 'running', 'passed', 'failed', 'errored', 'skipped', 'canceled')),
	exit_code integer,
	summary text not null default '',
	started_at timestamptz,
	finished_at timestamptz,
	duration_ms bigint,
	created_at timestamptz not null default now(),
	updated_at timestamptz not null default now()
);

create index if not exists check_runs_patchset_check_idx on check_runs(patchset_id, check_name);

create index if not exists check_runs_changeset_idx on check_runs(changeset_id);

create table if not exists check_run_logs(
	id text primary key,
	run_id text not null references check_runs(id),
	seq bigint not null,
	stream text not null check (stream in ('stdout', 'stderr')),
	chunk text not null,
	created_at timestamptz not null default now(),
	unique(run_id, seq)
);
