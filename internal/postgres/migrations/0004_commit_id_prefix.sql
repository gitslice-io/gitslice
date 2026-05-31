create index if not exists idx_commits_id_prefix
on commits (id text_pattern_ops);
