package gitcompat

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/proto/core/v1"
)

func TestProjectionTargets(t *testing.T) {
	prefixes := []string{"/acme/backend", "/acme/payment/shared"}
	cases := []struct {
		name    string
		changed []string
		want    []string
	}{
		{"inside a prefix", []string{"/acme/backend/api/a.go"}, []string{"/acme/backend/api/a.go"}},
		{"ancestor of a prefix", []string{"/acme"}, []string{"/acme/backend", "/acme/payment/shared"}},
		{"unrelated", []string{"/acme/payment/b.go", "/zeta/x"}, []string{}},
		{"nested targets collapse", []string{"/acme/backend/api", "/acme/backend/api/a.go"}, []string{"/acme/backend/api"}},
		{"prefix itself", []string{"/acme/payment/shared/"}, []string{"/acme/payment/shared"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := projectionTargets(tc.changed, prefixes)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("projectionTargets(%q) = %q, want %q", tc.changed, got, tc.want)
			}
		})
	}
}

func TestApplyListing(t *testing.T) {
	files := map[string]projectedFile{
		"acme/payment/a.go":     {Mode: "100644", ContentHash: "ha", Blob: "blob-a"},
		"acme/payment/dir/b.go": {Mode: "100644", ContentHash: "hb", Blob: "blob-b"},
		"acme/payment/keep.go":  {Mode: "100644", ContentHash: "hk", Blob: "blob-k"},
	}
	changes := applyListing(files, []string{"/acme/payment/a.go", "/acme/payment/dir", "/acme/payment/new.sh"}, map[string][]storage.FileEntry{
		"/acme/payment/a.go":   {{Path: "/acme/payment/a.go", ContentHash: "ha2", Mode: 0o644, Size: 3}},
		"/acme/payment/dir":    nil,
		"/acme/payment/new.sh": {{Path: "/acme/payment/new.sh", ContentHash: "hn", Mode: 0o755, Size: 5}},
	})
	want := []fileChange{
		{Path: "acme/payment/a.go", Mode: "100644", ContentHash: "ha2", Size: 3},
		{Path: "acme/payment/dir/b.go", Delete: true},
		{Path: "acme/payment/new.sh", Mode: "100755", ContentHash: "hn", Size: 5},
	}
	if !reflect.DeepEqual(changes, want) {
		t.Fatalf("changes = %#v, want %#v", changes, want)
	}
	if _, ok := files["acme/payment/dir/b.go"]; ok {
		t.Fatal("deleted file still in the projected tree")
	}
	if files["acme/payment/keep.go"].Blob != "blob-k" {
		t.Fatal("untouched file lost its blob id")
	}
	if again := applyListing(files, []string{"/acme/payment/new.sh"}, map[string][]storage.FileEntry{
		"/acme/payment/new.sh": {{Path: "/acme/payment/new.sh", ContentHash: "hn", Mode: 0o755, Size: 5}},
	}); len(again) != 0 {
		t.Fatalf("re-listing identical content produced changes: %#v", again)
	}
}

func TestProjectedCommitMessage(t *testing.T) {
	got := projectedCommitMessage(&corev1.Commit{Id: "commit_123", Message: "Add thing\r\n\n"}, nil)
	if got != "Add thing\n\nGitslice-Commit: commit_123\n" {
		t.Fatalf("message = %q", got)
	}
	if got := projectedCommitMessage(&corev1.Commit{Id: "commit_abcdefghijklmnop"}, nil); !strings.HasPrefix(got, "Gitslice commit commit_abcde\n") {
		t.Fatalf("empty message fallback = %q", got)
	}
	imported := &storage.GitImportedCommitRecord{GitCommitID: "abc123", FullMessage: "Add a\n\nWhy a.\n"}
	if got := projectedCommitMessage(&corev1.Commit{Id: "commit_9", Message: "Add a"}, imported); got != "Add a\n\nWhy a.\n\nGit-Commit: abc123\nGitslice-Commit: commit_9\n" {
		t.Fatalf("imported message = %q", got)
	}
	if name, email, when := importedAuthor(&storage.GitImportedCommitRecord{AuthorName: "Ada", AuthorEmail: "ada@example.invalid", AuthoredAt: "2025-12-10T08:30:00Z"}, "nic", "nic@x", 5); name != "Ada" || email != "ada@example.invalid" || when != 1765355400 {
		t.Fatalf("importedAuthor = %q %q %d", name, email, when)
	}
	if name, _, when := importedAuthor(nil, "nic", "nic@x", 5); name != "nic" || when != 5 {
		t.Fatalf("importedAuthor without a record = %q %d", name, when)
	}
}

