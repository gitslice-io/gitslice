package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/cli/cmd/account"
	"gitslice.io/gitslice/internal/cli/cmd/secret"
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

func TestEnhanceAndClassifyStandardGRPCStatusCodes(t *testing.T) {
	r := Runner{Home: t.TempDir(), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	cases := []struct {
		grpcCode  codes.Code
		message   string
		wantCode  string
		retriable bool
		wantHint  string
	}{
		{codes.NotFound, "slice missing", "not_found", false, ""},
		{codes.AlreadyExists, "slice exists", "already_exists", false, ""},
		{codes.InvalidArgument, "bad path", "invalid_argument", false, ""},
		{codes.FailedPrecondition, "conflict: stale base", "failed_precondition", false, ""},
		{codes.ResourceExhausted, "quota exceeded", "resource_exhausted", false, ""},
		{codes.Unimplemented, "not implemented", "unsupported", false, "Upgrade the server."},
		{codes.DeadlineExceeded, "timed out", "deadline_exceeded", true, ""},
		{codes.Canceled, "caller canceled", "canceled", false, ""},
		{codes.Unavailable, "connection reset", "server_unavailable", true, ""},
	}
	for _, tc := range cases {
		raw := status.Error(tc.grpcCode, tc.message)
		enhanced := r.enhanceCommandError(raw)
		var cmdErr commandError
		if !errors.As(enhanced, &cmdErr) {
			t.Fatalf("%s: enhanceCommandError did not return commandError: %T (%v)", tc.grpcCode, enhanced, enhanced)
		}
		if cmdErr.Code != tc.wantCode || cmdErr.Message != tc.message || cmdErr.Retriable != tc.retriable || cmdErr.Hint != tc.wantHint {
			t.Fatalf("%s: cmdErr = %+v, want code=%q msg=%q retriable=%v hint=%q", tc.grpcCode, cmdErr, tc.wantCode, tc.message, tc.retriable, tc.wantHint)
		}
		if status.Code(enhanced) != tc.grpcCode {
			t.Fatalf("%s: status.Code(enhanced) = %s, want %s", tc.grpcCode, status.Code(enhanced), tc.grpcCode)
		}

		// Direct classifyError on raw gRPC error produces the same classification.
		classified := classifyError(raw).Error
		if classified.Code != tc.wantCode || classified.Message != tc.message || classified.Retriable != tc.retriable || classified.Hint != tc.wantHint {
			t.Fatalf("%s: classifyError(raw) = %+v", tc.grpcCode, classified)
		}
	}
}

func TestTriggerErrorPreservesUnavailableAndMapsUnimplemented(t *testing.T) {
	r := Runner{Home: t.TempDir(), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}

	// Network drop (codes.Unavailable) must NOT be masked as "unsupported".
	dropped := &rpcUnavailableError{
		method:   "/gitslice.core.v1.AutomationService/ListAutomations",
		safe:     true,
		attempts: 3,
		err:      status.Error(codes.Unavailable, "connection reset by peer"),
	}
	enhancedDrop := r.enhanceCommandError(automationError(dropped))
	var dropCmd commandError
	if !errors.As(enhancedDrop, &dropCmd) {
		t.Fatalf("expected commandError for dropped trigger RPC, got %T: %v", enhancedDrop, enhancedDrop)
	}
	if dropCmd.Code != "server_unavailable" || !dropCmd.Retriable {
		t.Fatalf("trigger Unavailable mapped to %+v, want server_unavailable (retriable=true)", dropCmd)
	}

	rawUnavailable := r.enhanceCommandError(automationError(status.Error(codes.Unavailable, "transport closing")))
	if !errors.As(rawUnavailable, &dropCmd) || dropCmd.Code != "server_unavailable" || !dropCmd.Retriable {
		t.Fatalf("raw trigger Unavailable mapped to %+v, want server_unavailable (retriable=true)", dropCmd)
	}

	// Unimplemented still maps to unsupported with trigger-specific message.
	unimpl := r.enhanceCommandError(automationError(status.Error(codes.Unimplemented, "unknown service")))
	var unimplCmd commandError
	if !errors.As(unimpl, &unimplCmd) || unimplCmd.Code != "unsupported" || unimplCmd.Message != "this server does not run automations" || unimplCmd.Hint != "Upgrade the server." {
		t.Fatalf("trigger Unimplemented = %+v", unimplCmd)
	}
}

func TestDomainErrorHintOverridesAndMCPSharing(t *testing.T) {
	r := Runner{Home: t.TempDir(), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}

	accInvite := r.enhanceCommandError(account.WrapError(status.Error(codes.FailedPrecondition, "must invite them to the organization")))
	var cmdErr commandError
	if !errors.As(accInvite, &cmdErr) || cmdErr.Code != "failed_precondition" || !strings.Contains(cmdErr.Hint, "gs account invite") {
		t.Fatalf("account FailedPrecondition = %+v", cmdErr)
	}

	accLimit := r.enhanceCommandError(account.WrapError(status.Error(codes.ResourceExhausted, "too many invitations")))
	if !errors.As(accLimit, &cmdErr) || cmdErr.Code != "limit_reached" {
		t.Fatalf("account ResourceExhausted = %+v", cmdErr)
	}

	grpExists := r.enhanceCommandError(groupError(status.Error(codes.AlreadyExists, "group exists")))
	if !errors.As(grpExists, &cmdErr) || cmdErr.Code != "already_exists" || cmdErr.Hint != "Pick another group name." {
		t.Fatalf("group AlreadyExists = %+v", cmdErr)
	}

	secUnimpl := r.enhanceCommandError(secret.WrapError(status.Error(codes.Unimplemented, "unimplemented")))
	if !errors.As(secUnimpl, &cmdErr) || cmdErr.Code != "unsupported" || !strings.Contains(cmdErr.Hint, "gs slice secret") {
		t.Fatalf("secret Unimplemented = %+v", cmdErr)
	}

	// mcpToolError shares classifyErrorBody while preserving MCP wire codes.
	mcpNotFound := mcpToolError(status.Error(codes.NotFound, "file not found: /a"))
	if mcpNotFound["code"] != "notfound" || mcpNotFound["message"] != "file not found: /a" {
		t.Fatalf("mcpToolError(NotFound) = %v", mcpNotFound)
	}
	mcpDropped := mcpToolError(&rpcUnavailableError{
		method:   "/gitslice.core.v1.RepositoryService/ReadFile",
		safe:     true,
		attempts: 3,
		err:      status.Error(codes.Unavailable, "EOF"),
	})
	if mcpDropped["code"] != "server_unavailable" || mcpDropped["retriable"] != true || mcpDropped["hint"] == "" {
		t.Fatalf("mcpToolError(rpcUnavailableError) = %v", mcpDropped)
	}
}
