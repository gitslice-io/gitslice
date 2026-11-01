-- Account pages sort slices by when a changeset authored from each one last
-- landed (slices.last_activity_at in listings).
create index if not exists changesets_slice_landed on changesets(authoring_slice_id, updated_at) where status = 'submitted';
