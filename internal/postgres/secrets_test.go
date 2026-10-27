package postgres

import (
	"context"
	"encoding/base64"
	"errors"
	"reflect"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/secretbox"
	"gitslice.io/gitslice/internal/storage"
)

func TestSliceSecretsEncryptionAtRest(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	store.Slices().Secrets = postgresTestSecretBox(t)

	if err := store.Slices().SetSliceSecret(ctx, "slice_acme_payment", "API_TOKEN", "super secret", "user_alice"); err != nil {
		t.Fatal(err)
	}
	stored := storedSecretValue(t, ctx, store, "slice_acme_payment", "API_TOKEN")
	if !secretbox.IsSealed(stored) {
		t.Fatalf("stored value = %q, want enc:v1 envelope", stored)
	}
	if strings.Contains(stored, "super secret") {
		t.Fatal("stored encrypted value contains plaintext")
	}

	secrets, err := store.Slices().GetSliceSecrets(ctx, "slice_acme_payment")
	if err != nil {
		t.Fatal(err)
	}
	if got := secrets["API_TOKEN"]; got != "super secret" {
		t.Fatalf("GetSliceSecrets() API_TOKEN = %q, want decrypted value", got)
	}
}

func TestSliceSecretsLegacyPlaintextCompatibility(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	store.Slices().Secrets = postgresTestSecretBox(t)
	if _, err := store.db.ExecContext(ctx, `
		insert into secrets(id, account_id, name, value) values ('sec_legacy', 'acct_acme', 'LEGACY_TOKEN', 'legacy plaintext')
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
		insert into secret_slices(secret_id, slice_id, name) values ('sec_legacy', 'slice_acme_payment', 'LEGACY_TOKEN')
	`); err != nil {
		t.Fatal(err)
	}

	secrets, err := store.Slices().GetSliceSecrets(ctx, "slice_acme_payment")
	if err != nil {
		t.Fatal(err)
	}
	if got := secrets["LEGACY_TOKEN"]; got != "legacy plaintext" {
		t.Fatalf("GetSliceSecrets() LEGACY_TOKEN = %q, want legacy plaintext", got)
	}
}

func TestSliceSecretsPlaintextMode(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	if err := store.Slices().SetSliceSecret(ctx, "slice_acme_payment", "API_TOKEN", "plaintext dev value", "user_alice"); err != nil {
		t.Fatal(err)
	}
	if stored := storedSecretValue(t, ctx, store, "slice_acme_payment", "API_TOKEN"); stored != "plaintext dev value" {
		t.Fatalf("stored value = %q, want plaintext when no box is configured", stored)
	}
	secrets, err := store.Slices().GetSliceSecrets(ctx, "slice_acme_payment")
	if err != nil {
		t.Fatal(err)
	}
	if got := secrets["API_TOKEN"]; got != "plaintext dev value" {
		t.Fatalf("GetSliceSecrets() API_TOKEN = %q, want plaintext dev value", got)
	}
}

func TestSliceSecretsEncryptedValueRequiresBox(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	store.Slices().Secrets = postgresTestSecretBox(t)
	if err := store.Slices().SetSliceSecret(ctx, "slice_acme_payment", "API_TOKEN", "super secret", "user_alice"); err != nil {
		t.Fatal(err)
	}
	store.Slices().Secrets = nil

	if _, err := store.Slices().GetSliceSecrets(ctx, "slice_acme_payment"); err == nil {
		t.Fatal("GetSliceSecrets() = nil error for encrypted value without box, want configuration error")
	}
}

