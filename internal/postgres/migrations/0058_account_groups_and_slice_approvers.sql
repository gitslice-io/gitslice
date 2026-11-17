-- Account groups (@account/name), slice group members, and slice approvers (design/36 sections 3 & 5).
create table if not exists account_groups(
	id text primary key,
	account_id text not null references accounts(id) on delete cascade,
	name text not null,
	description text not null default '',
	created_by_subject_id text references subjects(id),
	created_at timestamptz not null default now(),
	updated_at timestamptz not null default now(),
	unique(account_id, name)
);

create index if not exists idx_account_groups_account
	on account_groups(account_id);

create table if not exists account_group_members(
	group_id text not null references account_groups(id) on delete cascade,
	subject_id text not null references subjects(id) on delete cascade,
	added_by_subject_id text references subjects(id),
	created_at timestamptz not null default now(),
	primary key(group_id, subject_id)
);

create index if not exists idx_account_group_members_subject
	on account_group_members(subject_id);

create table if not exists slice_group_members(
	slice_id text not null references slices(id) on delete cascade,
	group_id text not null references account_groups(id) on delete cascade,
	role text not null check (role in ('reader', 'contributor', 'writer', 'admin')),
	added_by text not null default '',
	created_at timestamptz not null default now(),
	updated_at timestamptz not null default now(),
	primary key(slice_id, group_id)
);

create index if not exists idx_slice_group_members_group
	on slice_group_members(group_id);

alter table slices
	add column if not exists approvers_group text not null default '';

alter table slice_definition_versions
	add column if not exists approvers_group text not null default '';