func TestFastImportPath(t *testing.T) {
	cases := map[string]string{
		"acme/a b.go":        "acme/a b.go",
		"acme/\"quoted\".go": `"acme/\"quoted\".go"`,
		"acme/back\\slash":   `"acme/back\\slash"`,
	}
	for in, want := range cases {
		if got := fastImportPath(in); got != want {
			t.Fatalf("fastImportPath(%q) = %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{".git/config", "acme/.GIT/x", "acme//x", ""} {
		if projectablePath(bad) {
			t.Fatalf("projectablePath(%q) = true", bad)
		}
	}
}

// TestRunFastImportDeterministic checks that commit ids depend only on their
// inputs: two repositories, and one batch versus two batches, agree.
func TestRunFastImportDeterministic(t *testing.T) {
	ctx := context.Background()
	usernames := map[string]string{"subj_a": "alice"}
	first := pendingCommit{
		native:  &corev1.Commit{Id: "commit_1", Author: "subj_a", Message: "one", CreatedAt: "2026-10-01T10:00:00.123Z"},
		changes: []fileChange{{Path: "acme/p/a.txt", Mode: "100644", ContentHash: "h1", Size: 2}},
	}
	second := pendingCommit{
		native: &corev1.Commit{Id: "commit_2", Author: "subj_unknown", Message: "two", CreatedAt: "2026-10-01T11:00:00Z"},
		changes: []fileChange{
			{Path: "acme/p/a.txt", Delete: true},
			{Path: "acme/p/b.sh", Mode: "100755", ContentHash: "h2", Size: 2},
		},
	}
	contents := map[string][]byte{"h1": []byte("a\n"), "h2": []byte("b\n")}

	newRepo := func() string {
		repo := t.TempDir()
		if err := runGit(ctx, "", nil, "init", "--bare", "--quiet", repo); err != nil {
			t.Fatal(err)
		}
		return repo
	}

	oneBatch := newRepo()
	shas, _, err := runFastImport(ctx, oneBatch, "", []pendingCommit{first, second}, contents, map[string]string{}, usernames)
	if err != nil {
		t.Fatal(err)
	}

	twoBatches := newRepo()
	firstShas, blobs, err := runFastImport(ctx, twoBatches, "", []pendingCommit{first}, map[string][]byte{"h1": contents["h1"]}, map[string]string{}, usernames)
	if err != nil {
		t.Fatal(err)
	}
	secondShas, _, err := runFastImport(ctx, twoBatches, firstShas[0], []pendingCommit{second}, map[string][]byte{"h2": contents["h2"]}, blobs, usernames)
	if err != nil {
		t.Fatal(err)
	}
	if shas[0] != firstShas[0] || shas[1] != secondShas[0] {
		t.Fatalf("one batch %v, two batches %v %v", shas, firstShas, secondShas)
	}

	author, err := gitOutput(ctx, oneBatch, nil, "log", "--format=%an <%ae> %at", projectedBranch)
	if err != nil {
		t.Fatal(err)
	}
	wantAuthors := "Gitslice <gitslice@users.noreply.gitslice.io> 1790852400\nalice <alice@users.noreply.gitslice.io> 1790848800\n"
	if author != wantAuthors {
		t.Fatalf("authors = %q, want %q", author, wantAuthors)
	}
	tree, err := gitOutput(ctx, oneBatch, nil, "ls-tree", "-r", projectedBranch)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tree, "100755 blob") || !strings.HasSuffix(strings.TrimSpace(tree), "acme/p/b.sh") || strings.Contains(tree, "a.txt") {
		t.Fatalf("unexpected projected tree:\n%s", tree)
	}
}

func TestGitFileMode(t *testing.T) {
	for mode, want := range map[uint32]string{
		0o100644: "100644",
		0o644:    "100644",
		0o100755: "100755",
		0o755:    "100755",
		0o120000: "120000",
		0o120777: "120000",
	} {
		if got := gitFileMode(mode); got != want {
			t.Fatalf("gitFileMode(%o) = %s, want %s", mode, got, want)
		}
	}
}
