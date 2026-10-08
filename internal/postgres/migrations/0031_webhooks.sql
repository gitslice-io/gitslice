-- Webhooks (design/24_webhooks.md). Events are recorded first, then fanned
-- out to the webhooks that want them as deliveries, which are retried until
-- they succeed or give up.
create table if not exists webhooks(
	id text primary key,
	slice_id text not null references slices(id) on delete cascade,
	url text not null,
	events jsonb not null default '[]',
	active boolean not null default true,
	secret text not null default '',
	created_by text not null default '',
	created_at timestamptz not null default now(),
	updated_at timestamptz not null default now()
);

create index if not exists idx_webhooks_slice on webhooks(slice_id);

create table if not exists webhook_events(
	id text primary key,
	kind text not null,
	slice_id text not null default '',
	actor_subject_id text not null default '',
	changeset_id text not null default '',
	patchset_id text not null default '',
	commit_id text not null default '',
	target_ref text not null default '',
	tag_name text not null default '',
	check_run_id text not null default '',
	changed_paths jsonb not null default '[]',
	data text not null default '',
	created_at timestamptz not null default now(),
	lease_until timestamptz,
	fanned_out_at timestamptz
);

create index if not exists idx_webhook_events_pending on webhook_events(created_at) where fanned_out_at is null;

create table if not exists webhook_deliveries(
	id text primary key,
	webhook_id text not null references webhooks(id) on delete cascade,
	event_id text not null,
	event text not null,
	status text not null default 'pending',
	attempts integer not null default 0,
	response_status integer not null default 0,
	error text not null default '',
	request_body bytea not null,
	response_body text not null default '',
	duration_ms bigint not null default 0,
	created_at timestamptz not null default now(),
	delivered_at timestamptz,
	next_attempt_at timestamptz not null default now(),
	lease_until timestamptz
);

create index if not exists idx_webhook_deliveries_due on webhook_deliveries(next_attempt_at) where status = 'pending';
create index if not exists idx_webhook_deliveries_webhook on webhook_deliveries(webhook_id, created_at desc);
