alter table agent_conversation_events add column if not exists client_seq bigint not null default 0;

create unique index if not exists agent_conversation_events_client_seq_idx
	on agent_conversation_events(conversation_id, client_seq)
	where client_seq > 0;
