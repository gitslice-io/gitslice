package cli

import (
	"context"
	"net"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gitslice-io/gitslice/internal/checkexec"
	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestMaterializeCheckTreeFetchesNestedPrefix(t *testing.T) {
	repo := checkTreeRepoClient{
		account: "acme",
		inner: newFakeCheckRepo(map[string][]*corev1.TreeEntry{
			"/acme": {
				checkDirEntry("/acme/backend"),
			},
			"/acme/backend": {
				checkDirEntry("/acme/backend/shared"),
			},
			"/acme/backend/shared": {
				checkFileEntry("/acme/backend/shared/config.sh", 0o100755),
			},
		}, map[string][]byte{
			"/acme/backend/shared/config.sh": []byte("echo from tree\n"),
		}),
	}

	dest := t.TempDir()
	d := &agentDaemon{}
	if err := d.materializeCheckTree(context.Background(), repo, "tree-1", []string{"backend", "backend/shared"}, dest); err != nil {
		t.Fatalf("materializeCheckTree() error = %v", err)
	}

	target := filepath.Join(dest, "backend", "shared", "config.sh")
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "echo from tree\n" {
		t.Fatalf("materialized content = %q", data)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("materialized mode = %#o, want 0755", got)
	}
}

func TestCheckRunSpecMaterializedHostExecution(t *testing.T) {
	repo := checkTreeRepoClient{
		account: "acme",
		inner: newFakeCheckRepo(map[string][]*corev1.TreeEntry{
			"/acme": {
				checkDirEntry("/acme/backend"),
			},
			"/acme/backend": {
				checkFileEntry("/acme/backend/message.txt", 0o100644),
			},
		}, map[string][]byte{
			"/acme/backend/message.txt": []byte("hello from result tree\n"),
		}),
	}
	spec := &corev1.CheckRunSpec{
		RunId:            "run-1",
		Name:             "backend/cat",
		Command:          "cat backend/message.txt",
		MaterializePaths: []string{"backend/message.txt"},
	}

	dest := t.TempDir()
	d := &agentDaemon{}
	if err := d.materializeCheckTree(context.Background(), repo, "tree-1", spec.GetMaterializePaths(), dest); err != nil {
		t.Fatalf("materializeCheckTree() error = %v", err)
	}
	result, err := checkexec.Run(context.Background(), dest, checkSpecFromProto(spec))
	if err != nil {
		t.Fatalf("checkexec.Run() error = %v", err)
	}
	if result.Status != "passed" || result.ExitCode != 0 {
		t.Fatalf("result = %#v, want passed exit 0", result)
	}
	if !strings.Contains(result.Log, "hello from result tree") {
		t.Fatalf("result log = %q, want materialized file content", result.Log)
	}
}

func TestCheckRunSpecMapsSetup(t *testing.T) {
	spec := checkSpecFromProto(&corev1.CheckRunSpec{
		Setup:  []string{"true"},
		Cache:  []string{"/go/pkg/mod"},
		Memory: "8g",
		Cpus:   "4",
	})
	if got := spec.Setup; len(got) != 1 || got[0] != "true" {
		t.Fatalf("Setup = %#v, want [true]", got)
	}
	if got := spec.Cache; len(got) != 1 || got[0] != "/go/pkg/mod" {
		t.Fatalf("Cache = %#v, want [/go/pkg/mod]", got)
	}
	if spec.Memory != "8g" {
		t.Fatalf("Memory = %q, want 8g", spec.Memory)
	}
	if spec.CPUs != "4" {
		t.Fatalf("CPUs = %q, want 4", spec.CPUs)
	}
}

func TestHandleRunChecksDedupsRecentlyCompletedRunID(t *testing.T) {
	repo := newFakeCheckRepo(map[string][]*corev1.TreeEntry{"/": nil}, nil)
	addr, stop := startFakeCheckRepoServer(t, repo)
	defer stop()

	sendQueue := &agentSendQueue{
		ch:   make(chan *corev1.DaemonMessage, 16),
		done: make(chan struct{}),
	}
	d := &agentDaemon{
		cfg:       UserConfig{ServerAddr: addr},
		baseCtx:   context.Background(),
		sendQueue: sendQueue,
		checkRuns: map[string]context.CancelFunc{},
	}
	req := &corev1.RunChecks{
		ResultTreeId: "tree-1",
		ServerAddr:   addr,
		Checks: []*corev1.CheckRunSpec{{
			RunId:            "run-dup",
			Name:             "unit",
			Command:          "printf ran",
			MaterializePaths: []string{"/"},
		}},
	}

	d.handleRunChecks(req)
	d.handleRunChecks(req)

	terminal := 0
	for {
		select {
		case msg := <-sendQueue.ch:
			update := msg.GetCheckUpdate()
			if update != nil && update.GetRunId() == "run-dup" && update.GetFinal() {
				terminal++
			}
		default:
			if terminal != 1 {
				t.Fatalf("terminal updates for duplicate run_id = %d, want 1", terminal)
			}
			return
		}
	}
}

type fakeCheckRepo struct {
	dirs  map[string][]*corev1.TreeEntry
	files map[string][]byte
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
	data, ok := f.files[req.GetPath()]
	if !ok {
		return nil, status.Error(codes.NotFound, "file not found")
	}
	return &corev1.ReadFileResponse{Data: data}, nil
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
