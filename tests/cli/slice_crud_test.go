package cli_test

import (
	"strings"
	"testing"
	"time"

	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

func TestCLISliceCRUD(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspace, "docs/seed.txt", "docs\n")
	writeWorkspaceFile(t, workspace, "docs/archive/seed.txt", "archive\n")
	writeWorkspaceFile(t, workspace, "multi-a/seed.txt", "multi a\n")
	writeWorkspaceFile(t, workspace, "multi-b/seed.txt", "multi b\n")
	runCLI(t, home, workspace, "cs", "create", "--title", "slice crud seed")
	runCLI(t, home, workspace, "cs", "submit")

	_, stderr := runCLIFails(t, home, workspace, "slice", "create", "acme/missing", "--include", "/acme/payment/missing")
	if !strings.Contains(stderr, "included path does not exist: /acme/payment/missing") {
		t.Fatalf("missing include stderr = %q, want path existence error", stderr)
	}

	created := runCLI(t, home, workspace, "slice", "create", "acme/docs", "--include", "/acme/payment/docs", "--visibility", "private")
	for _, want := range []string{"created slice acme:docs", "visibility: private", "/acme/payment/docs"} {
		if !strings.Contains(created, want) {
			t.Fatalf("created slice output missing %q:\n%s", want, created)
		}
	}
	// Slice history comes from an index the server updates after the submit
	// returns, so a loaded machine can briefly show it without the commit.
	var docsHistory string
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		docsHistory = runCLI(t, home, workspace, "log", "--slice", "acme/docs")
		if strings.Contains(docsHistory, "slice crud seed") || time.Now().After(deadline) {
			break
		}
	}
	if !strings.Contains(docsHistory, "slice crud seed") {
		t.Fatalf("docs slice commit history missing seed commit:\n%s", docsHistory)
	}

	listed := runCLI(t, home, workspace, "slice", "list", "acme")
	for _, want := range []string{"slices for account acme:", "acme:payment", "acme:docs", "visibility: private", "included paths: /acme/payment/docs"} {
		if !strings.Contains(listed, want) {
			t.Fatalf("slice list output missing %q:\n%s", want, listed)
		}
	}

	info := runCLI(t, home, workspace, "slice", "info", "acme/docs")
	for _, want := range []string{"ref: acme:docs", "id: slice_acme_docs", "definition_hash:"} {
		if !strings.Contains(info, want) {
			t.Fatalf("slice info output missing %q:\n%s", want, info)
		}
	}

	paths := strings.TrimSpace(runCLI(t, home, workspace, "slice", "paths", "acme/docs"))
	if paths != "/acme/payment/docs" {
		t.Fatalf("slice paths = %q, want /acme/payment/docs", paths)
	}

	runCLI(t, home, workspace, "slice", "create", "acme/multi", "--include", "/acme/payment/multi-a,/acme/payment/multi-b")
	multiPaths := strings.TrimSpace(runCLI(t, home, workspace, "slice", "paths", "acme/multi"))
	if multiPaths != "/acme/payment/multi-a\n/acme/payment/multi-b" {
		t.Fatalf("comma-separated slice paths = %q, want two paths", multiPaths)
	}

	updated := runCLI(t, home, workspace, "slice", "update", "acme/docs",
		"--include", "/acme/payment/docs,/acme/payment/docs/archive",
		"--visibility", "public",
	)
	for _, want := range []string{"updated slice acme:docs", "visibility: public", "/acme/payment/docs/archive"} {
		if !strings.Contains(updated, want) {
			t.Fatalf("slice update output missing %q:\n%s", want, updated)
		}
	}

	_, stderr = runCLIFails(t, home, workspace, "slice", "delete", "acme/docs")
	if !strings.Contains(stderr, "requires --yes") {
		t.Fatalf("slice delete without --yes stderr missing confirmation requirement:\n%s", stderr)
	}

	deleted := runCLI(t, home, workspace, "slice", "delete", "acme/docs", "--yes")
	if !strings.Contains(deleted, "deleted slice acme:docs") {
		t.Fatalf("unexpected delete output:\n%s", deleted)
	}
	_, stderr = runCLIFails(t, home, workspace, "slice", "info", "acme/docs")
	if !strings.Contains(stderr, "not found") {
		t.Fatalf("slice info after delete stderr = %q, want not found", stderr)
	}
}

func TestSliceDefinitionUpdateConflict(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspace, "docs/seed.txt", "docs\n")
	runCLI(t, home, workspace, "cs", "create", "--title", "slice definition seed")
	runCLI(t, home, workspace, "cs", "submit")

	token := ts.loginViaGRPC(t, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	slices := corev1.NewSliceServiceClient(conn)

	slice, err := slices.ResolveSlice(ctx, &corev1.ResolveSliceRequest{Ref: &corev1.SliceRef{Account: "acme", Slice: "payment"}})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := slices.UpdateSliceDefinition(ctx, &corev1.UpdateSliceDefinitionRequest{
		SliceId:                slice.Id,
		ExpectedDefinitionHash: slice.DefinitionHash,
		Definition: &corev1.SliceDefinition{
			Version:       slice.Definition.Version,
			Visibility:    "public",
			IncludedPaths: append(append([]string{}, slice.Definition.IncludedPaths...), "/acme/payment/docs"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != slice.Definition.Version+1 {
		t.Fatalf("expected definition version %d, got %d", slice.Definition.Version+1, updated.Version)
	}
	if updated.Visibility != "public" || !containsString(updated.IncludedPaths, "/acme/payment/docs") {
		t.Fatalf("unexpected updated definition: %#v", updated)
	}
	_, err = slices.UpdateSliceDefinition(ctx, &corev1.UpdateSliceDefinitionRequest{
		SliceId:                slice.Id,
		ExpectedDefinitionHash: slice.DefinitionHash,
		Definition: &corev1.SliceDefinition{
			Version:       updated.Version,
			Visibility:    "private",
			IncludedPaths: []string{"/acme/payment"},
		},
	})
	if grpcstatus.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected stale definition update to fail with FailedPrecondition, got %v", err)
	}
}
