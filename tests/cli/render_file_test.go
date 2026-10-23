package cli_test

import (
	"context"
	"strings"
	"testing"

	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// RenderFile turns a README into safe HTML on the server, with repository
// images marked for the client, under the same access rules as ReadFile.
func TestRenderFileServesSafeHTML(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	token := readToken(t, home)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	readme := strings.Join([]string{
		`<h1 align="center">Payment</h1>`,
		``,
		`Read the [guide](guide.md). ![logo](logo.png) ![shared](/img/shared.png)`,
		``,
		`<script>alert(1)</script><img src=x onerror="alert(2)">`,
		``,
		`[bad](javascript:alert(3))`,
	}, "\n")
	writeWorkspaceFile(t, workspace, "docs/README.md", readme)
	writeWorkspaceFile(t, workspace, "docs/big.md", strings.Repeat("x", 300<<10))
	writeWorkspaceFile(t, workspace, "docs/main.go", "package docs\n")
	runCLI(t, home, workspace, "create", "--all", "--message", "docs", "--submit")

	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	repository := corev1.NewRepositoryServiceClient(conn)
	ref, err := repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	slice := &corev1.SliceRef{Account: "acme", Slice: "payment"}
	render := func(ctx context.Context, path string, slice *corev1.SliceRef) (*corev1.RenderFileResponse, error) {
		return repository.RenderFile(ctx, &corev1.RenderFileRequest{CommitId: ref.CommitId, Path: path, Slice: slice})
	}

	got, err := render(ctx, "/acme/payment/docs/README.md", slice)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<h1 align="center">Payment</h1>`,
		`<a href="guide.md" target="_blank" rel="noopener noreferrer">guide</a>`,
		`data-repo-image="/acme/payment/docs/logo.png"`,
		// Root-absolute, against the slice's included path.
		`data-repo-image="/acme/payment/img/shared.png"`,
	} {
		if !strings.Contains(got.Html, want) {
			t.Errorf("rendered README lacks %q:\n%s", want, got.Html)
		}
	}
	for _, bad := range []string{"<script", "onerror", "javascript:"} {
		if strings.Contains(strings.ToLower(got.Html), bad) {
			t.Errorf("rendered README contains %q:\n%s", bad, got.Html)
		}
	}
	read, err := repository.ReadFile(ctx, &corev1.ReadFileRequest{CommitId: ref.CommitId, Path: "/acme/payment/docs/README.md"})
	if err != nil {
		t.Fatal(err)
	}
	if got.ContentHash == "" || got.ContentHash != read.ContentHash {
		t.Errorf("content hash %q, want the file's %q", got.ContentHash, read.ContentHash)
	}

	// Not Markdown, too large to show inline, or outside the slice.
	if _, err := render(ctx, "/acme/payment/docs/main.go", slice); status.Code(err) != codes.InvalidArgument {
		t.Errorf("rendering a Go file: %v, want InvalidArgument", err)
	}
	if _, err := render(ctx, "/acme/payment/docs/big.md", slice); status.Code(err) != codes.FailedPrecondition {
		t.Errorf("rendering a 300 KiB file: %v, want FailedPrecondition", err)
	}
	if _, err := render(ctx, "/acme/other/README.md", slice); err == nil {
		t.Error("rendered a path outside the slice")
	}

	// Without signing in, it answers exactly as ReadFile does.
	anon := context.Background()
	_, readErr := repository.ReadFile(anon, &corev1.ReadFileRequest{CommitId: ref.CommitId, Path: "/acme/payment/docs/README.md", Slice: slice})
	_, renderErr := render(anon, "/acme/payment/docs/README.md", slice)
	if status.Code(readErr) != status.Code(renderErr) {
		t.Errorf("signed out: ReadFile %v, RenderFile %v; want the same", readErr, renderErr)
	}
}
