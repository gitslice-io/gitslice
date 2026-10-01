package cli_test

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// TestGitAnonymousPublicReads covers unauthenticated Git HTTP access: anyone may
// clone a public slice, pushes always need credentials, and anonymous callers
// cannot tell a private slice from a missing one.
func TestGitAnonymousPublicReads(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	token := readToken(t, home)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspace, "anonymous.go", "package payment\nconst Anonymous = true\n")
	runCLI(t, home, workspace, "cs", "create", "--title", "anonymous read")
	runCLI(t, home, workspace, "cs", "submit")

	uploadInfoRefs := "/git/acme/payment.git/info/refs?service=git-upload-pack"
	statusCode, headers, body := gitHTTPRaw(t, ts.gitAddr, uploadInfoRefs, "")
	if statusCode != http.StatusUnauthorized {
		t.Fatalf("expected anonymous discovery of a private slice to return 401, got %d:\n%s", statusCode, string(body))
	}
	if got := headers.Get("WWW-Authenticate"); !strings.Contains(got, `Basic realm="gitslice"`) {
		t.Fatalf("expected basic auth challenge for a private slice, got %q", got)
	}

	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	slices := corev1.NewSliceServiceClient(conn)
	payment, err := slices.ResolveSlice(ctx, &corev1.ResolveSliceRequest{Ref: &corev1.SliceRef{Account: "acme", Slice: "payment"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := slices.UpdateSliceDefinition(ctx, &corev1.UpdateSliceDefinitionRequest{
		SliceId:                payment.Id,
		ExpectedDefinitionHash: payment.DefinitionHash,
		Definition: &corev1.SliceDefinition{
			IncludedPaths: payment.Definition.IncludedPaths,
			Visibility:    "public",
		},
	}); err != nil {
		t.Fatal(err)
	}

	statusCode, headers, body = gitHTTPRaw(t, ts.gitAddr, uploadInfoRefs, "")
	if statusCode != http.StatusOK {
		t.Fatalf("expected anonymous discovery of a public slice to return 200, got %d:\n%s", statusCode, string(body))
	}
	if got := headers.Get("Content-Type"); !strings.Contains(got, "application/x-git-upload-pack-advertisement") {
		t.Fatalf("expected upload-pack advertisement content type, got %q", got)
	}

	statusCode, _, body = gitHTTPRaw(t, ts.gitAddr, uploadInfoRefs, "Bearer not-a-token")
	if statusCode != http.StatusUnauthorized {
		t.Fatalf("expected an invalid token to be rejected even for a public slice, got %d:\n%s", statusCode, string(body))
	}

	cloneDir := filepath.Join(t.TempDir(), "payment")
	gitURL := "http://" + ts.gitAddr + "/git/acme/payment.git"
	runGit(t, "", "clone", gitURL, cloneDir)
	cloned, err := os.ReadFile(filepath.Join(cloneDir, "acme", "payment", "anonymous.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(cloned) != "package payment\nconst Anonymous = true\n" {
		t.Fatalf("unexpected anonymously cloned contents:\n%s", string(cloned))
	}

	receiveInfoRefs := "/git/acme/payment.git/info/refs?service=git-receive-pack"
	statusCode, headers, body = gitHTTPRaw(t, ts.gitAddr, receiveInfoRefs, "")
	if statusCode != http.StatusUnauthorized {
		t.Fatalf("expected anonymous receive-pack discovery of a public slice to return 401, got %d:\n%s", statusCode, string(body))
	}
	if got := headers.Get("WWW-Authenticate"); !strings.Contains(got, `Basic realm="gitslice"`) {
		t.Fatalf("expected receive-pack auth challenge, got %q", got)
	}

	statusCode, headers, body = gitHTTPRaw(t, ts.gitAddr, "/git/acme/missing.git/info/refs?service=git-upload-pack", "")
	if statusCode != http.StatusUnauthorized {
		t.Fatalf("expected anonymous discovery of a missing slice to return 401 like a private one, got %d:\n%s", statusCode, string(body))
	}
	if got := headers.Get("WWW-Authenticate"); !strings.Contains(got, `Basic realm="gitslice"`) {
		t.Fatalf("expected basic auth challenge for a missing slice, got %q", got)
	}
}
