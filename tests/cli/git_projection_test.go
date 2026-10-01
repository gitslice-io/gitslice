package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func gitAuth(token string) []string {
	return []string{"-c", "http.extraHeader=Authorization: Bearer " + token}
}

func gitWithAuth(t *testing.T, token, dir string, args ...string) string {
	t.Helper()
	full := append(gitAuth(token), args...)
	if dir != "" {
		full = append([]string{"-C", dir}, full...)
	}
	return runGit(t, "", full...)
}

func submitWorkspaceFile(t *testing.T, home, workspace, rel, content, title string) {
	t.Helper()
	writeWorkspaceFile(t, workspace, rel, content)
	runCLI(t, home, workspace, "cs", "create", "--title", title)
	runCLI(t, home, workspace, "cs", "submit")
}

// TestGitProjectionHistory checks that a slice's Git history has one commit per
// native commit that changed the slice, carries only that slice's metadata,
// is deterministic across a cold rebuild, and fast-forwards on later changes.
func TestGitProjectionHistory(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	payment := t.TempDir()
	backend := t.TempDir()
	loginTestCLI(t, ts, home, payment)
	token := readToken(t, home)
	runCLI(t, home, payment, "workspace", "init", "acme/payment")
	runCLI(t, home, backend, "workspace", "init", "acme/backend")

	submitWorkspaceFile(t, home, payment, "one.go", "package payment\nconst One = 1\n", "payment one")
	submitWorkspaceFile(t, home, backend, "acme/backend/hidden.go", "package backend\n", "backend only change")
	submitWorkspaceFile(t, home, payment, "two.go", "package payment\nconst Two = 2\n", "payment two")
	submitWorkspaceFile(t, home, payment, "one.go", "package payment\nconst One = 11\n", "payment one again")

	// A change in another account must neither appear in nor alter the slice's
	// history: no foreign changeset titles, no new projected commit.
	otherToken, _, otherSubject := ts.provisionAccount(t, "zeta-other", "zeta-other")
	otherHome := t.TempDir()
	otherWorkspace := t.TempDir()
	writeCLIAuthConfig(t, otherHome, ts.addr, otherToken, otherSubject)
	runCLI(t, otherHome, otherWorkspace, "workspace", "init", "zeta-other/home")
	submitWorkspaceFile(t, otherHome, otherWorkspace, "secret.txt", "secret\n", "SECRET foreign account title")

	gitURL := "http://" + ts.gitAddr + "/git/acme/payment.git"
	cloneDir := filepath.Join(t.TempDir(), "payment")
	gitWithAuth(t, token, "", "clone", gitURL, cloneDir)
	subjects := strings.Split(strings.TrimSpace(runGit(t, cloneDir, "log", "--format=%s")), "\n")
	want := []string{"payment one again", "payment two", "payment one"}
	if len(subjects) != len(want) {
		t.Fatalf("projected history subjects = %q, want %q", subjects, want)
	}
	for i := range want {
		if subjects[i] != want[i] {
			t.Fatalf("projected history subjects = %q, want %q", subjects, want)
		}
	}
	fullLog := runGit(t, cloneDir, "log", "--format=%B%n--%n%an <%ae>")
	if strings.Contains(fullLog, "SECRET") || strings.Contains(fullLog, "backend only change") {
		t.Fatalf("projected history leaked commits that did not change the slice:\n%s", fullLog)
	}
	if got := strings.Count(fullLog, "Gitslice-Commit: "); got != 3 {
		t.Fatalf("expected a Gitslice-Commit trailer on each of 3 commits, got %d:\n%s", got, fullLog)
	}
	if !strings.Contains(fullLog, "acme <acme@users.noreply.gitslice.io>") {
		t.Fatalf("expected author acme <acme@users.noreply.gitslice.io>:\n%s", fullLog)
	}
	head := strings.TrimSpace(runGit(t, cloneDir, "rev-parse", "HEAD"))

	// Determinism: an instance with an empty cache computes the same ids.
	if err := os.RemoveAll(filepath.Join(ts.objectRoot, "git-cache")); err != nil {
		t.Fatal(err)
	}
	coldDir := filepath.Join(t.TempDir(), "cold")
	gitWithAuth(t, token, "", "clone", gitURL, coldDir)
	if cold := strings.TrimSpace(runGit(t, coldDir, "rev-parse", "HEAD")); cold != head {
		t.Fatalf("cold rebuild head = %s, incremental head = %s", cold, head)
	}

	// Later native commits fast-forward existing clones.
	submitWorkspaceFile(t, home, payment, "three.go", "package payment\nconst Three = 3\n", "payment three")
	gitWithAuth(t, token, cloneDir, "pull", "--ff-only", "origin", "main")
	if got := strings.TrimSpace(runGit(t, cloneDir, "log", "-1", "--format=%s")); got != "payment three" {
		t.Fatalf("after pull, head subject = %q", got)
	}
	if parent := strings.TrimSpace(runGit(t, cloneDir, "rev-parse", "HEAD^")); parent != head {
		t.Fatalf("new projected commit parent = %s, want previous head %s", parent, head)
	}
	if _, err := os.Stat(filepath.Join(cloneDir, "acme", "payment", "three.go")); err != nil {
		t.Fatalf("pulled file missing: %v", err)
	}
	config := strings.TrimSpace(runGit(t, filepath.Join(ts.objectRoot, "git-cache", "acme", "payment.git"), "config", "uploadpack.allowReachableSHA1InWant"))
	if config != "true" {
		t.Fatalf("uploadpack.allowReachableSHA1InWant = %q, want true", config)
	}
}

