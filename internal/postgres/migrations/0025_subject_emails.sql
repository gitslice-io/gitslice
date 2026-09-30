-- Verified email addresses per subject, used to match agent owner emails when a
-- human claims co-ownership. See design/20_agent_signup_and_claim.md (phase 2).
-- source names who vouched for the address ("clerk", "service").
create table if not exists subject_emails(
	subject_id text not null references subjects(id),
	email text not null,
	source text not null,
	verified_at timestamptz not null default now(),
	primary key(subject_id, email)
);
create index if not exists subject_emails_email_idx on subject_emails(email);
