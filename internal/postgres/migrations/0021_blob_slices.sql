create table if not exists blob_slices (
  content_hash text not null,
  slice_id text not null,
  created_at timestamptz not null default now(),
  primary key (content_hash, slice_id)
);

create index if not exists blob_slices_slice_id on blob_slices(slice_id);
