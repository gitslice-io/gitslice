package service

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gitslice-io/gitslice/internal/postgres"
	"github.com/gitslice-io/gitslice/proto/core/v1"
)

func TestGitSnapshotReadsTreeWithBatchBlobContents(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	runGitTest(t, repo, "init")
	runGitTest(t, repo, "config", "user.name", "Snapshot Tester")
	runGitTest(t, repo, "config", "user.email", "snapshot@example.invalid")
	if err := os.MkdirAll(filepath.Join(repo, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "lib", "code.go"), []byte("package lib\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repo, "add", ".")
	runGitTest(t, repo, "commit", "-m", "initial")
	head := strings.TrimSpace(runGitTest(t, repo, "rev-parse", "HEAD"))

	snapshot, err := gitSnapshot(ctx, repo, head, "/acme/payment/vendor/repo")
	if err != nil {
		t.Fatal(err)
	}
	readme := snapshot["/acme/payment/vendor/repo/README.md"]
	if string(readme.Data) != "hello\n" || readme.ContentHash == "" || readme.BlobID == "" {
		t.Fatalf("unexpected README snapshot: %#v", readme)
	}
	code := snapshot["/acme/payment/vendor/repo/lib/code.go"]
	if string(code.Data) != "package lib\n" || code.ContentHash == "" || code.BlobID == "" {
		t.Fatalf("unexpected code snapshot: %#v", code)
	}
}

func TestGitDeltaEditsReadsOnlyChangedPaths(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	runGitTest(t, repo, "init")
	runGitTest(t, repo, "config", "user.name", "Delta Tester")
	runGitTest(t, repo, "config", "user.email", "delta@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte("package main\nconst A = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "b.go"), []byte("package main\nconst B = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repo, "add", ".")
	runGitTest(t, repo, "commit", "-m", "first")
	first := strings.TrimSpace(runGitTest(t, repo, "rev-parse", "HEAD"))

	if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte("package main\nconst A = 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(repo, "b.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "c.go"), []byte("package main\nconst C = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repo, "add", "-A")
	runGitTest(t, repo, "commit", "-m", "second")
	second := strings.TrimSpace(runGitTest(t, repo, "rev-parse", "HEAD"))

	edits, snapshot, err := gitDeltaEdits(ctx, repo, first, second, "/acme/payment/imported")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, edit := range edits {
		got[edit.Path] = edit.Op
	}
	want := map[string]string{
		"/acme/payment/imported/a.go": "upsert",
		"/acme/payment/imported/b.go": "delete",
		"/acme/payment/imported/c.go": "upsert",
	}
	if len(got) != len(want) {
		t.Fatalf("edits = %#v, want %#v", got, want)
	}
	for path, op := range want {
		if got[path] != op {
			t.Fatalf("edit %s = %q, want %q; all edits %#v", path, got[path], op, got)
		}
	}
	if len(snapshot) != 2 {
		t.Fatalf("snapshot has %d changed file(s), want 2", len(snapshot))
	}
	if string(snapshot["/acme/payment/imported/a.go"].Data) != "package main\nconst A = 2\n" {
		t.Fatalf("unexpected a.go data: %q", string(snapshot["/acme/payment/imported/a.go"].Data))
	}
}

func TestImmediateDirectoryEntriesFiltersFilesOutsidePrefix(t *testing.T) {
	files := []postgres.FileEntry{
		{Path: "/acme/backend/server.go", Mode: 0o100644},
		{Path: "/acme/payment/shared/code.go", Mode: 0o100644},
	}

	assertImmediateEntryNames(t, immediateDirectoryEntries("/acme/payment", files), "shared")
	assertImmediateEntryNames(t, immediateDirectoryEntries("/acme", files), "backend", "payment")
	assertImmediateEntryNames(t, immediateDirectoryEntries("/", files), "acme")
}

func runGitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
	}
	return string(out)
}

func assertImmediateEntryNames(t *testing.T, entries []*corev1.TreeEntry, want ...string) {
	t.Helper()
	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		got = append(got, entry.Name)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("entry names = %#v, want %#v", got, want)
	}
}
