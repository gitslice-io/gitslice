package memory

import (
	"context"
	"sort"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func (s *BlobStore) Upsert(ctx context.Context, blobID, contentHash string, size int64, storageLocation string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	s.b.blobs[blobID] = &corev1.BlobRecord{Id: blobID, ContentHash: contentHash, Size: size, StorageLocation: storageLocation, State: "available"}
	return nil
}

func (s *BlobStore) UpsertMany(ctx context.Context, blobs []storage.BlobUpsert) error {
	for _, blob := range blobs {
		if err := s.Upsert(ctx, blob.ID, blob.ContentHash, blob.Size, blob.StorageLocation); err != nil {
			return err
		}
	}
	return nil
}

func (s *BlobStore) GetByIDs(ctx context.Context, blobIDs []string) (map[string]*corev1.BlobRecord, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	out := map[string]*corev1.BlobRecord{}
	for _, id := range blobIDs {
		if blob := s.b.blobs[id]; blob != nil {
			out[id] = cloneBlob(blob)
		}
	}
	return out, nil
}

func (s *BlobStore) GetByID(ctx context.Context, blobID string) (*corev1.BlobRecord, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	blob := s.b.blobs[blobID]
	if blob == nil {
		return nil, storage.ErrNotFound
	}
	return cloneBlob(blob), nil
}

func (s *BlobStore) GetByContentHash(ctx context.Context, hashes []string) ([]*corev1.BlobRecord, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	wanted := map[string]struct{}{}
	for _, hash := range hashes {
		wanted[hash] = struct{}{}
	}
	var out []*corev1.BlobRecord
	for _, blob := range s.b.blobs {
		if _, ok := wanted[blob.ContentHash]; ok {
			out = append(out, cloneBlob(blob))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ContentHash < out[j].ContentHash })
	return out, nil
}

func (s *BlobStore) SetGitBlobIDs(ctx context.Context, ids map[string]string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	for contentHash, gitID := range ids {
		s.b.gitBlobIDs[contentHash] = gitID
	}
	return nil
}

func (s *BlobStore) GitBlobIDs(ctx context.Context, contentHashes []string) (map[string]string, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	out := map[string]string{}
	for _, contentHash := range contentHashes {
		if id, ok := s.b.gitBlobIDs[contentHash]; ok {
			out[contentHash] = id
		}
	}
	return out, nil
}

func (s *BlobStore) BlobsByGitIDs(ctx context.Context, gitBlobIDs []string) (map[string]*corev1.BlobRecord, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	wanted := map[string]bool{}
	for _, id := range gitBlobIDs {
		wanted[id] = true
	}
	out := map[string]*corev1.BlobRecord{}
	for _, blob := range s.b.blobs {
		if id := s.b.gitBlobIDs[blob.ContentHash]; id != "" && wanted[id] {
			out[id] = cloneBlob(blob)
		}
	}
	return out, nil
}

func (s *BlobStore) ListMissingGitBlobIDs(ctx context.Context, limit int) ([]*corev1.BlobRecord, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	var out []*corev1.BlobRecord
	for _, blob := range s.b.blobs {
		if _, ok := s.b.gitBlobIDs[blob.ContentHash]; !ok && blob.State == "available" {
			out = append(out, cloneBlob(blob))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ContentHash < out[j].ContentHash })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *BlobStore) AssociateSlices(ctx context.Context, sliceID string, contentHashes []string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if len(contentHashes) == 0 {
		return nil
	}
	if s.b.blobSlices[sliceID] == nil {
		s.b.blobSlices[sliceID] = map[string]struct{}{}
	}
	for _, contentHash := range contentHashes {
		s.b.blobSlices[sliceID][contentHash] = struct{}{}
	}
	return nil
}

func (s *BlobStore) SliceAssociations(ctx context.Context, sliceID string, contentHashes []string) (map[string]bool, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	out := map[string]bool{}
	associated := s.b.blobSlices[sliceID]
	for _, contentHash := range contentHashes {
		if _, ok := associated[contentHash]; ok {
			out[contentHash] = true
		}
	}
	return out, nil
}

func (s *BlobStore) PathsByContentHash(ctx context.Context, contentHashes []string) (map[string][]string, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	out := map[string][]string{}
	wanted := map[string]struct{}{}
	for _, contentHash := range contentHashes {
		wanted[contentHash] = struct{}{}
	}
	ref := s.b.refs[storage.DefaultTargetRef]
	if ref == nil {
		return out, nil
	}
	for path, file := range s.b.commitFiles[ref.CommitId] {
		if _, ok := wanted[file.ContentHash]; ok {
			out[file.ContentHash] = append(out[file.ContentHash], path)
		}
	}
	for contentHash := range out {
		sort.Strings(out[contentHash])
	}
	return out, nil
}
