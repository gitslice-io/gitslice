package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"gitslice.io/gitslice/internal/clientcache"
	"gitslice.io/gitslice/internal/objectid"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

type fakeTransferRepo struct {
	mu            sync.Mutex
	files         map[string][]byte
	readFilesRPCs int
	readFileRPCs  int
	batchSizes    []int
	failBatch     codes.Code
}

func (f *fakeTransferRepo) ReadFiles(_ context.Context, in *corev1.ReadFilesRequest, _ ...grpc.CallOption) (*corev1.ReadFilesResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.readFilesRPCs++
	f.batchSizes = append(f.batchSizes, len(in.GetPaths()))
	if f.failBatch != codes.OK {
		return nil, grpcstatus.Error(f.failBatch, "batch unavailable")
	}
	res := &corev1.ReadFilesResponse{}
	for _, p := range in.GetPaths() {
		res.Files = append(res.Files, &corev1.ReadFilesEntry{Path: p, Data: f.files[p]})
	}
	return res, nil
}

func (f *fakeTransferRepo) ReadFile(_ context.Context, in *corev1.ReadFileRequest, _ ...grpc.CallOption) (*corev1.ReadFileResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.readFileRPCs++
	data, ok := f.files[in.GetPath()]
	if !ok {
		return nil, grpcstatus.Error(codes.NotFound, "no such file")
	}
	return &corev1.ReadFileResponse{Data: data}, nil
}

