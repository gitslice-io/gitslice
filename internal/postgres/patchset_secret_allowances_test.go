package postgres

import (
	"errors"
	"testing"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestPatchsetSecretAllowancesPostgres(t *testing.T) {
	ctx, store, objects := newPostgresTestStoreWithObjects(t)

	var tableExists bool
	if err := store.db.QueryRowContext(ctx, `
		select exists (
			select 1
			from information_schema.tables
			where table_schema = current_schema()
			  and table_name = 'patchset_secret_allowances'
		)
	`).Scan(&tableExists); err != nil {
		t.Fatal(err)
	}
	if !tableExists {
		t.Fatal("expected patchset_secret_allowances table from migration 0055")
	}

	writer1 := provisionHumanPGTest(t, ctx, store, "pg_sec_w1", "pg-sec-w1@example.com")
	writer2 := provisionHumanPGTest(t, ctx, store, "pg_sec_w2", "pg-sec-w2@example.com")

	ref := getTestRef(t, ctx, store)
	cs, err := store.Changesets().Create(ctx, writer1, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
		TargetRef:      DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "pg secret allowance test",
	})
	if err != nil {
		t.Fatal(err)
	}
	ps1 := createPatchsetPGTest(t, ctx, store, objects, cs.Id, "", writer1, "", "/acme/payment/sec1.txt", "one\n")
	ps2 := createPatchsetPGTest(t, ctx, store, objects, cs.Id, ps1.Id, writer1, "", "/acme/payment/sec2.txt", "two\n")

	if _, _, err := store.Checks().AllowPatchsetSecrets(ctx, "", ps1.Id, writer1); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("empty changeset err = %v, want ErrInvalid", err)
	}
	if _, _, err := store.Checks().AllowPatchsetSecrets(ctx, cs.Id, "ps_missing", writer1); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("missing patchset err = %v, want ErrNotFound", err)
	}
	if _, err := store.Checks().GetPatchsetSecretAllowance(ctx, cs.Id, ps1.Id); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Get before allow err = %v, want ErrNotFound", err)
	}

	first, created, err := store.Checks().AllowPatchsetSecrets(ctx, cs.Id, ps1.Id, writer1)
	if err != nil || !created {
		t.Fatalf("first AllowPatchsetSecrets = (%+v, %v, %v), want created=true", first, created, err)
	}
	if first.ChangesetID != cs.Id || first.PatchsetID != ps1.Id || first.AllowedBySubjectID != writer1 || first.CreatedAt.IsZero() {
		t.Fatalf("first allowance = %+v", first)
	}

	// Idempotent repeat call keeps the first actor and timestamp.
	second, created2, err := store.Checks().AllowPatchsetSecrets(ctx, cs.Id, ps1.Id, writer2)
	if err != nil || created2 {
		t.Fatalf("second AllowPatchsetSecrets = (%+v, %v, %v), want created=false", second, created2, err)
	}
	if second.AllowedBySubjectID != writer1 || !second.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("second allowance = %+v, want original %+v", second, first)
	}

	got, err := store.Checks().GetPatchsetSecretAllowance(ctx, cs.Id, ps1.Id)
	if err != nil || got.AllowedBySubjectID != writer1 {
		t.Fatalf("GetPatchsetSecretAllowance(ps1) = %+v, %v", got, err)
	}
	if _, err := store.Checks().GetPatchsetSecretAllowance(ctx, cs.Id, ps2.Id); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("GetPatchsetSecretAllowance(ps2) = %v, want ErrNotFound", err)
	}
}
