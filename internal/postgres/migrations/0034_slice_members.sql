-- Slice members (design/26_slice_members.md): people and agents given a role
-- on one slice without joining the account that owns it.
create table if not exists slice_members(
	slice_id text not null references slices(id) on delete cascade,
	subject_id text not null references subjects(id) on delete cascade,
	role text not null,
	added_by text not null default '',
	created_at timestamptz not null default now(),
	primary key (slice_id, subject_id)
);

create index if not exists idx_slice_members_subject on slice_members(subject_id)
