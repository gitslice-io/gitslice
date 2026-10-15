package cli

import (
	"context"
	"strings"
	"testing"

	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestServerShellCompletionCompletesCommands(t *testing.T) {
	sh := &serverShell{}
	got := sh.completeLine(context.Background(), "c")
	for _, want := range []string{"cat ", "cd "} {
		if !stringSliceContains(got, want) {
			t.Fatalf("command completions missing %q: %#v", want, got)
		}
	}
}

func TestServerShellCompletionCompletesRelativePaths(t *testing.T) {
	sh := &serverShell{
		repo: newFakeShellRepoClient(map[string][]*corev1.TreeEntry{
			"/file-user": {
				{Path: "/file-user/docs", Name: "docs", Kind: corev1.EntryKind_ENTRY_KIND_DIRECTORY},
				{Path: "/file-user/notes.txt", Name: "notes.txt", Kind: corev1.EntryKind_ENTRY_KIND_FILE},
			},
			"/file-user/docs": {
				{Path: "/file-user/docs/readme.md", Name: "readme.md", Kind: corev1.EntryKind_ENTRY_KIND_FILE},
			},
		}),
		root:     "/file-user",
		cwd:      "/file-user",
		commitID: "commit_test",
		scope:    "file-user/home",
		scoped:   true,
	}
	if got := sh.completeLine(context.Background(), "cd d"); !stringSliceContains(got, "cd docs/") {
		t.Fatalf("cd path completions = %#v, want cd docs/", got)
	}
	if got := sh.completeLine(context.Background(), "cat n"); !stringSliceContains(got, "cat notes.txt") {
		t.Fatalf("cat path completions = %#v, want cat notes.txt", got)
	}
	if got := sh.completeLine(context.Background(), "write docs/readme.md h"); len(got) != 0 {
		t.Fatalf("write text argument should not complete paths: %#v", got)
	}
}

func TestServerShellCompletionUsesProjectionAncestors(t *testing.T) {
	sh := &serverShell{
		repo:       newFakeShellRepoClient(nil),
		root:       "/",
		cwd:        "/",
		commitID:   "commit_test",
		scope:      "nic4/new-slice",
		scoped:     true,
		projection: []string{"/nic4/tests"},
	}
	if got := sh.completeLine(context.Background(), "cd n"); !stringSliceContains(got, "cd nic4/") {
		t.Fatalf("root projection completions = %#v, want cd nic4/", got)
	}
	if got := sh.completeLine(context.Background(), "cd nic4/t"); !stringSliceContains(got, "cd nic4/tests/") {
		t.Fatalf("nested projection completions = %#v, want cd nic4/tests/", got)
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
