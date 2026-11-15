package secretbox

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	box := newTestBox(t, strings.Repeat("a", 32))

	sealed, err := box.Seal("super secret")
	if err != nil {
		t.Fatal(err)
	}
	if !IsSealed(sealed) {
		t.Fatalf("Seal() = %q, want enc:v1 prefix", sealed)
	}
	opened, err := box.Open(sealed)
	if err != nil {
		t.Fatal(err)
	}
	if opened != "super secret" {
		t.Fatalf("Open() = %q, want %q", opened, "super secret")
	}
}

func TestSealUsesRandomNonce(t *testing.T) {
	box := newTestBox(t, strings.Repeat("a", 32))
	first, err := box.Seal("same plaintext")
	if err != nil {
		t.Fatal(err)
	}
	second, err := box.Seal("same plaintext")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("Seal() returned identical values, want a random nonce per call")
	}
}

func TestWrongKeyFails(t *testing.T) {
	first := newTestBox(t, strings.Repeat("a", 32))
	second := newTestBox(t, strings.Repeat("b", 32))
	sealed, err := first.Seal("super secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.Open(sealed); err == nil {
		t.Fatal("Open() = nil error with wrong key, want authentication error")
	}
}

func TestTamperedCiphertextFails(t *testing.T) {
	box := newTestBox(t, strings.Repeat("a", 32))
	sealed, err := box.Seal("super secret")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(sealed, sealedPrefix))
	if err != nil {
		t.Fatal(err)
	}
	payload[len(payload)-1] ^= 1
	tampered := sealedPrefix + base64.StdEncoding.EncodeToString(payload)
	if _, err := box.Open(tampered); err == nil {
		t.Fatal("Open() = nil error for tampered ciphertext, want authentication error")
	}
}

func TestLegacyPlaintextPassthrough(t *testing.T) {
	box := newTestBox(t, strings.Repeat("a", 32))
	opened, err := box.Open("legacy plaintext")
	if err != nil {
		t.Fatal(err)
	}
	if opened != "legacy plaintext" {
		t.Fatalf("Open() = %q, want legacy plaintext unchanged", opened)
	}
	if IsSealed(opened) {
		t.Fatal("IsSealed() = true for legacy plaintext, want false")
	}
}

func TestNewAcceptsStandardAndRawStandardBase64(t *testing.T) {
	key := []byte(strings.Repeat("a", 32))
	for _, encoded := range []string{
		base64.StdEncoding.EncodeToString(key),
		base64.RawStdEncoding.EncodeToString(key),
	} {
		if _, err := New(encoded); err != nil {
			t.Fatalf("New(%q) = %v, want nil", encoded, err)
		}
	}
}

func TestNewRejectsBadKeys(t *testing.T) {
	tests := []struct {
		name string
		key  string
	}{
		{name: "bad encoding", key: "not base64!"},
		{name: "empty", key: ""},
		{name: "31 bytes", key: base64.StdEncoding.EncodeToString([]byte(strings.Repeat("a", 31)))},
		{name: "33 bytes", key: base64.StdEncoding.EncodeToString([]byte(strings.Repeat("a", 33)))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(tt.key); err == nil {
				t.Fatal("New() = nil error, want rejection")
			}
		})
	}
}

func TestOpenRejectsMalformedSealedValues(t *testing.T) {
	box := newTestBox(t, strings.Repeat("a", 32))
	for _, stored := range []string{
		sealedPrefix + "not base64!",
		sealedPrefix + base64.StdEncoding.EncodeToString([]byte("short")),
	} {
		if _, err := box.Open(stored); err == nil {
			t.Fatalf("Open(%q) = nil error, want rejection", stored)
		}
	}
}

func newTestBox(t *testing.T, key string) *Box {
	t.Helper()
	box, err := New(base64.StdEncoding.EncodeToString([]byte(key)))
	if err != nil {
		t.Fatal(err)
	}
	return box
}
