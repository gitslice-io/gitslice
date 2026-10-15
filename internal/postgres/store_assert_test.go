package postgres

import (
	"context"
	"database/sql"
	"testing"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func hasFinding(report IntegrityReport, code string) bool {
	for _, finding := range report.Findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}

func getTestRef(t *testing.T, ctx context.Context, store *DB) *corev1.Ref {
	t.Helper()
	ref, err := store.Repository().GetRef(ctx, DefaultTargetRef)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func assertPathHeadRowsForTest(t *testing.T, ctx context.Context, store *DB, want map[string]PathHead, wantChangesetID, wantPatchsetID string) {
	t.Helper()
	for p, wantHead := range want {
		var got PathHead
		var blobID, contentHash sql.NullString
		var mode, size sql.NullInt64
		var changesetID, patchsetID string
		if err := store.db.QueryRowContext(ctx, `
			select path,
			       exists,
			       entry_fingerprint,
			       blob_id,
			       content_hash,
			       mode,
			       size,
			       coalesce(accepted_changeset_id, ''),
			       coalesce(accepted_patchset_id, '')
			from path_heads
			where path = $1
		`, p).Scan(&got.Path, &got.Exists, &got.EntryFingerprint, &blobID, &contentHash, &mode, &size, &changesetID, &patchsetID); err != nil {
			t.Fatal(err)
		}
		if blobID.Valid {
			got.BlobID = blobID.String
		}
		if contentHash.Valid {
			got.ContentHash = contentHash.String
		}
		if mode.Valid {
			got.Mode = uint32(mode.Int64)
		}
		if size.Valid {
			got.Size = size.Int64
		}
		if got != wantHead {
			t.Fatalf("path_heads[%s] = %#v, want %#v", p, got, wantHead)
		}
		if changesetID != wantChangesetID || patchsetID != wantPatchsetID {
			t.Fatalf("path_heads[%s] attribution = (%q, %q), want (%q, %q)", p, changesetID, patchsetID, wantChangesetID, wantPatchsetID)
		}
	}
}

func outboxDepthForTest(t *testing.T, ctx context.Context, store *DB) int {
	t.Helper()
	depth, err := store.Changesets().OutboxDepth(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return depth
}

func outboxAttemptsForTest(t *testing.T, ctx context.Context, store *DB, id int64) int {
	t.Helper()
	var attempts int
	if err := store.db.QueryRowContext(ctx, `
		select attempts
		from outbox
		where id = $1
	`, id).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	return attempts
}

func commitChangedPathCountForTest(t *testing.T, ctx context.Context, store *DB, commitID, p string) int {
	t.Helper()
	var count int
	if err := store.db.QueryRowContext(ctx, `
		select count(*)
		from commit_changed_paths
		where target_ref = $1
		  and commit_id = $2
		  and path = $3
	`, DefaultTargetRef, commitID, p).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func commitChangedPathRowsForTest(t *testing.T, ctx context.Context, store *DB, targetRef string) []string {
	t.Helper()
	rows, err := store.db.QueryContext(ctx, `
		select commit_id, path, change_kind
		from commit_changed_paths
		where target_ref = $1
		order by commit_id, path, change_kind
	`, targetRef)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var commitID, p, changeKind string
		if err := rows.Scan(&commitID, &p, &changeKind); err != nil {
			t.Fatal(err)
		}
		out = append(out, commitID+"\x00"+p+"\x00"+changeKind)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
