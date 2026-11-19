-- Account-level CI daemon pool: checks and folder triggers in any slice of an
-- account run on these daemons unless the slice specifies its own override.
create table if not exists account_ci_daemons(
	account_id text not null references accounts(id) on delete cascade,
	daemon_id text not null,
	position integer not null default 0,
	added_at timestamptz not null default now(),
	primary key (account_id, daemon_id)
);

create index if not exists account_ci_daemons_daemon on account_ci_daemons(daemon_id);
