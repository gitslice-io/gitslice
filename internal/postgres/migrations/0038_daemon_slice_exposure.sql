-- A daemon runs the checks of the slices that name it and receives their secrets,
-- so its owner chooses which slices it is exposed to. A slice's owners and admins
-- may then pick any daemon exposed to the slice as its CI daemon.
create table if not exists daemon_slice_exposures(
	daemon_id text not null references agent_daemons(id) on delete cascade,
	slice_id text not null references slices(id) on delete cascade,
	exposed_by text not null,
	created_at timestamptz not null default now(),
	primary key (daemon_id, slice_id)
);

create index if not exists daemon_slice_exposures_slice_idx on daemon_slice_exposures(slice_id);

-- Slices that already name a CI daemon keep working: that is an exposure.
insert into daemon_slice_exposures(daemon_id, slice_id, exposed_by)
select slices.ci_daemon_id, slices.id, 'migration'
from slices
join agent_daemons on agent_daemons.id = slices.ci_daemon_id
on conflict do nothing;
