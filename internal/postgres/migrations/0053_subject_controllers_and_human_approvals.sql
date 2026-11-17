-- Subject controllers and required human approvals (design/36 section 3).
alter table subjects
	add column if not exists controller_subject_id text references subjects(id);

update subjects
set controller_subject_id = id
where kind = 'user'
  and controller_subject_id is null;

update subjects s
set controller_subject_id = r.claimed_by_subject_id
from agent_registrations r
where r.subject_id = s.id
  and r.claimed_by_subject_id is not null
  and s.controller_subject_id is null;

create index if not exists idx_subjects_controller_subject_id
	on subjects(controller_subject_id)
	where controller_subject_id is not null;

alter table slices
	add column if not exists required_human_approvals integer not null default 0;

alter table slice_definition_versions
	add column if not exists required_human_approvals integer not null default 0;
