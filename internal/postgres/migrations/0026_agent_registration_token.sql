-- Idempotent agent registration: RegisterAgent retried with the same
-- client-generated token resumes the existing registration instead of failing
-- on the now-taken username. Only the sha256 of the token is stored.
alter table agent_registrations add column if not exists registration_token_hash text;
create unique index if not exists agent_registrations_token_idx
	on agent_registrations(registration_token_hash) where registration_token_hash is not null;
