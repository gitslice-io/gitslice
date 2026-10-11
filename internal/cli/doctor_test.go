package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	healthv1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// fakeGitslice answers the RPCs gs doctor and gs create --submit make. Its
// one valid token belongs to user_alice, a member of alice (owner) and
// readers (reader). acme/secret is private to acme; the other slices are
// public.
type fakeGitslice struct {
	corev1.UnimplementedAuthServiceServer
	corev1.UnimplementedSliceServiceServer
	corev1.UnimplementedChangesetStackServiceServer
	corev1.UnimplementedChangesetServiceServer
	corev1.UnimplementedRepositoryServiceServer

	mu sync.Mutex
	// pendingSubmits is how many submits answer that the required check has
	// not reported yet.
	pendingSubmits int
	submits        int
	submitted      bool
}

const fakeToken = "secret-token"

func subjectFrom(ctx context.Context) (string, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	values := md.Get("authorization")
	if len(values) == 0 {
		return "", nil
	}
	if values[0] != "Bearer "+fakeToken {
		return "", status.Error(codes.Unauthenticated, "invalid token")
	}
	return "user_alice", nil
}

func (f *fakeGitslice) GetAuthStatus(ctx context.Context, _ *corev1.GetAuthStatusRequest) (*corev1.GetAuthStatusResponse, error) {
	subject, err := subjectFrom(ctx)
	if err != nil {
		return nil, err
	}
	if subject == "" {
		return nil, status.Error(codes.Unauthenticated, "missing subject")
	}
	return &corev1.GetAuthStatusResponse{
		SubjectId: subject,
		Accounts:  []string{"alice", "readers"},
		Memberships: []*corev1.AccountMembership{
			{Account: "alice", Kind: "personal", Role: "owner"},
			{Account: "readers", Kind: "organization", Role: "reader"},
		},
	}, nil
}

func (f *fakeGitslice) ResolveSlice(ctx context.Context, req *corev1.ResolveSliceRequest) (*corev1.Slice, error) {
	subject, err := subjectFrom(ctx)
	if err != nil {
		return nil, err
	}
	ref := req.GetRef()
	switch ref.GetAccount() + "/" + ref.GetSlice() {
	case "acme/secret":
		if subject == "" {
			return nil, status.Error(codes.Unauthenticated, "unauthenticated")
		}
		return nil, status.Error(codes.PermissionDenied, "unauthorized")
	case "alice/home", "readers/docs", "acme/open":
		return &corev1.Slice{Ref: ref, Definition: &corev1.SliceDefinition{Visibility: "public"}}, nil
	}
	return nil, status.Error(codes.NotFound, "not found")
}

func (f *fakeGitslice) GetStack(ctx context.Context, req *corev1.GetStackRequest) (*corev1.ChangesetStack, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cs := &corev1.Changeset{Id: "cs_00112233445566778899aabbccddeeff", Title: "daily sync", CurrentPatchsetId: "ps_1", Status: "draft"}
	if f.submitted {
		cs.Status = "submitted"
	}
	return &corev1.ChangesetStack{Id: req.GetStackId(), Entries: []*corev1.ChangesetStackEntry{{ChangesetId: cs.Id, Changeset: cs}}}, nil
}

func (f *fakeGitslice) SubmitChangeset(ctx context.Context, req *corev1.SubmitChangesetRequest) (*corev1.SubmitChangesetResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.submits++
	if f.submits <= f.pendingSubmits {
		return nil, status.Error(codes.FailedPrecondition, `conflict: required check "ci" has no result for current patchset`)
	}
	if f.pendingSubmits < 0 {
		return nil, status.Error(codes.FailedPrecondition, `conflict: required check "ci" is failing for current patchset`)
	}
	f.submitted = true
	return &corev1.SubmitChangesetResponse{Status: "submitted", CommitId: "sha256:abc"}, nil
}

func (f *fakeGitslice) GetChangeset(ctx context.Context, req *corev1.GetChangesetRequest) (*corev1.Changeset, error) {
	return &corev1.Changeset{Id: req.GetChangesetId(), Status: "submitted", CommitId: "sha256:abc"}, nil
}

