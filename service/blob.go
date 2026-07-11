package service

import (
	"bytes"
	"context"

	"github.com/gitslice-io/gitslice/internal/authz"
	"github.com/gitslice-io/gitslice/internal/objectid"
	"github.com/gitslice-io/gitslice/internal/objectstore/filesystem"
	"github.com/gitslice-io/gitslice/internal/paths"
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
	slice, err := resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, req.Slice, authz.ActionRead)
	if err != nil {
		return nil, err
	}
	accessible, err := accessibleBlobHashes(ctx, s.Blobs, slice, req.ContentHashes)
	if err != nil {
		return nil, grpcError(err)
	}
	accessibleHashes := make([]string, 0, len(accessible))
	for _, hash := range req.ContentHashes {
		if accessible[hash] {
			accessibleHashes = append(accessibleHashes, hash)
		}
	}
	records, err := s.Blobs.GetByContentHash(ctx, accessibleHashes)
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
	slice, err := resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, req.Slice, authz.ActionWrite)
	if err != nil {
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
	if err := s.Blobs.AssociateSlices(ctx, slice.Id, []string{contentHash}); err != nil {
		return nil, grpcError(err)
	}
	recordBlobUpload(int64(len(req.Data)))
	return &corev1.UploadBlobResponse{BlobId: blobID, ContentHash: contentHash, Size: int64(len(req.Data))}, nil
}

// accessibleBlobHashes filters hashes to those readable through the
// authorized slice: associated in blob_slices or recorded in path_heads at a
// path covered by the slice definition.
func accessibleBlobHashes(ctx context.Context, blobs storage.BlobStore, slice *corev1.Slice, hashes []string) (map[string]bool, error) {
	accessible, err := blobs.SliceAssociations(ctx, slice.Id, hashes)
	if err != nil {
		return nil, err
	}
	pathsByHash, err := blobs.PathsByContentHash(ctx, hashes)
	if err != nil {
		return nil, err
	}
	if slice.Definition == nil {
		return accessible, nil
	}
	for contentHash, blobPaths := range pathsByHash {
		for _, blobPath := range blobPaths {
			if paths.InAnyPrefix(slice.Definition.IncludedPaths, blobPath) {
				accessible[contentHash] = true
				break
			}
		}
	}
	return accessible, nil
}
