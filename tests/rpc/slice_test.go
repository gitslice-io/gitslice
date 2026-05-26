package rpc_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/gitslice-io/gitslice/internal/postgres"
	"github.com/gitslice-io/gitslice/proto/core/v1"
	"github.com/gitslice-io/gitslice/server"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthv1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	grpcstatus "google.golang.org/grpc/status"
)

func TestSliceServiceCustomSliceValidation(t *testing.T) {
	ts := startRPCServer(t)
	token := loginViaGRPC(t, ts.addr, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	clients := newTestCoreClients(conn)
	slices := corev1.NewSliceServiceClient(conn)

	submitDirectFile(t, ctx, clients, "/acme/payment/rpc-docs/seed.txt", "rpc docs\n", "rpc docs seed")
	submitDirectFile(t, ctx, clients, "/acme/payment/rpc-docs/archive/seed.txt", "rpc archive\n", "rpc archive seed")
	submitDirectFile(t, ctx, clients, "/acme/payment/rpc-multi-a/seed.txt", "rpc multi a\n", "rpc multi a seed")
	submitDirectFile(t, ctx, clients, "/acme/payment/rpc-multi-b/seed.txt", "rpc multi b\n", "rpc multi b seed")

	_, err := slices.CreateSlice(ctx, &corev1.CreateSliceRequest{
		Ref:           &corev1.SliceRef{Account: "acme", Slice: "rpc-missing"},
		IncludedPaths: []string{"/acme/payment/rpc-missing"},
	})
	if grpcstatus.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "included path does not exist: /acme/payment/rpc-missing") {
		t.Fatalf("missing include CreateSlice err = %v, want FailedPrecondition existence error", err)
	}

	_, err = slices.CreateSlice(ctx, &corev1.CreateSliceRequest{
		Ref:           &corev1.SliceRef{Account: "acme", Slice: "rpc-comma"},
		IncludedPaths: []string{"/acme/payment/rpc-multi-a,/acme/payment/rpc-multi-b"},
	})
	if grpcstatus.Code(err) != codes.InvalidArgument || !strings.Contains(err.Error(), "must not contain commas") {
		t.Fatalf("comma include CreateSlice err = %v, want InvalidArgument comma error", err)
	}

	created, err := slices.CreateSlice(ctx, &corev1.CreateSliceRequest{
		Ref:           &corev1.SliceRef{Account: "acme", Slice: "rpc-docs"},
		IncludedPaths: []string{"/acme/payment/rpc-docs"},
		Visibility:    "account",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Ref.Account != "acme" || created.Ref.Slice != "rpc-docs" || created.Definition.Visibility != "account" {
		t.Fatalf("unexpected created slice: %#v", created)
	}
	if got := created.Definition.IncludedPaths; len(got) != 1 || got[0] != "/acme/payment/rpc-docs" {
		t.Fatalf("created included paths = %#v, want [/acme/payment/rpc-docs]", got)
	}

	multi, err := slices.CreateSlice(ctx, &corev1.CreateSliceRequest{
		Ref:           &corev1.SliceRef{Account: "acme", Slice: "rpc-multi"},
		IncludedPaths: []string{"/acme/payment/rpc-multi-a", "/acme/payment/rpc-multi-b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := multi.Definition.IncludedPaths; len(got) != 2 || got[0] != "/acme/payment/rpc-multi-a" || got[1] != "/acme/payment/rpc-multi-b" {
		t.Fatalf("multi included paths = %#v, want both RPC include paths", got)
	}

	_, err = slices.UpdateSliceDefinition(ctx, &corev1.UpdateSliceDefinitionRequest{
		SliceId:                created.Id,
		ExpectedDefinitionHash: created.DefinitionHash,
		Definition: &corev1.SliceDefinition{
			IncludedPaths: []string{"/acme/payment/rpc-missing-update"},
			Visibility:    "account",
		},
	})
	if grpcstatus.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "included path does not exist: /acme/payment/rpc-missing-update") {
		t.Fatalf("missing include UpdateSliceDefinition err = %v, want FailedPrecondition existence error", err)
	}

	updated, err := slices.UpdateSliceDefinition(ctx, &corev1.UpdateSliceDefinitionRequest{
		SliceId:                created.Id,
		ExpectedDefinitionHash: created.DefinitionHash,
		Definition: &corev1.SliceDefinition{
			IncludedPaths: []string{"/acme/payment/rpc-docs", "/acme/payment/rpc-docs/archive"},
			Visibility:    "public",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Visibility != "public" || updated.Version != created.Definition.Version+1 {
		t.Fatalf("unexpected updated definition: %#v", updated)
	}
	if got := updated.IncludedPaths; len(got) != 2 || got[0] != "/acme/payment/rpc-docs" || got[1] != "/acme/payment/rpc-docs/archive" {
		t.Fatalf("updated included paths = %#v, want docs and archive", got)
	}

	resolved, err := slices.ResolveSlice(ctx, &corev1.ResolveSliceRequest{Ref: &corev1.SliceRef{Account: "acme", Slice: "rpc-docs"}})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Definition.Visibility != "public" || !containsString(resolved.Definition.IncludedPaths, "/acme/payment/rpc-docs/archive") {
		t.Fatalf("resolved slice did not persist updated definition: %#v", resolved)
	}
}

func TestChangesetServiceListAndDiff(t *testing.T) {
	ts := startRPCServer(t)
	token := loginViaGRPC(t, ts.addr, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	clients := newTestCoreClients(conn)

	ref, err := clients.repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	first, err := clients.blob.UploadBlob(ctx, &corev1.UploadBlobRequest{Data: []byte("package payment\nconst Version = 1\n")})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := clients.changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
		TargetRef:      postgres.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "rpc diff workflow",
	})
	if err != nil {
		t.Fatal(err)
	}
	firstPatchset, err := clients.changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        "/acme/payment/rpc_diff.go",
			BlobId:      first.BlobId,
			ContentHash: first.ContentHash,
			Mode:        0o644,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := clients.blob.UploadBlob(ctx, &corev1.UploadBlobRequest{Data: []byte("package payment\nconst Version = 2\n")})
	if err != nil {
		t.Fatal(err)
	}
	secondPatchset, err := clients.changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:               cs.Id,
		ExpectedCurrentPatchsetId: firstPatchset.Id,
		BaseCommitId:              ref.CommitId,
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        "/acme/payment/rpc_diff.go",
			BlobId:      second.BlobId,
			ContentHash: second.ContentHash,
			Mode:        0o644,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	listed, err := clients.changeset.ListChangesets(ctx, &corev1.ListChangesetsRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
		Status:         "draft",
		Limit:          10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !changesetListContains(listed.Changesets, cs.Id) {
		t.Fatalf("ListChangesets did not include %s: %#v", cs.Id, listed.Changesets)
	}

	firstDiff, err := clients.changeset.DiffChangeset(ctx, &corev1.DiffChangesetRequest{
		ChangesetId: cs.Id,
		Patchset:    "1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if firstDiff.ToPatchsetId != firstPatchset.Id || !containsString(firstDiff.ChangedPaths, "/acme/payment/rpc_diff.go") || !strings.Contains(firstDiff.Diff, "+const Version = 1") {
		t.Fatalf("unexpected first patchset diff: %#v", firstDiff)
	}

	between, err := clients.changeset.DiffChangeset(ctx, &corev1.DiffChangesetRequest{
		ChangesetId:  cs.Id,
		FromPatchset: "1",
		ToPatchset:   "2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if between.FromPatchsetId != firstPatchset.Id || between.ToPatchsetId != secondPatchset.Id {
		t.Fatalf("unexpected patchset ids in diff: %#v", between)
	}
	if !strings.Contains(between.Diff, "-const Version = 1") || !strings.Contains(between.Diff, "+const Version = 2") {
		t.Fatalf("patchset-to-patchset diff missing expected content:\n%s", between.Diff)
	}
}

func TestRPCAuthenticationBoundary(t *testing.T) {
	ts := startRPCServer(t)
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()

	fakeAccounts := corev1.NewFakeAccountServiceClient(conn)
	login, err := fakeAccounts.Login(context.Background(), &corev1.LoginRequest{DevUser: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if login.Token == "" || login.SubjectId != "user_alice" {
		t.Fatalf("unexpected public login response: %#v", login)
	}

	signup, err := fakeAccounts.ApproveSignup(context.Background(), &corev1.ApproveSignupRequest{
		Username:    "public-boundary",
		CallbackUrl: "http://127.0.0.1/callback",
		State:       "state",
	})
	if err != nil {
		t.Fatal(err)
	}
	if signup.Token == "" || signup.SubjectId != "user_public_boundary" {
		t.Fatalf("unexpected public signup response: %#v", signup)
	}

	health, err := healthv1.NewHealthClient(conn).Check(context.Background(), &healthv1.HealthCheckRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if health.Status != healthv1.HealthCheckResponse_SERVING {
		t.Fatalf("unexpected health status: %#v", health)
	}

	_, err = corev1.NewAuthServiceClient(conn).GetAuthStatus(context.Background(), &corev1.GetAuthStatusRequest{})
	assertGRPCCode(t, err, codes.Unauthenticated)
	_, err = corev1.NewSliceServiceClient(conn).ListSlices(context.Background(), &corev1.ListSlicesRequest{Account: "acme"})
	assertGRPCCode(t, err, codes.Unauthenticated)
}

func TestRPCAccountMembershipProtectsChangesetWritesAndSliceScopes(t *testing.T) {
	ts := startRPCServer(t)
	aliceToken := loginViaGRPC(t, ts.addr, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()

	aliceCtx := grpcAuthContext(aliceToken)
	clients := newTestCoreClients(conn)
	changesetID, patchsetID := createDirectPatchset(t, aliceCtx, clients, "/acme/payment/authz_member.go", "package payment\nconst Authz = true\n", "membership authz")

	signup, err := corev1.NewFakeAccountServiceClient(conn).ApproveSignup(context.Background(), &corev1.ApproveSignupRequest{
		Username:    "outsider-authz",
		CallbackUrl: "http://127.0.0.1/callback",
		State:       "state",
	})
	if err != nil {
		t.Fatal(err)
	}
	outsiderCtx := grpcAuthContext(signup.Token)

	slices := corev1.NewSliceServiceClient(conn)
	_, err = slices.ResolveSlice(outsiderCtx, &corev1.ResolveSliceRequest{
		Ref: &corev1.SliceRef{Account: "acme", Slice: "payment"},
	})
	assertGRPCCode(t, err, codes.PermissionDenied)
	_, err = slices.ListSlices(outsiderCtx, &corev1.ListSlicesRequest{Account: "acme"})
	assertGRPCCode(t, err, codes.PermissionDenied)

	workspace := corev1.NewWorkspaceServiceClient(conn)
	_, err = workspace.GetWorkspaceState(outsiderCtx, &corev1.GetWorkspaceStateRequest{
		Workspace: &corev1.WorkspaceRef{Id: "acme/payment"},
	})
	assertGRPCCode(t, err, codes.PermissionDenied)
	_, err = workspace.ValidateWorkspaceDiff(outsiderCtx, &corev1.ValidateWorkspaceDiffRequest{
		Workspace: &corev1.WorkspaceRef{Id: "acme/payment"},
		FileEdits: []*corev1.FileEdit{{
			Op:   "upsert",
			Path: "/acme/payment/unauthorized.go",
		}},
	})
	assertGRPCCode(t, err, codes.PermissionDenied)

	_, err = clients.repository.ListDirectory(outsiderCtx, &corev1.ListDirectoryRequest{
		Path:  "/acme/payment",
		Slice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
	})
	assertGRPCCode(t, err, codes.PermissionDenied)
	_, err = clients.repository.ListCommits(outsiderCtx, &corev1.ListCommitsRequest{
		RefName: postgres.DefaultTargetRef,
		Slice:   &corev1.SliceRef{Account: "acme", Slice: "payment"},
		Limit:   10,
	})
	assertGRPCCode(t, err, codes.PermissionDenied)
	_, err = clients.repository.ImportGitRepository(outsiderCtx, &corev1.ImportGitRepositoryRequest{
		Source:         "/unused/source",
		MountPath:      "/acme/payment/imported/unauthorized",
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
		Mode:           "shallow",
	})
	assertGRPCCode(t, err, codes.PermissionDenied)

	_, err = clients.changeset.CreateChangeset(outsiderCtx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
		Title:          "unauthorized create",
	})
	assertGRPCCode(t, err, codes.PermissionDenied)
	_, err = clients.changeset.GetChangeset(outsiderCtx, &corev1.GetChangesetRequest{ChangesetId: changesetID})
	assertGRPCCode(t, err, codes.PermissionDenied)
	_, err = clients.changeset.ListChangesets(outsiderCtx, &corev1.ListChangesetsRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
	})
	assertGRPCCode(t, err, codes.PermissionDenied)
	_, err = clients.changeset.DiffChangeset(outsiderCtx, &corev1.DiffChangesetRequest{ChangesetId: changesetID})
	assertGRPCCode(t, err, codes.PermissionDenied)
	_, err = clients.changeset.UpdateChangeset(outsiderCtx, &corev1.UpdateChangesetRequest{
		ChangesetId: changesetID,
		FileEdits: []*corev1.FileEdit{{
			Op:   "delete",
			Path: "/acme/payment/authz_member.go",
		}},
	})
	assertGRPCCode(t, err, codes.PermissionDenied)
	_, err = clients.changeset.SubmitChangeset(outsiderCtx, &corev1.SubmitChangesetRequest{
		ChangesetId:               changesetID,
		ExpectedCurrentPatchsetId: patchsetID,
	})
	assertGRPCCode(t, err, codes.PermissionDenied)
	_, err = clients.changeset.AbandonChangeset(outsiderCtx, &corev1.AbandonChangesetRequest{ChangesetId: changesetID})
	assertGRPCCode(t, err, codes.PermissionDenied)

	if _, err := clients.changeset.SubmitChangeset(aliceCtx, &corev1.SubmitChangesetRequest{
		ChangesetId:               changesetID,
		ExpectedCurrentPatchsetId: patchsetID,
	}); err != nil {
		t.Fatal(err)
	}
	submitted := waitForSubmittedChangeset(t, aliceCtx, clients.changeset, changesetID)
	if submitted.CommitId == "" {
		t.Fatalf("authorized submit did not publish commit: %#v", submitted)
	}
}

type testRPCServer struct {
	addr        string
	ctx         context.Context
	cancel      context.CancelFunc
	errCh       chan error
	databaseURL string
	schema      string
	objectRoot  string
}

func startRPCServer(t *testing.T) *testRPCServer {
	t.Helper()
	databaseURL := os.Getenv("GITSLICE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set GITSLICE_TEST_DATABASE_URL to run real Postgres RPC e2e tests")
	}
	schema := "gitslice_rpc_" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "_")) + "_" + time.Now().Format("150405000000")
	createSchema(t, databaseURL, schema)
	ts := &testRPCServer{
		addr:        freeAddr(t),
		databaseURL: databaseURL,
		schema:      schema,
		objectRoot:  t.TempDir(),
	}
	ts.start(t)
	t.Cleanup(func() {
		ts.stop(t)
		dropSchema(t, databaseURL, schema)
	})
	return ts
}

func (ts *testRPCServer) start(t *testing.T) {
	t.Helper()
	ts.ctx, ts.cancel = context.WithCancel(context.Background())
	ts.errCh = make(chan error, 1)
	go func() {
		ts.errCh <- server.Run(ts.ctx, server.Config{
			GRPCAddr:        ts.addr,
			DatabaseURL:     databaseURLWithSearchPath(t, ts.databaseURL, ts.schema),
			ObjectStoreRoot: ts.objectRoot,
			RunMigrations:   true,
		})
	}()
	waitForHealth(t, ts.addr)
}

func (ts *testRPCServer) stop(t *testing.T) {
	t.Helper()
	if ts.cancel == nil {
		return
	}
	ts.cancel()
	err := <-ts.errCh
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("server exited with error: %v", err)
	}
	ts.cancel = nil
}

type testCoreClients struct {
	repository corev1.RepositoryServiceClient
	blob       corev1.BlobServiceClient
	changeset  corev1.ChangesetServiceClient
}

func newTestCoreClients(conn *grpc.ClientConn) testCoreClients {
	return testCoreClients{
		repository: corev1.NewRepositoryServiceClient(conn),
		blob:       corev1.NewBlobServiceClient(conn),
		changeset:  corev1.NewChangesetServiceClient(conn),
	}
}

func dialTestGRPC(t *testing.T, addr string) *grpc.ClientConn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := grpc.DialContext(ctx, addr, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func loginViaGRPC(t *testing.T, addr, devUser string) string {
	t.Helper()
	conn := dialTestGRPC(t, addr)
	defer conn.Close()
	login, err := corev1.NewFakeAccountServiceClient(conn).Login(context.Background(), &corev1.LoginRequest{DevUser: devUser})
	if err != nil {
		t.Fatal(err)
	}
	if login.Token == "" {
		t.Fatalf("empty token from login: %#v", login)
	}
	return login.Token
}

func grpcAuthContext(token string) context.Context {
	return metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+token)
}

func assertGRPCCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if grpcstatus.Code(err) != want {
		t.Fatalf("grpc code = %v, want %v; err=%v", grpcstatus.Code(err), want, err)
	}
}

func createDirectPatchset(t *testing.T, ctx context.Context, clients testCoreClients, path, content, title string) (string, string) {
	t.Helper()
	ref, err := clients.repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	upload, err := clients.blob.UploadBlob(ctx, &corev1.UploadBlobRequest{Data: []byte(content)})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := clients.changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
		TargetRef:      postgres.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          title,
	})
	if err != nil {
		t.Fatal(err)
	}
	patchset, err := clients.changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		FileEdits: []*corev1.FileEdit{{
			Op:          "add",
			Path:        path,
			BlobId:      upload.BlobId,
			ContentHash: upload.ContentHash,
			Mode:        0o644,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return cs.Id, patchset.Id
}

func submitDirectFile(t *testing.T, ctx context.Context, clients testCoreClients, path, content, title string) *corev1.Changeset {
	t.Helper()
	changesetID, patchsetID := createDirectPatchset(t, ctx, clients, path, content, title)
	submitted, err := clients.changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{
		ChangesetId:               changesetID,
		ExpectedCurrentPatchsetId: patchsetID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if submitted.Status != "pending_publish" && submitted.Status != "submitted" {
		t.Fatalf("unexpected submit response: %#v", submitted)
	}
	return waitForSubmittedChangeset(t, ctx, clients.changeset, changesetID)
}

func waitForSubmittedChangeset(t *testing.T, ctx context.Context, client corev1.ChangesetServiceClient, changesetID string) *corev1.Changeset {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var last *corev1.Changeset
	for time.Now().Before(deadline) {
		cs, err := client.GetChangeset(ctx, &corev1.GetChangesetRequest{ChangesetId: changesetID})
		if err != nil {
			t.Fatal(err)
		}
		last = cs
		if cs.Status == "submitted" && cs.CommitId != "" {
			return cs
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("changeset %s did not reach submitted status, last=%#v", changesetID, last)
	return nil
}

func createSchema(t *testing.T, databaseURL, schema string) {
	t.Helper()
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`create schema ` + pqIdentifier(schema)); err != nil {
		t.Fatal(err)
	}
}

func dropSchema(t *testing.T, databaseURL, schema string) {
	t.Helper()
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Logf("open db for cleanup: %v", err)
		return
	}
	defer db.Close()
	if _, err := db.Exec(`drop schema if exists ` + pqIdentifier(schema) + ` cascade`); err != nil {
		t.Logf("drop schema %s: %v", schema, err)
	}
}

func pqIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func databaseURLWithSearchPath(t *testing.T, databaseURL, schema string) string {
	t.Helper()
	parsed, err := url.Parse(databaseURL)
	if err != nil || parsed.Scheme == "" {
		t.Fatalf("GITSLICE_TEST_DATABASE_URL must be a URL connection string for RPC e2e tests")
	}
	q := parsed.Query()
	q.Set("search_path", schema)
	parsed.RawQuery = q.Encode()
	return parsed.String()
}

func freeAddr(t *testing.T) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()
	return lis.Addr().String()
}

func waitForHealth(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		conn, err := grpc.DialContext(ctx, addr, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
		if err == nil {
			client := healthv1.NewHealthClient(conn)
			_, err = client.Check(ctx, &healthv1.HealthCheckRequest{})
			_ = conn.Close()
		}
		cancel()
		if err == nil {
			return
		}
		lastErr = err
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal(fmt.Errorf("server health check did not pass: %w", lastErr))
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func changesetListContains(values []*corev1.Changeset, want string) bool {
	for _, value := range values {
		if value != nil && value.Id == want {
			return true
		}
	}
	return false
}
