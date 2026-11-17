-- Advisory path intents (design/36 section 6)
create table if not exists path_intents(
	id text primary key,
	slice_id text not null references slices(id) on delete cascade,
	account_slug text not null,
	slice_slug text not null,
	subject_id text not null references subjects(id) on delete cascade,
	pattern text not null,
	message text not null default '',
	changeset_id text references changesets(id) on delete set null,
	expires_at timestamptz not null,
	created_at timestamptz not null default now()
);

create index if not exists path_intents_slice_expires_idx
	on path_intents(slice_id, expires_at);

create index if not exists path_intents_changeset_idx
	on path_intents(changeset_id) where changeset_id is not null;
