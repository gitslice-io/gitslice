package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/clientcache"
	"gitslice.io/gitslice/internal/mcpserver"
	"gitslice.io/gitslice/internal/objectid"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	grpcstatus "google.golang.org/grpc/status"
)

type stubRepoAndBlobServer struct {
	corev1.UnimplementedRepositoryServiceServer
	corev1.UnimplementedBlobServiceServer
	corev1.UnimplementedSliceServiceServer

	mu              sync.Mutex
	files           map[string][]byte
	streamBlobs     map[string][]byte
	readFilesCalls  int
	readFileCalls   int
	streamReadCalls int
	uploadBlobsCall int
	uploadBlobCall  int
	streamUpCalls   int
}

func (s *stubRepoAndBlobServer) ReadFile(_ context.Context, req *corev1.ReadFileRequest) (*corev1.ReadFileResponse, error) {
	s.mu.Lock()
	s.readFileCalls++
	data, ok := s.files[req.GetPath()]
	s.mu.Unlock()
	if !ok {
		return nil, grpcstatus.Error(codes.NotFound, "not found")
	}
	return &corev1.ReadFileResponse{Data: data}, nil
}

func (s *stubRepoAndBlobServer) ReadFiles(_ context.Context, req *corev1.ReadFilesRequest) (*corev1.ReadFilesResponse, error) {
	s.mu.Lock()
	s.readFilesCalls++
	out := make([]*corev1.ReadFilesEntry, 0, len(req.GetPaths()))
	for _, p := range req.GetPaths() {
		data, ok := s.files[p]
		if !ok {
			s.mu.Unlock()
			return nil, grpcstatus.Error(codes.NotFound, "not found: "+p)
		}
		out = append(out, &corev1.ReadFilesEntry{Path: p, Data: data})
	}
	s.mu.Unlock()
	return &corev1.ReadFilesResponse{Files: out}, nil
}

func (s *stubRepoAndBlobServer) ReadBlobStream(req *corev1.ReadBlobStreamRequest, stream corev1.BlobService_ReadBlobStreamServer) error {
	s.mu.Lock()
	s.streamReadCalls++
	data, ok := s.streamBlobs[req.GetContentHash()]
	s.mu.Unlock()
	if !ok {
		return grpcstatus.Error(codes.NotFound, "blob not found")
	}
	const chunkSize = 1 << 20
	for start := 0; start < len(data); start += chunkSize {
		end := min(start+chunkSize, len(data))
		if err := stream.Send(&corev1.ReadBlobChunk{Data: data[start:end]}); err != nil {
			return err
		}
	}
	return nil
}

func (s *stubRepoAndBlobServer) GetBlobStatus(_ context.Context, req *corev1.GetBlobStatusRequest) (*corev1.GetBlobStatusResponse, error) {
	out := make([]*corev1.BlobRecord, 0, len(req.GetContentHashes()))
	for _, h := range req.GetContentHashes() {
		out = append(out, &corev1.BlobRecord{ContentHash: h, State: "missing"})
	}
	return &corev1.GetBlobStatusResponse{Blobs: out}, nil
}

func (s *stubRepoAndBlobServer) UploadBlobs(_ context.Context, req *corev1.UploadBlobsRequest) (*corev1.UploadBlobsResponse, error) {
	s.mu.Lock()
	s.uploadBlobsCall++
	s.mu.Unlock()
	out := make([]*corev1.UploadBlobResponse, len(req.GetBlobs()))
	for i, b := range req.GetBlobs() {
		out[i] = &corev1.UploadBlobResponse{
			BlobId:      objectid.BlobID(b.GetData()),
			ContentHash: objectid.RawContentHash(b.GetData()),
			Size:        int64(len(b.GetData())),
		}
	}
	return &corev1.UploadBlobsResponse{Blobs: out}, nil
}

func (s *stubRepoAndBlobServer) UploadBlob(_ context.Context, req *corev1.UploadBlobRequest) (*corev1.UploadBlobResponse, error) {
	s.mu.Lock()
	s.uploadBlobCall++
	s.mu.Unlock()
	return &corev1.UploadBlobResponse{
		BlobId:      objectid.BlobID(req.GetData()),
		ContentHash: objectid.RawContentHash(req.GetData()),
		Size:        int64(len(req.GetData())),
	}, nil
}

