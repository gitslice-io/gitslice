package cli_test

import (
	"testing"

	"gitslice.io/gitslice/internal/postgres"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestRepositoryReadAPIs(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	token := readToken(t, home)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	const content = "package api\nconst ReadAPI = true\n"
	writeWorkspaceFile(t, workspace, "api/read_api.go", content)
	runCLI(t, home, workspace, "cs", "create", "--title", "repository api read")
	runCLI(t, home, workspace, "cs", "submit")

	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	repository := corev1.NewRepositoryServiceClient(conn)

	ref, err := repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if ref.Name != postgres.DefaultTargetRef || ref.CommitId == "" {
		t.Fatalf("unexpected default ref: %#v", ref)
	}
	commit, err := repository.GetCommit(ctx, &corev1.GetCommitRequest{CommitId: ref.CommitId})
	if err != nil {
		t.Fatal(err)
	}
	if !containsString(commit.ChangedPaths, "/acme/payment/api/read_api.go") {
		t.Fatalf("expected commit to include changed path, got %#v", commit.ChangedPaths)
	}
	root, err := repository.ListDirectory(ctx, &corev1.ListDirectoryRequest{CommitId: ref.CommitId, Path: "/"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasEntry(root.Entries, "acme", corev1.EntryKind_ENTRY_KIND_DIRECTORY) {
		t.Fatalf("expected root to contain acme directory: %#v", root.Entries)
	}
	payment, err := repository.ListDirectory(ctx, &corev1.ListDirectoryRequest{CommitId: ref.CommitId, Path: "/acme/payment"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasEntry(payment.Entries, "api", corev1.EntryKind_ENTRY_KIND_DIRECTORY) {
		t.Fatalf("expected payment directory to contain api directory: %#v", payment.Entries)
	}
	resolvedDir, err := repository.ResolvePath(ctx, &corev1.ResolvePathRequest{CommitId: ref.CommitId, Path: "/acme/payment/api"})
	if err != nil {
		t.Fatal(err)
	}
	if resolvedDir.Entry.Kind != corev1.EntryKind_ENTRY_KIND_DIRECTORY {
		t.Fatalf("expected api to resolve as directory: %#v", resolvedDir.Entry)
	}
	resolvedFile, err := repository.ResolvePath(ctx, &corev1.ResolvePathRequest{CommitId: ref.CommitId, Path: "/acme/payment/api/read_api.go"})
	if err != nil {
		t.Fatal(err)
	}
	if resolvedFile.Entry.Kind != corev1.EntryKind_ENTRY_KIND_FILE || resolvedFile.Entry.ContentHash == "" {
		t.Fatalf("expected read_api.go to resolve as file with content hash: %#v", resolvedFile.Entry)
	}
	read, err := repository.ReadFile(ctx, &corev1.ReadFileRequest{CommitId: ref.CommitId, Path: "/acme/payment/api/read_api.go"})
	if err != nil {
		t.Fatal(err)
	}
	if string(read.Data) != content || read.ContentHash != resolvedFile.Entry.ContentHash {
		t.Fatalf("unexpected read response: content=%q hash=%q want hash=%q", string(read.Data), read.ContentHash, resolvedFile.Entry.ContentHash)
	}
	partial, err := repository.ReadFile(ctx, &corev1.ReadFileRequest{CommitId: ref.CommitId, Path: "/acme/payment/api/read_api.go", Offset: 8, Length: 3})
	if err != nil {
		t.Fatal(err)
	}
	if string(partial.Data) != "api" || partial.Offset != 8 {
		t.Fatalf("unexpected partial read response: %#v data=%q", partial, string(partial.Data))
	}
}
