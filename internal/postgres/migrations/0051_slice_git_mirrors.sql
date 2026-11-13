-- A slice's Git mirror: the server pushes the slice's projection to a
-- repository elsewhere (design/35_git_mirrors.md). token_secret names an
-- account secret. The value is never stored here.
create table if not exists slice_git_mirrors(
	slice_id text primary key references slices(id) on delete cascade,
	url text not null,
	branch text not null default 'main',
	username text not null default '',
	token_secret text not null default '',
	created_by text not null default '',
	created_at timestamptz not null default now(),
	updated_at timestamptz not null default now(),
	-- The mirror is pending while pending_seq > pushed_seq.
	pending_seq bigint not null default 1,
	pushed_seq bigint not null default 0,
	next_attempt_at timestamptz not null default now(),
	lease_until timestamptz,
	failures integer not null default 0,
	last_attempt_at timestamptz,
	last_pushed_at timestamptz,
	last_pushed_commit text not null default '',
	last_pushed_native text not null default '',
	last_error text not null default ''
);

create index if not exists slice_git_mirrors_due on slice_git_mirrors(next_attempt_at) where pending_seq > pushed_seq;
