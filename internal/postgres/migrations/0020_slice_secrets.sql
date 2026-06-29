create table if not exists slice_secrets (
  slice_id text not null,
  name text not null,
  value text not null,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  primary key (slice_id, name)
);
