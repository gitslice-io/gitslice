-- Indexes covering hot read/GC/auth/trigger paths identified in PERF-SRV-06.
create index if not exists idx_path_heads_content_hash
	on path_heads(content_hash, path)
	where content_hash is not null;

create index if not exists idx_current_path_entities_triggers_yaml
	on current_path_entities(target_ref, path)
	where path like '%/.gitslice/triggers.yaml';

create index if not exists idx_changesets_commit_id
	on changesets(commit_id)
	where commit_id is not null;

create index if not exists idx_account_memberships_subject_account
	on account_memberships(subject_id, account_id);

create index if not exists idx_agent_registrations_account_id
	on agent_registrations(account_id);

create index if not exists idx_sessions_subject_id
	on sessions(subject_id);
