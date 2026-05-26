create table if not exists commit_changed_paths(
	target_ref text not null references refs(name),
	commit_id text not null references commits(id),
	path text not null,
	change_kind text not null default 'modified',
	committed_at timestamptz not null,
	primary key(target_ref, commit_id, path)
);

create index if not exists idx_commit_changed_paths_ref_path_time on commit_changed_paths(target_ref, path, committed_at desc, commit_id);
create index if not exists idx_commit_changed_paths_path_time on commit_changed_paths(path, committed_at desc, commit_id);

insert into commit_changed_paths(target_ref, commit_id, path, committed_at)
select refs.name, commits.id, changed.path, commits.created_at
from refs
cross join commits
cross join lateral jsonb_array_elements_text(commits.changed_paths) as changed(path)
where refs.name = 'refs/global/main'
and not exists (
	select 1
	from commit_changed_paths indexed
	where indexed.target_ref = refs.name
	  and indexed.commit_id = commits.id
	  and indexed.path = changed.path
)
on conflict do nothing;