func TestSecretsScopedToSeveralSlices(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	slices := store.Slices()
	slices.Secrets = postgresTestSecretBox(t)
	seedSecretTestSlice(t, ctx, store, "slice_acme_web", "web")

	shared, err := slices.CreateSecret(ctx, "acme", "DEPLOY_TOKEN", "shared", storage.SecretScope{SliceIDs: []string{"slice_acme_payment", "slice_acme_backend", "slice_acme_payment"}}, "user_alice")
	if err != nil {
		t.Fatal(err)
	}
	if got := secretSliceNames(shared); !reflect.DeepEqual(got, []string{"backend", "payment"}) {
		t.Fatalf("CreateSecret slices = %v, want [backend payment]", got)
	}
	if shared.Account != "acme" || shared.CreatedBy != "user_alice" || shared.AllSlices {
		t.Fatalf("CreateSecret = %+v", shared)
	}
	// One name may hold another value for other slices, not for these.
	if _, err := slices.CreateSecret(ctx, "acme", "DEPLOY_TOKEN", "web", storage.SecretScope{SliceIDs: []string{"slice_acme_web"}}, "user_alice"); err != nil {
		t.Fatalf("CreateSecret for another slice: %v", err)
	}
	if _, err := slices.CreateSecret(ctx, "acme", "DEPLOY_TOKEN", "x", storage.SecretScope{SliceIDs: []string{"slice_acme_backend"}}, "user_alice"); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("CreateSecret overlapping = %v, want ErrConflict", err)
	}
	// An all-slices secret is overridden where a secret lists the slice.
	if _, err := slices.CreateSecret(ctx, "acme", "DEPLOY_TOKEN", "default", storage.SecretScope{AllSlices: true}, "user_alice"); err != nil {
		t.Fatalf("CreateSecret all slices: %v", err)
	}
	if _, err := slices.CreateSecret(ctx, "acme", "DEPLOY_TOKEN", "again", storage.SecretScope{AllSlices: true}, "user_alice"); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("second all-slices secret = %v, want ErrConflict", err)
	}
	npm, err := slices.CreateSecret(ctx, "acme", "NPM_TOKEN", "npm", storage.SecretScope{AllSlices: true}, "user_alice")
	if err != nil {
		t.Fatal(err)
	}
	for sliceID, want := range map[string]map[string]string{
		"slice_acme_payment": {"DEPLOY_TOKEN": "shared", "NPM_TOKEN": "npm"},
		"slice_acme_backend": {"DEPLOY_TOKEN": "shared", "NPM_TOKEN": "npm"},
		"slice_acme_web":     {"DEPLOY_TOKEN": "web", "NPM_TOKEN": "npm"},
	} {
		got, err := slices.GetSliceSecrets(ctx, sliceID)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("GetSliceSecrets(%s) = %v, want %v", sliceID, got, want)
		}
	}
	reaching, err := slices.ListSliceSecrets(ctx, "slice_acme_payment")
	if err != nil {
		t.Fatal(err)
	}
	if len(reaching) != 2 || reaching[0].SecretID != shared.ID || len(reaching[0].SharedWith) != 1 || reaching[0].SharedWith[0].Name != "backend" || reaching[1].SecretID != npm.ID || !reaching[1].AllSlices {
		t.Fatalf("ListSliceSecrets = %+v", reaching)
	}

	// Rescoping: web cannot join (it has its own), payment can leave.
	if _, err := slices.UpdateSecret(ctx, shared.ID, nil, &storage.SecretScope{SliceIDs: []string{"slice_acme_payment", "slice_acme_web"}}, "user_bob"); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("UpdateSecret onto web = %v, want ErrConflict", err)
	}
	value := "rotated"
	updated, err := slices.UpdateSecret(ctx, shared.ID, &value, &storage.SecretScope{SliceIDs: []string{"slice_acme_backend"}}, "user_bob")
	if err != nil {
		t.Fatal(err)
	}
	if updated.UpdatedBy != "user_bob" || !reflect.DeepEqual(secretSliceNames(updated), []string{"backend"}) {
		t.Fatalf("UpdateSecret = %+v", updated)
	}
	payment, _ := slices.GetSliceSecrets(ctx, "slice_acme_payment")
	backend, _ := slices.GetSliceSecrets(ctx, "slice_acme_backend")
	if payment["DEPLOY_TOKEN"] != "default" || backend["DEPLOY_TOKEN"] != "rotated" {
		t.Fatalf("after rescoping payment = %v, backend = %v", payment, backend)
	}

	// Another account's slice cannot be listed.
	if _, err := store.db.ExecContext(ctx, `insert into accounts(id, slug, kind) values ('acct_other', 'other', 'org')`); err != nil {
		t.Fatal(err)
	}
	if _, err := slices.CreateSecret(ctx, "other", "TOKEN", "x", storage.SecretScope{SliceIDs: []string{"slice_acme_payment"}}, "user_alice"); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("CreateSecret for another account's slice = %v, want ErrInvalid", err)
	}
	if _, err := slices.CreateSecret(ctx, "acme", "TOKEN", "x", storage.SecretScope{}, "user_alice"); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("CreateSecret with no scope = %v, want ErrInvalid", err)
	}

	listed, err := slices.ListSecrets(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 4 {
		t.Fatalf("ListSecrets = %d secrets, want 4", len(listed))
	}
	if err := slices.DeleteSecret(ctx, npm.ID); err != nil {
		t.Fatal(err)
	}
	if err := slices.DeleteSecret(ctx, npm.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second DeleteSecret = %v, want ErrNotFound", err)
	}
}

