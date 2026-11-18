package postgres

import (
	"database/sql"
	"errors"
	"slices"
	"testing"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestSQLUtilJSONColumnScanAndValue(t *testing.T) {
	var list []string
	col := jsonCol(&list)
	if err := col.Scan(`["a","b"]`); err != nil {
		t.Fatalf("Scan string: %v", err)
	}
	if !slices.Equal(list, []string{"a", "b"}) {
		t.Fatalf("scanned string = %v, want [a b]", list)
	}
	if err := col.Scan(nil); err != nil {
		t.Fatalf("Scan nil: %v", err)
	}
	if list != nil {
		t.Fatalf("scanned nil = %v, want nil", list)
	}
	val, err := jsonVal([]string{"x", "y"}).Value()
	if err != nil {
		t.Fatalf("Value: %v", err)
	}
	raw, ok := val.([]byte)
	if !ok || string(raw) != `["x","y"]` {
		t.Fatalf("Value = %#v (%T), want [\"x\",\"y\"]", val, val)
	}
}

func TestInTxRollbackAndSingleVsBatchGetParityPostgres(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	base := getTestRef(t, ctx, store)

	// execInTx rolls back when callback returns an error.
	boom := errors.New("boom")
	err := execInTx(ctx, store.db, func(tx *sql.Tx) error {
		if _, execErr := tx.ExecContext(ctx, `insert into subjects(id, kind, display_name) values ('subj_rollback_test', 'user', 'Rollback')`); execErr != nil {
			return execErr
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("execInTx err = %v, want boom", err)
	}
	found, err := querySlice(ctx, store.db, scanString, `select id from subjects where id = $1`, "subj_rollback_test")
	if err != nil || len(found) != 0 {
		t.Fatalf("rolled-back row still visible: %v, %v", found, err)
	}

	// Verify Get (routed through getManyByIDs) and List hydrate identical fields,
	// and ApprovalSubjects reads directly via dbtx.
	if _, execErr := store.db.ExecContext(ctx, `
		insert into subjects(id, kind, controller_subject_id, display_name)
		values ('user_collab_b8', 'user', 'user_collab_b8', 'Collab'),
		       ('user_approver_b8', 'user', 'user_approver_b8', 'Approver')
		on conflict (id) do nothing
	`); execErr != nil {
		t.Fatal(execErr)
	}

	blobID, blobHash := upsertTestBlob(t, ctx, store, "b8 parity\n")
	ps := createDraftPatchset(t, ctx, store, base.CommitId, "/acme/payment/b8_parity.txt", blobID, blobHash)
	if _, err := store.Changesets().AddCollaborator(ctx, ps.ChangesetId, "user_collab_b8", "user_alice"); err != nil {
		t.Fatalf("AddCollaborator: %v", err)
	}
	if _, err := store.Changesets().Approve(ctx, ps.ChangesetId, "user_approver_b8"); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	approvers, err := store.Changesets().ApprovalSubjects(ctx, ps.ChangesetId, ps.Id)
	if err != nil || !slices.Equal(approvers, []string{"user_approver_b8"}) {
		t.Fatalf("ApprovalSubjects = (%v, %v), want [user_approver_b8]", approvers, err)
	}

	single, err := store.Changesets().Get(ctx, ps.ChangesetId)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	listed, _, err := store.Changesets().List(ctx, storage.ChangesetListFilter{
		Slice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
		Limit: 20,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var fromList *corev1.Changeset
	for _, item := range listed {
		if item.GetId() == ps.ChangesetId {
			fromList = item
			break
		}
	}
	if fromList == nil {
		t.Fatalf("changeset %s not found in List", ps.ChangesetId)
	}
	if len(single.GetPatchsets()) != len(fromList.GetPatchsets()) ||
		!slices.Equal(single.GetCollaborators(), fromList.GetCollaborators()) ||
		len(single.GetApprovals()) != len(fromList.GetApprovals()) {
		t.Fatalf("Get vs List hydration mismatch:\nGet:  %+v\nList: %+v", single, fromList)
	}
}
