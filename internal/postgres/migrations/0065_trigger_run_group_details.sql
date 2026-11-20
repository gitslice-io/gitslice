-- Automation runs: a concurrency group shared by automations that must not
-- run at once (concurrency_group), and a link to an outside page about the
-- run (details_url, set by gs.setDetailsUrl).
alter table trigger_runs add column if not exists concurrency_group text not null default '';

alter table trigger_runs add column if not exists details_url text not null default '';

create index if not exists trigger_runs_open_group_idx on trigger_runs (account_id, concurrency_group, created_at)
  where concurrency_group <> '' and status in ('queued', 'running', 'waiting');
