package service

import (
	"bytes"
	"context"

	"github.com/gitslice-io/gitslice/internal/objectid"
	"github.com/gitslice-io/gitslice/internal/objectstore/filesystem"
	"github.com/gitslice-io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Services) GetBlobStatus(ctx context.Context, req *corev1.GetBlobStatusRequest) (*corev1.GetBlobStatusResponse, error) {
	if _, err := requireSubject(ctx); err != nil {
		return nil, err
	}
	records, err := s.Store.GetBlobsByContentHash(ctx, req.ContentHashes)
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

func (s *Services) UploadBlob(ctx context.Context, req *corev1.UploadBlobRequest) (*corev1.UploadBlobResponse, error) {
	if _, err := requireSubject(ctx); err != nil {
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
	if err := s.Store.UpsertBlob(ctx, blobID, contentHash, int64(len(req.Data)), key); err != nil {
		return nil, grpcError(err)
	}
	return &corev1.UploadBlobResponse{BlobId: blobID, ContentHash: contentHash, Size: int64(len(req.Data))}, nil
}
