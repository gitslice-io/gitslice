package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/gitslice-io/gitslice/internal/clientcache"
	"github.com/gitslice-io/gitslice/internal/objectid"
	"github.com/gitslice-io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
)

func TestSchemaCommandEmitsMachineReadableContract(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := Runner{Stdout: &stdout, Stderr: &stderr}
	if err := r.Run(context.Background(), []string{"schema"}); err != nil {
		t.Fatalf("schema failed: %v\nstderr:\n%s", err, stderr.String())
	}

	var got struct {
		SchemaVersion string `json:"schema_version"`
		Commands      []struct {
			Use string `json:"use"`
		} `json:"commands"`
		ErrorOutput map[string]any `json:"error_output"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("schema output is not JSON: %v\n%s", err, stdout.String())
	}
	if got.SchemaVersion != "v1" {
		t.Fatalf("unexpected schema version %q", got.SchemaVersion)
	}
	if len(got.Commands) == 0 {
		t.Fatal("schema did not include commands")
	}
	if got.ErrorOutput["stream"] != "stderr" {
		t.Fatalf("expected stderr error stream, got %#v", got.ErrorOutput["stream"])
	}
}

func TestInvalidFormatReturnsStructuredCommandError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := Runner{Stdout: &stdout, Stderr: &stderr}
	err := r.Run(context.Background(), []string{"status", "--format", "yaml"})
	if err == nil {
		t.Fatal("status with invalid format unexpectedly succeeded")
	}
	var cmdErr commandError
	if !errors.As(err, &cmdErr) {
		t.Fatalf("expected commandError, got %T: %v", err, err)
	}
	if cmdErr.Code != "invalid_format" {
		t.Fatalf("unexpected error code %q", cmdErr.Code)
	}
}

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

	if err := attachBlobIDs(context.Background(), client, cache, edits); err != nil {
		t.Fatal(err)
	}
	if client.uploads != 0 {
		t.Fatalf("expected no uploads, got %d", client.uploads)
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

	if err := attachBlobIDs(context.Background(), client, cache, edits); err != nil {
		t.Fatal(err)
	}
	if client.uploads != 1 {
		t.Fatalf("expected one upload, got %d", client.uploads)
	}
	wantBlobID := objectid.BlobID(content)
	for _, edit := range edits {
		if edit.BlobId != wantBlobID {
			t.Fatalf("expected uploaded blob id %q, got %q", wantBlobID, edit.BlobId)
		}
	}
}

type fakeBlobClient struct {
	corev1.BlobServiceClient
	status  map[string]*corev1.BlobRecord
	uploads int
}

func (f *fakeBlobClient) GetBlobStatus(ctx context.Context, req *corev1.GetBlobStatusRequest, opts ...grpc.CallOption) (*corev1.GetBlobStatusResponse, error) {
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

func (f *fakeBlobClient) UploadBlob(ctx context.Context, req *corev1.UploadBlobRequest, opts ...grpc.CallOption) (*corev1.UploadBlobResponse, error) {
	f.uploads++
	return &corev1.UploadBlobResponse{
		BlobId:      objectid.BlobID(req.Data),
		ContentHash: objectid.RawContentHash(req.Data),
		Size:        int64(len(req.Data)),
	}, nil
}
