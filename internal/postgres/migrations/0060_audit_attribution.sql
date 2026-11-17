-- Audit attribution across changesets, patchsets, approvals, tags, and secret allowances (design/36 section 7)
alter table changesets
	add column if not exists acting_subject_id text not null default '',
	add column if not exists controller_subject_id text not null default '',
	add column if not exists key_id text not null default '',
	add column if not exists daemon_id text not null default '',
	add column if not exists conversation_id text not null default '',
	add column if not exists submitted_by_subject_id text not null default '',
	add column if not exists submitted_controller_subject_id text not null default '',
	add column if not exists submitted_key_id text not null default '',
	add column if not exists submitted_daemon_id text not null default '',
	add column if not exists submitted_conversation_id text not null default '';

update changesets
set acting_subject_id = author_subject_id
where acting_subject_id = '';

alter table patchsets
	add column if not exists acting_subject_id text not null default '',
	add column if not exists controller_subject_id text not null default '',
	add column if not exists key_id text not null default '',
	add column if not exists daemon_id text not null default '',
	add column if not exists conversation_id text not null default '';

update patchsets
set acting_subject_id = author_subject_id,
    conversation_id = coalesce(authoring_conversation_id, '')
where acting_subject_id = '';

alter table approvals
	add column if not exists acting_subject_id text not null default '',
	add column if not exists controller_subject_id text not null default '',
	add column if not exists key_id text not null default '',
	add column if not exists daemon_id text not null default '',
	add column if not exists conversation_id text not null default '';

update approvals
set acting_subject_id = subject_id
where acting_subject_id = '';

alter table slice_tags
	add column if not exists acting_subject_id text not null default '',
	add column if not exists controller_subject_id text not null default '',
	add column if not exists key_id text not null default '',
	add column if not exists daemon_id text not null default '',
	add column if not exists conversation_id text not null default '';

update slice_tags
set acting_subject_id = created_by
where acting_subject_id = '';

alter table patchset_secret_allowances
	add column if not exists acting_subject_id text not null default '',
	add column if not exists controller_subject_id text not null default '',
	add column if not exists key_id text not null default '',
	add column if not exists daemon_id text not null default '',
	add column if not exists conversation_id text not null default '';

update patchset_secret_allowances
set acting_subject_id = allowed_by_subject_id
where acting_subject_id = '';
