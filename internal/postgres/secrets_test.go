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

func TestSecretsEncryptionAtRest(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	store.Slices().Secrets = postgresTestSecretBox(t)

	if err := store.Slices().SetSliceSecret(ctx, "slice_acme_payment", "API_TOKEN", "super secret", "user_alice"); err != nil {
		t.Fatal(err)
	}
	stored := storedSecretValue(t, ctx, store, "/acme/payment", "API_TOKEN")
	if !secretbox.IsSealed(stored) {
		t.Fatalf("stored value = %q, want enc:v1 envelope", stored)
	}
	if strings.Contains(stored, "super secret") {
		t.Fatal("stored encrypted value contains plaintext")
	}

	secrets, err := store.Slices().SecretValuesAt(ctx, "/acme/payment/api")
	if err != nil {
		t.Fatal(err)
	}
	if got := secrets["API_TOKEN"]; got != "super secret" {
		t.Fatalf("SecretValuesAt() API_TOKEN = %q, want decrypted value", got)
	}
}

func TestSecretsLegacyPlaintextCompatibility(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	store.Slices().Secrets = postgresTestSecretBox(t)
	for _, stmt := range []string{
		`insert into secrets(id, account_id, name, value) values ('sec_legacy', 'acct_acme', 'LEGACY_TOKEN', 'legacy plaintext')`,
		`insert into secret_paths(secret_id, path, name) values ('sec_legacy', '/acme/payment', 'LEGACY_TOKEN')`,
	} {
		if _, err := store.db.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}

	secrets, err := store.Slices().SecretValuesAt(ctx, "/acme/payment")
	if err != nil {
		t.Fatal(err)
	}
	if got := secrets["LEGACY_TOKEN"]; got != "legacy plaintext" {
		t.Fatalf("SecretValuesAt() LEGACY_TOKEN = %q, want legacy plaintext", got)
	}
}

func TestSecretsPlaintextMode(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	if err := store.Slices().SetSliceSecret(ctx, "slice_acme_payment", "API_TOKEN", "plaintext dev value", "user_alice"); err != nil {
		t.Fatal(err)
	}
	if stored := storedSecretValue(t, ctx, store, "/acme/payment", "API_TOKEN"); stored != "plaintext dev value" {
		t.Fatalf("stored value = %q, want plaintext when no box is configured", stored)
	}
	secrets, err := store.Slices().SecretValuesAt(ctx, "/acme/payment")
	if err != nil {
		t.Fatal(err)
	}
	if got := secrets["API_TOKEN"]; got != "plaintext dev value" {
		t.Fatalf("SecretValuesAt() API_TOKEN = %q, want plaintext dev value", got)
	}
}

func TestSecretsEncryptedValueRequiresBox(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	store.Slices().Secrets = postgresTestSecretBox(t)
	if err := store.Slices().SetSliceSecret(ctx, "slice_acme_payment", "API_TOKEN", "super secret", "user_alice"); err != nil {
		t.Fatal(err)
	}
	store.Slices().Secrets = nil

	if _, err := store.Slices().SecretValuesAt(ctx, "/acme/payment"); err == nil {
		t.Fatal("SecretValuesAt() = nil error for encrypted value without box, want configuration error")
	}
}

