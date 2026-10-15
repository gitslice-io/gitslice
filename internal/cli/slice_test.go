package cli

import (
	"context"
	"strings"
	"testing"
)

func TestResolveSliceRefInputAcceptsExplicitSliceOffline(t *testing.T) {
	r := Runner{}
	explicit, err := r.resolveSliceRefInput(context.Background(), UserConfig{SubjectID: "user_alice"}, nil, "acme/Payment_API")
	if err != nil {
		t.Fatal(err)
	}
	if explicit.Account != "acme" || explicit.Slice != "payment-api" {
		t.Fatalf("unexpected explicit slice ref: %#v", explicit)
	}

	// A bare slug now expands to the signed-in personal account via a server
	// round-trip (the account is no longer derivable from the subject id), so
	// offline resolution without a connection requires an explicit account.
	if _, err := r.resolveSliceRefInput(context.Background(), UserConfig{SubjectID: "user_alice"}, nil, "Payment_API"); !isUserErrorCode(err, "account_required") {
		t.Fatalf("bare slice ref offline = %v, want account_required", err)
	}
}

func TestResolveSliceRefInputRejectsBareSliceWithoutAccount(t *testing.T) {
	r := Runner{}
	_, err := r.resolveSliceRefInput(context.Background(), UserConfig{}, nil, "payment")
	if err == nil {
		t.Fatal("expected account_required error")
	}
	if !isUserErrorCode(err, "account_required") {
		t.Fatalf("expected account_required, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "account:slice") || !strings.Contains(err.Error(), "gs auth status") {
		t.Fatalf("expected bare slice recovery hint, got:\n%v", err)
	}
}
