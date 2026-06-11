create table if not exists slice_included_paths(
	slice_id text not null references slices(id) on delete cascade,
	prefix text not null,
	primary key(slice_id, prefix)
);

create index if not exists idx_slice_included_paths_prefix
	on slice_included_paths(prefix);

insert into slice_included_paths(slice_id, prefix)
select slices.id, included.prefix
from slices
cross join lateral jsonb_array_elements_text(slices.included_paths) as included(prefix)
on conflict do nothing;
