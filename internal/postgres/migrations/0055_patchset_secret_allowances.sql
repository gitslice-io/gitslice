create table if not exists patchset_secret_allowances(
	changeset_id text not null references changesets(id) on delete cascade,
	patchset_id text not null,
	allowed_by_subject_id text not null references subjects(id),
	created_at timestamptz not null default now(),
	primary key(changeset_id, patchset_id)
);
