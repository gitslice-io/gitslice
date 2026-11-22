-- Agent onboarding improvements (design/40_agent_onboarding.md):
-- 1. Optional requested slice + role on self-registered agent claims so humans
--    can claim ownership and grant slice access in one action.
-- 2. Short-lived, single-use human-minted agent join invites ("gsi_...").

alter table agent_registrations
  add column if not exists requested_slice_account text not null default '',
  add column if not exists requested_slice_slug text not null default '',
  add column if not exists requested_slice_role text not null default '';

create table if not exists agent_invites (
  id text primary key,
  token_hash text not null unique,
  created_by_subject_id text not null references subjects(id) on delete cascade,
  slice_id text not null default '',
  slice_account text not null default '',
  slice_slug text not null default '',
  slice_role text not null default '',
  suggested_username text not null default '',
  expires_at timestamptz not null,
  consumed_at timestamptz,
  consumed_by_subject_id text,
  revoked_at timestamptz,
  created_at timestamptz not null default now()
);

create index if not exists idx_agent_invites_creator_created
  on agent_invites(created_by_subject_id, created_at desc);
