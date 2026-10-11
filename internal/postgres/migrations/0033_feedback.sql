-- Feedback about Gitslice from people and agents (design/25_feedback.md).
create table if not exists feedback(
	id text primary key,
	subject_id text not null,
	account text not null default '',
	body text not null,
	category text not null default 'other',
	context jsonb not null default '{}',
	status text not null default 'open',
	resolution text not null default '',
	resolved_by text not null default '',
	resolved_at timestamptz,
	created_at timestamptz not null default now()
);

create index if not exists idx_feedback_created on feedback(created_at desc, id desc);
create index if not exists idx_feedback_status_created on feedback(status, created_at desc, id desc);
create index if not exists idx_feedback_subject_created on feedback(subject_id, created_at desc, id desc);
