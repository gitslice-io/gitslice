package memory

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"gitslice.io/gitslice/internal/storage"
)

// GitMirrorStore is the in-memory storage.GitMirrorStore.
type GitMirrorStore struct {
	b *backend

	mu      sync.Mutex
	mirrors map[string]*storage.GitMirror
	// Now replaces the clock (tests).
	Now func() time.Time
}

func (s *GitMirrorStore) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}

// named fills in the slice's names, or reports that the slice is gone.
func (s *GitMirrorStore) named(m storage.GitMirror) (storage.GitMirror, bool) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	slice := s.b.slices[m.SliceID]
	if slice == nil || slice.GetRef() == nil {
		return m, false
	}
	m.Account, m.Slice = slice.GetRef().GetAccount(), slice.GetRef().GetSlice()
	return m, true
}

func (s *GitMirrorStore) GetGitMirror(ctx context.Context, sliceID string) (*storage.GitMirror, error) {
	s.mu.Lock()
	m := s.mirrors[strings.TrimSpace(sliceID)]
	s.mu.Unlock()
	if m == nil {
		return nil, storage.ErrNotFound
	}
	out, ok := s.named(*m)
	if !ok {
		return nil, storage.ErrNotFound
	}
	return &out, nil
}

func (s *GitMirrorStore) PutGitMirror(ctx context.Context, m storage.GitMirror) (*storage.GitMirror, error) {
	if strings.TrimSpace(m.SliceID) == "" || strings.TrimSpace(m.URL) == "" {
		return nil, storage.ErrInvalid
	}
	if m.Branch == "" {
		m.Branch = "main"
	}
	now := s.now()
	s.mu.Lock()
	if s.mirrors == nil {
		s.mirrors = map[string]*storage.GitMirror{}
	}
	current := s.mirrors[m.SliceID]
	if current == nil {
		current = &storage.GitMirror{SliceID: m.SliceID, CreatedAt: now}
		s.mirrors[m.SliceID] = current
	}
	current.URL, current.Branch, current.Username, current.TokenSecret, current.CreatedBy = m.URL, m.Branch, m.Username, m.TokenSecret, m.CreatedBy
	current.UpdatedAt = now
	current.PendingSeq++
	current.NextAttemptAt, current.Failures, current.LastError = now, 0, ""
	s.mu.Unlock()
	return s.GetGitMirror(ctx, m.SliceID)
}

func (s *GitMirrorStore) DeleteGitMirror(ctx context.Context, sliceID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.mirrors, strings.TrimSpace(sliceID))
	return nil
}

func (s *GitMirrorStore) ListGitMirrors(ctx context.Context) ([]storage.GitMirror, error) {
	s.mu.Lock()
	all := make([]storage.GitMirror, 0, len(s.mirrors))
	for _, m := range s.mirrors {
		all = append(all, *m)
	}
	s.mu.Unlock()
	var out []storage.GitMirror
	for _, m := range all {
		if named, ok := s.named(m); ok {
			out = append(out, named)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Account+"/"+out[i].Slice < out[j].Account+"/"+out[j].Slice })
	return out, nil
}

func (s *GitMirrorStore) MarkGitMirrorsPending(ctx context.Context, sliceIDs []string, now bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range sliceIDs {
		if m := s.mirrors[id]; m != nil {
			m.PendingSeq++
			if now {
				m.NextAttemptAt, m.Failures = s.now(), 0
			}
		}
	}
	return nil
}

func (s *GitMirrorStore) ClaimDueGitMirrors(ctx context.Context, limit int, lease time.Duration) ([]storage.GitMirror, error) {
	if limit <= 0 {
		limit = 10
	}
	now := s.now()
	s.mu.Lock()
	var claimed []storage.GitMirror
	for _, m := range s.mirrors {
		if len(claimed) >= limit {
			break
		}
		if !m.Pending() || m.NextAttemptAt.After(now) || m.LeaseUntil.After(now) {
			continue
		}
		m.LeaseUntil = now.Add(lease)
		claimed = append(claimed, *m)
	}
	s.mu.Unlock()
	var out []storage.GitMirror
	for _, m := range claimed {
		if named, ok := s.named(m); ok {
			out = append(out, named)
		}
	}
	return out, nil
}

func (s *GitMirrorStore) FinishGitMirror(ctx context.Context, sliceID string, push storage.GitMirrorPush) error {
	at := push.At
	if at.IsZero() {
		at = s.now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.mirrors[sliceID]
	if m == nil {
		return nil
	}
	m.LeaseUntil, m.LastAttemptAt = time.Time{}, at
	if push.OK {
		m.PushedSeq = max(m.PushedSeq, push.Seq)
		m.Failures, m.LastError, m.NextAttemptAt = 0, "", at
		if push.Commit != "" {
			m.LastPushedAt, m.LastPushedCommit, m.LastPushedNative = at, push.Commit, push.Native
		}
		return nil
	}
	m.Failures++
	m.LastError, m.NextAttemptAt = push.Error, push.NextAttemptAt
	return nil
}

var _ storage.GitMirrorStore = (*GitMirrorStore)(nil)
