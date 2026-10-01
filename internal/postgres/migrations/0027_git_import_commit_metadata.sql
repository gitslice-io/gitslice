-- Original Git metadata for imported commits (design/21_self_hosting.md): the
-- importing subject publishes each commit, so the Git author, author date and
-- full message are kept here and surfaced on Commit.git_import.
alter table git_import_commits add column if not exists author_name text not null default '';
alter table git_import_commits add column if not exists author_email text not null default '';
alter table git_import_commits add column if not exists authored_at timestamptz;
alter table git_import_commits add column if not exists full_message text not null default '';
create index if not exists idx_git_import_commits_native on git_import_commits(native_commit_id)
