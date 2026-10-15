package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"

	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestAuthStatusReportsSignedOut(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
	if err := r.Run(context.Background(), []string{"auth", "status", "--json"}); err != nil {
		t.Fatalf("auth status failed: %v\nstderr:\n%s", err, stderr.String())
	}

	var got authStatusOutput
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("auth status output is not JSON: %v\n%s", err, stdout.String())
	}
	if got.SignedIn {
		t.Fatalf("expected signed out status, got %#v", got)
	}
	if got.ServerAddr != "" || got.SubjectID != "" {
		t.Fatalf("signed out status exposed config fields: %#v", got)
	}
}

func TestAuthStatusReportsStoredLoginWithoutToken(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
	serverAddr := startFakeAuthStatusServer(t, "secret-token", "user_alice")
	if err := r.writeUserConfig(UserConfig{
		ServerAddr: serverAddr,
		Token:      "secret-token",
		SubjectID:  "stale_local_subject",
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), []string{"auth", "status", "--json"}); err != nil {
		t.Fatalf("auth status failed: %v\nstderr:\n%s", err, stderr.String())
	}
	if bytes.Contains(stdout.Bytes(), []byte("secret-token")) || bytes.Contains(stdout.Bytes(), []byte("token")) {
		t.Fatalf("auth status leaked token data:\n%s", stdout.String())
	}

	var got authStatusOutput
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("auth status output is not JSON: %v\n%s", err, stdout.String())
	}
	if !got.SignedIn {
		t.Fatalf("expected signed in status, got %#v", got)
	}
	if got.ServerAddr != serverAddr || got.SubjectID != "user_alice" {
		t.Fatalf("unexpected auth status: %#v", got)
	}
}

func TestAuthStatusReportsInvalidStoredToken(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
	serverAddr := startFakeAuthStatusServer(t, "valid-token", "user_alice")
	if err := r.writeUserConfig(UserConfig{
		ServerAddr: serverAddr,
		Token:      "stale-token",
		SubjectID:  "user_alice",
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), []string{"auth", "status", "--json"}); err != nil {
		t.Fatalf("auth status failed: %v\nstderr:\n%s", err, stderr.String())
	}

	var got authStatusOutput
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("auth status output is not JSON: %v\n%s", err, stdout.String())
	}
	if got.SignedIn {
		t.Fatalf("expected signed out status, got %#v", got)
	}
	if got.ServerAddr != serverAddr || got.SubjectID != "" || got.Reason != "invalid_token" {
		t.Fatalf("unexpected auth status for invalid token: %#v", got)
	}
}

func TestAuthTokenPrintsValidatedToken(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
	serverAddr := startFakeAuthStatusServer(t, "secret-token", "user_alice")
	if err := r.writeUserConfig(UserConfig{
		ServerAddr: serverAddr,
		Token:      "secret-token",
		SubjectID:  "stale_local_subject",
	}); err != nil {
		t.Fatal(err)
	}

	if err := r.Run(context.Background(), []string{"auth", "token"}); err != nil {
		t.Fatalf("auth token failed: %v\nstderr:\n%s", err, stderr.String())
	}
	if got, want := strings.TrimSpace(stdout.String()), "secret-token"; got != want {
		t.Fatalf("auth token output = %q, want %q", got, want)
	}

	stdout.Reset()
	stderr.Reset()
	if err := r.Run(context.Background(), []string{"auth", "token", "--json"}); err != nil {
		t.Fatalf("auth token --json failed: %v\nstderr:\n%s", err, stderr.String())
	}
	var got authTokenOutput
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("auth token JSON is invalid: %v\n%s", err, stdout.String())
	}
	if got.Token != "secret-token" || got.ServerAddr != serverAddr || got.SubjectID != "user_alice" {
		t.Fatalf("unexpected auth token JSON: %#v", got)
	}
}

func TestAuthTokenRejectsInvalidStoredToken(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
	serverAddr := startFakeAuthStatusServer(t, "valid-token", "user_alice")
	if err := r.writeUserConfig(UserConfig{
		ServerAddr: serverAddr,
		Token:      "stale-token",
		SubjectID:  "user_alice",
	}); err != nil {
		t.Fatal(err)
	}

	err := r.Run(context.Background(), []string{"auth", "token"})
	if err == nil {
		t.Fatal("expected invalid token error")
	}
	if !isUserErrorCode(err, "invalid_token") {
		t.Fatalf("expected invalid_token, got %T: %v", err, err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("auth token printed invalid token:\n%s", stdout.String())
	}
	if !strings.Contains(err.Error(), "gs auth status") || !strings.Contains(err.Error(), "gs auth login") {
		t.Fatalf("expected recovery hint, got:\n%v", err)
	}
}

func TestAuthLogoutClearsTokenAndPreservesLocalConfig(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
	if err := r.writeUserConfig(UserConfig{
		ServerAddr: "127.0.0.1:50051",
		Token:      "secret-token",
		SubjectID:  "user_alice",
		Aliases:    map[string]string{"who": "version"},
	}); err != nil {
		t.Fatal(err)
	}

	if err := r.Run(context.Background(), []string{"auth", "logout", "--json"}); err != nil {
		t.Fatalf("auth logout failed: %v\nstderr:\n%s", err, stderr.String())
	}
	var out map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("auth logout output is not JSON: %v\n%s", err, stdout.String())
	}
	if out["signed_in"] != false || out["was_signed_in"] != true || out["server_addr"] != "127.0.0.1:50051" {
		t.Fatalf("unexpected auth logout output: %#v", out)
	}

	cfg, err := r.readPartialUserConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != "" || cfg.SubjectID != "" {
		t.Fatalf("auth logout did not clear credentials: %#v", cfg)
	}
	if cfg.ServerAddr != "127.0.0.1:50051" || cfg.Aliases["who"] != "version" {
		t.Fatalf("auth logout did not preserve local config: %#v", cfg)
	}
}

type fakeAuthStatusServer struct {
	corev1.UnimplementedAuthServiceServer
	subjectID string
}

func (f fakeAuthStatusServer) GetAuthStatus(ctx context.Context, req *corev1.GetAuthStatusRequest) (*corev1.GetAuthStatusResponse, error) {
	return &corev1.GetAuthStatusResponse{SubjectId: f.subjectID}, nil
}

func startFakeAuthStatusServer(t *testing.T, validToken, subjectID string) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "missing metadata")
		}
		values := md.Get("authorization")
		if len(values) != 1 || values[0] != "Bearer "+validToken {
			return nil, status.Error(codes.Unauthenticated, "invalid token")
		}
		return handler(ctx, req)
	}))
	corev1.RegisterAuthServiceServer(server, fakeAuthStatusServer{subjectID: subjectID})
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Serve(lis)
	}()
	t.Cleanup(func() {
		server.Stop()
		_ = lis.Close()
		if err := <-errCh; err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			t.Errorf("fake auth status server failed: %v", err)
		}
	})
	return lis.Addr().String()
}
