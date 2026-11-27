package service

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"

	"gitslice.io/gitslice/internal/activity"
	"gitslice.io/gitslice/internal/authz"
	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/objectstore/filesystem"
	"gitslice.io/gitslice/internal/paths"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/proto/core/v1"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type BlobService struct {
	Auth        storage.RoleLookupStore
	Blobs       storage.BlobStore
	Slices      storage.SliceStore
	ObjectStore ObjectStore

	gate *activity.Gate
}

func (s *BlobService) activityGate() *activity.Gate {
	if s != nil && s.gate != nil {
		return s.gate
	}
	return activity.Default
}

const getBlobStatusMaxHashes = 1000

func (s *BlobService) GetBlobStatus(ctx context.Context, req *corev1.GetBlobStatusRequest) (*corev1.GetBlobStatusResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	if req.Slice == nil {
		return nil, status.Error(codes.InvalidArgument, "slice is required")
	}
	if len(req.GetContentHashes()) > getBlobStatusMaxHashes {
		return nil, status.Errorf(codes.InvalidArgument, "check at most %d blob hashes per call", getBlobStatusMaxHashes)
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
	stored, err := s.uploadBlobs(ctx, req.Slice, []*corev1.UploadBlobsEntry{{ContentHash: req.ContentHash, Data: req.Data}})
	if err != nil {
		return nil, err
	}
	return stored[0], nil
}

const (
	uploadBlobsMaxBlobs = 4000
	uploadBlobsMaxBytes = 64 << 20
	uploadBlobsWrites   = 64
	blobPackThreshold   = 8
)

// UploadBlobs is UploadBlob for many blobs in one call (design/28_benchmarks.md).
func (s *BlobService) UploadBlobs(ctx context.Context, req *corev1.UploadBlobsRequest) (*corev1.UploadBlobsResponse, error) {
	if len(req.GetBlobs()) > uploadBlobsMaxBlobs {
		return nil, status.Errorf(codes.InvalidArgument, "upload at most %d blobs per call", uploadBlobsMaxBlobs)
	}
	var total int64
	for _, blob := range req.GetBlobs() {
		total += int64(len(blob.GetData()))
	}
	if total > uploadBlobsMaxBytes {
		return nil, status.Errorf(codes.ResourceExhausted, "these blobs total %d bytes, more than the %d a call may upload; use UploadBlobStream for large blobs", total, uploadBlobsMaxBytes)
	}
	stored, err := s.uploadBlobs(ctx, req.Slice, req.GetBlobs())
	if err != nil {
		return nil, err
	}
	return &corev1.UploadBlobsResponse{Blobs: stored}, nil
}

// uploadBlobs stores blobs into a slice the caller can write: one slice
// authorization, each distinct blob written once (packed when supported), and
// one statement each to record the blobs, their Git ids and their slice.
func (s *BlobService) uploadBlobs(ctx context.Context, sliceRef *corev1.SliceRef, blobs []*corev1.UploadBlobsEntry) ([]*corev1.UploadBlobResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	if sliceRef == nil {
		return nil, status.Error(codes.InvalidArgument, "slice is required")
	}
	slice, err := resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, sliceRef, authz.ActionPropose)
	if err != nil {
		return nil, err
	}

	type hashedBlob struct {
		blobID      string
		contentHash string
		gitID       string
		size        int64
	}
	hashed := make([]hashedBlob, len(blobs))
	hashOne := func(i int) error {
		b := blobs[i].GetData()
		h := objectid.NewBlobHasher()
		_, _ = h.Write(b)
		contentHash := h.RawContentHash()
		if blobs[i].GetContentHash() != "" && blobs[i].GetContentHash() != contentHash {
			message := "content hash does not match blob bytes"
			if len(blobs) > 1 {
				message = fmt.Sprintf("content hash %s does not match the bytes of blob %d", blobs[i].GetContentHash(), i)
			}
			return status.Error(codes.InvalidArgument, message)
		}
		hashed[i] = hashedBlob{
			blobID:      h.BlobID(),
			contentHash: contentHash,
			gitID:       objectid.GitBlobID(b),
			size:        int64(len(b)),
		}
		return nil
	}
	if len(blobs) >= 32 {
		hg, _ := errgroup.WithContext(ctx)
		hg.SetLimit(8)
		for i := range blobs {
			idx := i
			hg.Go(func() error { return hashOne(idx) })
		}
		if err := hg.Wait(); err != nil {
			return nil, err
		}
	} else {
		for i := range blobs {
			if err := hashOne(i); err != nil {
				return nil, err
			}
		}
	}

	out := make([]*corev1.UploadBlobResponse, len(blobs))
	data := make(map[string][]byte, len(blobs))
	rows := make([]storage.BlobUpsert, 0, len(blobs))
	gitIDs := make(map[string]string, len(blobs))
	hashes := make([]string, 0, len(blobs))
	for i, hb := range hashed {
		out[i] = &corev1.UploadBlobResponse{BlobId: hb.blobID, ContentHash: hb.contentHash, Size: hb.size}
		if _, seen := data[hb.contentHash]; seen {
			continue
		}
		data[hb.contentHash] = blobs[i].GetData()
		rows = append(rows, storage.BlobUpsert{ID: hb.blobID, ContentHash: hb.contentHash, Size: hb.size, StorageLocation: filesystem.BlobKey(hb.contentHash)})
		if hb.gitID != "" {
			gitIDs[hb.contentHash] = hb.gitID
		}
		hashes = append(hashes, hb.contentHash)
	}

	if pw, ok := s.ObjectStore.(interface {
		PutPack(context.Context, string, map[string][]byte) error
	}); ok && len(hashes) >= blobPackThreshold {
		packID, err := objectid.RandomID("bpk")
		if err != nil {
			return nil, grpcError(err)
		}
		packObjects := make(map[string][]byte, len(hashes))
		for _, contentHash := range hashes {
			packObjects[filesystem.BlobKey(contentHash)] = data[contentHash]
		}
		if err := pw.PutPack(ctx, "packs/blobs/"+packID+".pack", packObjects); err != nil {
			return nil, grpcError(err)
		}
	} else {
		group, groupCtx := errgroup.WithContext(ctx)
		group.SetLimit(uploadBlobsWrites)
		for _, contentHash := range hashes {
			group.Go(func() error {
				return s.ObjectStore.Put(groupCtx, filesystem.BlobKey(contentHash), bytes.NewReader(data[contentHash]))
			})
		}
		if err := group.Wait(); err != nil {
			return nil, grpcError(err)
		}
	}
	if err := s.Blobs.UpsertMany(ctx, rows); err != nil {
		return nil, grpcError(err)
	}
	s.recordGitBlobIDs(ctx, gitIDs)
	if err := s.Blobs.AssociateSlices(ctx, slice.Id, hashes); err != nil {
		return nil, grpcError(err)
	}
	for _, row := range rows {
		recordBlobUpload(row.Size)
	}
	return out, nil
}

// recordGitBlobIDs stores blobs' Git ids, keyed by content hash. The ids are
// metadata that can be recomputed, so a failure here must not fail the
// upload: the backfill picks the blobs up later.
func (s *BlobService) recordGitBlobIDs(ctx context.Context, gitIDs map[string]string) {
	if len(gitIDs) == 0 {
		return
	}
	if err := s.Blobs.SetGitBlobIDs(ctx, gitIDs); err != nil {
		slog.Warn("could not record blobs' git ids; the backfill will", "blobs", len(gitIDs), "error", err)
	}
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