func (s *stubRepoAndBlobServer) UploadBlobStream(stream corev1.BlobService_UploadBlobStreamServer) error {
	s.mu.Lock()
	s.streamUpCalls++
	s.mu.Unlock()
	var buf bytes.Buffer
	for {
		chunk, err := stream.Recv()
		if err != nil {
			break
		}
		if d := chunk.GetData(); len(d) > 0 {
			buf.Write(d)
		}
	}
	data := buf.Bytes()
	return stream.SendAndClose(&corev1.UploadBlobResponse{
		BlobId:      objectid.BlobID(data),
		ContentHash: objectid.RawContentHash(data),
		Size:        int64(len(data)),
	})
}

func startStubGRPCServer(t *testing.T, svc *stubRepoAndBlobServer) *grpc.ClientConn {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	corev1.RegisterRepositoryServiceServer(srv, svc)
	corev1.RegisterBlobServiceServer(srv, svc)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.Dial(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestContentHashVerifiedInRemoteDiffAndMCP(t *testing.T) {
	svc := &stubRepoAndBlobServer{
		files: map[string][]byte{
			"/acme/app/a.txt": []byte("tampered server payload"),
		},
	}
	conn := startStubGRPCServer(t, svc)
	repo := corev1.NewRepositoryServiceClient(conn)
	blob := corev1.NewBlobServiceClient(conn)
	sliceRef := &corev1.SliceRef{Account: "acme", Slice: "app"}
	expectedHash := objectid.RawContentHash([]byte("expected genuine payload"))

	// 1. remoteFileBytes rejects mismatched content hash.
	cache1, err := clientcache.New(filepath.Join(t.TempDir(), "c1"))
	if err != nil {
		t.Fatal(err)
	}
	r := Runner{Home: t.TempDir()}
	if _, _, err := r.remoteFileBytes(context.Background(), repo, cache1, "c1", BaseSnapshotFile{
		Path:        "/acme/app/a.txt",
		ContentHash: expectedHash,
	}, true); err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("remoteFileBytes accepted tampered payload: %v", err)
	}

	// 2. workspaceBaseDiffFile rejects mismatched content hash.
	cache2, err := clientcache.New(filepath.Join(t.TempDir(), "c2"))
	if err != nil {
		t.Fatal(err)
	}
	base := BaseSnapshot{Files: map[string]BaseSnapshotFile{
		"/acme/app/a.txt": {Path: "/acme/app/a.txt", ContentHash: expectedHash},
	}}
	if _, err := r.workspaceBaseDiffFile(context.Background(), repo, blob, sliceRef, cache2, base, "c1", "/acme/app/a.txt"); err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("workspaceBaseDiffFile accepted tampered payload: %v", err)
	}

	// 3. mcpserver.Backend.Contents rejects mismatched content hash.
	cache3, err := clientcache.New(filepath.Join(t.TempDir(), "c3"))
	if err != nil {
		t.Fatal(err)
	}
	mcp := r.newMCPBackend(UserConfig{}, conn, cache3, "")
	if _, err := mcp.Contents(context.Background(), mcpserver.Slice{Ref: sliceRef, Included: []string{"/acme/app"}}, "c1", []*corev1.TreeEntry{
		{Path: "/acme/app/a.txt", ContentHash: expectedHash, Size: 24},
	}); err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("mcpBackend.Contents accepted tampered payload: %v", err)
	}
}

