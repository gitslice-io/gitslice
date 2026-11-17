package memory

import (
	"context"
	"errors"
	"testing"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestPatchsetSecretAllowancesMemory(t *testing.T) {
	ctx := context.Background()
	stores := New()

	writer1 := mustProvisionHumanForTest(t, ctx, stores, "mem_w1", "w1@example.com")
	writer2 := mustProvisionHumanForTest(t, ctx, stores, "mem_w2", "w2@example.com")

	sliceRef := &corev1.SliceRef{Account: "acme", Slice: "sec-allow"}
	stores.PutSlice(sliceRef, []string{"/acme/sec-allow"}, "private")

	cs, ps1 := mustCreateDraftOnSlice(t, ctx, stores, writer1, sliceRef, "/acme/sec-allow/a.txt", "v1\n")
	ps2 := mustCreateDraftOnSlicePatchset(t, ctx, stores, cs.Id, ps1.Id, ps1.BaseCommitId, writer1, "/acme/sec-allow/b.txt", "v2\n")

	if _, _, err := stores.Checks.AllowPatchsetSecrets(ctx, "", ps1.Id, writer1); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("empty changeset err = %v, want ErrInvalid", err)
	}
	if _, _, err := stores.Checks.AllowPatchsetSecrets(ctx, cs.Id, "ps_missing", writer1); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("missing patchset err = %v, want ErrNotFound", err)
	}
	if _, err := stores.Checks.GetPatchsetSecretAllowance(ctx, cs.Id, ps1.Id); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Get before allow err = %v, want ErrNotFound", err)
	}

	first, created, err := stores.Checks.AllowPatchsetSecrets(ctx, cs.Id, ps1.Id, writer1)
	if err != nil || !created {
		t.Fatalf("first AllowPatchsetSecrets = (%+v, %v, %v), want created=true", first, created, err)
	}
	if first.ChangesetID != cs.Id || first.PatchsetID != ps1.Id || first.AllowedBySubjectID != writer1 {
		t.Fatalf("first allowance = %+v", first)
	}

	// Idempotent second call keeps the original writer and created=false.
	second, created2, err := stores.Checks.AllowPatchsetSecrets(ctx, cs.Id, ps1.Id, writer2)
	if err != nil || created2 {
		t.Fatalf("second AllowPatchsetSecrets = (%+v, %v, %v), want created=false", second, created2, err)
	}
	if second.AllowedBySubjectID != writer1 || !second.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("second allowance = %+v, want original %+v", second, first)
	}

	got, err := stores.Checks.GetPatchsetSecretAllowance(ctx, cs.Id, ps1.Id)
	if err != nil || got.AllowedBySubjectID != writer1 {
		t.Fatalf("GetPatchsetSecretAllowance(ps1) = %+v, %v", got, err)
	}

	// Subsequent patchsets must not inherit an earlier patchset's allowance.
	if _, err := stores.Checks.GetPatchsetSecretAllowance(ctx, cs.Id, ps2.Id); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("GetPatchsetSecretAllowance(ps2) = %v, want ErrNotFound", err)
	}
}

func mustCreateDraftOnSlicePatchset(t *testing.T, ctx context.Context, stores *Stores, csID, expectedPatchsetID, baseCommitID, authorSubjectID, path, content string) *corev1.Patchset {
	t.Helper()
	other, otherPS := mustCreateDraftOnSlice(t, ctx, stores, authorSubjectID, &corev1.SliceRef{Account: "acme", Slice: "sec-allow"}, path, content)
	_ = other
	ps, err := stores.Changesets.AddPatchset(ctx, csID, expectedPatchsetID, &corev1.Patchset{
		BaseCommitId: baseCommitID,
		Author:       authorSubjectID,
		ChangedPaths: []string{path},
		FileEdits:    otherPS.FileEdits,
		PathBases:    otherPS.PathBases,
		ReadSet:      otherPS.ReadSet,
		WriteSet:     otherPS.WriteSet,
	})
	if err != nil {
		t.Fatalf("AddPatchset: %v", err)
	}
	return ps
}