func (f *fakeGitslice) GetRef(ctx context.Context, req *corev1.GetRefRequest) (*corev1.Ref, error) {
	return &corev1.Ref{Name: req.GetRefName(), CommitId: "sha256:abc"}, nil
}

func startFakeGitslice(t *testing.T, f *fakeGitslice) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	healthServer := health.NewServer()
	healthServer.SetServingStatus("", healthv1.HealthCheckResponse_SERVING)
	healthv1.RegisterHealthServer(server, healthServer)
	corev1.RegisterAuthServiceServer(server, f)
	corev1.RegisterSliceServiceServer(server, f)
	corev1.RegisterChangesetStackServiceServer(server, f)
	corev1.RegisterChangesetServiceServer(server, f)
	corev1.RegisterRepositoryServiceServer(server, f)
	errCh := make(chan error, 1)
	go func() { errCh <- server.Serve(lis) }()
	t.Cleanup(func() {
		server.Stop()
		if err := <-errCh; err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			t.Errorf("fake server: %v", err)
		}
	})
	return lis.Addr().String()
}

func runDoctorJSON(t *testing.T, r Runner, args ...string) (doctorOutput, error) {
	t.Helper()
	var stdout bytes.Buffer
	r.Stdout = &stdout
	r.Stderr = &bytes.Buffer{}
	err := r.Run(context.Background(), append([]string{"doctor", "--json"}, args...))
	var out doctorOutput
	if jsonErr := json.Unmarshal(stdout.Bytes(), &out); jsonErr != nil {
		t.Fatalf("doctor --json output %q: %v (command error %v)", stdout.String(), jsonErr, err)
	}
	return out, err
}

func doctorCheckNamed(t *testing.T, out doctorOutput, name string) doctorCheck {
	t.Helper()
	for _, check := range out.Checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("no %s check in %+v", name, out.Checks)
	return doctorCheck{}
}

func TestDoctorExplainsSliceAccess(t *testing.T) {
	base, _ := latestReleaseServer(t, "v9.9.9")
	t.Setenv("GS_DOWNLOAD_BASE", base)
	t.Setenv("GS_SERVER_ADDR", "")
	withVersion(t, "v0.1.0")
	addr := startFakeGitslice(t, &fakeGitslice{})
	r := Runner{Home: t.TempDir(), Dir: t.TempDir()}
	if err := r.writeUserConfig(UserConfig{ServerAddr: addr, Token: "gsk_" + fakeToken, SubjectID: "user_alice"}); err != nil {
		t.Fatal(err)
	}
	// The saved token is wrong: the server says so and doctor points to login.
	out, err := runDoctorJSON(t, r, "acme/open")
	if err == nil || out.OK {
		t.Fatal("a rejected token should fail")
	}
	if auth := doctorCheckNamed(t, out, "auth"); auth.Status != "fail" || !strings.Contains(auth.Summary, "did not accept the saved agent API key") || !strings.Contains(auth.Hint, "gs auth login") {
		t.Fatalf("auth = %+v", auth)
	}
	if strings.Contains(strings.Join([]string{out.Checks[3].Summary, out.Checks[3].Hint}, " "), fakeToken) {
		t.Fatal("doctor printed the token")
	}
	if version := doctorCheckNamed(t, out, "version"); version.Status != "warn" || !strings.Contains(version.Summary, "v9.9.9 is available") || version.Hint != "Run gs upgrade." {
		t.Fatalf("version = %+v", version)
	}

	if err := r.writeUserConfig(UserConfig{ServerAddr: addr, Token: fakeToken, SubjectID: "user_alice"}); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		slice, status, summary, hint string
	}{
		{"alice/home", "ok", "slice alice/home is public; you are owner of alice", ""},
		{"home", "ok", "slice alice/home is public; you are owner of alice", ""},
		{"readers/docs", "warn", "you are reader of readers, so you can read it but not change it", "writer role"},
		{"acme/open", "ok", "you can read it, but changesets from it need write access in acme", ""},
		{"acme/secret", "fail", "slice acme/secret is private and user_alice is not a member of acme", "/claims"},
		{"acme/missing", "fail", "slice acme/missing does not exist", "gs slice list acme"},
	} {
		out, err := runDoctorJSON(t, r, tt.slice)
		check := doctorCheckNamed(t, out, "slice")
		if check.Status != tt.status || !strings.Contains(check.Summary, tt.summary) || !strings.Contains(check.Hint, tt.hint) {
			t.Fatalf("%s: slice check = %+v", tt.slice, check)
		}
		if (tt.status == "fail") != (err != nil) {
			t.Fatalf("%s: err = %v", tt.slice, err)
		}
		if auth := doctorCheckNamed(t, out, "auth"); auth.Status != "ok" || !strings.Contains(auth.Summary, "signed in as user_alice (accounts: alice, readers)") {
			t.Fatalf("auth = %+v", auth)
		}
		if server := doctorCheckNamed(t, out, "server"); server.Status != "ok" {
			t.Fatalf("server = %+v", server)
		}
	}
}

