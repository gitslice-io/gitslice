create table if not exists changeset_stacks(
	id text primary key,
	authoring_account text not null,
	authoring_slice text not null,
	authoring_slice_id text not null references slices(id),
	target_ref text not null references refs(name),
	base_commit_id text not null,
	title text not null,
	status text not null,
	active_entry_changeset_id text references changesets(id),
	root_entry_changeset_id text references changesets(id),
	created_by text not null references subjects(id),
	created_at timestamptz not null default now(),
	updated_at timestamptz not null default now()
);

create table if not exists changeset_stack_entries(
	stack_id text not null references changeset_stacks(id),
	changeset_id text not null references changesets(id),
	parent_changeset_id text references changesets(id),
	parent_patchset_id text references patchsets(id),
	sibling_order bigint not null,
	display_order bigint not null,
	depth bigint not null,
	state text not null,
	created_at timestamptz not null default now(),
	updated_at timestamptz not null default now(),
	primary key(stack_id, changeset_id),
	unique(stack_id, parent_changeset_id, sibling_order),
	unique(stack_id, display_order)
);

alter table changesets
	add column if not exists stack_id text references changeset_stacks(id),
	add column if not exists stack_order bigint,
	add column if not exists stack_depth bigint,
	add column if not exists sibling_order bigint,
	add column if not exists parent_changeset_id text references changesets(id),
	add column if not exists parent_patchset_id text references patchsets(id),
	add column if not exists base_kind text not null default 'commit';

alter table patchsets
	add column if not exists base_kind text not null default 'commit',
	add column if not exists base_patchset_id text references patchsets(id),
	add column if not exists base_tree_id text,
	add column if not exists result_tree_id text,
	add column if not exists stack_parent_patchset_id text references patchsets(id);

create unique index if not exists idx_changeset_stack_one_root
	on changeset_stack_entries(stack_id)
	where parent_changeset_id is null;
create index if not exists idx_changeset_stack_entries_parent
	on changeset_stack_entries(parent_changeset_id);
create index if not exists idx_changeset_stack_entries_parent_patchset
	on changeset_stack_entries(parent_patchset_id);
create index if not exists idx_changeset_stack_entries_order
	on changeset_stack_entries(stack_id, display_order);