func TestAtomicJSONWritesTightenPermissionsAndCompactBaseSnapshot(t *testing.T) {
	dir := t.TempDir()

	// Pre-create sensitive files with world-readable 0644 permissions and verify atomic writes tighten them to 0600.
	cfgPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfgPath, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(cfgPath, map[string]string{"token": "secret"}, 0o600); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(cfgPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("writeJSONFile perm = %v, err=%v; want 0600", info.Mode().Perm(), err)
	}

	keyPath := filepath.Join(dir, "service-key.pem")
	if err := os.WriteFile(keyPath, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeServicePrivateKey(keyPath, []byte("PRIVATE KEY")); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(keyPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("writeServicePrivateKey perm = %v, err=%v; want 0600", info.Mode().Perm(), err)
	}

	pendingPath := filepath.Join(dir, "sub", "pending-agent-registration.json")
	if err := savePendingAgentRegistration(pendingPath, pendingAgentRegistration{Username: "bot1", Token: "reg_tok"}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(pendingPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("savePendingAgentRegistration perm = %v, err=%v; want 0600", info.Mode().Perm(), err)
	}
	if dirInfo, err := os.Stat(filepath.Dir(pendingPath)); err != nil || dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("pending registration parent dir perm = %v, want 0700", dirInfo.Mode().Perm())
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Fatalf("leftover temp file %s after atomic write", e.Name())
		}
	}

	// writeBaseSnapshot writes compact JSON (single line + trailing newline).
	wsDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wsDir, ".gs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, ".gs", "slice.json"), []byte(`{"account":"acme","slice":"app"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	r := Runner{Home: t.TempDir(), Dir: wsDir}
	if err := r.writeBaseSnapshot(BaseSnapshot{
		CommitID: "c1",
		Files: map[string]BaseSnapshotFile{
			"/acme/app/a.go": {Path: "/acme/app/a.go", RelPath: "acme/app/a.go", ContentHash: "h1", Mode: 0o100644, Size: 10},
		},
	}); err != nil {
		t.Fatal(err)
	}
	rawSnap, err := os.ReadFile(filepath.Join(wsDir, ".gs", "base_snapshot.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(strings.TrimSpace(string(rawSnap)), "\n") != 0 {
		t.Fatalf("expected compact single-line base_snapshot.json, got:\n%s", string(rawSnap))
	}
}

func TestValidateBrowserURLAndDynamicReservedAliases(t *testing.T) {
	for _, badURL := range []string{
		"",
		"javascript:alert(1)",
		"file:///etc/passwd",
		"-aCalculator",
		"--new-window",
		"http://-evil.example.com/path",
		"http:///no-host",
		"https://gitslice.io/path\ninjected",
	} {
		if _, err := validateBrowserURL(badURL); err == nil {
			t.Fatalf("validateBrowserURL(%q) unexpectedly succeeded", badURL)
		}
		if err := openBrowserURL(badURL); err == nil {
			t.Fatalf("openBrowserURL(%q) unexpectedly succeeded", badURL)
		}
	}
	for _, goodURL := range []string{
		"https://gitslice.io/slices/acme/app",
		"http://127.0.0.1:8082/cli-login?code=abc",
	} {
		if got, err := validateBrowserURL(goodURL); err != nil || got != goodURL {
			t.Fatalf("validateBrowserURL(%q) = %q, err=%v", goodURL, got, err)
		}
	}

	// Dynamic reservedAliasNames blocks hidden commands (mountd) and core subcommands/aliases.
	reserved := reservedAliasNames()
	for _, mustReserve := range []string{
		"mountd", "mount", "unmount", "submit", "create", "modify", "sync", "mcp", "agent", "upgrade", "cfg", "st", "ws",
	} {
		if !reserved[mustReserve] {
			t.Fatalf("expected %q to be reserved in reservedAliasNames()", mustReserve)
		}
	}

	home := t.TempDir()
	r := Runner{Home: home, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	if err := r.Run(context.Background(), []string{"alias", "set", "mountd", "version"}); err == nil {
		t.Fatal("gs alias set mountd unexpectedly succeeded")
	}

	// Even if config.json is manually poisoned with an alias for mountd or submit, expandAliasArgs refuses to expand it.
	if err := r.writeUserConfig(UserConfig{
		ServerAddr: "127.0.0.1:50051",
		Token:      "tok",
		Aliases: map[string]string{
			"mountd": "version",
			"submit": "version",
			"myver":  "version",
		},
	}); err != nil {
		t.Fatal(err)
	}
	gotMountd, err := r.expandAliasArgs([]string{"--insecure", "mountd", "--arg"})
	if err != nil || strings.Join(gotMountd, " ") != "--insecure mountd --arg" {
		t.Fatalf("expandAliasArgs expanded reserved mountd: %v (err=%v)", gotMountd, err)
	}
	gotCustom, err := r.expandAliasArgs([]string{"--insecure", "myver"})
	if err != nil || strings.Join(gotCustom, " ") != "--insecure version" {
		t.Fatalf("expandAliasArgs failed after --insecure flag: %v (err=%v)", gotCustom, err)
	}
}

func TestPerfStatCacheFallbackUploadAndBatchedRPCs(t *testing.T) {
	// 1. Stat-cache hit without object in clientcache still uploads by re-reading AbsPath on miss.
	wsDir := t.TempDir()
	home := t.TempDir()
	past := time.Now().Add(-time.Hour)
	if err := os.MkdirAll(filepath.Join(wsDir, ".gs"), 0o755); err != nil {
		t.Fatal(err)
	}
	ws := WorkspaceConfig{Account: "acme", Slice: "app", IncludedPaths: []string{"/acme/app"}}
	rawWS, _ := json.Marshal(ws)
	if err := os.WriteFile(filepath.Join(wsDir, ".gs", "slice.json"), rawWS, 0o644); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(wsDir, "acme", "app", "hello.txt")
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte("hello from disk fallback\n")
	if err := os.WriteFile(filePath, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filePath, past, past); err != nil {
		t.Fatal(err)
	}

	r := Runner{Home: home, Dir: wsDir}
	if _, err := r.scanWorkspaceFiles(ws); err != nil {
		t.Fatal(err)
	}
	// Wipe clientcache while keeping .gs/stat_cache.json intact!
	cache, err := r.objectCache()
	if err != nil {
		t.Fatal(err)
	}
	hash := objectid.RawContentHash(content)
	cacheObjPath, _ := cache.Path(hash)
	_ = os.Remove(cacheObjPath)
	if cache.Exists(hash) {
		t.Fatal("expected object to be absent from clientcache before fallback upload")
	}

	svc := &stubRepoAndBlobServer{
		files: map[string][]byte{
			"/acme/app/f1.txt": []byte("one"),
			"/acme/app/f2.txt": []byte("two"),
			"/acme/app/f3.txt": []byte("three"),
		},
		streamBlobs: map[string][]byte{},
	}
	largeData := bytes.Repeat([]byte("L"), int(blobStreamThresholdBytes)+128)
	largeHash := objectid.RawContentHash(largeData)
	svc.streamBlobs[largeHash] = largeData

	conn := startStubGRPCServer(t, svc)
	edits, _, err := r.snapshotEditsAgainstBase(context.Background(), conn, UserConfig{Token: "t"}, ws, BaseSnapshot{}, true)
	if err != nil {
		t.Fatalf("snapshotEditsAgainstBase upload after cache wipe failed: %v", err)
	}
	if len(edits) != 1 || edits[0].BlobId == "" {
		t.Fatalf("expected uploaded edit with BlobId, got %+v", edits)
	}
	if !cache.Exists(hash) {
		t.Fatal("expected fallback upload to repopulate clientcache from AbsPath")
	}

	// 2. prefetchRemoteFilesToCache batches small files into 1 ReadFiles RPC and streams large blobs via ReadBlobStream.
	prefetchCache, err := clientcache.New(filepath.Join(t.TempDir(), "pf"))
	if err != nil {
		t.Fatal(err)
	}
	targets := []remoteReadTarget{
		{Path: "/acme/app/f1.txt", ContentHash: objectid.RawContentHash([]byte("one")), Size: 3},
		{Path: "/acme/app/f2.txt", ContentHash: objectid.RawContentHash([]byte("two")), Size: 3},
		{Path: "/acme/app/f3.txt", ContentHash: objectid.RawContentHash([]byte("three")), Size: 5},
		{Path: "/acme/app/large.bin", ContentHash: largeHash, Size: int64(len(largeData))},
	}
	if _, err := prefetchRemoteFilesToCache(
		context.Background(),
		corev1.NewRepositoryServiceClient(conn),
		corev1.NewBlobServiceClient(conn),
		&corev1.SliceRef{Account: "acme", Slice: "app"},
		prefetchCache,
		"c1",
		targets,
	); err != nil {
		t.Fatalf("prefetchRemoteFilesToCache failed: %v", err)
	}
	if svc.readFilesCalls != 1 || svc.readFileCalls != 0 || svc.streamReadCalls != 1 {
		t.Fatalf("expected 1 ReadFiles + 0 ReadFile + 1 ReadBlobStream, got readFiles=%d readFile=%d stream=%d",
			svc.readFilesCalls, svc.readFileCalls, svc.streamReadCalls)
	}

	// 3. mcpserver.Backend.FileEdits batches multiple small writes into 1 UploadBlobs RPC and streams large writes.
	mcpCache, _ := clientcache.New(filepath.Join(t.TempDir(), "mcp"))
	mcp := r.newMCPBackend(UserConfig{}, conn, mcpCache, "")
	s1, s2, sLarge := "alpha", "beta", string(largeData)
	svc.uploadBlobsCall = 0
	svc.uploadBlobCall = 0
	svc.streamUpCalls = 0
	mcpEdits, err := mcp.FileEdits(context.Background(), mcpserver.Slice{
		Ref:      &corev1.SliceRef{Account: "acme", Slice: "app"},
		Included: []string{"/acme/app"},
	}, []mcpserver.EditSpec{
		{Op: "write", Path: "a.txt", Content: &s1},
		{Op: "write", Path: "b.txt", Content: &s2},
		{Op: "write", Path: "big.bin", Content: &sLarge},
	})
	if err != nil {
		t.Fatalf("mcp.fileEdits failed: %v", err)
	}
	if len(mcpEdits) != 3 || svc.uploadBlobsCall != 1 || svc.uploadBlobCall != 0 || svc.streamUpCalls != 1 {
		t.Fatalf("expected 1 batched UploadBlobs + 0 unary UploadBlob + 1 UploadBlobStream, got batch=%d unary=%d stream=%d",
			svc.uploadBlobsCall, svc.uploadBlobCall, svc.streamUpCalls)
	}
}
