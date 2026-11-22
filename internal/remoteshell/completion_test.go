package remoteshell

import (
	"bytes"
	"context"
	"strings"
	"testing"

	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestShellCompletionCompletesCommands(t *testing.T) {
	sh := New(Config{})
	got := sh.CompleteLine(context.Background(), "c")
	for _, want := range []string{"cat ", "cd "} {
		if !containsString(got, want) {
			t.Fatalf("command completions missing %q: %#v", want, got)
		}
	}
}

func TestShellCompletionCompletesRelativePaths(t *testing.T) {
	sh := New(Config{
		Repo: newFakeShellRepoClient(map[string][]*corev1.TreeEntry{
			"/file-user": {
				{Path: "/file-user/docs", Name: "docs", Kind: corev1.EntryKind_ENTRY_KIND_DIRECTORY},
				{Path: "/file-user/notes.txt", Name: "notes.txt", Kind: corev1.EntryKind_ENTRY_KIND_FILE},
			},
			"/file-user/docs": {
				{Path: "/file-user/docs/readme.md", Name: "readme.md", Kind: corev1.EntryKind_ENTRY_KIND_FILE},
			},
		}),
		Root:     "/file-user",
		Cwd:      "/file-user",
		CommitID: "commit_test",
		Scope:    "file-user/home",
		Scoped:   true,
	})
	if got := sh.CompleteLine(context.Background(), "cd d"); !containsString(got, "cd docs/") {
		t.Fatalf("cd path completions = %#v, want cd docs/", got)
	}
	if got := sh.CompleteLine(context.Background(), "cat n"); !containsString(got, "cat notes.txt") {
		t.Fatalf("cat path completions = %#v, want cat notes.txt", got)
	}
	if got := sh.CompleteLine(context.Background(), "write docs/readme.md h"); len(got) != 0 {
		t.Fatalf("write text argument should not complete paths: %#v", got)
	}
}

func TestShellCompletionUsesProjectionAncestors(t *testing.T) {
	sh := New(Config{
		Repo:       newFakeShellRepoClient(nil),
		Root:       "/",
		Cwd:        "/",
		CommitID:   "commit_test",
		Scope:      "nic4/new-slice",
		Scoped:     true,
		Projection: []string{"/nic4/tests"},
	})
	if got := sh.CompleteLine(context.Background(), "cd n"); !containsString(got, "cd nic4/") {
		t.Fatalf("root projection completions = %#v, want cd nic4/", got)
	}
	if got := sh.CompleteLine(context.Background(), "cd nic4/t"); !containsString(got, "cd nic4/tests/") {
		t.Fatalf("nested projection completions = %#v, want cd nic4/tests/", got)
	}
}

func TestShellExecBasicNavigation(t *testing.T) {
	var stdout, stderr bytes.Buffer
	sh := New(Config{
		Stdout: &stdout,
		Stderr: &stderr,
		Repo: newFakeShellRepoClient(map[string][]*corev1.TreeEntry{
			"/acme": {
				{Path: "/acme/docs", Name: "docs", Kind: corev1.EntryKind_ENTRY_KIND_DIRECTORY},
			},
		}),
		Root:     "/acme",
		Cwd:      "/acme",
		CommitID: "sha256:abcdef1234567890",
		Scope:    "acme:core",
		Scoped:   true,
	})
	if done, err := sh.Exec(context.Background(), "pwd"); err != nil || done {
		t.Fatalf("pwd = (%v, %v)", done, err)
	}
	if done, err := sh.Exec(context.Background(), "cd docs"); err != nil || done {
		t.Fatalf("cd docs = (%v, %v)", done, err)
	}
	if done, err := sh.Exec(context.Background(), "pwd"); err != nil || done {
		t.Fatalf("pwd after cd = (%v, %v)", done, err)
	}
	if done, err := sh.Exec(context.Background(), "exit"); err != nil || !done {
		t.Fatalf("exit = (%v, %v), want (true, nil)", done, err)
	}
	if got := stdout.String(); got != "/\n/docs\n" {
		t.Fatalf("stdout = %q, want %q", got, "/\n/docs\n")
	}
}

type fakeShellRepoClient struct {
	corev1.RepositoryServiceClient
	entries map[string]*corev1.TreeEntry
	dirs    map[string][]*corev1.TreeEntry
}

func newFakeShellRepoClient(dirs map[string][]*corev1.TreeEntry) *fakeShellRepoClient {
	entries := map[string]*corev1.TreeEntry{}
	for dir, children := range dirs {
		entries[dir] = &corev1.TreeEntry{
			Path: dir,
			Name: pathBaseForTest(dir),
			Kind: corev1.EntryKind_ENTRY_KIND_DIRECTORY,
		}
		for _, child := range children {
			if child != nil {
				entries[child.Path] = child
			}
		}
	}
	return &fakeShellRepoClient{entries: entries, dirs: dirs}
}

func (f *fakeShellRepoClient) ResolvePath(ctx context.Context, req *corev1.ResolvePathRequest, opts ...grpc.CallOption) (*corev1.ResolvePathResponse, error) {
	entry := f.entries[req.Path]
	if entry == nil {
		return nil, status.Error(codes.NotFound, "path not found")
	}
	return &corev1.ResolvePathResponse{Entry: entry}, nil
}

func (f *fakeShellRepoClient) ListDirectory(ctx context.Context, req *corev1.ListDirectoryRequest, opts ...grpc.CallOption) (*corev1.ListDirectoryResponse, error) {
	entries, ok := f.dirs[req.Path]
	if !ok {
		return nil, status.Error(codes.NotFound, "path not found")
	}
	return &corev1.ListDirectoryResponse{Entries: entries}, nil
}

func pathBaseForTest(p string) string {
	p = strings.TrimRight(p, "/")
	if p == "" {
		return "/"
	}
	if slash := strings.LastIndex(p, "/"); slash >= 0 {
		return p[slash+1:]
	}
	return p
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
