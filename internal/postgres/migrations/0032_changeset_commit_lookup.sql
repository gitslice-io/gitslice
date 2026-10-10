-- Find the changeset a commit landed as from a prefix of the commit id: a
-- commit id pasted where a changeset id belongs opens that changeset.
create index if not exists idx_changesets_commit_id_prefix on changesets(commit_id text_pattern_ops) where commit_id is not null;
