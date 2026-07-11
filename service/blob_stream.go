package service

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/gitslice-io/gitslice/internal/authz"
	"github.com/gitslice-io/gitslice/internal/objectid"
	"github.com/gitslice-io/gitslice/internal/objectstore/filesystem"
	"github.com/gitslice-io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const blobStreamChunkBytes = 1 << 20

func (s *BlobService) UploadBlobStream(stream corev1.BlobService_UploadBlobStreamServer) error {
	ctx := stream.Context()
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return err
	}
	first, err := stream.Recv()
	if errors.Is(err, io.EOF) {
		return status.Error(codes.InvalidArgument, "upload init chunk is required")
	}
	if err != nil {
		return grpcError(err)
	}
	init := first.GetInit()
	if init == nil {
		return status.Error(codes.InvalidArgument, "first upload chunk must be init")
	}
	if init.Slice == nil {
		return status.Error(codes.InvalidArgument, "slice is required")
	}
	if init.Size != nil && *init.Size < 0 {
		return status.Error(codes.InvalidArgument, "size must be non-negative")
	}
	slice, err := resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, init.Slice, authz.ActionWrite)
	if err != nil {
		return err
	}

	stagingID, err := objectid.RandomID("blob_upload")
	if err != nil {
		return grpcError(err)
	}
	stagingKey := "staging/blob-uploads/" + stagingID
	pr, pw := io.Pipe()
	putErr := make(chan error, 1)
	go func() {
		putErr <- s.ObjectStore.Put(ctx, stagingKey, pr)
	}()

	writer := newHashVerifyingWriteCloser(pw, init.GetContentHash(), init.Size)
	abort := func(cause error) error {
		_ = pw.CloseWithError(cause)
		<-putErr
		_ = s.ObjectStore.Delete(context.WithoutCancel(ctx), stagingKey)
		return cause
	}

	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return abort(grpcError(err))
		}
		if chunk.GetInit() != nil {
			return abort(status.Error(codes.InvalidArgument, "upload init chunk must appear only once"))
		}
		data := chunk.GetData()
		if len(data) > blobStreamChunkBytes {
			return abort(status.Errorf(codes.InvalidArgument, "upload chunk exceeds %d bytes", blobStreamChunkBytes))
		}
		if _, err := writer.Write(data); err != nil {
			return abort(grpcError(err))
		}
	}

	verifyErr := writer.Close()
	if err := <-putErr; err != nil {
		_ = s.ObjectStore.Delete(context.WithoutCancel(ctx), stagingKey)
		return grpcError(err)
	}
	if verifyErr != nil {
		_ = s.ObjectStore.Delete(context.WithoutCancel(ctx), stagingKey)
		return verifyErr
	}

	contentHash := writer.ContentHash()
	blobID := writer.BlobID()
	size := writer.Size()
	finalKey := filesystem.BlobKey(contentHash)
	if err := copyObject(ctx, s.ObjectStore, stagingKey, finalKey); err != nil {
		_ = s.ObjectStore.Delete(context.WithoutCancel(ctx), stagingKey)
		return grpcError(err)
	}
	if err := s.ObjectStore.Delete(context.WithoutCancel(ctx), stagingKey); err != nil {
		return grpcError(err)
	}
	if err := s.Blobs.Upsert(ctx, blobID, contentHash, size, finalKey); err != nil {
		return grpcError(err)
	}
	if err := s.Blobs.AssociateSlices(ctx, slice.Id, []string{contentHash}); err != nil {
		return grpcError(err)
	}
	recordBlobUpload(size)
	return stream.SendAndClose(&corev1.UploadBlobResponse{BlobId: blobID, ContentHash: contentHash, Size: size})
}

