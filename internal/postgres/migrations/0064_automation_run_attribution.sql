-- Automation run attribution (who invoked manual runs, and human vs agent kind).
alter table trigger_runs add column if not exists triggered_by text not null default '';

alter table trigger_runs add column if not exists triggered_by_kind text not null default '';
