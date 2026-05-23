create table if not exists accounts(
	id text primary key,
	slug text unique not null,
	kind text not null,
	created_at timestamptz not null default now(),
	updated_at timestamptz not null default now()
);

create table if not exists subjects(
	id text primary key,
	kind text not null,
	external_provider text,
	external_subject text,
	display_name text not null,
	created_at timestamptz not null default now()
);

create table if not exists account_memberships(
	account_id text not null references accounts(id),
	subject_id text not null references subjects(id),
	role text not null,
	created_at timestamptz not null default now(),
	primary key(account_id, subject_id, role)
);

create table if not exists sessions(
	id text primary key,
	subject_id text not null references subjects(id),
	token_hash text unique not null,
	expires_at timestamptz not null,
	revoked_at timestamptz,
	created_at timestamptz not null default now()
);

create table if not exists refs(
	name text primary key,
	commit_id text not null,
	version bigint not null default 0,
	updated_at timestamptz not null default now(),
	updated_by text
);

create table if not exists commits(
	id text primary key,
	parent_ids jsonb not null,
	root_tree_id text not null,
	author_subject_id text,
	message text not null,
	created_at timestamptz not null,
	changed_paths jsonb not null,
	metadata jsonb not null default '{}'::jsonb
);

create table if not exists commit_files(
	commit_id text not null references commits(id),
	path text not null,
	blob_id text not null,
	content_hash text not null,
	mode integer not null,
	size bigint not null,
	primary key(commit_id, path)
);

create table if not exists blobs(
	id text primary key,
	content_hash text unique not null,
	size bigint not null,
	storage_location text not null,
	state text not null,
	created_at timestamptz not null default now()
);

create table if not exists slices(
	id text primary key,
	account_id text not null references accounts(id),
	slug text not null,
	version bigint not null,
	definition_hash text not null,
	visibility text not null,
	included_paths jsonb not null,
	created_at timestamptz not null default now(),
	updated_at timestamptz not null default now(),
	unique(account_id, slug)
);

create table if not exists changesets(
	id text primary key,
	authoring_account text not null,
	authoring_slice text not null,
	authoring_slice_id text not null references slices(id),
	author_subject_id text not null references subjects(id),
	target_ref text not null references refs(name),
	base_commit_id text not null,
	title text not null,
	description text not null,
	status text not null,
	affected_paths jsonb not null,
	current_patchset_id text,
	current_patchset_number bigint not null default 0,
	commit_id text,
	created_at timestamptz not null default now(),
	updated_at timestamptz not null default now()
);

create table if not exists patchsets(
	id text primary key,
	changeset_id text not null references changesets(id),
	number bigint not null,
	base_commit_id text not null,
	author_subject_id text not null references subjects(id),
	file_edits jsonb not null,
	changed_paths jsonb not null,
	coverage jsonb not null,
	path_bases jsonb not null,
	read_set jsonb not null,
	write_set jsonb not null,
	created_at timestamptz not null default now(),
	unique(changeset_id, number)
);

create index if not exists idx_sessions_token_hash on sessions(token_hash) where revoked_at is null;
create index if not exists idx_slices_account_slug on slices(account_id, slug);
create index if not exists idx_commit_files_path on commit_files(commit_id, path);
create index if not exists idx_changesets_target_status on changesets(target_ref, status);
create index if not exists idx_patchsets_changeset_number on patchsets(changeset_id, number desc);
create index if not exists idx_blobs_content_hash on blobs(content_hash);