func (s *BlobService) ReadBlobStream(req *corev1.ReadBlobStreamRequest, stream corev1.BlobService_ReadBlobStreamServer) error {
	ctx := stream.Context()
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return err
	}
	if req.Slice == nil {
		return status.Error(codes.InvalidArgument, "slice is required")
	}
	slice, err := resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, req.Slice, authz.ActionRead)
	if err != nil {
		return err
	}
	contentHash := strings.TrimSpace(req.ContentHash)
	if contentHash == "" {
		return status.Error(codes.InvalidArgument, "content hash is required")
	}
	if req.Offset < 0 {
		return status.Error(codes.InvalidArgument, "offset must be non-negative")
	}
	if req.Length < 0 {
		return status.Error(codes.InvalidArgument, "length must be non-negative")
	}
	accessible, err := accessibleBlobHashes(ctx, s.Blobs, slice, []string{contentHash})
	if err != nil {
		return grpcError(err)
	}
	if !accessible[contentHash] {
		return status.Error(codes.NotFound, "blob not found")
	}
	records, err := s.Blobs.GetByContentHash(ctx, []string{contentHash})
	if err != nil {
		return grpcError(err)
	}
	if len(records) == 0 || records[0].State != "available" {
		return status.Error(codes.NotFound, "blob not found")
	}
	location := records[0].StorageLocation
	if location == "" {
		location = filesystem.BlobKey(contentHash)
	}
	rc, err := s.ObjectStore.Get(ctx, location, req.Offset, req.Length)
	if err != nil {
		return grpcError(err)
	}
	defer rc.Close()

	buf := make([]byte, blobStreamChunkBytes)
	offset := req.Offset
	for {
		n, err := rc.Read(buf)
		if n > 0 {
			data := append([]byte(nil), buf[:n]...)
			if sendErr := stream.Send(&corev1.ReadBlobChunk{Data: data, Offset: offset, ContentHash: contentHash}); sendErr != nil {
				return grpcError(sendErr)
			}
			offset += int64(n)
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return grpcError(err)
		}
	}
}

type hashVerifyingWriteCloser struct {
	w            io.WriteCloser
	hasher       *objectid.BlobHasher
	expectedHash string
	expectedSize *int64
	closed       bool
}

func newHashVerifyingWriteCloser(w io.WriteCloser, expectedHash string, expectedSize *int64) *hashVerifyingWriteCloser {
	return &hashVerifyingWriteCloser{
		w:            w,
		hasher:       objectid.NewBlobHasher(),
		expectedHash: strings.TrimSpace(expectedHash),
		expectedSize: expectedSize,
	}
}

func (w *hashVerifyingWriteCloser) Write(p []byte) (int, error) {
	if w.closed {
		return 0, errors.New("blob writer is closed")
	}
	n, err := w.w.Write(p)
	if n > 0 {
		_, _ = w.hasher.Write(p[:n])
	}
	return n, err
}

func (w *hashVerifyingWriteCloser) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	closeErr := w.w.Close()
	if closeErr != nil {
		return closeErr
	}
	if w.expectedSize != nil && *w.expectedSize != w.hasher.Size() {
		return status.Errorf(codes.InvalidArgument, "declared blob size %d does not match uploaded size %d", *w.expectedSize, w.hasher.Size())
	}
	if w.expectedHash != "" && w.expectedHash != w.hasher.RawContentHash() {
		return status.Error(codes.InvalidArgument, "content hash does not match blob bytes")
	}
	return nil
}

func (w *hashVerifyingWriteCloser) ContentHash() string {
	return w.hasher.RawContentHash()
}

func (w *hashVerifyingWriteCloser) BlobID() string {
	return w.hasher.BlobID()
}

func (w *hashVerifyingWriteCloser) Size() int64 {
	return w.hasher.Size()
}

func copyObject(ctx context.Context, store ObjectStore, fromKey, toKey string) error {
	rc, err := store.Get(ctx, fromKey, 0, 0)
	if err != nil {
		return err
	}
	defer rc.Close()
	if err := store.Put(ctx, toKey, rc); err != nil {
		return err
	}
	return nil
}
