create table if not exists cli_login_sessions(
	code_hash text primary key,
	status text not null default 'pending',
	subject_id text references subjects(id),
	session_token text,
	created_at timestamptz not null default now(),
	expires_at timestamptz not null
);
