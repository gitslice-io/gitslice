package memory

import (
	"context"
	"slices"
	"sort"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/intents"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func (s *ChangesetStore) CreatePathIntent(ctx context.Context, intent storage.PathIntent) (*storage.PathIntent, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if strings.TrimSpace(intent.SliceID) == "" || strings.TrimSpace(intent.SubjectID) == "" || strings.TrimSpace(intent.Pattern) == "" {
		return nil, storage.ErrInvalid
	}
	if _, ok := s.b.slices[intent.SliceID]; !ok {
		return nil, storage.ErrNotFound
	}
	now := time.Now().UTC()
	if intent.CreatedAt.IsZero() {
		intent.CreatedAt = now
	} else {
		intent.CreatedAt = intent.CreatedAt.UTC()
	}
	intent.ExpiresAt = intent.ExpiresAt.UTC()
	if intent.ID == "" {
		intent.ID = s.b.nextIDLocked("int")
	}
	if intent.ChangesetID != "" {
		if _, ok := s.b.changesets[intent.ChangesetID]; !ok {
			return nil, storage.ErrNotFound
		}
	}
	cp := intent
	s.b.pathIntents[cp.ID] = cp
	out := cp
	return &out, nil
}

func (s *ChangesetStore) GetPathIntent(ctx context.Context, intentID string) (*storage.PathIntent, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	intent, ok := s.b.pathIntents[strings.TrimSpace(intentID)]
	if !ok {
		return nil, storage.ErrNotFound
	}
	cp := intent
	return &cp, nil
}

func (s *ChangesetStore) ListPathIntents(ctx context.Context, sliceID string, now time.Time) ([]storage.PathIntent, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	var out []storage.PathIntent
	for _, intent := range s.b.pathIntents {
		if intent.SliceID != sliceID {
			continue
		}
		if !intent.ExpiresAt.After(now) {
			continue
		}
		out = append(out, intent)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (s *ChangesetStore) DeletePathIntent(ctx context.Context, intentID string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	intentID = strings.TrimSpace(intentID)
	if _, ok := s.b.pathIntents[intentID]; !ok {
		return storage.ErrNotFound
	}
	delete(s.b.pathIntents, intentID)
	return nil
}

func (s *ChangesetStore) LinkPathIntentsToChangeset(ctx context.Context, intentIDs []string, subjectID, sliceID, changesetID string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if len(intentIDs) == 0 || changesetID == "" {
		return nil
	}
	now := time.Now().UTC()
	for _, id := range intentIDs {
		intent, ok := s.b.pathIntents[id]
		if !ok {
			continue
		}
		if intent.SubjectID != subjectID || intent.SliceID != sliceID || intent.ChangesetID != "" || !intent.ExpiresAt.After(now) {
			continue
		}
		intent.ChangesetID = changesetID
		s.b.pathIntents[id] = intent
	}
	return nil
}

func (b *backend) clearSubmittedPathIntentsLocked(cs *corev1.Changeset, changedPaths []string) {
	if cs == nil {
		return
	}
	sliceID := b.sliceRefs[sliceRefKey(cs.AuthoringSlice)]
	mentioned := intents.ExtractMentionedIntentIDs(cs.Title, cs.Description)
	for id, intent := range b.pathIntents {
		if intent.ChangesetID == cs.Id {
			delete(b.pathIntents, id)
			continue
		}
		if sliceID != "" && intent.SliceID == sliceID && intent.SubjectID == cs.Author {
			if slices.Contains(mentioned, id) || len(intents.OverlappingPaths(intent.Pattern, changedPaths)) > 0 {
				delete(b.pathIntents, id)
			}
		}
	}
}

func (b *backend) clearAbandonedPathIntentsLocked(cs *corev1.Changeset) {
	if cs == nil {
		return
	}
	sliceID := b.sliceRefs[sliceRefKey(cs.AuthoringSlice)]
	mentioned := intents.ExtractMentionedIntentIDs(cs.Title, cs.Description)
	for id, intent := range b.pathIntents {
		if intent.ChangesetID == cs.Id {
			delete(b.pathIntents, id)
			continue
		}
		if sliceID != "" && intent.SliceID == sliceID && intent.SubjectID == cs.Author && slices.Contains(mentioned, id) {
			delete(b.pathIntents, id)
		}
	}
}
