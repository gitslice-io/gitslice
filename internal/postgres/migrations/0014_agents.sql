create table if not exists agent_daemons(
	id text primary key,
	subject_id text not null references subjects(id),
	account text not null,
	name text not null default '',
	runtime text not null default 'codex',
	version text not null default '',
	status text not null default 'offline',
	last_seen_at timestamptz not null default now(),
	created_at timestamptz not null default now()
);

create index if not exists agent_daemons_subject_idx on agent_daemons(subject_id);

create table if not exists agent_conversations(
	id text primary key,
	daemon_id text not null references agent_daemons(id),
	subject_id text not null references subjects(id),
	slice_id text not null,
	account text not null default '',
	slice_name text not null default '',
	title text not null default '',
	status text not null default 'active',
	workspace_subdir text not null default '',
	next_seq bigint not null default 1,
	created_at timestamptz not null default now(),
	updated_at timestamptz not null default now()
);

create index if not exists agent_conversations_slice_idx on agent_conversations(slice_id);

create index if not exists agent_conversations_daemon_idx on agent_conversations(daemon_id);

create table if not exists agent_conversation_events(
	id text primary key,
	conversation_id text not null references agent_conversations(id),
	seq bigint not null,
	role text not null,
	type text not null,
	text text not null default '',
	data_json text not null default '',
	created_at timestamptz not null default now(),
	unique(conversation_id, seq)
);

create index if not exists agent_conversation_events_conv_idx on agent_conversation_events(conversation_id, seq);
