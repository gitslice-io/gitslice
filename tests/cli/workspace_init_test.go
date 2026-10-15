package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

func TestWorkspaceInitHydrateUsesGlobalClientObjectCache(t *testing.T) {
	ts := startTestServer(t)
	seedHome := t.TempDir()
	seedWorkspace := t.TempDir()
	loginTestCLI(t, ts, seedHome, seedWorkspace)
	runCLI(t, seedHome, seedWorkspace, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, seedWorkspace, "cached.go", "package payment\nconst Cached = true\n")
	runCLI(t, seedHome, seedWorkspace, "cs", "create", "--title", "seed cached")
	runCLI(t, seedHome, seedWorkspace, "cs", "submit")

	home := t.TempDir()
	firstWorkspace := t.TempDir()
	secondWorkspace := t.TempDir()
	loginTestCLI(t, ts, home, firstWorkspace)
	first := runCLI(t, home, firstWorkspace, "workspace", "init", "acme/payment")
	if !strings.Contains(first, "hydrated 1 file(s) through cache (0 hit(s), 1 miss(es))") {
		t.Fatalf("expected first hydrate to miss cache, got:\n%s", first)
	}
	assertWorkspaceFile(t, firstWorkspace, "acme/payment/cached.go", "package payment\nconst Cached = true\n")

	second := runCLI(t, home, secondWorkspace, "workspace", "init", "acme/payment")
	if !strings.Contains(second, "hydrated 1 file(s) through cache (1 hit(s), 0 miss(es))") {
		t.Fatalf("expected second hydrate to hit cache, got:\n%s", second)
	}
	assertWorkspaceFile(t, secondWorkspace, "acme/payment/cached.go", "package payment\nconst Cached = true\n")
}

func TestWorkspaceInitMaterializesCanonicalLayoutAndRequiresEmptyDirectory(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	outsideWorkspace := t.TempDir()
	workspace := t.TempDir()
	nonEmptyWorkspace := t.TempDir()

	runCLISignupThroughWeb(t, home, outsideWorkspace, ts, "init_user")
	runCLI(t, home, outsideWorkspace, "fs", "mkdir", "/init-user/hello")
	runCLI(t, home, outsideWorkspace, "fs", "mkdir", "/init-user/hello/empty")
	runCLI(t, home, outsideWorkspace, "fs", "write", "/init-user/hello/readme.txt", "--text", "hello workspace\n")
	runCLI(t, home, outsideWorkspace, "slice", "create", "init-user/hello", "--include", "/init-user/hello")

	initOutput := runCLI(t, home, workspace, "init", "init-user/hello")
	if !strings.Contains(initOutput, "hydrated 1 file(s) through cache") {
		t.Fatalf("unexpected workspace init output:\n%s", initOutput)
	}
	assertWorkspaceMetadataDoesNotContainToken(t, workspace, readToken(t, home))
	assertWorkspaceFile(t, workspace, "init-user/hello/readme.txt", "hello workspace\n")
	assertWorkspaceDir(t, workspace, "init-user/hello/empty")
	status := runCLI(t, home, workspace, "status")
	if !strings.Contains(status, "status: clean") {
		t.Fatalf("expected clean canonical workspace after init, got:\n%s", status)
	}

	writeWorkspaceFile(t, nonEmptyWorkspace, "existing.txt", "do not overwrite\n")
	_, stderr := runCLIFails(t, home, nonEmptyWorkspace, "init", "init-user/hello")
	if !strings.Contains(stderr, "workspace init requires an empty directory") {
		t.Fatalf("expected non-empty workspace init rejection, got:\n%s", stderr)
	}

	nestedEmpty := filepath.Join(workspace, "init-user", "hello", "nested-empty")
	if err := os.MkdirAll(nestedEmpty, 0o755); err != nil {
		t.Fatal(err)
	}
	_, stderr = runCLIFails(t, home, nestedEmpty, "init", "init-user/hello")
	if !strings.Contains(stderr, "already inside a gitslice workspace") {
		t.Fatalf("expected nested workspace init rejection, got:\n%s", stderr)
	}
}

func TestWorkspaceCommandsWorkFromSubdirectories(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()

	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")

	subdir := filepath.Join(workspace, "acme", "payment", "pkg")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeWorkspaceFile(t, workspace, "acme/payment/pkg/subdir.go", "package pkg\nconst FromSubdir = true\n")

	status := runCLI(t, home, subdir, "status")
	if !strings.Contains(status, "/acme/payment/pkg/subdir.go") {
		t.Fatalf("expected subdir status to scan workspace root, got:\n%s", status)
	}
	runCLI(t, home, subdir, "cs", "create", "--title", "subdir edit")
	runCLI(t, home, subdir, "cs", "submit")
	status = runCLI(t, home, subdir, "status")
	if !strings.Contains(status, "status: clean") {
		t.Fatalf("expected clean status from subdir after submit, got:\n%s", status)
	}
}

func TestWorkspaceServiceHelpers(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	token := readToken(t, home)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	const content = "package payment\nconst Hydrate = true\n"
	writeWorkspaceFile(t, workspace, "hydrate.go", content)
	runCLI(t, home, workspace, "cs", "create", "--title", "hydrate helper")
	runCLI(t, home, workspace, "cs", "submit")

	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	workspaceClient := corev1.NewWorkspaceServiceClient(conn)
	ref := &corev1.WorkspaceRef{Id: "acme/payment"}

	state, err := workspaceClient.GetWorkspaceState(ctx, &corev1.GetWorkspaceStateRequest{Workspace: ref})
	if err != nil {
		t.Fatal(err)
	}
	if state.BaseCommitId == "" || state.Slice == nil || state.Slice.SliceId == "" || !containsString(state.HydratedPaths, "/acme/payment") {
		t.Fatalf("unexpected workspace state: %#v", state)
	}
	hydrated, err := workspaceClient.HydratePaths(ctx, &corev1.HydratePathsRequest{
		Workspace: ref,
		Paths:     []string{"/acme/payment/hydrate.go"},
		Mode:      "file_contents",
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(hydrated.Data) != content || hydrated.Entry == nil || hydrated.Entry.Kind != corev1.EntryKind_ENTRY_KIND_FILE {
		t.Fatalf("unexpected hydration response: %#v data=%q", hydrated, string(hydrated.Data))
	}
	recorded, err := workspaceClient.RecordWorkspaceOperation(ctx, &corev1.RecordWorkspaceOperationRequest{
		Operation: &corev1.WorkspaceOperation{
			Workspace:     ref,
			OperationType: "functional_test",
			Description:   "record workspace operation",
			AffectedPaths: []string{"/acme/payment/hydrate.go"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(recorded.OperationId, "op_") {
		t.Fatalf("expected generated operation id, got %#v", recorded)
	}
	_, err = workspaceClient.GetWorkspaceState(ctx, &corev1.GetWorkspaceStateRequest{Workspace: &corev1.WorkspaceRef{Id: "bad-workspace"}})
	if grpcstatus.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected invalid workspace id to fail with InvalidArgument, got %v", err)
	}
}