func TestDoctorSignedOutAndUnreachable(t *testing.T) {
	base, _ := latestReleaseServer(t, "v0.1.0")
	t.Setenv("GS_DOWNLOAD_BASE", base)
	withVersion(t, "v0.1.0")
	addr := startFakeGitslice(t, &fakeGitslice{})
	t.Setenv("GS_SERVER_ADDR", addr)

	// Signed out: a warning, until a private slice needs the sign-in.
	r := Runner{Home: t.TempDir(), Dir: t.TempDir()}
	out, err := runDoctorJSON(t, r)
	if err != nil || !out.OK {
		t.Fatalf("signed out with no slice should pass: %v %+v", err, out)
	}
	if auth := doctorCheckNamed(t, out, "auth"); auth.Status != "warn" || !strings.Contains(auth.Hint, "gs auth register-agent") {
		t.Fatalf("auth = %+v", auth)
	}
	if version := doctorCheckNamed(t, out, "version"); version.Status != "ok" || version.Summary != "gs v0.1.0 is the latest release" {
		t.Fatalf("version = %+v", version)
	}
	out, err = runDoctorJSON(t, r, "acme/secret")
	if err == nil {
		t.Fatal("a private slice while signed out should fail")
	}
	if slice := doctorCheckNamed(t, out, "slice"); slice.Status != "fail" || slice.Summary != "slice acme/secret is private and you are not signed in" {
		t.Fatalf("slice = %+v", slice)
	}

	// A server that does not answer.
	if err := r.writeUserConfig(UserConfig{ServerAddr: "127.0.0.1:1", Token: fakeToken}); err != nil {
		t.Fatal(err)
	}
	out, _ = runDoctorJSON(t, r, "acme/open")
	if server := doctorCheckNamed(t, out, "server"); server.Status != "fail" || !strings.Contains(server.Summary, "127.0.0.1:1") {
		t.Fatalf("server = %+v", server)
	}
	if auth := doctorCheckNamed(t, out, "auth"); auth.Status != "skip" {
		t.Fatalf("auth = %+v", auth)
	}
	if config := doctorCheckNamed(t, out, "config"); config.Status != "warn" || !strings.Contains(config.Summary, "GS_SERVER_ADDR is "+addr) {
		t.Fatalf("config = %+v", config)
	}
}

func TestDoctorTextOutput(t *testing.T) {
	base, _ := latestReleaseServer(t, "v0.1.0")
	t.Setenv("GS_DOWNLOAD_BASE", base)
	t.Setenv("GS_SERVER_ADDR", "")
	withVersion(t, "v0.1.0")
	addr := startFakeGitslice(t, &fakeGitslice{})
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Dir: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
	if err := r.writeUserConfig(UserConfig{ServerAddr: addr, Token: fakeToken}); err != nil {
		t.Fatal(err)
	}
	err := r.Run(context.Background(), []string{"doctor", "acme/secret"})
	if err == nil || err.Error() != "gs doctor found 1 problem" {
		t.Fatalf("err = %v", err)
	}
	for _, want := range []string{
		"✓ gs v0.1.0 is the latest release\n",
		"✓ server " + addr + " answered in ",
		"- not in a workspace\n",
		"✗ slice acme/secret is private and user_alice is not a member of acme\n    If acme is an agent you own, claim co-ownership at https://gitslice.io/claims",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("missing %q in:\n%s", want, stdout.String())
		}
	}
}

