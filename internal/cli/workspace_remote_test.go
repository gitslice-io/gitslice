package cli

import (
	"context"
	"testing"

	"google.golang.org/grpc"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// countingRepo answers ListDirectory (recursively, or one level when
// oneLevel) and ResolvePath from a fixed tree, counting the calls.
type countingRepo struct {
	corev1.RepositoryServiceClient
	files    map[string]bool // path -> is a file (false: directory)
	oneLevel bool
	lists    int
	resolves int
}

func (c *countingRepo) entry(p string) *corev1.TreeEntry {
	kind := corev1.EntryKind_ENTRY_KIND_DIRECTORY
	if c.files[p] {
		kind = corev1.EntryKind_ENTRY_KIND_FILE
	}
	return &corev1.TreeEntry{Path: p, Kind: kind, ContentHash: "sha256:" + p, Mode: 0o100644}
}

func (c *countingRepo) ListDirectory(_ context.Context, req *corev1.ListDirectoryRequest, _ ...grpc.CallOption) (*corev1.ListDirectoryResponse, error) {
	c.lists++
	var out []*corev1.TreeEntry
	for _, p := range sortedKeys(c.files) {
		if len(p) <= len(req.Path)+1 || p[:len(req.Path)+1] != req.Path+"/" {
			continue
		}
		rest := p[len(req.Path)+1:]
		if c.oneLevel && containsSlash(rest) {
			continue
		}
		out = append(out, c.entry(p))
	}
	return &corev1.ListDirectoryResponse{Entries: out, Recursive: req.Recursive && !c.oneLevel}, nil
}

func (c *countingRepo) ResolvePath(_ context.Context, req *corev1.ResolvePathRequest, _ ...grpc.CallOption) (*corev1.ResolvePathResponse, error) {
	c.resolves++
	return &corev1.ResolvePathResponse{Entry: c.entry(req.Path)}, nil
}

func containsSlash(s string) bool {
	for _, r := range s {
		if r == '/' {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func TestRemoteWorkspaceFilesListsOnce(t *testing.T) {
	tree := map[string]bool{"/acme/app/src": false, "/acme/app/README.md": true, "/acme/app/src/a.go": true, "/acme/app/src/b.go": true}
	ws := WorkspaceConfig{Account: "acme", Slice: "app", IncludedPaths: []string{"/acme/app"}}
	for _, oneLevel := range []bool{false, true} {
		repo := &countingRepo{files: tree, oneLevel: oneLevel}
		got, err := Runner{}.remoteWorkspaceFiles(context.Background(), repo, ws, "c1")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 3 || got["/acme/app/src/b.go"].RelPath != "acme/app/src/b.go" || got["/acme/app/src/b.go"].ContentHash != "sha256:/acme/app/src/b.go" {
			t.Fatalf("oneLevel=%v: files = %+v", oneLevel, got)
		}
		if !oneLevel && (repo.lists != 1 || repo.resolves != 0) {
			t.Fatalf("recursive server: %d listings and %d resolves, want 1 and 0", repo.lists, repo.resolves)
		}
	}
}
