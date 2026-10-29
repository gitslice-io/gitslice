-- Triggers (design/34_triggers.md). A trigger run is one firing of one
-- trigger for one event. The unique index makes fan-out safe to repeat, so
-- an event never starts the same trigger twice.
create table if not exists trigger_runs(
	id text primary key,
	account_id text not null references accounts(id),
	trigger_id text not null,
	folder text not null,
	action text not null,
	event_id text not null,
	event_kind text not null,
	changeset_id text not null default '',
	patchset_id text not null default '',
	commit_id text not null default '',
	target_ref text not null default '',
	definition_commit_id text not null default '',
	spec jsonb not null,
	payload jsonb not null,
	state jsonb,
	status text not null,
	attempt integer not null default 0,
	max_attempts integer not null default 1,
	error text not null default '',
	response_status integer not null default 0,
	lease_until timestamptz,
	next_attempt_at timestamptz not null default now(),
	deadline timestamptz,
	created_at timestamptz not null default now(),
	started_at timestamptz,
	finished_at timestamptz,
	updated_at timestamptz not null default now()
);

create unique index if not exists trigger_runs_trigger_event on trigger_runs(account_id, trigger_id, event_id);

create index if not exists trigger_runs_due on trigger_runs(next_attempt_at) where status = 'queued';

create index if not exists trigger_runs_active on trigger_runs(account_id, status) where status in ('running', 'waiting');

create index if not exists trigger_runs_changeset on trigger_runs(changeset_id) where changeset_id <> '';

create index if not exists trigger_runs_trigger on trigger_runs(account_id, trigger_id, created_at desc);

create table if not exists trigger_run_logs(
	run_id text not null references trigger_runs(id) on delete cascade,
	seq bigint not null,
	stream text not null,
	chunk text not null,
	created_at timestamptz not null default now(),
	primary key (run_id, seq)
);

-- Checks a trigger reports carry the run, a link and annotations.
alter table check_runs add column if not exists trigger_run_id text;

alter table check_runs add column if not exists details_url text not null default '';

alter table check_runs add column if not exists annotations jsonb
