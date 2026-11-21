-- Reusable automation templates (template_id, template_commit_id) and
-- multi-step pipelines (parent_run_id, step_index, step_id, outputs).
alter table trigger_runs add column if not exists template_id text not null default '';
alter table trigger_runs add column if not exists template_commit_id text not null default '';
alter table trigger_runs add column if not exists parent_run_id text not null default '';
alter table trigger_runs add column if not exists step_index integer not null default 0;
alter table trigger_runs add column if not exists step_id text not null default '';
alter table trigger_runs add column if not exists outputs jsonb not null default '{}'::jsonb;

create index if not exists trigger_runs_parent_idx on trigger_runs (parent_run_id, step_index)
  where parent_run_id <> '';
