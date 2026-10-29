package memory

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestChangesetStoreDoesNotResolveDeprecatedHandle(t *testing.T) {
	ctx := context.Background()
	stores := New()
	ref := &corev1.SliceRef{Account: "acme", Slice: "payment"}
	if _, err := stores.Slices.Create(ctx, "user_acme", ref, []string{"/acme/payment"}, "private", 0, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := stores.Changesets.Create(ctx, "user_acme", &corev1.CreateChangesetRequest{AuthoringSlice: ref})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := stores.Changesets.Get(ctx, "acme:payment@1"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Get(deprecated handle) error = %v, want ErrNotFound", err)
	}
	if _, err := stores.Changesets.Get(ctx, cs.Id); err != nil {
		t.Fatalf("Get(full id) error = %v", err)
	}
}

func TestCheckRunStatusGuardRejectsTerminalAndSupersededResults(t *testing.T) {
	ctx := context.Background()
	stores := New()
	ref := &corev1.SliceRef{Account: "acme", Slice: "payment"}
	if _, err := stores.Slices.Create(ctx, "user_acme", ref, []string{"/acme/payment"}, "private", 0, []string{"unit"}); err != nil {
		t.Fatal(err)
	}
	cs, err := stores.Changesets.Create(ctx, "user_acme", &corev1.CreateChangesetRequest{
		AuthoringSlice: ref,
		BaseCommitId:   "mem_root",
	})
	if err != nil {
		t.Fatal(err)
	}
	patchset, err := stores.Changesets.AddPatchset(ctx, cs.Id, "", &corev1.Patchset{
		BaseCommitId: "mem_root",
		Author:       "user_acme",
		ChangedPaths: []string{"/acme/payment/change.go"},
	})
	if err != nil {
		t.Fatal(err)
	}

	run, err := stores.Checks.CreateCheckRun(ctx, storage.CheckRunInput{
		ChangesetID: cs.Id,
		PatchsetID:  patchset.Id,
		CheckName:   "unit",
		Status:      "running",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stores.Checks.UpdateCheckRunStatus(ctx, run.Id, "passed", 0, "ok"); err != nil {
		t.Fatal(err)
	}
	if _, err := stores.Checks.UpdateCheckRunStatus(ctx, run.Id, "failed", 1, "late fail"); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("terminal UpdateCheckRunStatus error = %v, want ErrConflict", err)
	}
	if got := stores.backend.checkResults[patchsetRequirementKey(cs.Id, patchset.Id)]["unit"]; got != storage.CheckStatusPass {
		t.Fatalf("check result after stale terminal update = %q, want pass", got)
	}

	oldRun, err := stores.Checks.CreateCheckRun(ctx, storage.CheckRunInput{
		ChangesetID: cs.Id,
		PatchsetID:  patchset.Id,
		CheckName:   "lint",
		Status:      "queued",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stores.Checks.CreateCheckRun(ctx, storage.CheckRunInput{
		ChangesetID: cs.Id,
		PatchsetID:  patchset.Id,
		CheckName:   "lint",
		Status:      "queued",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := stores.Checks.UpdateCheckRunStatus(ctx, oldRun.Id, "passed", 0, "stale pass"); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("superseded result UpdateCheckRunStatus error = %v, want ErrConflict", err)
	}
	canceled, err := stores.Checks.UpdateCheckRunStatus(ctx, oldRun.Id, "canceled", -1, "superseded")
	if err != nil {
		t.Fatalf("superseded cancel UpdateCheckRunStatus: %v", err)
	}
	if canceled.Status != "canceled" {
		t.Fatalf("superseded cancel status = %q, want canceled", canceled.Status)
	}
	if got := stores.backend.checkResults[patchsetRequirementKey(cs.Id, patchset.Id)]["lint"]; got != "" {
		t.Fatalf("check result for superseded canceled run = %q, want empty", got)
	}
}

func TestSliceSecretsCRUDAndNameValidation(t *testing.T) {
	ctx := context.Background()
	stores := New()
	slice, err := stores.Slices.Create(ctx, "user_acme", &corev1.SliceRef{Account: "acme", Slice: "payment"}, []string{"/acme/payment"}, "private", 0, nil)
	if err != nil {
		t.Fatal(err)
	}

	invalidNames := []string{"", "token", "1TOKEN", "TOKEN-NAME", "TOKEN NAME", "TOKEN.name", "TOKEN/value"}
	for _, name := range invalidNames {
		if err := stores.Slices.SetSliceSecret(ctx, slice.Id, name, "value", "user_acme"); !errors.Is(err, storage.ErrInvalid) {
			t.Fatalf("SetSliceSecret(%q) error = %v, want ErrInvalid", name, err)
		}
		if err := stores.Slices.DeleteSliceSecret(ctx, slice.Id, name, "user_acme"); !errors.Is(err, storage.ErrInvalid) {
			t.Fatalf("DeleteSliceSecret(%q) error = %v, want ErrInvalid", name, err)
		}
	}

	if err := stores.Slices.SetSliceSecret(ctx, slice.Id, "CI_TOKEN", "v1", "user_acme"); err != nil {
		t.Fatalf("SetSliceSecret CI_TOKEN: %v", err)
	}
	if err := stores.Slices.SetSliceSecret(ctx, slice.Id, "_LEADING_UNDERSCORE", "underscore", "user_acme"); err != nil {
		t.Fatalf("SetSliceSecret _LEADING_UNDERSCORE: %v", err)
	}
	if err := stores.Slices.SetSliceSecret(ctx, slice.Id, "CI_TOKEN", "v2", "user_acme"); err != nil {
		t.Fatalf("SetSliceSecret CI_TOKEN upsert: %v", err)
	}

	names := sliceSecretNames(t, stores, slice.Id)
	if want := []string{"CI_TOKEN", "_LEADING_UNDERSCORE"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("slice secret names = %#v, want %#v", names, want)
	}

	secrets, err := stores.Slices.SecretValuesAt(ctx, "/acme/payment")
	if err != nil {
		t.Fatalf("SecretValuesAt: %v", err)
	}
	if got := secrets["CI_TOKEN"]; got != "v2" {
		t.Fatalf("CI_TOKEN = %q, want v2", got)
	}
	secrets["CI_TOKEN"] = "mutated"
	secrets, err = stores.Slices.SecretValuesAt(ctx, "/acme/payment")
	if err != nil {
		t.Fatalf("SecretValuesAt after mutation: %v", err)
	}
	if got := secrets["CI_TOKEN"]; got != "v2" {
		t.Fatalf("CI_TOKEN after returned map mutation = %q, want v2", got)
	}

	if err := stores.Slices.DeleteSliceSecret(ctx, slice.Id, "CI_TOKEN", "user_acme"); err != nil {
		t.Fatalf("DeleteSliceSecret CI_TOKEN: %v", err)
	}
	names = sliceSecretNames(t, stores, slice.Id)
	if want := []string{"_LEADING_UNDERSCORE"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("slice secret names after delete = %#v, want %#v", names, want)
	}
}

func sliceSecretNames(t *testing.T, stores *Stores, sliceID string) []string {
	t.Helper()
	secrets, err := stores.Slices.ListSliceSecrets(context.Background(), sliceID)
	if err != nil {
		t.Fatalf("ListSliceSecrets: %v", err)
	}
	var names []string
	for _, secret := range secrets {
		names = append(names, secret.Name)
	}
	return names
}
