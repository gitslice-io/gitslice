alter table changesets
	add column if not exists base_kind text not null default 'commit';
