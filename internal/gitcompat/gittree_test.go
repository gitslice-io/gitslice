package gitcompat

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/objectid"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// lazyHistory builds the commits for the given history the lazy way, writing
// each object into repo with git hash-object (the test does not need a pack).
func lazyHistory(t *testing.T, repo string, commits []pendingCommit, contents map[string][]byte, usernames map[string]string) []string {
	t.Helper()
	ctx := context.Background()
	files := map[string]projectedFile{}
	builder, err := newTreeBuilder(files)
	if err != nil {
		t.Fatal(err)
	}
	write := func(kind string, data []byte) string {
		cmd := exec.CommandContext(ctx, "git", "hash-object", "-w", "--literally", "-t", kind, "--stdin")
		cmd.Dir = repo
		cmd.Stdin = strings.NewReader(string(data))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("hash-object %s: %v\n%s", kind, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	var shas []string
	parent := ""
	for _, commit := range commits {
		for _, change := range commit.changes {
			blob := ""
			if !change.Delete {
				blob = objectid.GitBlobID(contents[change.ContentHash])
			}
			builder.apply(change, blob)
		}
		tree := builder.root()
		for _, o := range builder.takeObjects() {
			kind := map[byte]string{objCommit: "commit", objTree: "tree"}[o.kind]
			if got := write(kind, o.data); got != o.id {
				t.Fatalf("tree %s was written as %s", o.id, got)
			}
		}
		name, email := projectedIdentity(commit.native.Author, usernames)
		when := projectedTime(commit.native.CreatedAt)
		authorName, authorEmail, authored := importedAuthor(commit.imported, name, email, when)
		data := encodeCommit(tree, parent, authorName, authorEmail, authored, name, email, when, projectedCommitMessage(commit.native, commit.imported))
		parent = write("commit", data)
		shas = append(shas, parent)
	}
	return shas
}

// TestLazyObjectsMatchFastImport is the contract of the lazy projection: the
// trees and commits it computes from paths, modes and blob ids are the ones
// git fast-import writes after reading every file.
func TestLazyObjectsMatchFastImport(t *testing.T) {
	ctx := context.Background()
	contents := map[string][]byte{
		"sha256:01": []byte("alpha\n"),
		"sha256:02": []byte("beta\n"),
		"sha256:03": []byte("package main\n"),
		"sha256:04": []byte("beta, changed\n"),
		"sha256:05": []byte("../target"),
		"sha256:06": []byte(""),
		"sha256:07": []byte("unicode\n"),
	}
	native := func(id, author, when, msg string) *corev1.Commit {
		return &corev1.Commit{Id: id, Author: author, CreatedAt: when, Message: msg}
	}
	add := func(path, mode, hash string) fileChange {
		return fileChange{Path: path, Mode: mode, ContentHash: hash, Size: int64(len(contents[hash]))}
	}
	del := func(path string) fileChange { return fileChange{Path: path, Delete: true} }
	commits := []pendingCommit{
		{native: native("sha256:c1", "user_a", "2026-10-01T10:00:00Z", "first"), changes: []fileChange{
			add("acme/p/a.txt", "100644", "sha256:01"),
			add("acme/p/a/b.txt", "100644", "sha256:02"),
			add("acme/p/a/c/d.go", "100755", "sha256:03"),
			add("acme/p/link", "120000", "sha256:05"),
			add("acme/p/a-b.txt", "100644", "sha256:01"),
			add("acme/p/a.b/x", "100644", "sha256:06"),
		}},
		{native: native("sha256:c2", "user_b", "2026-10-01T11:00:00Z", "modify and add\n\nwith a body"), changes: []fileChange{
			add("acme/p/a/b.txt", "100644", "sha256:04"),
			add("acme/p/z z/file name.txt", "100644", "sha256:07"),
			add("acme/p/é/ü.txt", "100644", "sha256:07"),
		}},
		{native: native("sha256:c3", "user_a", "2026-10-02T09:30:00Z", "delete one file; its directory goes with it"), changes: []fileChange{
			del("acme/p/a/c/d.go"),
		}},
		{native: native("sha256:c4", "user_unknown", "not a time", ""), changes: []fileChange{
			del("acme/p/a/b.txt"), del("acme/p/a.b/x"),
			add("acme/q/other.txt", "100644", "sha256:02"),
		}},
		{native: native("sha256:c5", "user_a", "2026-10-03T00:00:00Z", "remove everything"), changes: []fileChange{
			del("acme/p/a.txt"), del("acme/p/link"), del("acme/p/a-b.txt"), del("acme/p/z z/file name.txt"), del("acme/p/é/ü.txt"), del("acme/q/other.txt"),
		}},
		{native: native("sha256:c6", "user_a", "2026-10-03T01:00:00Z", "and start again"), changes: []fileChange{
			add("acme/p/a.txt", "100644", "sha256:01"),
		}},
	}
	usernames := map[string]string{"user_a": "alice", "user_b": "bob"}

	eagerRepo := t.TempDir()
	lazyRepo := t.TempDir()
	for _, repo := range []string{eagerRepo, lazyRepo} {
		if err := runGit(ctx, "", nil, "init", "--bare", "--quiet", repo); err != nil {
			t.Fatal(err)
		}
	}
	var need []string
	for hash := range contents {
		need = append(need, hash)
	}
	fetch := func(_ context.Context, hash string) ([]byte, error) { return contents[hash], nil }
	eager, _, _, err := runFastImport(ctx, eagerRepo, "", commits, need, fetch, map[string]string{}, usernames)
	if err != nil {
		t.Fatal(err)
	}
	lazy := lazyHistory(t, lazyRepo, commits, contents, usernames)
	if len(eager) != len(lazy) {
		t.Fatalf("%d eager commits, %d lazy", len(eager), len(lazy))
	}
	for i := range eager {
		if eager[i] != lazy[i] {
			t.Fatalf("commit %d: fast-import wrote %s, the lazy build computed %s", i+1, eager[i], lazy[i])
		}
	}
}
