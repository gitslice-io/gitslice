-- The Git blob id of each file: SHA-1 of "blob <size>\0" and the bytes, which
-- is the id a Git client expects. Filled at upload and, for older blobs, by the
-- backfill (design/23_large_git_projection.md). Null until then.
alter table blobs add column if not exists git_blob_id text;

create index if not exists idx_blobs_git_blob_id on blobs(git_blob_id) where git_blob_id is not null;
