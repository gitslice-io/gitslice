package service

import (
	"bytes"
	"context"

	"github.com/gitslice-io/gitslice/internal/authz"
	"github.com/gitslice-io/gitslice/internal/objectid"
	"github.com/gitslice-io/gitslice/internal/objectstore/filesystem"
	"github.com/gitslice-io/gitslice/internal/storage"
	"github.com/gitslice-io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type BlobService struct {
	Auth        storage.AuthStore
	Blobs       storage.BlobStore
	Slices      storage.SliceStore
	ObjectStore ObjectStore
}

func (s *BlobService) GetBlobStatus(ctx context.Context, req *corev1.GetBlobStatusRequest) (*corev1.GetBlobStatusResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	if req.Slice == nil {
		return nil, status.Error(codes.InvalidArgument, "slice is required")
	}
	if _, err := resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, req.Slice, authz.ActionRead); err != nil {
		return nil, err
	}
	records, err := s.Blobs.GetByContentHash(ctx, req.ContentHashes)
	if err != nil {
		return nil, grpcError(err)
	}
	byHash := map[string]*corev1.BlobRecord{}
	for _, record := range records {
		byHash[record.ContentHash] = record
	}
	out := make([]*corev1.BlobRecord, 0, len(req.ContentHashes))
	for _, hash := range req.ContentHashes {
		if record := byHash[hash]; record != nil {
			out = append(out, record)
			continue
		}
		out = append(out, &corev1.BlobRecord{ContentHash: hash, State: "missing"})
	}
	return &corev1.GetBlobStatusResponse{Blobs: out}, nil
}

func (s *BlobService) UploadBlob(ctx context.Context, req *corev1.UploadBlobRequest) (*corev1.UploadBlobResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	if req.Slice == nil {
		return nil, status.Error(codes.InvalidArgument, "slice is required")
	}
	if _, err := resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, req.Slice, authz.ActionWrite); err != nil {
		return nil, err
	}
	contentHash := objectid.RawContentHash(req.Data)
	if req.ContentHash != "" && req.ContentHash != contentHash {
		return nil, status.Error(codes.InvalidArgument, "content hash does not match blob bytes")
	}
	blobID := objectid.BlobID(req.Data)
	key := filesystem.BlobKey(contentHash)
	if err := s.ObjectStore.Put(ctx, key, bytes.NewReader(req.Data)); err != nil {
		return nil, grpcError(err)
	}
	if err := s.Blobs.Upsert(ctx, blobID, contentHash, int64(len(req.Data)), key); err != nil {
		return nil, grpcError(err)
	}
	return &corev1.UploadBlobResponse{BlobId: blobID, ContentHash: contentHash, Size: int64(len(req.Data))}, nil
}
