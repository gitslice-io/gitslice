alter table agent_conversation_events add column if not exists item_id text not null default '';

create index if not exists agent_conversation_events_item_idx
	on agent_conversation_events(conversation_id, item_id)
	where item_id <> '';
