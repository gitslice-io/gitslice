package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"gitslice.io/gitslice/internal/storage/memory"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"gitslice.io/gitslice/service"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestAuthRegisterAgentValidatesUsernameLocally(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
	// An unroutable server proves no RPC is attempted.
	err := r.Run(context.Background(), []string{"auth", "register-agent", "--server", "127.0.0.1:1", "--username", "ab", "--email", "owner@example.com"})
	if err == nil || !strings.Contains(err.Error(), "at least 4 characters") {
		t.Fatalf("err = %v; want a local username error", err)
	}
	err = r.Run(context.Background(), []string{"auth", "register-agent", "--server", "127.0.0.1:1", "--username", "good-name", "--email", "nope"})
	if err == nil || !strings.Contains(err.Error(), "not a valid address") {
		t.Fatalf("err = %v; want a local email error", err)
	}
}

func TestAuthRegisterAgentPrintsClaimSteps(t *testing.T) {
	serverAddr := startAgentSignupServer(t, true)
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
	if err := r.Run(context.Background(), []string{"auth", "register-agent", "--server", serverAddr, "--web-url", "https://gitslice.test", "--username", "Claim_Bot", "--email", "owner@example.com"}); err != nil {
		t.Fatalf("register-agent: %v\n%s", err, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"account claim-bot", "https://gitslice.test/claims", "sign in with owner@example.com", "gs claims accept agent_", "gs init claim-bot:home"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "gsk_") {
		t.Fatalf("output leaked the API key:\n%s", out)
	}
	if _, err := os.Stat(r.pendingAgentRegistrationPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending registration file should be removed after success, stat err = %v", err)
	}
}

// The server commits the registration but the first response is lost. The CLI
// must retry with the same registration token and resume the same agent.
func TestAuthRegisterAgentResumesAfterLostResponse(t *testing.T) {
	previousDelay := registerAgentRetryDelay
	registerAgentRetryDelay = 0
	t.Cleanup(func() { registerAgentRetryDelay = previousDelay })

	mem := memory.New()
	handlers := service.New(service.Stores{
		Auth: mem.Auth, Blobs: mem.Blobs, Changesets: mem.Changesets, Repository: mem.Repository,
		Slices: mem.Slices, Agents: mem.Agents, Checks: mem.Checks,
	}, mem.Objects, nil)
	handlers.Auth.AgentSignupEnabled = true
	var calls atomic.Int32
	grpcServer := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		resp, err := handler(ctx, req)
		if calls.Add(1) == 1 && err == nil {
			return nil, status.Error(codes.Unavailable, "connection reset after commit")
		}
		return resp, err
	}))
	corev1.RegisterAuthServiceServer(grpcServer, handlers.Auth)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = grpcServer.Serve(lis) }()
	t.Cleanup(grpcServer.Stop)

	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
	if err := r.Run(context.Background(), []string{"auth", "register-agent", "--server", lis.Addr().String(), "--username", "resume-bot", "--email", "owner@example.com", "--json"}); err != nil {
		t.Fatalf("register-agent: %v\nstderr:\n%s", err, stderr.String())
	}
	if calls.Load() != 2 || !strings.Contains(stderr.String(), "retrying safely") {
		t.Fatalf("calls = %d, stderr = %q; want one automatic retry", calls.Load(), stderr.String())
	}
	var got struct {
		SubjectID string `json:"subject_id"`
		Account   string `json:"account"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	cfg, err := r.readPartialUserConfig()
	if err != nil {
		t.Fatal(err)
	}
	subject, err := mem.Auth.SubjectForToken(context.Background(), cfg.Token)
	if err != nil || subject.ID != got.SubjectID || got.Account != "resume-bot" {
		t.Fatalf("saved key resolves to %+v (%v); want %s", subject, err, got.SubjectID)
	}
	if role, err := mem.Auth.AccountRole(context.Background(), got.SubjectID, "resume-bot"); err != nil || role != "admin" {
		t.Fatalf("resumed agent role = %q, %v", role, err)
	}
}

func TestBrowsePrintWarnsForPrivateSlice(t *testing.T) {
	serverAddr := startAgentSignupServer(t, true)
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
	if err := r.Run(context.Background(), []string{"auth", "register-agent", "--server", serverAddr, "--username", "browse-bot", "--email", "owner@example.com", "--quiet"}); err != nil {
		t.Fatalf("register-agent: %v\n%s", err, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if err := r.Run(context.Background(), []string{"browse", "--print", "--web-url", "https://gitslice.test", "slices/browse-bot/home"}); err != nil {
		t.Fatalf("browse: %v\n%s", err, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "https://gitslice.test/slices/browse-bot/home" {
		t.Fatalf("stdout = %q; want only the URL", got)
	}
	if !strings.Contains(stderr.String(), "browse-bot/home is private") || !strings.Contains(stderr.String(), "--visibility public") {
		t.Fatalf("stderr = %q; want a private-slice note", stderr.String())
	}
}
