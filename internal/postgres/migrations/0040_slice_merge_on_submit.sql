-- merge_on_submit: when a changeset's base has gone stale, the server merges
-- it onto the latest base at submit (a new patchset that reruns CI) instead of
-- refusing it (design/07 section 7.4). It is a slice setting, not part of the
-- definition, so changing it makes no new definition version.
alter table slices add column if not exists merge_on_submit boolean not null default false;
