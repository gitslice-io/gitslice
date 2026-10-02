package cli_test

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestSliceTagsInGitProjection covers native tags: creation, immutability,
// listing, and their publication as refs/tags/<name> in the projected
// repository. Semver tags are also published under the slice's path, for Go
// modules rooted in that subdirectory.
func TestSliceTagsInGitProjection(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	payment := t.TempDir()
	backend := t.TempDir()
	loginTestCLI(t, ts, home, payment)
	token := readToken(t, home)
	runCLI(t, home, payment, "workspace", "init", "acme/payment")
	runCLI(t, home, backend, "workspace", "init", "acme/backend")

	writeWorkspaceFile(t, payment, "one.go", "package payment\nconst One = 1\n")
	runCLI(t, home, payment, "cs", "create", "--title", "payment one")
	// Take the commit from the submit response. Slice history is indexed after
	// submit returns, so on a busy host `gs log` could still name the previous
	// commit; the tag then pointed outside the projection and was left out.
	paymentOne := submittedRefCommitID(t, runCLI(t, home, payment, "cs", "submit", "--json"))
	// The global head moves past the payment commit without touching the slice.
	submitWorkspaceFile(t, home, backend, "acme/backend/b.go", "package backend\n", "backend change")

	// Default commit is the head, which maps to the last payment commit.
	runCLI(t, home, payment, "tag", "create", "v1.0.0", "-m", "first release")
	runCLI(t, home, payment, "tag", "create", "release-1", "--commit", paymentOne)
	runCLI(t, home, payment, "tag", "create", "release-1", "--commit", paymentOne) // idempotent
	if _, stderr := runCLIFails(t, home, payment, "tag", "create", "v1.0.0", "--commit", paymentOne+"x"); !strings.Contains(stderr, "not found") {
		t.Fatalf("tagging an unknown commit should fail with not found:\n%s", stderr)
	}
	if _, stderr := runCLIFails(t, home, payment, "tag", "create", "bad..name"); !strings.Contains(stderr, "invalid") {
		t.Fatalf("invalid tag name should be rejected:\n%s", stderr)
	}

	submitWorkspaceFile(t, home, payment, "two.go", "package payment\nconst Two = 2\n", "payment two")
	if _, stderr := runCLIFails(t, home, payment, "tag", "create", "release-1"); !strings.Contains(stderr, "tag_exists") && !strings.Contains(stderr, "already") {
		t.Fatalf("moving an existing tag should fail:\n%s", stderr)
	}

	listed := runCLI(t, home, payment, "tag", "list", "--json")
	for _, want := range []string{`"v1.0.0"`, `"release-1"`, `"first release"`} {
		if !strings.Contains(listed, want) {
			t.Fatalf("tag list missing %s:\n%s", want, listed)
		}
	}

	cloneDir := filepath.Join(t.TempDir(), "payment")
	gitWithAuth(t, token, "", "clone", "http://"+ts.gitAddr+"/git/acme/payment.git", cloneDir)
	tags := strings.Fields(runGit(t, cloneDir, "tag", "-l"))
	want := map[string]bool{"v1.0.0": true, "release-1": true, "acme/payment/v1.0.0": true}
	if len(tags) != len(want) {
		t.Fatalf("projected tags = %q, want %v", tags, want)
	}
	for _, tag := range tags {
		if !want[tag] {
			t.Fatalf("unexpected projected tag %q in %q", tag, tags)
		}
	}
	paymentOneGit := strings.TrimSpace(runGit(t, cloneDir, "rev-parse", "HEAD^"))
	for _, tag := range []string{"v1.0.0", "release-1", "acme/payment/v1.0.0"} {
		if got := strings.TrimSpace(runGit(t, cloneDir, "rev-parse", tag+"^{commit}")); got != paymentOneGit {
			t.Fatalf("tag %s -> %s, want the payment one commit %s", tag, got, paymentOneGit)
		}
	}
	if subject := strings.TrimSpace(runGit(t, cloneDir, "log", "-1", "--format=%s", "v1.0.0")); subject != "payment one" {
		t.Fatalf("v1.0.0 points at %q, want payment one", subject)
	}
}
