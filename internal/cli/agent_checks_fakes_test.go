package cli

import (
	"context"
	"net"
	"path"
	"sync"
	"testing"

	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeCheckRepo struct {
	dirs  map[string][]*corev1.TreeEntry
	files map[string][]byte

	mu               sync.Mutex
	activeLists      int
	activeReads      int
	maxActiveLists   int
	maxActiveReads   int
	listCalls        map[string]int
	readCalls        int
	listStarted      chan string
	listRelease      chan struct{}
	listBlockedPaths map[string]struct{}
	readStarted      chan struct{}
	readRelease      chan struct{}
}

func newFakeCheckRepo(dirs map[string][]*corev1.TreeEntry, files map[string][]byte) *fakeCheckRepo {
	return &fakeCheckRepo{dirs: dirs, files: files}
}

func (f *fakeCheckRepo) ListDirectory(ctx context.Context, req *corev1.ListDirectoryRequest, opts ...grpc.CallOption) (*corev1.ListDirectoryResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if req.GetRootTreeId() != "tree-1" {
		return nil, status.Errorf(codes.InvalidArgument, "root tree id = %q", req.GetRootTreeId())
	}
	if err := f.beginList(ctx, req.GetPath()); err != nil {
		return nil, err
	}
	defer f.endList()
	entries, ok := f.dirs[req.GetPath()]
	if !ok {
		return nil, status.Error(codes.NotFound, "directory not found")
	}
	return &corev1.ListDirectoryResponse{Entries: entries}, nil
}

func (f *fakeCheckRepo) ReadFile(ctx context.Context, req *corev1.ReadFileRequest, opts ...grpc.CallOption) (*corev1.ReadFileResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if req.GetRootTreeId() != "tree-1" {
		return nil, status.Errorf(codes.InvalidArgument, "root tree id = %q", req.GetRootTreeId())
	}
	f.mu.Lock()
	f.readCalls++
	f.mu.Unlock()
	if err := f.beginRead(ctx); err != nil {
		return nil, err
	}
	defer f.endRead()
	data, ok := f.files[req.GetPath()]
	if !ok {
		return nil, status.Error(codes.NotFound, "file not found")
	}
	return &corev1.ReadFileResponse{Data: data}, nil
}

func (f *fakeCheckRepo) beginRead(ctx context.Context) error {
	f.mu.Lock()
	f.activeReads++
	if f.activeReads > f.maxActiveReads {
		f.maxActiveReads = f.activeReads
	}
	started := f.readStarted
	release := f.readRelease
	f.mu.Unlock()

	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if release == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		f.endRead()
		return ctx.Err()
	case <-release:
		return nil
	}
}

func (f *fakeCheckRepo) beginList(ctx context.Context, dir string) error {
	f.mu.Lock()
	if f.listCalls == nil {
		f.listCalls = map[string]int{}
	}
	f.listCalls[dir]++
	f.activeLists++
	if f.activeLists > f.maxActiveLists {
		f.maxActiveLists = f.activeLists
	}
	started := f.listStarted
	release := f.listRelease
	_, blocked := f.listBlockedPaths[dir]
	f.mu.Unlock()

	if started != nil {
		select {
		case started <- dir:
		default:
		}
	}
	if release == nil || !blocked {
		return nil
	}
	select {
	case <-ctx.Done():
		f.endList()
		return ctx.Err()
	case <-release:
		return nil
	}
}

func (f *fakeCheckRepo) endList() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.activeLists--
}

func (f *fakeCheckRepo) endRead() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.activeReads--
}

func (f *fakeCheckRepo) maxConcurrentLists() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.maxActiveLists
}

func (f *fakeCheckRepo) maxConcurrentReads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.maxActiveReads
}

func (f *fakeCheckRepo) listCallCounts() map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]int, len(f.listCalls))
	for dir, calls := range f.listCalls {
		out[dir] = calls
	}
	return out
}

func (f *fakeCheckRepo) readCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.readCalls
}

func checkDirEntry(p string) *corev1.TreeEntry {
	return &corev1.TreeEntry{
		Path: p,
		Name: path.Base(p),
		Kind: corev1.EntryKind_ENTRY_KIND_DIRECTORY,
	}
}

func checkFileEntry(p string, mode uint32) *corev1.TreeEntry {
	return &corev1.TreeEntry{
		Path: p,
		Name: path.Base(p),
		Kind: corev1.EntryKind_ENTRY_KIND_FILE,
		Mode: mode,
	}
}

type fakeCheckRepoServer struct {
	corev1.UnimplementedRepositoryServiceServer
	repo *fakeCheckRepo
}

func startFakeCheckRepoServer(t *testing.T, repo *fakeCheckRepo) (string, func()) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	corev1.RegisterRepositoryServiceServer(srv, &fakeCheckRepoServer{repo: repo})
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve(lis)
	}()
	stop := func() {
		srv.Stop()
		_ = lis.Close()
		<-errCh
	}
	return lis.Addr().String(), stop
}

func (s *fakeCheckRepoServer) ListDirectory(ctx context.Context, req *corev1.ListDirectoryRequest) (*corev1.ListDirectoryResponse, error) {
	return s.repo.ListDirectory(ctx, req)
}

func (s *fakeCheckRepoServer) ReadFile(ctx context.Context, req *corev1.ReadFileRequest) (*corev1.ReadFileResponse, error) {
	return s.repo.ReadFile(ctx, req)
}
