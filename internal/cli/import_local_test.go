package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"gitslice.io/gitslice/internal/objectid"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
)

type stubLocalImportServer struct {
	corev1.UnimplementedRepositoryServiceServer
	corev1.UnimplementedBlobServiceServer
	corev1.UnimplementedSliceServiceServer

	mu         sync.Mutex
	blobs      map[string]string // contentHash -> blobID
	completed  []*corev1.ImportedGitCommit
	batches    []*corev1.ImportGitCommitBatchRequest
	uploadCall int
	completedF bool
}

func (s *stubLocalImportServer) ResolveSlice(_ context.Context, req *corev1.ResolveSliceRequest) (*corev1.Slice, error) {
	return &corev1.Slice{
		Id:  "slice_home",
		Ref: req.GetRef(),
		Definition: &corev1.SliceDefinition{
			IncludedPaths: []string{"/" + req.GetRef().GetAccount()},
		},
	}, nil
}

func (s *stubLocalImportServer) GetBlobStatus(_ context.Context, req *corev1.GetBlobStatusRequest) (*corev1.GetBlobStatusResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*corev1.BlobRecord, 0, len(req.GetContentHashes()))
	for _, h := range req.GetContentHashes() {
		if id, ok := s.blobs[h]; ok {
			out = append(out, &corev1.BlobRecord{Id: id, ContentHash: h, State: "available"})
		} else {
			out = append(out, &corev1.BlobRecord{ContentHash: h, State: "missing"})
		}
	}
	return &corev1.GetBlobStatusResponse{Blobs: out}, nil
}

func (s *stubLocalImportServer) UploadBlobs(_ context.Context, req *corev1.UploadBlobsRequest) (*corev1.UploadBlobsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.uploadCall++
	out := make([]*corev1.UploadBlobResponse, 0, len(req.GetBlobs()))
	for _, b := range req.GetBlobs() {
		hash := objectid.RawContentHash(b.GetData())
		id := objectid.BlobID(b.GetData())
		s.blobs[hash] = id
		out = append(out, &corev1.UploadBlobResponse{BlobId: id, ContentHash: hash, Size: int64(len(b.GetData()))})
	}
	return &corev1.UploadBlobsResponse{Blobs: out}, nil
}

func (s *stubLocalImportServer) BeginGitImport(_ context.Context, req *corev1.BeginGitImportRequest) (*corev1.BeginGitImportResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res := &corev1.BeginGitImportResponse{
		ImportId:         "gimp_test",
		TargetRef:        req.GetTargetRef(),
		MountPath:        req.GetMountPath(),
		CompletedCommits: append([]*corev1.ImportedGitCommit(nil), s.completed...),
	}
	if len(s.completed) > 0 {
		res.FinalCommitId = s.completed[len(s.completed)-1].NativeCommitId
	}
	return res, nil
}

func (s *stubLocalImportServer) ImportGitCommitBatch(_ context.Context, req *corev1.ImportGitCommitBatchRequest) (*corev1.ImportGitCommitBatchResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.batches = append(s.batches, req)
	res := &corev1.ImportGitCommitBatchResponse{}
	for _, c := range req.GetCommits() {
		nativeID := fmt.Sprintf("sha256:native_%d", c.GetPosition())
		entry := &corev1.ImportedGitCommit{
			GitCommitId:    c.GetGitCommitId(),
			NativeCommitId: nativeID,
			Message:        c.GetMessage(),
		}
		s.completed = append(s.completed, entry)
		res.Commits = append(res.Commits, entry)
		res.FinalCommitId = nativeID
	}
	return res, nil
}

func (s *stubLocalImportServer) CompleteGitImport(_ context.Context, req *corev1.CompleteGitImportRequest) (*corev1.CompleteGitImportResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completedF = true
	return &corev1.CompleteGitImportResponse{FinalCommitId: req.GetFinalCommitId()}, nil
}

func TestLocalGitImportDeepBatchedAndResume(t *testing.T) {
	repo := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runGit("init", "-q")
	runGit("config", "user.name", "Local Author")
	runGit("config", "user.email", "local@example.invalid")

	write := func(rel, content string) {
		t.Helper()
		full := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.txt", "one\n")
	runGit("add", "-A")
	runGit("commit", "-q", "-m", "commit 1")

	write("b.txt", "two\n")
	runGit("add", "-A")
	runGit("commit", "-q", "-m", "commit 2")

	runGit("mv", "b.txt", "c.txt")
	runGit("commit", "-q", "-m", "commit 3 move")

	write("a.txt", "one-updated\n")
	if err := os.Remove(filepath.Join(repo, "c.txt")); err != nil {
		t.Fatal(err)
	}
	runGit("add", "-A")
	runGit("commit", "-q", "-m", "commit 4 update and delete")

	stub := &stubLocalImportServer{blobs: map[string]string{}}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	corev1.RegisterRepositoryServiceServer(srv, stub)
	corev1.RegisterBlobServiceServer(srv, stub)
	corev1.RegisterSliceServiceServer(srv, stub)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	home := t.TempDir()
	t.Setenv("HOME", home)
	cfgDir := filepath.Join(home, ".gitslice")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfgBytes, _ := json.Marshal(UserConfig{ServerAddr: lis.Addr().String(), Token: "tok_test"})
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), cfgBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	runner := Runner{Stdout: &stdout, Stderr: &stderr, Dir: home, Home: home}
	if err := runner.Run(context.Background(), []string{
		"import", repo,
		"--local", "--deep", "--batch-size", "2",
		"--mount", "/acme/imported",
		"--json",
	}); err != nil {
		t.Fatalf("gs import --local failed: %v\nstderr:\n%s", err, stderr.String())
	}

	var res corev1.ImportGitRepositoryResponse
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		t.Fatalf("decode json output: %v\n%s", err, stdout.String())
	}
	if len(res.Commits) != 4 || res.FinalCommitId != "sha256:native_4" {
		t.Fatalf("unexpected import response: %+v", &res)
	}
	if len(stub.batches) != 2 {
		t.Fatalf("got %d batch RPCs, want 2 (4 commits with batch-size 2)", len(stub.batches))
	}
	if !stub.completedF {
		t.Fatal("expected CompleteGitImport to be called")
	}
	if len(stub.blobs) != 3 {
		t.Fatalf("uploaded %d unique blobs, want 3 (commit 3 moved b.txt->c.txt with identical blob)", len(stub.blobs))
	}

	// Re-running with --resume skips all 4 commits without uploading or publishing again.
	prevBatches := len(stub.batches)
	prevUploads := stub.uploadCall
	stdout.Reset()
	stderr.Reset()
	if err := runner.Run(context.Background(), []string{
		"import", repo,
		"--local", "--deep", "--batch-size", "2",
		"--mount", "/acme/imported",
		"--json",
	}); err != nil {
		t.Fatalf("resumed gs import --local failed: %v", err)
	}
	if len(stub.batches) != prevBatches || stub.uploadCall != prevUploads {
		t.Fatalf("resumed run sent extra work: batches %d->%d, uploads %d->%d", prevBatches, len(stub.batches), prevUploads, stub.uploadCall)
	}
}