func TestAuthErrorsSayWhatIsWrong(t *testing.T) {
	r := Runner{Home: t.TempDir()}
	err := r.enhanceCommandError(status.Error(codes.Unauthenticated, "unauthenticated"))
	var cmdErr commandError
	if !errors.As(err, &cmdErr) || cmdErr.Code != "unauthenticated" || !strings.HasPrefix(cmdErr.Message, "not signed in") ||
		!strings.Contains(cmdErr.Hint, "gs auth login") || !strings.Contains(cmdErr.Hint, "https://gitslice.io/claims") {
		t.Fatalf("signed out: %#v", err)
	}
	if exitCodeForError(err) != 4 {
		t.Fatalf("exit code = %d", exitCodeForError(err))
	}

	if err := r.writeUserConfig(UserConfig{ServerAddr: "api.example.com:443", Token: "t", SubjectID: "user_bob"}); err != nil {
		t.Fatal(err)
	}
	err = r.enhanceCommandError(status.Error(codes.Unauthenticated, "token expired"))
	if !errors.As(err, &cmdErr) || cmdErr.Message != "authentication failed: api.example.com:443 did not accept the saved token (token expired)" {
		t.Fatalf("rejected token: %#v", err)
	}

	err = r.enhanceCommandError(status.Error(codes.PermissionDenied, "unauthorized"))
	if !errors.As(err, &cmdErr) || cmdErr.Code != "permission_denied" || cmdErr.Message != "permission denied for user_bob" ||
		!strings.Contains(cmdErr.Hint, "claim co-ownership at https://gitslice.io/claims") || !strings.Contains(cmdErr.Hint, "gs doctor") {
		t.Fatalf("permission denied: %#v", err)
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatal("the gRPC code should still be reachable")
	}
}

func TestCreateSubmitWaitsForRequiredChecks(t *testing.T) {
	old := submitRetryInterval
	submitRetryInterval = 10 * time.Millisecond
	t.Cleanup(func() { submitRetryInterval = old })
	cfg := UserConfig{Token: fakeToken}
	state := WorkspaceState{ActiveStackID: "stk_1", CurrentChangesetID: "cs_00112233445566778899aabbccddeeff"}

	f := &fakeGitslice{pendingSubmits: 3}
	conn, err := dial(context.Background(), startFakeGitslice(t, f))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var stderr bytes.Buffer
	r := Runner{Stdout: &bytes.Buffer{}, Stderr: &stderr}
	res, err := r.submitWhenChecksReport(context.Background(), commandOptions{Format: "text"}, conn, cfg, state, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "submitted" || len(res.Results) != 1 || res.Results[0].Status != "submitted" || f.submits != 4 {
		t.Fatalf("res = %+v after %d submits", res, f.submits)
	}
	if strings.Count(stderr.String(), `waiting for required check "ci" to pass...`) != 1 {
		t.Fatalf("stderr = %q", stderr.String())
	}

	// A failing check ends it at once; a check that never reports, at the
	// timeout.
	for _, tt := range []struct {
		pending int
		reason  string
	}{
		{-1, "is failing"},
		{1000, "has no result"},
	} {
		f := &fakeGitslice{pendingSubmits: tt.pending}
		conn, err := dial(context.Background(), startFakeGitslice(t, f))
		if err != nil {
			t.Fatal(err)
		}
		res, err := r.submitWhenChecksReport(context.Background(), commandOptions{Format: "json"}, conn, cfg, state, 100*time.Millisecond)
		conn.Close()
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != "partial" || !strings.Contains(res.Results[0].BlockedReason, tt.reason) {
			t.Fatalf("pending %d: res = %+v", tt.pending, res)
		}
		if tt.pending < 0 && f.submits != 1 {
			t.Fatalf("a failing check was retried %d times", f.submits)
		}
	}
}
