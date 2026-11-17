-- Scoped and child API keys (design/36 section 4).
alter table api_keys
	add column if not exists slice_ids text[],
	add column if not exists max_role text,
	add column if not exists parent_key_id text references api_keys(id) on delete cascade,
	add column if not exists purpose text not null default '',
	add column if not exists daemon_id text not null default '',
	add column if not exists conversation_id text not null default '';

alter table api_keys
	drop constraint if exists api_keys_max_role_check;

alter table api_keys
	add constraint api_keys_max_role_check
		check (max_role is null or max_role in ('reader', 'contributor', 'writer'));

create index if not exists idx_api_keys_parent_key_id
	on api_keys(parent_key_id);
