create table if not exists outbox(
	id bigserial primary key,
	kind text not null,
	payload jsonb not null,
	created_at timestamptz not null default now(),
	processed_at timestamptz,
	attempts integer not null default 0
);

create index if not exists idx_outbox_processed_id on outbox(processed_at, id);
