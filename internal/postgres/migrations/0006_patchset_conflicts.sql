alter table patchsets
	add column if not exists conflicts jsonb not null default '[]'::jsonb;

alter table patchsets
	add column if not exists kind text not null default '';
