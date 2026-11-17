alter table agent_conversations
	add column if not exists role text not null default 'contributor';

alter table agent_conversations
	add constraint agent_conversations_role_check
	check (role in ('reader', 'contributor', 'writer'));
