package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/storage"
)

// EventStore keeps the historical-named webhook_events event log used by
// triggers and Git mirrors.
type EventStore struct {
	db *sql.DB
}

func (s *EventStore) AppendEvent(ctx context.Context, event storage.Event) (*storage.Event, error) {
	return appendEvent(ctx, s.db, event)
}

// appendEvent inserts an event with db or inside a transaction (the publisher
// records changeset.submitted in the transaction that publishes). The table's
// name is historical; it is the event log for triggers and Git mirrors.
func appendEvent(ctx context.Context, db queryRower, event storage.Event) (*storage.Event, error) {
	if event.ID == "" {
		id, err := objectid.RandomID("evt")
		if err != nil {
			return nil, err
		}
		event.ID = id
	}
	paths, err := json.Marshal(nonNil(event.ChangedPaths))
	if err != nil {
		return nil, err
	}
	err = db.QueryRowContext(ctx, `
		insert into webhook_events(id, kind, slice_id, actor_subject_id, changeset_id, patchset_id, commit_id,
			target_ref, tag_name, check_run_id, changed_paths, data, created_at)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, clock_timestamp())
		returning created_at
	`, event.ID, event.Kind, event.SliceID, event.ActorSubjectID, event.ChangesetID, event.PatchsetID, event.CommitID,
		event.TargetRef, event.TagName, event.CheckRunID, paths, event.Data).Scan(&event.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &event, nil
}

func (s *EventStore) ClaimEvents(ctx context.Context, limit int, lease time.Duration) ([]storage.Event, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		update webhook_events set lease_until = now() + ($2::bigint * interval '1 millisecond')
		where id in (
			select id from webhook_events
			where fanned_out_at is null and (lease_until is null or lease_until < now())
			order by created_at, id
			limit $1
			for update skip locked
		)
		returning id, kind, slice_id, actor_subject_id, changeset_id, patchset_id, commit_id, target_ref,
			tag_name, check_run_id, changed_paths, data, created_at
	`, limit, lease.Milliseconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []storage.Event
	for rows.Next() {
		var event storage.Event
		var paths []byte
		if err := rows.Scan(&event.ID, &event.Kind, &event.SliceID, &event.ActorSubjectID, &event.ChangesetID, &event.PatchsetID, &event.CommitID,
			&event.TargetRef, &event.TagName, &event.CheckRunID, &paths, &event.Data, &event.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(paths, &event.ChangedPaths); err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sortEventsOldestFirst(out)
	return out, nil
}

func (s *EventStore) CompleteEvent(ctx context.Context, eventID string) error {
	_, err := s.db.ExecContext(ctx, `
		update webhook_events set fanned_out_at = now(), lease_until = null
		where id = $1 and fanned_out_at is null
	`, eventID)
	return err
}

func (s *EventStore) PruneEvents(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx, `delete from webhook_events where fanned_out_at is not null and fanned_out_at < $1`, before)
	return err
}

type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func sortEventsOldestFirst(events []storage.Event) {
	for i := 1; i < len(events); i++ {
		for j := i; j > 0 && (events[j].CreatedAt.Before(events[j-1].CreatedAt) ||
			(events[j].CreatedAt.Equal(events[j-1].CreatedAt) && events[j].ID < events[j-1].ID)); j-- {
			events[j], events[j-1] = events[j-1], events[j]
		}
	}
}

var _ storage.EventStore = (*EventStore)(nil)
