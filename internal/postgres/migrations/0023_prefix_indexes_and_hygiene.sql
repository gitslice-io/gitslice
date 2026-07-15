-- Pattern-ops indexes so leading-anchored LIKE prefix scans use btree
-- regardless of database collation.
create index if not exists idx_current_path_entities_ref_path_pattern
	on current_path_entities(target_ref, path text_pattern_ops);
create index if not exists idx_commit_changed_paths_ref_path_pattern
	on commit_changed_paths(target_ref, path text_pattern_ops, committed_at desc);
create index if not exists idx_path_heads_path_pattern
	on path_heads(path text_pattern_ops);

-- Hygiene: drop indexes duplicated by primary keys / unique constraints.
drop index if exists idx_blobs_content_hash;
drop index if exists idx_slices_account_slug;
drop index if exists idx_path_heads_fingerprint;

-- Hygiene: queue-poll indexes only ever serve the unprocessed subset.
create index if not exists idx_outbox_unprocessed
	on outbox(id) where processed_at is null;
drop index if exists idx_outbox_processed_id;
create index if not exists idx_pending_publish_pending_sequence
	on pending_publish(sequence) where status = 'pending';

-- Keep idx_pending_publish_status_sequence: non-pending pending_publish reads
-- still exist for published history rebuild and GC reachability.