func TestSecretPathsAndPrecedence(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	slices := store.Slices()
	slices.Secrets = postgresTestSecretBox(t)

	root, err := slices.CreateSecret(ctx, "acme", "DEPLOY_TOKEN", "default", []string{"/acme"}, "user_alice")
	if err != nil {
		t.Fatal(err)
	}
	payment, err := slices.CreateSecret(ctx, "acme", "DEPLOY_TOKEN", "payment", []string{"/acme/payment/", "acme/payment", "/acme/web"}, "user_alice")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(payment.Paths, []string{"/acme/payment", "/acme/web"}) || payment.Account != "acme" || payment.CreatedBy != "user_alice" {
		t.Fatalf("CreateSecret = %+v, want paths [/acme/payment /acme/web]", payment)
	}
	if _, err := slices.CreateSecret(ctx, "acme", "NPM_TOKEN", "npm", []string{"/acme"}, "user_alice"); err != nil {
		t.Fatal(err)
	}

	// The longest covering path wins; nothing above a folder's own secrets.
	for folder, want := range map[string]map[string]string{
		"/acme":                     {"DEPLOY_TOKEN": "default", "NPM_TOKEN": "npm"},
		"/acme/backend":             {"DEPLOY_TOKEN": "default", "NPM_TOKEN": "npm"},
		"/acme/payment":             {"DEPLOY_TOKEN": "payment", "NPM_TOKEN": "npm"},
		"/acme/payment/shared/deep": {"DEPLOY_TOKEN": "payment", "NPM_TOKEN": "npm"},
		"/acme/paymentx":            {"DEPLOY_TOKEN": "default", "NPM_TOKEN": "npm"},
		"/other":                    {},
	} {
		got, err := slices.SecretValuesAt(ctx, folder)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("SecretValuesAt(%s) = %v, want %v", folder, got, want)
		}
	}

	// One secret per name per path; other names and nested paths are fine.
	if _, err := slices.CreateSecret(ctx, "acme", "DEPLOY_TOKEN", "x", []string{"/acme/web"}, "user_alice"); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("CreateSecret on a path that has the name = %v, want ErrConflict", err)
	}
	if _, err := slices.UpdateSecret(ctx, root.ID, nil, []string{"/acme", "/acme/payment"}, "user_bob"); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("UpdateSecret onto a path that has the name = %v, want ErrConflict", err)
	}

	// Paths stay inside the account, canonical, and bounded.
	if _, err := store.db.ExecContext(ctx, `insert into accounts(id, slug, kind) values ('acct_other', 'other', 'org')`); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]string{nil, {""}, {"/other/x"}, {"/acmex"}, {"/acme/../other"}, {"/"}} {
		if _, err := slices.CreateSecret(ctx, "acme", "TOKEN", "x", bad, "user_alice"); !errors.Is(err, storage.ErrInvalid) {
			t.Fatalf("CreateSecret(paths %q) = %v, want ErrInvalid", bad, err)
		}
	}
	var many []string
	for i := 0; i <= storage.MaxSecretPaths; i++ {
		many = append(many, "/acme/f"+strings.Repeat("x", i))
	}
	if _, err := slices.CreateSecret(ctx, "acme", "TOKEN", "x", many, "user_alice"); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("CreateSecret with %d paths = %v, want ErrInvalid", len(many), err)
	}

	// Rescoping, with a new value.
	value := "rotated"
	updated, err := slices.UpdateSecret(ctx, payment.ID, &value, []string{"/acme/payment/shared"}, "user_bob")
	if err != nil {
		t.Fatal(err)
	}
	if updated.UpdatedBy != "user_bob" || !reflect.DeepEqual(updated.Paths, []string{"/acme/payment/shared"}) {
		t.Fatalf("UpdateSecret = %+v", updated)
	}
	at, _ := slices.SecretValuesAt(ctx, "/acme/payment")
	shared, _ := slices.SecretValuesAt(ctx, "/acme/payment/shared")
	if at["DEPLOY_TOKEN"] != "default" || shared["DEPLOY_TOKEN"] != "rotated" {
		t.Fatalf("after rescoping /acme/payment = %v, /acme/payment/shared = %v", at, shared)
	}
	// Leaving the paths out keeps them.
	kept, err := slices.UpdateSecret(ctx, payment.ID, &value, nil, "user_bob")
	if err != nil || !reflect.DeepEqual(kept.Paths, []string{"/acme/payment/shared"}) {
		t.Fatalf("UpdateSecret value only = %+v, %v", kept, err)
	}

	listed, err := slices.ListSecrets(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 3 || !reflect.DeepEqual(listed[0].Paths, []string{"/acme"}) {
		t.Fatalf("ListSecrets = %+v", listed)
	}
	if err := slices.DeleteSecret(ctx, root.ID); err != nil {
		t.Fatal(err)
	}
	if err := slices.DeleteSecret(ctx, root.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second DeleteSecret = %v, want ErrNotFound", err)
	}
}

func TestListSliceSecretsCoversAndNests(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	slices := store.Slices()
	// backend includes /acme/backend and /acme/payment/shared.
	want := map[string]bool{}
	for name, p := range map[string]string{
		"ROOT":    "/acme",
		"PAYMENT": "/acme/payment",
		"DEEP":    "/acme/backend/api",
		"WEB":     "/acme/web",
	} {
		secret, err := slices.CreateSecret(ctx, "acme", name, "x", []string{p}, "user_alice")
		if err != nil {
			t.Fatal(err)
		}
		if name != "WEB" {
			want[secret.ID] = true
		}
	}
	got, err := slices.ListSliceSecrets(ctx, "slice_acme_backend")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("ListSliceSecrets = %+v, want %d secrets", got, len(want))
	}
	for _, secret := range got {
		if !want[secret.SecretID] || len(secret.Paths) != 1 {
			t.Fatalf("ListSliceSecrets has %+v", secret)
		}
	}
}

