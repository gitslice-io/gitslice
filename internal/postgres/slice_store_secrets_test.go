package postgres

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/gitslice-io/gitslice/internal/secretbox"
)

func TestSliceSecretsEncryptionAtRest(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	box := postgresTestSecretBox(t)
	store.Slices().Secrets = box

	if err := store.Slices().SetSliceSecret(ctx, "slice_acme_payment", "API_TOKEN", "super secret"); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := store.db.QueryRowContext(ctx, `
		select value from slice_secrets where slice_id = $1 and name = $2
	`, "slice_acme_payment", "API_TOKEN").Scan(&stored); err != nil {
		t.Fatal(err)
	}
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
		insert into slice_secrets(slice_id, name, value, created_at, updated_at)
		values ($1, $2, $3, now(), now())
	`, "slice_acme_payment", "LEGACY_TOKEN", "legacy plaintext"); err != nil {
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
	if err := store.Slices().SetSliceSecret(ctx, "slice_acme_payment", "API_TOKEN", "plaintext dev value"); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := store.db.QueryRowContext(ctx, `
		select value from slice_secrets where slice_id = $1 and name = $2
	`, "slice_acme_payment", "API_TOKEN").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "plaintext dev value" {
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
	if err := store.Slices().SetSliceSecret(ctx, "slice_acme_payment", "API_TOKEN", "super secret"); err != nil {
		t.Fatal(err)
	}
	store.Slices().Secrets = nil

	if _, err := store.Slices().GetSliceSecrets(ctx, "slice_acme_payment"); err == nil {
		t.Fatal("GetSliceSecrets() = nil error for encrypted value without box, want configuration error")
	}
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
