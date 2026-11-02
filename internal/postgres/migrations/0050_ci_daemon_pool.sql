-- A slice's pool of CI daemons, and claims on check runs
-- (design/32_ci_dispatch_on_cloudflare.md, layer 3).
-- slices.ci_daemon_id stays the first daemon of the pool, for older readers.
create table if not exists slice_ci_daemons(
	slice_id text not null references slices(id) on delete cascade,
	daemon_id text not null,
	position integer not null default 0,
	added_at timestamptz not null default now(),
	primary key (slice_id, daemon_id)
);

create index if not exists slice_ci_daemons_daemon on slice_ci_daemons(daemon_id);

insert into slice_ci_daemons(slice_id, daemon_id, position)
select id, ci_daemon_id, 0 from slices
where ci_daemon_id is not null and ci_daemon_id <> ''
on conflict do nothing;

-- claimed_by is the daemon process (RegisterDaemon.instance_id) a run was
-- sent to, and lease_expires_at when the claim lapses unless that process
-- shows signs of life. A lapsed claim on a queued run frees it for another
-- process, and on a running run fails it and queues another attempt.
alter table check_runs add column if not exists claimed_by text;
alter table check_runs add column if not exists lease_expires_at timestamptz;

create index if not exists check_runs_claims on check_runs(claimed_by) where claimed_by is not null and status in ('queued', 'running');
create index if not exists check_runs_leases on check_runs(lease_expires_at) where lease_expires_at is not null and status in ('queued', 'running');

-- standby_until: the daemon waits on the doorbell (design/32 layer 1) and
-- counts as online until then without polling.
alter table agent_daemons add column if not exists standby_until timestamptz;