func newTransferTestCache(t *testing.T) *clientcache.ObjectCache {
	t.Helper()
	cache, err := clientcache.New(filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	return cache
}

func transferTargets(n int) ([]remoteReadTarget, map[string][]byte) {
	files := map[string][]byte{}
	targets := make([]remoteReadTarget, 0, n)
	for i := 0; i < n; i++ {
		p := fmt.Sprintf("/acme/app/f%04d.txt", i)
		data := []byte(fmt.Sprintf("content-%d", i))
		files[p] = data
		targets = append(targets, remoteReadTarget{Path: p, ContentHash: objectid.RawContentHash(data), Size: int64(len(data))})
	}
	return targets, files
}

func TestTransferManagerFetchBatchesAndSkipsCached(t *testing.T) {
	cache := newTransferTestCache(t)
	targets, files := transferTargets(600)
	repo := &fakeTransferRepo{files: files}
	// One file is already cached and must not be fetched again.
	if _, err := cache.PutBytes(files[targets[0].Path]); err != nil {
		t.Fatal(err)
	}
	tm := NewTransferManager(repo, nil, nil, cache)
	res, err := tm.FetchFilesToCache(context.Background(), TransferFetchRequest{CommitID: "c1", Targets: targets, Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	// 599 uncached files in batches of at most hydrateBatchFiles (256).
	if repo.readFilesRPCs != 3 || repo.readFileRPCs != 0 {
		t.Fatalf("ReadFiles=%d ReadFile=%d (batch sizes %v), want 3 and 0", repo.readFilesRPCs, repo.readFileRPCs, repo.batchSizes)
	}
	if res.FetchedFiles != 599 {
		t.Fatalf("FetchedFiles = %d, want 599", res.FetchedFiles)
	}
	for _, target := range targets {
		if !cache.Exists(target.ContentHash) {
			t.Fatalf("%s not cached", target.Path)
		}
	}
	// Everything is cached now: a second fetch makes no RPCs.
	if _, err := tm.FetchFilesToCache(context.Background(), TransferFetchRequest{CommitID: "c1", Targets: targets}); err != nil {
		t.Fatal(err)
	}
	if repo.readFilesRPCs != 3 {
		t.Fatalf("second fetch made %d more ReadFiles calls", repo.readFilesRPCs-3)
	}
}

func TestTransferManagerFallsBackToSingleReadsAndReturnsUnhashedData(t *testing.T) {
	cache := newTransferTestCache(t)
	targets, files := transferTargets(3)
	unhashed := remoteReadTarget{Path: "/acme/app/unhashed.txt"}
	files[unhashed.Path] = []byte("no hash known up front")
	targets = append(targets, unhashed)
	repo := &fakeTransferRepo{files: files, failBatch: codes.Unimplemented}
	tm := NewTransferManager(repo, nil, nil, cache)
	res, err := tm.FetchFilesToCache(context.Background(), TransferFetchRequest{CommitID: "c1", Targets: targets})
	if err != nil {
		t.Fatal(err)
	}
	if repo.readFilesRPCs != 1 || repo.readFileRPCs != len(targets) {
		t.Fatalf("ReadFiles=%d ReadFile=%d, want 1 and %d", repo.readFilesRPCs, repo.readFileRPCs, len(targets))
	}
	if got := string(res.UnhashedData[unhashed.Path]); got != "no hash known up front" {
		t.Fatalf("unhashed data = %q", got)
	}
	for _, target := range targets[:3] {
		if !cache.Exists(target.ContentHash) {
			t.Fatalf("%s not cached by single-read fallback", target.Path)
		}
	}
}

func TestTransferManagerRejectsHashMismatch(t *testing.T) {
	cache := newTransferTestCache(t)
	targets, files := transferTargets(1)
	files[targets[0].Path] = []byte("tampered")
	tm := NewTransferManager(&fakeTransferRepo{files: files}, nil, nil, cache)
	if _, err := tm.FetchFilesToCache(context.Background(), TransferFetchRequest{CommitID: "c1", Targets: targets}); err == nil {
		t.Fatal("expected a content hash mismatch error")
	}
}

type fakeTransferBlob struct {
	corev1.BlobServiceClient
	mu          sync.Mutex
	statusCalls int
	uploadCalls int
	uploaded    map[string]int
	states      map[string]string
}

func (f *fakeTransferBlob) GetBlobStatus(_ context.Context, in *corev1.GetBlobStatusRequest, _ ...grpc.CallOption) (*corev1.GetBlobStatusResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statusCalls++
	res := &corev1.GetBlobStatusResponse{}
	for _, hash := range in.GetContentHashes() {
		if state, ok := f.states[hash]; ok {
			res.Blobs = append(res.Blobs, &corev1.BlobRecord{Id: "blob_" + hash[len(hash)-6:], ContentHash: hash, State: state})
		}
	}
	return res, nil
}

func (f *fakeTransferBlob) UploadBlobs(_ context.Context, in *corev1.UploadBlobsRequest, _ ...grpc.CallOption) (*corev1.UploadBlobsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.uploadCalls++
	res := &corev1.UploadBlobsResponse{}
	for _, blob := range in.GetBlobs() {
		f.uploaded[blob.GetContentHash()]++
		res.Blobs = append(res.Blobs, &corev1.UploadBlobResponse{BlobId: "up_" + blob.GetContentHash()[len(blob.GetContentHash())-6:], ContentHash: blob.GetContentHash(), Size: int64(len(blob.GetData()))})
	}
	return res, nil
}

func TestTransferManagerAttachAndUploadBlobsDedupsAndHonorsAvailableState(t *testing.T) {
	dataA, dataB, dataC := []byte("alpha"), []byte("beta"), []byte("gamma")
	hashA, hashB, hashC := objectid.RawContentHash(dataA), objectid.RawContentHash(dataB), objectid.RawContentHash(dataC)
	blob := &fakeTransferBlob{
		uploaded: map[string]int{},
		// A is available on the server; B exists but is not available yet, so
		// it must be uploaded; C is unknown.
		states: map[string]string{hashA: "available", hashB: "pending"},
	}
	edits := []*corev1.FileEdit{
		{Op: "upsert", Path: "/a", ContentHash: hashA},
		{Op: "upsert", Path: "/b1", ContentHash: hashB},
		{Op: "upsert", Path: "/b2", ContentHash: hashB},
		{Op: "upsert", Path: "/c", ContentHash: hashC},
		{Op: "delete", Path: "/gone"},
	}
	tm := NewTransferManager(nil, blob, &corev1.SliceRef{Account: "acme", Slice: "app"}, nil)
	stats, err := tm.AttachAndUploadBlobs(context.Background(), edits, BlobBytesSources(map[string][]byte{hashA: dataA, hashB: dataB, hashC: dataC}), nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if blob.uploadCalls != 1 || blob.uploaded[hashA] != 0 || blob.uploaded[hashB] != 1 || blob.uploaded[hashC] != 1 {
		t.Fatalf("uploadCalls=%d uploaded=%v, want one batch holding only B and C", blob.uploadCalls, blob.uploaded)
	}
	if stats.RequiredBlobs != 3 || stats.AvailableBlobs != 1 || stats.UploadedBlobs != 2 {
		t.Fatalf("stats = %+v", stats)
	}
	for _, edit := range edits[:4] {
		if edit.BlobId == "" {
			t.Fatalf("edit %s has no blob id", edit.Path)
		}
	}
	if edits[1].BlobId != edits[2].BlobId {
		t.Fatalf("edits with the same content got different blobs: %s vs %s", edits[1].BlobId, edits[2].BlobId)
	}
	if edits[4].BlobId != "" {
		t.Fatalf("delete edit got blob id %q", edits[4].BlobId)
	}
}
