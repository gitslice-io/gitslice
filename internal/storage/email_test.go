package storage

import (
	"errors"
	"testing"
)

func TestNormalizeOwnerEmail(t *testing.T) {
	got, err := NormalizeOwnerEmail("  Me@Example.COM ")
	if err != nil || got != "me@example.com" {
		t.Fatalf("NormalizeOwnerEmail = %q, %v; want me@example.com", got, err)
	}
	for _, bad := range []string{"", "no-at", "@example.com", "a@b@example.com", "a@localhost", "a@.com", "a@example.", "a b@example.com"} {
		if _, err := NormalizeOwnerEmail(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("NormalizeOwnerEmail(%q) err = %v; want ErrInvalid", bad, err)
		}
	}
}
