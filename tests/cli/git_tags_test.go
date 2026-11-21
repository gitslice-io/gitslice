package cli_test

import (
	"encoding/json"
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

// TestHomeSliceAggregatesTags verifies that an account's home slice aggregates
// tags from the account's other slices in `gs tag list` (unnamespaced,
// deduplicated by tag name) and in the home slice's Git projection (namespaced
// as <slice>/<tag> for sub-slice tags alongside home's own tags).
func TestHomeSliceAggregatesTags(t *testing.T) {
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
	paymentOne := submittedRefCommitID(t, runCLI(t, home, payment, "cs", "submit", "--json"))

	writeWorkspaceFile(t, backend, "b.go", "package backend\nconst B = 1\n")
	runCLI(t, home, backend, "cs", "create", "--title", "backend one")
	backendOne := submittedRefCommitID(t, runCLI(t, home, backend, "cs", "submit", "--json"))

	// Two slices tag the same commit with different names.
	runCLI(t, home, payment, "tag", "create", "pay-snap", "--commit", paymentOne)
	runCLI(t, home, backend, "tag", "create", "back-snap", "--commit", paymentOne)

	// Two slices reuse the same tag name on different commits.
	runCLI(t, home, payment, "tag", "create", "v1.0.0", "--commit", paymentOne)
	runCLI(t, home, backend, "tag", "create", "v1.0.0", "--commit", backendOne)

	// Home creates its own tag.
	runCLI(t, home, payment, "tag", "create", "home-v1", "--slice", "acme/home", "--commit", backendOne)

	listed := runCLI(t, home, payment, "tag", "list", "--slice", "acme/home", "--json")
	var payload struct {
		Tags []struct {
			Slice    string `json:"slice"`
			Name     string `json:"name"`
			CommitID string `json:"commit_id"`
		} `json:"tags"`
	}
	if err := json.Unmarshal([]byte(listed), &payload); err != nil {
		t.Fatalf("unmarshal tag list: %v\n%s", err, listed)
	}
	gotByName := map[string]struct{ slice, commit string }{}
	for _, tag := range payload.Tags {
		if _, dup := gotByName[tag.Name]; dup {
			t.Fatalf("duplicate tag %q in home tag list:\n%s", tag.Name, listed)
		}
		gotByName[tag.Name] = struct{ slice, commit string }{tag.Slice, tag.CommitID}
	}
	for _, name := range []string{"pay-snap", "back-snap", "v1.0.0", "home-v1"} {
		if _, ok := gotByName[name]; !ok {
			t.Fatalf("home tag list missing %q:\n%s", name, listed)
		}
	}
	if gotByName["pay-snap"].commit != paymentOne || gotByName["back-snap"].commit != paymentOne {
		t.Fatalf("same-commit tags = %+v, want both at %s", gotByName, paymentOne)
	}
	if gotByName["v1.0.0"].slice != "acme/backend" || gotByName["v1.0.0"].commit != backendOne {
		t.Fatalf("deduplicated v1.0.0 = %+v, want acme/backend@%s", gotByName["v1.0.0"], backendOne)
	}
	if gotByName["home-v1"].slice != "acme/home" || gotByName["home-v1"].commit != backendOne {
		t.Fatalf("home-v1 = %+v, want acme/home@%s", gotByName["home-v1"], backendOne)
	}

	// Plain-text `gs tag list` on home also includes each tag's originating slice.
	plainHome := runCLI(t, home, payment, "tag", "list", "--slice", "acme/home")
	for _, want := range []string{"pay-snap\tacme/payment\t", "back-snap\tacme/backend\t", "home-v1\tacme/home\t"} {
		if !strings.Contains(plainHome, want) {
			t.Fatalf("home plain tag list missing %q:\n%s", want, plainHome)
		}
	}

	// Git projection of acme/home publishes home's own tags directly and
	// sub-slice tags namespaced as <slice>/<tag>.
	cloneDir := filepath.Join(t.TempDir(), "home-git")
	gitWithAuth(t, token, "", "clone", "http://"+ts.gitAddr+"/git/acme/home.git", cloneDir)
	gitTags := strings.Fields(runGit(t, cloneDir, "tag", "-l"))
	wantGitTags := map[string]bool{
		"home-v1":           true,
		"payment/pay-snap":  true,
		"payment/v1.0.0":    true,
		"backend/back-snap": true,
		"backend/v1.0.0":    true,
	}
	if len(gitTags) != len(wantGitTags) {
		t.Fatalf("projected home tags = %q, want %v", gitTags, wantGitTags)
	}
	for _, tag := range gitTags {
		if !wantGitTags[tag] {
			t.Fatalf("unexpected projected home tag %q in %q", tag, gitTags)
		}
	}
	if subject := strings.TrimSpace(runGit(t, cloneDir, "log", "-1", "--format=%s", "payment/v1.0.0")); subject != "payment one" {
		t.Fatalf("payment/v1.0.0 points at %q, want payment one", subject)
	}
	if subject := strings.TrimSpace(runGit(t, cloneDir, "log", "-1", "--format=%s", "backend/v1.0.0")); subject != "backend one" {
		t.Fatalf("backend/v1.0.0 points at %q, want backend one", subject)
	}
	if subject := strings.TrimSpace(runGit(t, cloneDir, "log", "-1", "--format=%s", "home-v1")); subject != "backend one" {
		t.Fatalf("home-v1 points at %q, want backend one", subject)
	}
}
