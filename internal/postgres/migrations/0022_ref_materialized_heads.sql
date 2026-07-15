create table if not exists ref_materialized_heads (
  target_ref text primary key references refs(name),
  commit_id text not null,
  updated_at timestamptz not null default now()
);
