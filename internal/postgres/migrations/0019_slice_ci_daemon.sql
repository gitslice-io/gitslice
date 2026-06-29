alter table slices
	add column if not exists ci_daemon_id text;

create index if not exists check_runs_daemon_status_idx
	on check_runs(daemon_id, status)
	where superseded_by_run_id is null and daemon_id is not null;
