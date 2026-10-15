package rpc_test

import (
	"bytes"
	"context"
	"io"
	"testing"

	"gitslice.io/gitslice/internal/objectid"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

func TestRPCBlobStatusUploadAndHashValidation(t *testing.T) {
	ts := startRPCServer(t)
	token := ts.loginViaGRPC(t, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	blob := corev1.NewBlobServiceClient(conn)

	data := []byte("package blob\nconst RPCBlob = true\n")
	contentHash := objectid.RawContentHash(data)
	_, err := blob.GetBlobStatus(ctx, &corev1.GetBlobStatusRequest{ContentHashes: []string{contentHash}})
	assertGRPCCode(t, err, codes.InvalidArgument)
	_, err = blob.UploadBlob(ctx, &corev1.UploadBlobRequest{ContentHash: contentHash, Data: data})
	assertGRPCCode(t, err, codes.InvalidArgument)

	before, err := blob.GetBlobStatus(ctx, &corev1.GetBlobStatusRequest{ContentHashes: []string{contentHash}, Slice: testPaymentSliceRef()})
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Blobs) != 1 || before.Blobs[0].ContentHash != contentHash || before.Blobs[0].State != "missing" {
		t.Fatalf("unexpected pre-upload blob status: %#v", before.Blobs)
	}

	_, err = blob.UploadBlob(ctx, &corev1.UploadBlobRequest{ContentHash: "sha256:not-the-content", Data: data, Slice: testPaymentSliceRef()})
	if grpcstatus.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected mismatched content hash to fail with InvalidArgument, got %v", err)
	}

	uploaded, err := blob.UploadBlob(ctx, &corev1.UploadBlobRequest{ContentHash: contentHash, Data: data, Slice: testPaymentSliceRef()})
	if err != nil {
		t.Fatal(err)
	}
	if uploaded.ContentHash != contentHash || uploaded.BlobId != objectid.BlobID(data) || uploaded.Size != int64(len(data)) {
		t.Fatalf("unexpected upload response: %#v", uploaded)
	}
	after, err := blob.GetBlobStatus(ctx, &corev1.GetBlobStatusRequest{ContentHashes: []string{contentHash, "sha256:missing"}, Slice: testPaymentSliceRef()})
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Blobs) != 2 {
		t.Fatalf("expected two blob status records, got %#v", after.Blobs)
	}
	if after.Blobs[0].Id != uploaded.BlobId || after.Blobs[0].State != "available" || after.Blobs[0].Size != int64(len(data)) {
		t.Fatalf("unexpected uploaded blob status: %#v", after.Blobs[0])
	}
	if after.Blobs[1].ContentHash != "sha256:missing" || after.Blobs[1].State != "missing" {
		t.Fatalf("unexpected missing blob status: %#v", after.Blobs[1])
	}
}

func TestRPCStreamingBlobUploadReadAndHashMismatch(t *testing.T) {
	ts := startRPCServer(t)
	token := ts.loginViaGRPC(t, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	blob := corev1.NewBlobServiceClient(conn)

	data := bytes.Repeat([]byte("streaming blob payload\n"), 300000)
	contentHash := objectid.RawContentHash(data)
	uploaded := uploadBlobStreamForTest(t, ctx, blob, testPaymentSliceRef(), contentHash, data)
	if uploaded.ContentHash != contentHash || uploaded.BlobId != objectid.BlobID(data) || uploaded.Size != int64(len(data)) {
		t.Fatalf("unexpected streaming upload response: %#v", uploaded)
	}
	read := readBlobStreamForTest(t, ctx, blob, testPaymentSliceRef(), contentHash)
	if !bytes.Equal(read, data) {
		t.Fatalf("streaming read length = %d, want %d", len(read), len(data))
	}

	badData := []byte("bad hash streaming upload")
	_, err := uploadBlobStreamForTestErr(ctx, blob, testPaymentSliceRef(), "sha256:not-the-content", badData)
	if grpcstatus.Code(err) != codes.InvalidArgument {
		t.Fatalf("streaming hash mismatch error = %v, want InvalidArgument", err)
	}
	actualBadHash := objectid.RawContentHash(badData)
	status, err := blob.GetBlobStatus(ctx, &corev1.GetBlobStatusRequest{ContentHashes: []string{actualBadHash}, Slice: testPaymentSliceRef()})
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Blobs) != 1 || status.Blobs[0].State != "missing" {
		t.Fatalf("mismatched streaming upload left available metadata: %#v", status.Blobs)
	}
}

func uploadBlobStreamForTest(t *testing.T, ctx context.Context, blob corev1.BlobServiceClient, slice *corev1.SliceRef, contentHash string, data []byte) *corev1.UploadBlobResponse {
	t.Helper()
	res, err := uploadBlobStreamForTestErr(ctx, blob, slice, contentHash, data)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func uploadBlobStreamForTestErr(ctx context.Context, blob corev1.BlobServiceClient, slice *corev1.SliceRef, contentHash string, data []byte) (*corev1.UploadBlobResponse, error) {
	stream, err := blob.UploadBlobStream(ctx)
	if err != nil {
		return nil, err
	}
	size := int64(len(data))
	if err := stream.Send(&corev1.UploadBlobChunk{Payload: &corev1.UploadBlobChunk_Init{Init: &corev1.UploadBlobInit{
		Slice:       slice,
		ContentHash: contentHash,
		Size:        &size,
	}}}); err != nil {
		return nil, err
	}
	const chunkSize = 1 << 20
	for offset := 0; offset < len(data); offset += chunkSize {
		end := offset + chunkSize
		if end > len(data) {
			end = len(data)
		}
		if err := stream.Send(&corev1.UploadBlobChunk{Payload: &corev1.UploadBlobChunk_Data{Data: data[offset:end]}}); err != nil {
			return nil, err
		}
	}
	return stream.CloseAndRecv()
}

func readBlobStreamForTest(t *testing.T, ctx context.Context, blob corev1.BlobServiceClient, slice *corev1.SliceRef, contentHash string) []byte {
	t.Helper()
	stream, err := blob.ReadBlobStream(ctx, &corev1.ReadBlobStreamRequest{Slice: slice, ContentHash: contentHash})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			return out.Bytes()
		}
		if err != nil {
			t.Fatal(err)
		}
		out.Write(chunk.Data)
	}
}
