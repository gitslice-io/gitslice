create table if not exists fs_entities(
	account_id text not null references accounts(id),
	entity_id text not null,
	kind text not null,
	created_commit_id text references commits(id),
	deleted_commit_id text references commits(id),
	created_at timestamptz not null default now(),
	primary key(account_id, entity_id)
);

create table if not exists current_path_entities(
	target_ref text not null references refs(name),
	path text not null,
	account_id text not null,
	entity_id text not null,
	kind text not null,
	content_hash text,
	mode integer,
	updated_at timestamptz not null default now(),
	primary key(target_ref, path),
	foreign key(account_id, entity_id) references fs_entities(account_id, entity_id)
);

create index if not exists idx_current_path_entities_ref_prefix
	on current_path_entities(target_ref, path);

create index if not exists idx_current_path_entities_ref_entity
	on current_path_entities(target_ref, account_id, entity_id);

create table if not exists commit_entity_changes(
	target_ref text not null references refs(name),
	commit_id text not null references commits(id),
	account_id text not null,
	entity_id text not null,
	kind text not null,
	path text not null,
	old_path text,
	change_kind text not null,
	source text not null default 'explicit',
	confidence integer not null default 100,
	content_hash text,
	mode integer,
	committed_at timestamptz not null,
	primary key(target_ref, commit_id, account_id, entity_id, path, change_kind),
	foreign key(account_id, entity_id) references fs_entities(account_id, entity_id)
);

create index if not exists idx_commit_entity_changes_ref_entity_time
	on commit_entity_changes(target_ref, account_id, entity_id, committed_at desc, commit_id);

create index if not exists idx_commit_entity_changes_ref_path_time
	on commit_entity_changes(target_ref, path, committed_at desc, commit_id);

insert into fs_entities(account_id, entity_id, kind, created_commit_id, created_at)
select accounts.id,
       'ent_migrated_' || md5(path_heads.path),
       case
         when path_heads.content_hash is null then 'directory'
         else 'file'
       end,
       refs.commit_id,
       now()
from path_heads
join accounts on accounts.slug = split_part(trim(leading '/' from path_heads.path), '/', 1)
cross join refs
where refs.name = 'refs/global/main'
  and path_heads.exists
on conflict do nothing;

insert into current_path_entities(target_ref, path, account_id, entity_id, kind, content_hash, mode)
select refs.name,
       path_heads.path,
       accounts.id,
       'ent_migrated_' || md5(path_heads.path),
       case
         when path_heads.content_hash is null then 'directory'
         else 'file'
       end,
       path_heads.content_hash,
       path_heads.mode
from path_heads
join accounts on accounts.slug = split_part(trim(leading '/' from path_heads.path), '/', 1)
cross join refs
where refs.name = 'refs/global/main'
  and path_heads.exists
on conflict do nothing;
