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

func TestContains(t *testing.T) {
	if !Contains("/acme/payment", "/acme/payment/app.go") {
		t.Fatal("expected child path to be contained")
	}
	if Contains("/acme/payment", "/acme/payments/app.go") {
		t.Fatal("unexpected prefix match")
	}
}