// TestGitPushStaleBase checks pushes from clones that missed later native
// commits: disjoint edits land, overlapping edits hit the normal conflict check.
func TestGitPushStaleBase(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	token := readToken(t, home)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	submitWorkspaceFile(t, home, workspace, "shared.go", "package payment\nconst Shared = 1\n", "base shared")
	submitWorkspaceFile(t, home, workspace, "own.go", "package payment\nconst Own = 1\n", "base own")

	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	changesets := corev1.NewChangesetServiceClient(conn)

	gitURL := "http://" + ts.gitAddr + "/git/acme/payment.git"
	staleDisjoint := filepath.Join(t.TempDir(), "stale-disjoint")
	staleConflict := filepath.Join(t.TempDir(), "stale-conflict")
	for _, dir := range []string{staleDisjoint, staleConflict} {
		gitWithAuth(t, token, "", "clone", gitURL, dir)
		runGit(t, "", "-C", dir, "config", "user.name", "Stale Pusher")
		runGit(t, "", "-C", dir, "config", "user.email", "stale@example.invalid")
	}

	// The native head moves after both clones were taken.
	submitWorkspaceFile(t, home, workspace, "shared.go", "package payment\nconst Shared = 2\n", "native shared update")

	writeWorkspaceFile(t, staleDisjoint, "acme/payment/own.go", "package payment\nconst Own = 2\n")
	runGit(t, "", "-C", staleDisjoint, "commit", "-am", "stale but disjoint")
	if _, stderr, err := runGitResult("", append(append([]string{"-C", staleDisjoint}, gitAuth(token)...), "push", "origin", "HEAD:refs/changes/new")...); err != nil {
		t.Fatalf("stale disjoint push failed: %v\n%s", err, stderr)
	}
	draft := singleDraftChangeset(t, ctx, changesets)
	runCLI(t, home, workspace, "cs", "submit", shortChangesetID(draft.Id))
	waitForSubmittedChangeset(t, ctx, changesets, draft.Id)

	check := filepath.Join(t.TempDir(), "check")
	gitWithAuth(t, token, "", "clone", gitURL, check)
	for rel, want := range map[string]string{
		"acme/payment/own.go":    "package payment\nconst Own = 2\n",
		"acme/payment/shared.go": "package payment\nconst Shared = 2\n",
	} {
		data, err := os.ReadFile(filepath.Join(check, rel))
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != want {
			t.Fatalf("%s = %q, want %q (a stale push must not revert newer native changes)", rel, data, want)
		}
	}

	writeWorkspaceFile(t, staleConflict, "acme/payment/shared.go", "package payment\nconst Shared = 3\n")
	runGit(t, "", "-C", staleConflict, "commit", "-am", "stale and conflicting")
	if _, stderr, err := runGitResult("", append(append([]string{"-C", staleConflict}, gitAuth(token)...), "push", "origin", "HEAD:refs/changes/new")...); err != nil {
		t.Fatalf("stale conflicting push was rejected at push time: %v\n%s", err, stderr)
	}
	conflicting := singleDraftChangeset(t, ctx, changesets)
	_, stderr := runCLIFails(t, home, workspace, "cs", "submit", shortChangesetID(conflicting.Id))
	if !strings.Contains(stderr, "FailedPrecondition") && !strings.Contains(strings.ToLower(stderr), "conflict") {
		t.Fatalf("expected the stale conflicting changeset to be rejected at submit, got:\n%s", stderr)
	}
}

