create table if not exists packed_objects (
	object_key text primary key,
	pack_key text not null,
	byte_offset bigint not null,
	byte_length bigint not null,
	codec text not null default 'raw',
	raw_length bigint not null,
	created_at timestamptz not null default now()
);

create index if not exists idx_packed_objects_pack_key
	on packed_objects(pack_key);
