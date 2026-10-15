package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func writeWorkspaceFile(t *testing.T, workspace, rel, content string) {
	t.Helper()
	writeWorkspaceFileBytes(t, workspace, rel, []byte(content))
}

func writeWorkspaceFileBytes(t *testing.T, workspace, rel string, content []byte) {
	t.Helper()
	path := filepath.Join(workspace, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertWorkspaceFile(t *testing.T, workspace, rel, want string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(workspace, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("unexpected workspace file %s:\nwant:\n%s\ngot:\n%s", rel, want, string(got))
	}
}

func assertWorkspaceDir(t *testing.T, workspace, rel string) {
	t.Helper()
	info, err := os.Stat(filepath.Join(workspace, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatalf("workspace path %s is not a directory", rel)
	}
}

func createImportGitRepo(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.name", "Import Tester")
	runGit(t, repo, "config", "user.email", "importer@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "first commit")
	if err := os.MkdirAll(filepath.Join(repo, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "lib", "code.go"), []byte("package lib\nconst Value = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "lib/code.go")
	runGit(t, repo, "commit", "-m", "second commit")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "third commit")
	return repo
}

func cloneSlice(t *testing.T, ts *testServer, token string) string {
	t.Helper()
	cloneDir := filepath.Join(t.TempDir(), "payment")
	gitURL := "http://" + ts.gitAddr + "/git/acme/payment.git"
	runGit(t, "", "-c", "http.extraHeader=Authorization: Bearer "+token, "clone", gitURL, cloneDir)
	return cloneDir
}

func assertProjectedFile(t *testing.T, cloneDir, rel, want string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(cloneDir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("unexpected contents for %s:\nwant:\n%s\ngot:\n%s", rel, want, string(got))
	}
}

func assertConflictError(t *testing.T, stderr string) {
	t.Helper()
	if !strings.Contains(stderr, "FailedPrecondition") && !strings.Contains(stderr, "conflict") {
		t.Fatalf("expected conflict error, got stderr:\n%s", stderr)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func hasEntry(entries []*corev1.TreeEntry, name string, kind corev1.EntryKind) bool {
	for _, entry := range entries {
		if entry.Name == name && entry.Kind == kind {
			return true
		}
	}
	return false
}

func uploadTestFileCount(t *testing.T) int {
	t.Helper()
	const defaultFileCount = 256
	value := os.Getenv("GITSLICE_UPLOAD_TEST_FILES")
	if value == "" {
		return defaultFileCount
	}
	count, err := strconv.Atoi(value)
	if err != nil || count <= 0 {
		t.Fatalf("GITSLICE_UPLOAD_TEST_FILES must be a positive integer, got %q", value)
	}
	return count
}

func countRemoteTree(t *testing.T, ctx context.Context, repo corev1.RepositoryServiceClient, commitID, p string) (int, int) {
	t.Helper()
	files := 0
	dirs := 0
	cursor := ""
	for {
		list, err := repo.ListDirectory(ctx, &corev1.ListDirectoryRequest{CommitId: commitID, Path: p, PageSize: 1000, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range list.Entries {
			switch entry.Kind {
			case corev1.EntryKind_ENTRY_KIND_FILE:
				files++
			case corev1.EntryKind_ENTRY_KIND_DIRECTORY:
				dirs++
				childFiles, childDirs := countRemoteTree(t, ctx, repo, commitID, entry.Path)
				files += childFiles
				dirs += childDirs
			}
		}
		if list.NextCursor == "" {
			return files, dirs
		}
		if list.NextCursor == cursor {
			t.Fatalf("directory cursor did not advance for %s", p)
		}
		cursor = list.NextCursor
	}
}

func readToken(t *testing.T, home string) string {
	t.Helper()
	var cfg struct {
		Token string `json:"token"`
	}
	if err := readJSONFile(filepath.Join(home, ".gitslice", "config.json"), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Token == "" {
		t.Fatal("empty token in CLI config")
	}
	return cfg.Token
}

func assertWorkspaceMetadataDoesNotContainToken(t *testing.T, workspace, token string) {
	t.Helper()
	if token == "" {
		t.Fatal("test setup produced empty token")
	}
	for _, rel := range []string{".gs/slice.json", ".gs/state.json", ".gs/base_snapshot.json"} {
		if _, err := os.Stat(filepath.Join(workspace, rel)); err != nil {
			t.Fatalf("expected workspace metadata %s: %v", rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(workspace, ".gs", "config.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace must not store auth config in .gs/config.json; stat err=%v", err)
	}
	entries, err := os.ReadDir(filepath.Join(workspace, ".gs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(workspace, ".gs", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte(token)) {
			t.Fatalf("workspace metadata %s contains bearer token", entry.Name())
		}
	}
}

func readJSONFile(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}