// TestGitPushIntoEmptySlice pushes an unrelated root commit into a slice that
// has no history yet.
func TestGitPushIntoEmptySlice(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	token := readToken(t, home)
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	// A slice needs its path to exist; an empty directory leaves the projected
	// history empty because Git has no representation for it.
	runCLI(t, home, workspace, "fs", "mkdir", "/acme/fresh")
	if _, err := corev1.NewSliceServiceClient(conn).CreateSlice(ctx, &corev1.CreateSliceRequest{
		Ref:           &corev1.SliceRef{Account: "acme", Slice: "fresh"},
		IncludedPaths: []string{"/acme/fresh"},
		Visibility:    "private",
	}); err != nil {
		t.Fatal(err)
	}
	changesets := corev1.NewChangesetServiceClient(conn)

	gitURL := "http://" + ts.gitAddr + "/git/acme/fresh.git"
	if refs := strings.TrimSpace(gitWithAuth(t, token, "", "ls-remote", gitURL)); refs != "" {
		t.Fatalf("expected an empty projected repository, got refs:\n%s", refs)
	}

	local := t.TempDir()
	runGit(t, local, "init", "-q")
	runGit(t, local, "config", "user.name", "First Pusher")
	runGit(t, local, "config", "user.email", "first@example.invalid")
	writeWorkspaceFile(t, local, "acme/fresh/readme.md", "# fresh\n")
	runGit(t, local, "add", ".")
	runGit(t, local, "commit", "-qm", "initial import")
	if _, stderr, err := runGitResult("", append(append([]string{"-C", local}, gitAuth(token)...), "push", gitURL, "HEAD:refs/changes/new")...); err != nil {
		t.Fatalf("push into empty slice failed: %v\n%s", err, stderr)
	}
	drafts, err := changesets.ListChangesets(ctx, &corev1.ListChangesetsRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "fresh"},
		Status:         "draft",
		Limit:          10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(drafts.Changesets) != 1 {
		t.Fatalf("draft changesets in acme/fresh = %d, want 1", len(drafts.Changesets))
	}
	draft := drafts.Changesets[0]
	runCLI(t, home, workspace, "cs", "submit", shortChangesetID(draft.Id))
	waitForSubmittedChangeset(t, ctx, changesets, draft.Id)

	cloneDir := filepath.Join(t.TempDir(), "fresh")
	deadline := time.Now().Add(10 * time.Second)
	for {
		gitWithAuth(t, token, "", "clone", "-q", gitURL, cloneDir)
		if _, err := os.Stat(filepath.Join(cloneDir, "acme", "fresh", "readme.md")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pushed file never appeared in the projection")
		}
		_ = os.RemoveAll(cloneDir)
		time.Sleep(100 * time.Millisecond)
	}
	if got := strings.TrimSpace(runGit(t, cloneDir, "log", "-1", "--format=%s")); got != "initial import" {
		t.Fatalf("projected subject = %q, want %q", got, "initial import")
	}
}
