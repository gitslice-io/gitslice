package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/gitslice-io/gitslice/internal/storage/memory"
	"github.com/gitslice-io/gitslice/server"
	"github.com/gitslice-io/gitslice/service"
	"google.golang.org/grpc"
)

// startAgentSignupServer runs the real gRPC server wiring (auth interceptor,
// public-method allowlist, signup limiter) over in-memory stores.
func startAgentSignupServer(t *testing.T, enabled bool) string {
	t.Helper()
	addr, _ := startAgentSignupServerWithStores(t, enabled, nil)
	return addr
}

// startAgentSignupServerWithStores also returns the stores and resolves each
// staticTokens key to its subject id before falling back to stored tokens.
func startAgentSignupServerWithStores(t *testing.T, enabled bool, staticTokens map[string]string) (string, *memory.Stores) {
	t.Helper()
	mem := memory.New()
	handlers := service.New(service.Stores{
		Auth:       mem.Auth,
		Blobs:      mem.Blobs,
		Changesets: mem.Changesets,
		Repository: mem.Repository,
		Slices:     mem.Slices,
		Agents:     mem.Agents,
		Checks:     mem.Checks,
	}, mem.Objects, nil)
	handlers.Auth.AgentSignupEnabled = enabled
	resolve := func(ctx context.Context, token string) (string, error) {
		if subjectID, ok := staticTokens[token]; ok {
			return subjectID, nil
		}
		subject, err := mem.Auth.SubjectForToken(ctx, token)
		if err != nil {
			return "", err
		}
		return subject.ID, nil
	}
	grpcServer := server.NewGRPCServer(resolve, handlers, server.Config{AgentSignupPerHour: 5})
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	errCh := make(chan error, 1)
	go func() { errCh <- grpcServer.Serve(lis) }()
	t.Cleanup(func() {
		grpcServer.Stop()
		if err := <-errCh; err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			t.Errorf("server failed: %v", err)
		}
	})
	return lis.Addr().String(), mem
}

func TestAuthRegisterAgentSavesKeyAndSignsIn(t *testing.T) {
	serverAddr := startAgentSignupServer(t, true)
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Stdout: &stdout, Stderr: &stderr}

	if err := r.Run(context.Background(), []string{"auth", "register-agent", "--server", serverAddr, "--username", "release-bot", "--email", "Owner@Example.com", "--json"}); err != nil {
		t.Fatalf("register-agent failed: %v\nstderr:\n%s", err, stderr.String())
	}
	if strings.Contains(stdout.String(), "gsk_") {
		t.Fatalf("register-agent printed the API key:\n%s", stdout.String())
	}
	var got struct {
		ServerAddr string `json:"server_addr"`
		SubjectID  string `json:"subject_id"`
		Account    string `json:"account"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, stdout.String())
	}
	if got.ServerAddr != serverAddr || got.Account != "release-bot" || !strings.HasPrefix(got.SubjectID, "agent_") {
		t.Fatalf("unexpected output: %+v", got)
	}

	cfg, err := r.readPartialUserConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(cfg.Token, "gsk_") || cfg.SubjectID != got.SubjectID {
		t.Fatalf("saved config = server %q subject %q token-prefix-ok=%v", cfg.ServerAddr, cfg.SubjectID, strings.HasPrefix(cfg.Token, "gsk_"))
	}

	// The saved key authenticates later commands against the real interceptor.
	stdout.Reset()
	if err := r.Run(context.Background(), []string{"auth", "status", "--json"}); err != nil {
		t.Fatalf("auth status failed: %v\nstderr:\n%s", err, stderr.String())
	}
	var authStatus authStatusOutput
	if err := json.Unmarshal(stdout.Bytes(), &authStatus); err != nil {
		t.Fatalf("auth status output is not JSON: %v\n%s", err, stdout.String())
	}
	if !authStatus.SignedIn || authStatus.SubjectID != got.SubjectID {
		t.Fatalf("auth status = %+v", authStatus)
	}
}

func TestAuthRegisterAgentDisabledServer(t *testing.T) {
	serverAddr := startAgentSignupServer(t, false)
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
	err := r.Run(context.Background(), []string{"auth", "register-agent", "--server", serverAddr, "--username", "release-bot", "--email", "owner@example.com"})
	if err == nil || !strings.Contains(err.Error()+stderr.String(), "disabled") {
		t.Fatalf("expected disabled error, got %v\nstderr:\n%s", err, stderr.String())
	}
	if cfg, err := r.readPartialUserConfig(); err == nil && cfg.Token != "" {
		t.Fatal("a failed registration must not save credentials")
	}
}

func TestAuthRegisterAgentRequiresFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
	if err := r.Run(context.Background(), []string{"auth", "register-agent", "--username", "release-bot"}); err == nil {
		t.Fatal("expected an error without --email")
	}
}
