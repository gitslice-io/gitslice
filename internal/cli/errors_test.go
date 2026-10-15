package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestExitCodeForError(t *testing.T) {
	if got := exitCodeForError(nil); got != 0 {
		t.Fatalf("nil exit code = %d, want 0", got)
	}
	if got := exitCodeForError(errors.New("boom")); got != 1 {
		t.Fatalf("general exit code = %d, want 1", got)
	}
	if got := exitCodeForError(context.Canceled); got != 2 {
		t.Fatalf("canceled exit code = %d, want 2", got)
	}
	if got := exitCodeForError(userError("not_logged_in", "not logged in", "")); got != 4 {
		t.Fatalf("not logged in exit code = %d, want 4", got)
	}
	if got := exitCodeForError(status.Error(codes.Unauthenticated, "invalid token")); got != 4 {
		t.Fatalf("unauthenticated exit code = %d, want 4", got)
	}
}

func TestUnauthenticatedErrorsIncludeRecoveryHint(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
	if err := r.writeUserConfig(UserConfig{
		ServerAddr: "127.0.0.1:50051",
		Token:      "stale-token",
		SubjectID:  "user_alice",
	}); err != nil {
		t.Fatal(err)
	}

	err := r.enhanceCommandError(status.Error(codes.Unauthenticated, "invalid token"))
	if !strings.Contains(err.Error(), "gs auth status") {
		t.Fatalf("expected auth status recovery hint, got:\n%v", err)
	}
	if !strings.Contains(err.Error(), "gs auth login") {
		t.Fatalf("expected login recovery hint, got:\n%v", err)
	}
}

func TestInvalidFormatReturnsStructuredCommandError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := Runner{Stdout: &stdout, Stderr: &stderr}
	err := r.Run(context.Background(), []string{"status", "--format", "yaml"})
	if err == nil {
		t.Fatal("status with invalid format unexpectedly succeeded")
	}
	var cmdErr commandError
	if !errors.As(err, &cmdErr) {
		t.Fatalf("expected commandError, got %T: %v", err, err)
	}
	if cmdErr.Code != "invalid_format" {
		t.Fatalf("unexpected error code %q", cmdErr.Code)
	}
}
