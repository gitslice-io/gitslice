-- Changeset original author and collaborators (design/36 section 2).
alter table changesets
	add column if not exists original_author_subject_id text references subjects(id);

create table if not exists changeset_collaborators(
	changeset_id text not null references changesets(id) on delete cascade,
	subject_id text not null references subjects(id) on delete cascade,
	added_by text not null default '',
	created_at timestamptz not null default now(),
	primary key (changeset_id, subject_id)
);

create index if not exists idx_changeset_collaborators_subject
	on changeset_collaborators(subject_id);
