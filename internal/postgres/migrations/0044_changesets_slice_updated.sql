-- A slice's changesets list pages by (updated_at, id), most recent first, with
-- an optional status: this index serves the paging without sorting the slice's
-- changesets each time.
create index if not exists idx_changesets_slice_updated on changesets(authoring_account, authoring_slice, updated_at desc, id desc);
