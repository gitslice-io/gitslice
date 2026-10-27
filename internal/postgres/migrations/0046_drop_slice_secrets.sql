-- Secrets moved to secrets and secret_slices (0045, design/33_secrets.md).
-- Production had no slice secrets when they moved, so the old table and the
-- marker of its one-time copy go without a copy.
drop table if exists slice_secrets;

drop table if exists secret_migrations
