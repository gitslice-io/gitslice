package memory

import (
	"context"

	"gitslice.io/gitslice/internal/storage"
)

// BlameStore is the in-memory storage.BlameStore.
type BlameStore struct {
	b *backend
}

func (s *BlameStore) BlameSnapshots(ctx context.Context, commitIDs []string) (map[string]map[string]*storage.BlameSnapshot, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	out := map[string]map[string]*storage.BlameSnapshot{}
	for _, id := range commitIDs {
		for p, snapshot := range s.b.blameSnapshots[id] {
			if out[id] == nil {
				out[id] = map[string]*storage.BlameSnapshot{}
			}
			copied := snapshot
			copied.Owners = append([]string(nil), snapshot.Owners...)
			out[id][p] = &copied
		}
	}
	return out, nil
}

func (s *BlameStore) PutBlameSnapshot(ctx context.Context, snapshot storage.BlameSnapshot) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if s.b.blameSnapshots[snapshot.CommitID] == nil {
		s.b.blameSnapshots[snapshot.CommitID] = map[string]storage.BlameSnapshot{}
	}
	if _, ok := s.b.blameSnapshots[snapshot.CommitID][snapshot.Path]; ok {
		return nil
	}
	snapshot.Owners = append([]string(nil), snapshot.Owners...)
	s.b.blameSnapshots[snapshot.CommitID][snapshot.Path] = snapshot
	return nil
}
