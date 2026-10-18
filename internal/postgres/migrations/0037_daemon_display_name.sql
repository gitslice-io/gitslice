-- A daemon registers under the name it was started with (gs agent start --name),
-- and a restart finds the same row by that name. label is what its owner renamed
-- it to. It is shown in place of name and is never used to find the row.
alter table agent_daemons add column if not exists label text not null default '';
