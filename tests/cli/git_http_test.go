package cli_test

import (
	"encoding/base64"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestGitHTTPAuthAndUnsupportedOperationMatrix(t *testing.T) {
	ts := startTestServer(t)
	token := ts.loginViaGRPC(t, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)

	uploadInfoRefs := "/git/acme/payment.git/info/refs?service=git-upload-pack"
	statusCode, headers, body := gitHTTPRaw(t, ts.gitAddr, uploadInfoRefs, "")
	if statusCode != http.StatusUnauthorized {
		t.Fatalf("expected unauthenticated upload-pack discovery to return 401, got %d:\n%s", statusCode, string(body))
	}
	if got := headers.Get("WWW-Authenticate"); !strings.Contains(got, `Basic realm="gitslice"`) {
		t.Fatalf("expected basic auth challenge, got %q", got)
	}

	statusCode, _, body = gitHTTPRaw(t, ts.gitAddr, uploadInfoRefs, "Bearer not-a-token")
	if statusCode != http.StatusUnauthorized {
		t.Fatalf("expected invalid token upload-pack discovery to return 401, got %d:\n%s", statusCode, string(body))
	}

	outsiderToken, _, _ := ts.provisionAccount(t, "git-public-outsider", "git-public-outsider")
	statusCode, _, body = gitHTTPRaw(t, ts.gitAddr, uploadInfoRefs, "Bearer "+outsiderToken)
	if statusCode != http.StatusForbidden {
		t.Fatalf("expected private upload-pack discovery for outsider to return 403, got %d:\n%s", statusCode, string(body))
	}

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
	statusCode, headers, body = gitHTTPRaw(t, ts.gitAddr, uploadInfoRefs, "Bearer "+outsiderToken)
	if statusCode != http.StatusOK {
		t.Fatalf("expected public upload-pack discovery for outsider to return 200, got %d:\n%s", statusCode, string(body))
	}
	if got := headers.Get("Content-Type"); !strings.Contains(got, "application/x-git-upload-pack-advertisement") {
		t.Fatalf("expected public upload-pack advertisement content type, got %q", got)
	}

	basicAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:"+token))
	statusCode, headers, body = gitHTTPRaw(t, ts.gitAddr, uploadInfoRefs, basicAuth)
	if statusCode != http.StatusOK {
		t.Fatalf("expected basic-auth upload-pack discovery to return 200, got %d:\n%s", statusCode, string(body))
	}
	if got := headers.Get("Content-Type"); !strings.Contains(got, "application/x-git-upload-pack-advertisement") {
		t.Fatalf("expected upload-pack advertisement content type, got %q", got)
	}

	statusCode, _, body = gitHTTPRaw(t, ts.gitAddr, "/git/acme/missing.git/info/refs?service=git-upload-pack", "Bearer "+token)
	if statusCode != http.StatusNotFound {
		t.Fatalf("expected missing slice to return 404, got %d:\n%s", statusCode, string(body))
	}

	receiveInfoRefs := "/git/acme/payment.git/info/refs?service=git-receive-pack"
	statusCode, headers, body = gitHTTPRaw(t, ts.gitAddr, receiveInfoRefs, "")
	if statusCode != http.StatusUnauthorized {
		t.Fatalf("expected unauthenticated receive-pack discovery to return 401, got %d:\n%s", statusCode, string(body))
	}
	if got := headers.Get("WWW-Authenticate"); !strings.Contains(got, `Basic realm="gitslice"`) {
		t.Fatalf("expected receive-pack auth challenge, got %q", got)
	}

	statusCode, headers, body = gitHTTPRaw(t, ts.gitAddr, receiveInfoRefs, "Bearer "+token)
	if statusCode != http.StatusOK {
		t.Fatalf("expected authenticated receive-pack discovery to return 200, got %d:\n%s", statusCode, string(body))
	}
	if !strings.Contains(headers.Get("Content-Type"), "application/x-git-receive-pack-advertisement") {
		t.Fatalf("expected receive-pack advertisement content type, got %q", headers.Get("Content-Type"))
	}

	statusCode, _, body = gitHTTPRaw(t, ts.gitAddr, "/not-git/acme/payment.git/info/refs?service=git-upload-pack", "Bearer "+token)
	if statusCode != http.StatusNotFound {
		t.Fatalf("expected non-git route to return 404, got %d:\n%s", statusCode, string(body))
	}
}

func TestGitCloneProjection(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	token := readToken(t, home)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspace, "git_layer.go", "package payment\nconst GitLayer = true\n")
	runCLI(t, home, workspace, "cs", "create", "--title", "git projection")
	runCLI(t, home, workspace, "cs", "submit")

	cloneDir := filepath.Join(t.TempDir(), "payment")
	gitURL := "http://" + ts.gitAddr + "/git/acme/payment.git"
	_, stderr, err := runGitResult("", "clone", gitURL, filepath.Join(t.TempDir(), "unauthenticated-payment"))
	if err == nil {
		t.Fatal("expected unauthenticated git clone to be rejected")
	}
	if !strings.Contains(stderr, "401") &&
		!strings.Contains(stderr, "Authentication failed") &&
		!strings.Contains(stderr, "authentication") &&
		!strings.Contains(stderr, "Username") {
		t.Fatalf("expected unauthenticated clone rejection, got stderr:\n%s", stderr)
	}
	runGit(t, "", "-c", "http.extraHeader=Authorization: Bearer "+token, "clone", gitURL, cloneDir)
	projected, err := os.ReadFile(filepath.Join(cloneDir, "acme", "payment", "git_layer.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(projected) != "package payment\nconst GitLayer = true\n" {
		t.Fatalf("unexpected projected file contents:\n%s", string(projected))
	}
	writeWorkspaceFile(t, workspace, "git_fetch.go", "package payment\nconst GitFetch = true\n")
	runCLI(t, home, workspace, "cs", "create", "--title", "git fetch projection")
	runCLI(t, home, workspace, "cs", "submit")
	runGit(t, "", "-C", cloneDir, "-c", "http.extraHeader=Authorization: Bearer "+token, "fetch", "origin", "main")
	runGit(t, "", "-C", cloneDir, "checkout", "-B", "main", "origin/main")
	projectedFetch, err := os.ReadFile(filepath.Join(cloneDir, "acme", "payment", "git_fetch.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(projectedFetch) != "package payment\nconst GitFetch = true\n" {
		t.Fatalf("unexpected fetched file contents:\n%s", string(projectedFetch))
	}
}

func TestGitPushIntoChangesets(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	token := readToken(t, home)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspace, "git_push_base.go", "package payment\nconst GitPushBase = true\n")
	runCLI(t, home, workspace, "cs", "create", "--title", "git push base")
	runCLI(t, home, workspace, "cs", "submit")

	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	changesets := corev1.NewChangesetServiceClient(conn)

	cloneDir := filepath.Join(t.TempDir(), "payment")
	gitURL := "http://" + ts.gitAddr + "/git/acme/payment.git"
	runGit(t, "", "-c", "http.extraHeader=Authorization: Bearer "+token, "clone", gitURL, cloneDir)
	runGit(t, "", "-C", cloneDir, "config", "user.name", "Git Pusher")
	runGit(t, "", "-C", cloneDir, "config", "user.email", "git-pusher@example.invalid")

	writeWorkspaceFile(t, cloneDir, "acme/payment/git_push_one.go", "package payment\nconst GitPushOne = true\n")
	runGit(t, "", "-C", cloneDir, "add", "acme/payment/git_push_one.go")
	runGit(t, "", "-C", cloneDir, "commit", "-m", "git push first patchset")
	_, stderr, err := runGitResult("", "-C", cloneDir, "-c", "http.extraHeader=Authorization: Bearer "+token, "push", "origin", "HEAD:refs/changes/new")
	if err != nil {
		t.Fatalf("git push refs/changes/new failed: %v\nstderr:\n%s", err, stderr)
	}
	draft := singleDraftChangeset(t, ctx, changesets)
	draftSelector := shortChangesetID(draft.Id)
	if !strings.Contains(stderr, "Created changeset "+draftSelector) {
		t.Fatalf("push output missing created changeset id:\n%s", stderr)
	}
	if len(draft.Patchsets) != 1 {
		t.Fatalf("draft patchset count = %d, want 1: %#v", len(draft.Patchsets), draft)
	}
	if !containsString(draft.Patchsets[0].ChangedPaths, "/acme/payment/git_push_one.go") {
		t.Fatalf("first patchset changed paths = %#v", draft.Patchsets[0].ChangedPaths)
	}

	writeWorkspaceFile(t, cloneDir, "acme/payment/git_push_two.go", "package payment\nconst GitPushTwo = true\n")
	runGit(t, "", "-C", cloneDir, "add", "acme/payment/git_push_two.go")
	runGit(t, "", "-C", cloneDir, "commit", "-m", "git push second patchset")
	_, stderr, err = runGitResult("", "-C", cloneDir, "-c", "http.extraHeader=Authorization: Bearer "+token, "push", "origin", "HEAD:refs/changes/"+draftSelector)
	if err != nil {
		t.Fatalf("git push existing changeset failed: %v\nstderr:\n%s", err, stderr)
	}
	if !strings.Contains(stderr, "Updated changeset "+draftSelector) {
		t.Fatalf("push output missing updated changeset id:\n%s", stderr)
	}
	updated, err := changesets.GetChangeset(ctx, &corev1.GetChangesetRequest{ChangesetId: draft.Id})
	if err != nil {
		t.Fatal(err)
	}
	if updated.CurrentPatchsetNumber != 2 || len(updated.Patchsets) != 2 {
		t.Fatalf("updated changeset patchsets = current %d count %d, want current 2 count 2", updated.CurrentPatchsetNumber, len(updated.Patchsets))
	}
	second := updated.Patchsets[1]
	for _, want := range []string{"/acme/payment/git_push_one.go", "/acme/payment/git_push_two.go"} {
		if !containsString(second.ChangedPaths, want) {
			t.Fatalf("second patchset changed paths missing %s: %#v", want, second.ChangedPaths)
		}
	}

	_, stderr, err = runGitResult("", "-C", cloneDir, "-c", "http.extraHeader=Authorization: Bearer "+token, "push", "origin", "HEAD:refs/heads/main")
	if err == nil {
		t.Fatal("expected protected branch push to be rejected")
	}
	if !strings.Contains(stderr, "protected") && !strings.Contains(stderr, "refs/changes/new") {
		t.Fatalf("protected branch rejection did not guide to changes refs:\n%s", stderr)
	}

	outsiderToken, _, _ := ts.provisionAccount(t, "git-push-outsider", "git-push-outsider")
	_, stderr, err = runGitResult("", "-C", cloneDir, "-c", "http.extraHeader=Authorization: Bearer "+outsiderToken, "push", "origin", "HEAD:refs/changes/new")
	if err == nil {
		t.Fatal("expected unauthorized git push to be rejected")
	}
	if !strings.Contains(stderr, "403") && !strings.Contains(strings.ToLower(stderr), "permission") {
		t.Fatalf("unauthorized push did not fail with auth guidance:\n%s", stderr)
	}

	runCLI(t, home, workspace, "cs", "submit", shortChangesetID(updated.Id))
	submitted := waitForSubmittedChangeset(t, ctx, changesets, updated.Id)
	if !containsString(submitted.AffectedPaths, "/acme/payment/git_push_two.go") {
		t.Fatalf("submitted changeset affected paths = %#v", submitted.AffectedPaths)
	}
}
