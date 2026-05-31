alter table changesets add column if not exists number bigint;

with numbered as (
	select
		id,
		row_number() over (
			partition by authoring_slice_id
			order by created_at, id
		) as number
	from changesets
)
update changesets c
set number = numbered.number
from numbered
where c.id = numbered.id and c.number is null;

alter table changesets alter column number set not null;

create unique index if not exists idx_changesets_authoring_slice_number
	on changesets(authoring_slice_id, number);
