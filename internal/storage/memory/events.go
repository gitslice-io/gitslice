package memory

import (
	"context"
	"time"

	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/storage"
)

type memEvent struct {
	event     storage.Event
	completed time.Time
	leaseTill time.Time
}

// EventStore keeps the event log used by triggers and Git mirrors.
type EventStore struct{ b *backend }

func (s *EventStore) AppendEvent(ctx context.Context, event storage.Event) (*storage.Event, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	return s.b.appendEventLocked(event)
}

// appendEventLocked records an event while the publisher holds the backend
// lock, matching the Postgres transaction used for published changesets.
func (b *backend) appendEventLocked(event storage.Event) (*storage.Event, error) {
	if event.ID == "" {
		id, err := objectid.RandomID("evt")
		if err != nil {
			return nil, err
		}
		event.ID = id
	}
	event.CreatedAt = time.Now().UTC()
	event.ChangedPaths = append([]string(nil), event.ChangedPaths...)
	b.events = append(b.events, &memEvent{event: event})
	out := event
	return &out, nil
}

func (s *EventStore) ClaimEvents(ctx context.Context, limit int, lease time.Duration) ([]storage.Event, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if limit <= 0 {
		limit = 50
	}
	now := time.Now()
	var out []storage.Event
	for _, entry := range s.b.events {
		if len(out) >= limit {
			break
		}
		if !entry.completed.IsZero() || now.Before(entry.leaseTill) {
			continue
		}
		entry.leaseTill = now.Add(lease)
		event := entry.event
		event.ChangedPaths = append([]string(nil), event.ChangedPaths...)
		out = append(out, event)
	}
	return out, nil
}

func (s *EventStore) CompleteEvent(ctx context.Context, eventID string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	for _, entry := range s.b.events {
		if entry.event.ID != eventID {
			continue
		}
		if entry.completed.IsZero() {
			entry.completed = time.Now()
			entry.leaseTill = time.Time{}
		}
		return nil
	}
	return storage.ErrNotFound
}

func (s *EventStore) PruneEvents(ctx context.Context, before time.Time) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	kept := s.b.events[:0]
	for _, entry := range s.b.events {
		if entry.completed.IsZero() || entry.completed.After(before) {
			kept = append(kept, entry)
		}
	}
	s.b.events = kept
	return nil
}

var _ storage.EventStore = (*EventStore)(nil)
