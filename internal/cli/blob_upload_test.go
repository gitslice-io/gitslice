package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/clientcache"
	"gitslice.io/gitslice/internal/objectid"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
)

func TestAttachBlobIDsReusesServerBlobStatus(t *testing.T) {
	cache, err := clientcache.New(filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("package payment\n")
	cached, err := cache.PutBytes(content)
	if err != nil {
		t.Fatal(err)
	}
	client := &fakeBlobClient{
		status: map[string]*corev1.BlobRecord{
			cached.ContentHash: {Id: "blob_existing", ContentHash: cached.ContentHash, State: "available"},
		},
	}
	edits := []*corev1.FileEdit{
		{Op: "upsert", Path: "/acme/payment/a.go", ContentHash: cached.ContentHash},
		{Op: "upsert", Path: "/acme/payment/b.go", ContentHash: cached.ContentHash},
	}

	if err := attachBlobIDs(context.Background(), client, &corev1.SliceRef{Account: "acme", Slice: "payment"}, cache, edits); err != nil {
		t.Fatal(err)
	}
	if uploads, _, _ := client.uploadSnapshot(); uploads != 0 {
		t.Fatalf("expected no uploads, got %d", uploads)
	}
	for _, edit := range edits {
		if edit.BlobId != "blob_existing" {
			t.Fatalf("expected existing blob id, got %q", edit.BlobId)
		}
	}
}

func TestAttachBlobIDsUploadsEachMissingHashOnceFromCache(t *testing.T) {
	cache, err := clientcache.New(filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("package payment\nconst Created = true\n")
	cached, err := cache.PutBytes(content)
	if err != nil {
		t.Fatal(err)
	}
	client := &fakeBlobClient{status: map[string]*corev1.BlobRecord{}}
	edits := []*corev1.FileEdit{
		{Op: "upsert", Path: "/acme/payment/a.go", ContentHash: cached.ContentHash},
		{Op: "upsert", Path: "/acme/payment/b.go", ContentHash: cached.ContentHash},
	}

	if err := attachBlobIDs(context.Background(), client, &corev1.SliceRef{Account: "acme", Slice: "payment"}, cache, edits); err != nil {
		t.Fatal(err)
	}
	if uploads, _, _ := client.uploadSnapshot(); uploads != 1 {
		t.Fatalf("expected one upload, got %d", uploads)
	}
	wantBlobID := objectid.BlobID(content)
	for _, edit := range edits {
		if edit.BlobId != wantBlobID {
			t.Fatalf("expected uploaded blob id %q, got %q", wantBlobID, edit.BlobId)
		}
	}
}

func TestAttachBlobIDsUploadsSmallBlobsInBatches(t *testing.T) {
	cache, err := clientcache.New(filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	files := uploadBatchBlobs + 10
	edits := make([]*corev1.FileEdit, 0, files)
	for i := 0; i < files; i++ {
		cached, err := cache.PutBytes([]byte(fmt.Sprintf("content-%d", i)))
		if err != nil {
			t.Fatal(err)
		}
		edits = append(edits, &corev1.FileEdit{Op: "upsert", Path: fmt.Sprintf("/acme/payment/%d.txt", i), ContentHash: cached.ContentHash})
	}
	client := &fakeBlobClient{status: map[string]*corev1.BlobRecord{}}
	if err := attachBlobIDs(context.Background(), client, &corev1.SliceRef{Account: "acme", Slice: "payment"}, cache, edits); err != nil {
		t.Fatal(err)
	}
	if client.batchCalls != 2 || client.uploads != files {
		t.Fatalf("uploaded %d blobs in %d calls, want %d in 2", client.uploads, client.batchCalls, files)
	}
	for i, edit := range edits {
		if want := objectid.BlobID([]byte(fmt.Sprintf("content-%d", i))); edit.BlobId != want {
			t.Fatalf("edit %d blob id = %q, want %q", i, edit.BlobId, want)
		}
	}
}

// TestAttachBlobIDsUploadsMissingHashesConcurrently: batches go up a few at
// a time (here one blob per batch, three at once).
func TestAttachBlobIDsUploadsMissingHashesConcurrently(t *testing.T) {
	defer func(size int) { uploadBatchBlobs = size }(uploadBatchBlobs)
	uploadBatchBlobs = 1
	cache, err := clientcache.New(filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	edits := make([]*corev1.FileEdit, 0, 6)
	for i := 0; i < 6; i++ {
		cached, err := cache.PutBytes([]byte(fmt.Sprintf("content-%d", i)))
		if err != nil {
			t.Fatal(err)
		}
		edits = append(edits, &corev1.FileEdit{Op: "upsert", Path: fmt.Sprintf("/acme/payment/%d.txt", i), ContentHash: cached.ContentHash})
	}
	client := &fakeBlobClient{
		status:        map[string]*corev1.BlobRecord{},
		uploadStarted: make(chan struct{}, len(edits)),
		uploadRelease: make(chan struct{}),
	}
	type attachResult struct {
		stats blobAttachStats
		err   error
	}
	done := make(chan attachResult, 1)
	go func() {
		stats, err := attachBlobIDsWithStats(context.Background(), client, &corev1.SliceRef{Account: "acme", Slice: "payment"}, cache, edits, 3)
		done <- attachResult{stats: stats, err: err}
	}()

	for i := 0; i < 3; i++ {
		select {
		case <-client.uploadStarted:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for upload worker")
		}
	}
	uploads, current, maximum := client.uploadSnapshot()
	if uploads != 3 || current != 3 || maximum != 3 {
		t.Fatalf("upload concurrency uploads=%d current=%d max=%d, want 3", uploads, current, maximum)
	}
	close(client.uploadRelease)
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.stats.RequiredBlobs != 6 || result.stats.UploadedBlobs != 6 || result.stats.UploadWorkers != 3 {
			t.Fatalf("unexpected upload stats: %#v", result.stats)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for uploads to finish")
	}
	for _, edit := range edits {
		if edit.BlobId == "" {
			t.Fatalf("missing blob id for %s", edit.Path)
		}
	}
}

type fakeBlobClient struct {
	corev1.BlobServiceClient
	mu             sync.Mutex
	status         map[string]*corev1.BlobRecord
	batchCalls     int
	uploads        int
	currentUploads int
	maximumUploads int
	uploadStarted  chan struct{}
	uploadRelease  chan struct{}
}

func (f *fakeBlobClient) GetBlobStatus(ctx context.Context, req *corev1.GetBlobStatusRequest, opts ...grpc.CallOption) (*corev1.GetBlobStatusResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*corev1.BlobRecord, 0, len(req.ContentHashes))
	for _, hash := range req.ContentHashes {
		if record := f.status[hash]; record != nil {
			out = append(out, record)
			continue
		}
		out = append(out, &corev1.BlobRecord{ContentHash: hash, State: "missing"})
	}
	return &corev1.GetBlobStatusResponse{Blobs: out}, nil
}

func (f *fakeBlobClient) UploadBlobs(ctx context.Context, req *corev1.UploadBlobsRequest, opts ...grpc.CallOption) (*corev1.UploadBlobsResponse, error) {
	f.mu.Lock()
	f.batchCalls++
	f.uploads += len(req.Blobs)
	f.currentUploads++
	if f.currentUploads > f.maximumUploads {
		f.maximumUploads = f.currentUploads
	}
	started := f.uploadStarted
	release := f.uploadRelease
	f.mu.Unlock()
	if started != nil {
		started <- struct{}{}
	}
	if release != nil {
		select {
		case <-ctx.Done():
			f.mu.Lock()
			f.currentUploads--
			f.mu.Unlock()
			return nil, ctx.Err()
		case <-release:
		}
	}
	f.mu.Lock()
	f.currentUploads--
	f.mu.Unlock()
	out := &corev1.UploadBlobsResponse{}
	for _, blob := range req.Blobs {
		out.Blobs = append(out.Blobs, &corev1.UploadBlobResponse{BlobId: objectid.BlobID(blob.Data), ContentHash: objectid.RawContentHash(blob.Data), Size: int64(len(blob.Data))})
	}
	return out, nil
}

func (f *fakeBlobClient) UploadBlob(ctx context.Context, req *corev1.UploadBlobRequest, opts ...grpc.CallOption) (*corev1.UploadBlobResponse, error) {
	f.mu.Lock()
	f.uploads++
	f.currentUploads++
	if f.currentUploads > f.maximumUploads {
		f.maximumUploads = f.currentUploads
	}
	started := f.uploadStarted
	release := f.uploadRelease
	f.mu.Unlock()
	if started != nil {
		started <- struct{}{}
	}
	if release != nil {
		select {
		case <-ctx.Done():
			f.mu.Lock()
			f.currentUploads--
			f.mu.Unlock()
			return nil, ctx.Err()
		case <-release:
		}
	}
	f.mu.Lock()
	f.currentUploads--
	f.mu.Unlock()
	return &corev1.UploadBlobResponse{
		BlobId:      objectid.BlobID(req.Data),
		ContentHash: objectid.RawContentHash(req.Data),
		Size:        int64(len(req.Data)),
	}, nil
}

func (f *fakeBlobClient) uploadSnapshot() (uploads, current, maximum int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.uploads, f.currentUploads, f.maximumUploads
}