func TestSliceSecretCallsUseIncludedPaths(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	slices := store.Slices()

	// backend has two included paths; a new secret gets both.
	if err := slices.SetSliceSecret(ctx, "slice_acme_backend", "TOKEN", "backend", "user_alice"); err != nil {
		t.Fatal(err)
	}
	listed, _ := slices.ListSecrets(ctx, "acme")
	if len(listed) != 1 || !reflect.DeepEqual(listed[0].Paths, []string{"/acme/backend", "/acme/payment/shared"}) {
		t.Fatalf("after SetSliceSecret = %+v", listed)
	}
	// Setting it again changes the value in place.
	if err := slices.SetSliceSecret(ctx, "slice_acme_backend", "TOKEN", "backend2", "user_alice"); err != nil {
		t.Fatal(err)
	}
	if values, _ := slices.SecretValuesAt(ctx, "/acme/payment/shared"); values["TOKEN"] != "backend2" {
		t.Fatalf("TOKEN = %q, want backend2", values["TOKEN"])
	}

	// A secret that also applies elsewhere is not changed from a slice.
	shared, err := slices.CreateSecret(ctx, "acme", "SHARED", "shared", []string{"/acme/payment", "/acme/web"}, "user_alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := slices.SetSliceSecret(ctx, "slice_acme_payment", "SHARED", "mine", "user_alice"); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("SetSliceSecret over a shared secret = %v, want ErrConflict", err)
	}
	// Deleting it from the slice takes the slice's paths out of it.
	if err := slices.DeleteSliceSecret(ctx, "slice_acme_payment", "SHARED", "user_alice"); err != nil {
		t.Fatal(err)
	}
	got, err := slices.GetSecret(ctx, shared.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Paths, []string{"/acme/web"}) {
		t.Fatalf("paths after DeleteSliceSecret = %v, want [/acme/web]", got.Paths)
	}
	// A secret only on the slice's paths is deleted.
	if err := slices.DeleteSliceSecret(ctx, "slice_acme_backend", "TOKEN", "user_alice"); err != nil {
		t.Fatal(err)
	}
	if listed, _ := slices.ListSecrets(ctx, "acme"); len(listed) != 1 {
		t.Fatalf("after deleting TOKEN = %+v", listed)
	}
	// A secret on a folder above the slice is not removed from it, but a
	// slice secret overrides it.
	if _, err := slices.CreateSecret(ctx, "acme", "NPM_TOKEN", "npm", []string{"/acme"}, "user_alice"); err != nil {
		t.Fatal(err)
	}
	if err := slices.DeleteSliceSecret(ctx, "slice_acme_payment", "NPM_TOKEN", "user_alice"); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("DeleteSliceSecret of an account-root secret = %v, want ErrConflict", err)
	}
	if err := slices.SetSliceSecret(ctx, "slice_acme_payment", "NPM_TOKEN", "payment", "user_alice"); err != nil {
		t.Fatal(err)
	}
	payment, _ := slices.SecretValuesAt(ctx, "/acme/payment")
	backend, _ := slices.SecretValuesAt(ctx, "/acme/backend")
	if payment["NPM_TOKEN"] != "payment" || backend["NPM_TOKEN"] != "npm" {
		t.Fatalf("payment = %v, backend = %v", payment, backend)
	}
	// Nothing happens when no secret by the name applies.
	if err := slices.DeleteSliceSecret(ctx, "slice_acme_payment", "MISSING", "user_alice"); err != nil {
		t.Fatalf("DeleteSliceSecret of a missing name = %v", err)
	}
}

func TestSecretsKeepPathsWhenSliceDeleted(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	secret, err := store.Slices().CreateSecret(ctx, "acme", "TOKEN", "x", []string{"/acme/payment"}, "user_alice")
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
	if !reflect.DeepEqual(got.Paths, []string{"/acme/payment"}) {
		t.Fatalf("paths after the slice was deleted = %v, want [/acme/payment]", got.Paths)
	}
}

// TestSecretPathsMigration checks migration 0047: secrets written before
// paths (all_slices, or listing slices) get paths once, and a later rescope
// is not undone by the next start.
func TestSecretPathsMigration(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	for _, stmt := range []string{
		`insert into secrets(id, account_id, name, value, all_slices) values ('sec_all', 'acct_acme', 'NPM_TOKEN', 'npm', true)`,
		`insert into secrets(id, account_id, name, value) values ('sec_listed', 'acct_acme', 'DEPLOY_TOKEN', 'deploy')`,
		`insert into secret_slices(secret_id, slice_id, name) values ('sec_listed', 'slice_acme_payment', 'DEPLOY_TOKEN')`,
		`insert into secret_slices(secret_id, slice_id, name) values ('sec_listed', 'slice_acme_backend', 'DEPLOY_TOKEN')`,
	} {
		if _, err := store.db.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	all, err := store.Slices().GetSecret(ctx, "sec_all")
	if err != nil {
		t.Fatal(err)
	}
	listed, err := store.Slices().GetSecret(ctx, "sec_listed")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(all.Paths, []string{"/acme"}) || !reflect.DeepEqual(listed.Paths, []string{"/acme/backend", "/acme/payment", "/acme/payment/shared"}) {
		t.Fatalf("after migration all = %v, listed = %v", all.Paths, listed.Paths)
	}
	if _, err := store.Slices().UpdateSecret(ctx, "sec_listed", nil, []string{"/acme/web"}, "user_alice"); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	listed, _ = store.Slices().GetSecret(ctx, "sec_listed")
	if !reflect.DeepEqual(listed.Paths, []string{"/acme/web"}) {
		t.Fatalf("after a second start listed = %v, want [/acme/web]", listed.Paths)
	}
}

func storedSecretValue(t *testing.T, ctx context.Context, store *DB, path, name string) string {
	t.Helper()
	var stored string
	if err := store.db.QueryRowContext(ctx, `
		select secrets.value from secrets join secret_paths on secret_paths.secret_id = secrets.id
		where secret_paths.path = $1 and secret_paths.name = $2
	`, path, name).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	return stored
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
