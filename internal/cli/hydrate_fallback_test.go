package cli

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"gitslice.io/gitslice/internal/clientcache"
	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// oneLevelRepoServer is a repository server older than recursive listing and
// ReadFiles: it lists one level whatever is asked, and reads a file per call.
type oneLevelRepoServer struct {
	corev1.UnimplementedRepositoryServiceServer
	files map[string][]byte

	mu    sync.Mutex
	lists int
	reads int
}

func (s *oneLevelRepoServer) ListDirectory(_ context.Context, req *corev1.ListDirectoryRequest) (*corev1.ListDirectoryResponse, error) {
	s.mu.Lock()
	s.lists++
	s.mu.Unlock()
	prefix := strings.TrimSuffix(req.GetPath(), "/") + "/"
	seen := map[string]bool{}
	var entries []*corev1.TreeEntry
	for p, data := range s.files {
		rest, ok := strings.CutPrefix(p, prefix)
		if !ok {
			continue
		}
		name, _, nested := strings.Cut(rest, "/")
		child := prefix + name
		if seen[child] {
			continue
		}
		seen[child] = true
		if nested {
			entries = append(entries, &corev1.TreeEntry{Path: child, Name: name, Kind: corev1.EntryKind_ENTRY_KIND_DIRECTORY})
			continue
		}
		entries = append(entries, &corev1.TreeEntry{Path: child, Name: name, Kind: corev1.EntryKind_ENTRY_KIND_FILE, Mode: 0o100644, ContentHash: objectid.RawContentHash(data), Size: int64(len(data))})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return &corev1.ListDirectoryResponse{Entries: entries}, nil
}

func (s *oneLevelRepoServer) ReadFile(_ context.Context, req *corev1.ReadFileRequest) (*corev1.ReadFileResponse, error) {
	s.mu.Lock()
	s.reads++
	s.mu.Unlock()
	return &corev1.ReadFileResponse{Data: s.files[req.GetPath()]}, nil
}

// TestHydrateFromServerWithoutBatchCalls: against a server without recursive
// listing or ReadFiles, a checkout walks the directories and reads the files
// one by one, and still writes every file.
func TestHydrateFromServerWithoutBatchCalls(t *testing.T) {
	files := map[string][]byte{
		"/acme/payment/a.go":             []byte("package payment\n"),
		"/acme/payment/src/b.go":         []byte("package src\n"),
		"/acme/payment/src/deep/c.go":    []byte("package deep\n"),
		"/acme/payment/src/deep/copy.go": []byte("package src\n"),
	}
	server := &oneLevelRepoServer{files: files}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	corev1.RegisterRepositoryServiceServer(srv, server)
	go srv.Serve(lis)
	defer srv.Stop()
	conn, err := grpc.Dial(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	cache, err := clientcache.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	h := &workspaceHydrator{
		root:     t.TempDir(),
		repo:     corev1.NewRepositoryServiceClient(conn),
		cache:    cache,
		ws:       WorkspaceConfig{Account: "acme", Slice: "payment", IncludedPaths: []string{"/acme/payment"}},
		commitID: "commit-1",
		base:     BaseSnapshot{Files: map[string]BaseSnapshotFile{}},
	}
	ctx := context.Background()
	if err := h.hydrateDirectory(ctx, "/acme/payment"); err != nil {
		t.Fatal(err)
	}
	if err := h.materialize(ctx); err != nil {
		t.Fatal(err)
	}
	for p, want := range files {
		rel, err := workspaceRelPath(h.ws, p)
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(h.root, filepath.FromSlash(rel)))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s = %q, %v; want %q", p, got, err, want)
		}
	}
	// Three directories walked; identical content read once.
	if server.lists != 3 || server.reads != 3 {
		t.Fatalf("lists=%d reads=%d, want 3 and 3", server.lists, server.reads)
	}
}
