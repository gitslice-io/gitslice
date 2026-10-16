-- Slice lifecycle (design/29_slice_lifecycle.md): renaming, archiving and
-- retiring slices that have history.
--
-- archived_at hides a slice from listings and makes it read-only. deleted_at
-- retires a slice that has changesets: the row stays, so the changesets that
-- reference it and its name stay valid, but it no longer resolves or lists.
alter table slices add column if not exists archived_at timestamptz;
alter table slices add column if not exists deleted_at timestamptz;

-- A renamed slice's earlier names keep resolving to it, and stay reserved so
-- no other slice can take them.
create table if not exists slice_aliases(
	account_id text not null references accounts(id),
	slug text not null,
	slice_id text not null references slices(id),
	created_at timestamptz not null default now(),
	primary key (account_id, slug)
);

create index if not exists idx_slice_aliases_slice on slice_aliases(slice_id)
