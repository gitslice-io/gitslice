package paths

import "testing"

func TestCanonical(t *testing.T) {
	got, err := Canonical("acme/payment/../payment/app.go")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/acme/payment/app.go" {
		t.Fatalf("got %q", got)
	}
}

func TestCanonicalRejectsAccountRoot(t *testing.T) {
	if _, err := Canonical("/acme"); err == nil {
		t.Fatal("expected account-root path to be rejected by Canonical")
	}
}

func TestCanonicalPrefix(t *testing.T) {
	// Account-root prefix (single segment) is allowed.
	got, err := CanonicalPrefix("/acme")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/acme" {
		t.Fatalf("got %q, want /acme", got)
	}
	// Deeper prefixes still clean normally.
	got, err = CanonicalPrefix("acme/payment/../payment")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/acme/payment" {
		t.Fatalf("got %q, want /acme/payment", got)
	}
	// Empty paths and paths that clean away to the root are still rejected.
	if _, err := CanonicalPrefix(""); err == nil {
		t.Fatal("expected empty path to be rejected")
	}
	if _, err := CanonicalPrefix("/.."); err == nil {
		t.Fatal("expected root path to be rejected")
	}
}

func TestContains(t *testing.T) {
	if !Contains("/acme/payment", "/acme/payment/app.go") {
		t.Fatal("expected child path to be contained")
	}
	if Contains("/acme/payment", "/acme/payments/app.go") {
		t.Fatal("unexpected prefix match")
	}
}

func TestFromWorkspacePath(t *testing.T) {
	got, err := FromWorkspacePath("/acme/payment", "app.go")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/acme/payment/app.go" {
		t.Fatalf("got %q", got)
	}
	got, err = FromWorkspacePath("/acme/payment", "acme/backend/app.go")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/acme/backend/app.go" {
		t.Fatalf("got %q", got)
	}
}