func TestSliceSecretCallsOnSharedSecrets(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	slices := store.Slices()
	shared, err := slices.CreateSecret(ctx, "acme", "TOKEN", "shared", storage.SecretScope{SliceIDs: []string{"slice_acme_payment", "slice_acme_backend"}}, "user_alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := slices.SetSliceSecret(ctx, "slice_acme_payment", "TOKEN", "mine", "user_alice"); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("SetSliceSecret over a shared secret = %v, want ErrConflict", err)
	}
	// Removing it from one slice takes that slice out of the scope.
	if err := slices.DeleteSliceSecret(ctx, "slice_acme_payment", "TOKEN", "user_alice"); err != nil {
		t.Fatal(err)
	}
	got, err := slices.GetSecret(ctx, shared.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(secretSliceNames(got), []string{"backend"}) {
		t.Fatalf("scope after DeleteSliceSecret = %v, want [backend]", secretSliceNames(got))
	}
	// Now only backend lists it: the slice calls change and delete it.
	if err := slices.SetSliceSecret(ctx, "slice_acme_backend", "TOKEN", "backend", "user_alice"); err != nil {
		t.Fatal(err)
	}
	if values, _ := slices.GetSliceSecrets(ctx, "slice_acme_backend"); values["TOKEN"] != "backend" {
		t.Fatalf("TOKEN = %q, want backend", values["TOKEN"])
	}
	if err := slices.DeleteSliceSecret(ctx, "slice_acme_backend", "TOKEN", "user_alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := slices.GetSecret(ctx, shared.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetSecret after its last slice deleted it = %v, want ErrNotFound", err)
	}
	// An all-slices secret is not removed from one slice.
	if _, err := slices.CreateSecret(ctx, "acme", "NPM_TOKEN", "npm", storage.SecretScope{AllSlices: true}, "user_alice"); err != nil {
		t.Fatal(err)
	}
	if err := slices.DeleteSliceSecret(ctx, "slice_acme_payment", "NPM_TOKEN", "user_alice"); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("DeleteSliceSecret of an all-slices secret = %v, want ErrConflict", err)
	}
	// Setting one on a slice overrides it there.
	if err := slices.SetSliceSecret(ctx, "slice_acme_payment", "NPM_TOKEN", "payment", "user_alice"); err != nil {
		t.Fatal(err)
	}
	payment, _ := slices.GetSliceSecrets(ctx, "slice_acme_payment")
	backend, _ := slices.GetSliceSecrets(ctx, "slice_acme_backend")
	if payment["NPM_TOKEN"] != "payment" || backend["NPM_TOKEN"] != "npm" {
		t.Fatalf("payment = %v, backend = %v", payment, backend)
	}
}

func TestSecretScopeLosesRetiredSlice(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	secret, err := store.Slices().CreateSecret(ctx, "acme", "TOKEN", "x", storage.SecretScope{SliceIDs: []string{"slice_acme_payment"}}, "user_alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Slices().Delete(ctx, "slice_acme_payment"); err != nil {
		t.Fatal(err)
	}
	got, err := store.Slices().GetSecret(ctx, secret.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Slices) != 0 {
		t.Fatalf("slices after the slice was deleted = %v, want none", got.Slices)
	}
}

func seedSecretTestSlice(t *testing.T, ctx context.Context, store *DB, id, slug string) {
	t.Helper()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	seedStoreTestSlice(t, ctx, tx, id, slug, []string{"/acme/" + slug})
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func storedSecretValue(t *testing.T, ctx context.Context, store *DB, sliceID, name string) string {
	t.Helper()
	var stored string
	if err := store.db.QueryRowContext(ctx, `
		select secrets.value from secrets join secret_slices on secret_slices.secret_id = secrets.id
		where secret_slices.slice_id = $1 and secret_slices.name = $2
	`, sliceID, name).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	return stored
}

func secretSliceNames(secret *storage.Secret) []string {
	var names []string
	for _, slice := range secret.Slices {
		names = append(names, slice.Name)
	}
	return names
}

func postgresTestSecretBox(t *testing.T) *secretbox.Box {
	t.Helper()
	key := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	box, err := secretbox.New(key)
	if err != nil {
		t.Fatal(err)
	}
	return box
}
