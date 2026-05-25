package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gitslice-io/gitslice/internal/clientcache"
	"github.com/gitslice-io/gitslice/internal/objectid"
	"github.com/gitslice-io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestSchemaCommandEmitsMachineReadableContract(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := Runner{Stdout: &stdout, Stderr: &stderr}
	if err := r.Run(context.Background(), []string{"schema"}); err != nil {
		t.Fatalf("schema failed: %v\nstderr:\n%s", err, stderr.String())
	}

	var got struct {
		SchemaVersion string `json:"schema_version"`
		Commands      []struct {
			Use string `json:"use"`
		} `json:"commands"`
		HelpTopics []struct {
			Name string `json:"name"`
		} `json:"help_topics"`
		ErrorOutput map[string]any `json:"error_output"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("schema output is not JSON: %v\n%s", err, stdout.String())
	}
	if got.SchemaVersion != "v1" {
		t.Fatalf("unexpected schema version %q", got.SchemaVersion)
	}
	if len(got.Commands) == 0 {
		t.Fatal("schema did not include commands")
	}
	uses := map[string]bool{}
	for _, command := range got.Commands {
		uses[command.Use] = true
	}
	for _, want := range []string{"gs fs ls [absolute-path]", "gs fs cat <absolute-path>", "gs fs mkdir <absolute-path>", "gs help <topic>"} {
		if !uses[want] {
			t.Fatalf("schema missing %q", want)
		}
	}
	topics := map[string]bool{}
	for _, topic := range got.HelpTopics {
		topics[topic.Name] = true
	}
	for _, want := range []string{"environment", "formatting", "exit-codes", "paths", "slices"} {
		if !topics[want] {
			t.Fatalf("schema missing help topic %q", want)
		}
	}
	if got.ErrorOutput["stream"] != "stderr" {
		t.Fatalf("expected stderr error stream, got %#v", got.ErrorOutput["stream"])
	}
}

func TestHelpTopics(t *testing.T) {
	for _, tc := range []struct {
		name string
		want string
	}{
		{"environment", "GITSLICE_GRPC_ADDR"},
		{"formatting", "--format json"},
		{"exit-codes", "4"},
		{"paths", "account-rooted"},
		{"slices", "nic/home"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			r := Runner{Home: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
			if err := r.Run(context.Background(), []string{"help", tc.name}); err != nil {
				t.Fatalf("help %s failed: %v\nstderr:\n%s", tc.name, err, stderr.String())
			}
			if !strings.Contains(stdout.String(), tc.want) {
				t.Fatalf("help %s missing %q:\n%s", tc.name, tc.want, stdout.String())
			}
		})
	}
}

func TestHelpCommandStillShowsCommandHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
	if err := r.Run(context.Background(), []string{"help", "auth", "status"}); err != nil {
		t.Fatalf("help auth status failed: %v\nstderr:\n%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Show current authentication status") {
		t.Fatalf("expected command help, got:\n%s", stdout.String())
	}
}

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
	if !strings.Contains(err.Error(), "gs auth signup --username alice") {
		t.Fatalf("expected username-specific signup hint, got:\n%v", err)
	}
}

func TestContextReportsAuthAndWorkspace(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	nested := filepath.Join(workspace, "src", "pkg")
	if err := os.MkdirAll(filepath.Join(workspace, ".gs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	serverAddr := startFakeAuthStatusServer(t, "secret-token", "user_alice")
	var stdout, stderr bytes.Buffer
	r := Runner{Home: home, Dir: nested, Stdout: &stdout, Stderr: &stderr}
	if err := r.writeUserConfig(UserConfig{
		ServerAddr: serverAddr,
		Token:      "secret-token",
		SubjectID:  "stale_local_subject",
	}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(workspace, ".gs", "slice.json"), WorkspaceConfig{
		Account:        "acme",
		Slice:          "payment",
		SliceID:        "slice_acme_payment",
		DefinitionHash: "sha256:def",
		IncludedPaths:  []string{"/acme/payment"},
		BaseCommitID:   "cmt_config",
	}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(workspace, ".gs", "state.json"), WorkspaceState{
		CurrentChangesetID: "cs_123",
		CurrentPatchsetID:  "ps_123",
		BaseCommitID:       "cmt_state",
	}, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := r.Run(context.Background(), []string{"context", "--json"}); err != nil {
		t.Fatalf("context failed: %v\nstderr:\n%s", err, stderr.String())
	}
	var got contextOutput
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("context output is not JSON: %v\n%s", err, stdout.String())
	}
	if !got.SignedIn || got.SubjectID != "user_alice" || got.ServerAddr != serverAddr {
		t.Fatalf("unexpected auth context: %#v", got)
	}
	if got.Workspace == nil {
		t.Fatalf("expected workspace context: %#v", got)
	}
	if got.Workspace.Root != workspace || got.Workspace.Ref != "acme/payment" || got.Workspace.BaseCommitID != "cmt_state" {
		t.Fatalf("unexpected workspace context: %#v", got.Workspace)
	}
	if got.ActiveSlice != "acme/payment" || got.ActiveSliceSource != "workspace" {
		t.Fatalf("unexpected active slice: %#v", got)
	}
}

func TestAuthSignupStoresCallbackToken(t *testing.T) {
	home := t.TempDir()
	stdoutReader, stdoutWriter := io.Pipe()
	var stderr bytes.Buffer
	r := Runner{Home: home, Stdout: stdoutWriter, Stderr: &stderr}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		err := r.Run(ctx, []string{
			"auth", "signup",
			"--username", "New_User",
			"--server", "127.0.0.1:50051",
			"--web-url", "http://signup.example.invalid",
			"--no-browser",
		})
		_ = stdoutWriter.Close()
		errCh <- err
	}()

	approvalURL := readSignupApprovalURL(t, stdoutReader)
	go io.Copy(io.Discard, stdoutReader)
	parsed, err := url.Parse(approvalURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	if query.Get("username") != "New_User" {
		t.Fatalf("approval username = %q, want New_User", query.Get("username"))
	}
	callbackURL := query.Get("callback_url")
	state := query.Get("state")
	if callbackURL == "" || state == "" {
		t.Fatalf("approval URL missing callback/state: %s", approvalURL)
	}
	callback, err := url.Parse(callbackURL)
	if err != nil {
		t.Fatal(err)
	}
	callbackQuery := callback.Query()
	callbackQuery.Set("token", "callback-token")
	callbackQuery.Set("subject_id", "user_new_user")
	callbackQuery.Set("state", state)
	callback.RawQuery = callbackQuery.Encode()
	resp, err := http.Get(callback.String())
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("callback status = %d, want 200", resp.StatusCode)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("signup failed: %v\nstderr:\n%s", err, stderr.String())
	}

	var cfg UserConfig
	if err := readJSONFile(filepath.Join(home, ".gitslice", "config.json"), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.ServerAddr != "127.0.0.1:50051" || cfg.Token != "callback-token" || cfg.SubjectID != "user_new_user" {
		t.Fatalf("unexpected stored signup config: %#v", cfg)
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

func TestAttachBlobIDsReusesServerBlobStatus(t *testing.T) {
	cache, err := clientcache.New(filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("package payment\n")
	cached, err := cache.PutBytes(content)
	if err != nil {
		t.Fatal(err)
	}
	client := &fakeBlobClient{
		status: map[string]*corev1.BlobRecord{
			cached.ContentHash: {Id: "blob_existing", ContentHash: cached.ContentHash, State: "available"},
		},
	}
	edits := []*corev1.FileEdit{
		{Op: "upsert", Path: "/acme/payment/a.go", ContentHash: cached.ContentHash},
		{Op: "upsert", Path: "/acme/payment/b.go", ContentHash: cached.ContentHash},
	}

	if err := attachBlobIDs(context.Background(), client, cache, edits); err != nil {
		t.Fatal(err)
	}
	if client.uploads != 0 {
		t.Fatalf("expected no uploads, got %d", client.uploads)
	}
	for _, edit := range edits {
		if edit.BlobId != "blob_existing" {
			t.Fatalf("expected existing blob id, got %q", edit.BlobId)
		}
	}
}

func TestAttachBlobIDsUploadsEachMissingHashOnceFromCache(t *testing.T) {
	cache, err := clientcache.New(filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("package payment\nconst Created = true\n")
	cached, err := cache.PutBytes(content)
	if err != nil {
		t.Fatal(err)
	}
	client := &fakeBlobClient{status: map[string]*corev1.BlobRecord{}}
	edits := []*corev1.FileEdit{
		{Op: "upsert", Path: "/acme/payment/a.go", ContentHash: cached.ContentHash},
		{Op: "upsert", Path: "/acme/payment/b.go", ContentHash: cached.ContentHash},
	}

	if err := attachBlobIDs(context.Background(), client, cache, edits); err != nil {
		t.Fatal(err)
	}
	if client.uploads != 1 {
		t.Fatalf("expected one upload, got %d", client.uploads)
	}
	wantBlobID := objectid.BlobID(content)
	for _, edit := range edits {
		if edit.BlobId != wantBlobID {
			t.Fatalf("expected uploaded blob id %q, got %q", wantBlobID, edit.BlobId)
		}
	}
}

type fakeBlobClient struct {
	corev1.BlobServiceClient
	status  map[string]*corev1.BlobRecord
	uploads int
}

func (f *fakeBlobClient) GetBlobStatus(ctx context.Context, req *corev1.GetBlobStatusRequest, opts ...grpc.CallOption) (*corev1.GetBlobStatusResponse, error) {
	out := make([]*corev1.BlobRecord, 0, len(req.ContentHashes))
	for _, hash := range req.ContentHashes {
		if record := f.status[hash]; record != nil {
			out = append(out, record)
			continue
		}
		out = append(out, &corev1.BlobRecord{ContentHash: hash, State: "missing"})
	}
	return &corev1.GetBlobStatusResponse{Blobs: out}, nil
}

func (f *fakeBlobClient) UploadBlob(ctx context.Context, req *corev1.UploadBlobRequest, opts ...grpc.CallOption) (*corev1.UploadBlobResponse, error) {
	f.uploads++
	return &corev1.UploadBlobResponse{
		BlobId:      objectid.BlobID(req.Data),
		ContentHash: objectid.RawContentHash(req.Data),
		Size:        int64(len(req.Data)),
	}, nil
}

func readSignupApprovalURL(t *testing.T, r io.Reader) string {
	t.Helper()
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "http://") || strings.HasPrefix(line, "https://") {
			return line
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	t.Fatal("approval URL was not printed")
	return ""
}

type fakeAuthStatusServer struct {
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
