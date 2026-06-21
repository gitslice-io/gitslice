alter table patchsets add column if not exists authoring_conversation_id text;

alter table patchsets add column if not exists authoring_conversation_seq bigint not null default 0;

create index if not exists patchsets_conversation_idx on patchsets(authoring_conversation_id);
