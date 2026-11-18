alter table changesets
	add column if not exists submit_blocked_code text not null default '';

update changesets
set submit_blocked_code = case
	when submit_blocked_reason = 'BlockedOnBaseChangeset' then 'blocked_on_parent'
	when submit_blocked_reason ilike '%path base conflict%' then 'stale_path_base'
	when submit_blocked_reason ilike '%requirements changed%'
	  or submit_blocked_reason ilike '%outside latest slice definition%' then 'requirements_changed'
	when submit_blocked_reason like 'required approvals %'
	  or submit_blocked_reason like 'required human approvals %'
	  or submit_blocked_reason like 'path owner%'
	  or submit_blocked_reason like '.gitslice%' then 'approvals_missing'
	when submit_blocked_reason like 'required check %' then 'checks_missing'
	else ''
end
where submit_blocked_code = ''
  and submit_blocked_reason <> '';
