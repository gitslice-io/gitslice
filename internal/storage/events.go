package storage

import (
	"context"
	"time"
)

// Events recorded by Gitslice automation.
const (
	EventPush               = "push"
	EventTagCreated         = "tag.created"
	EventChangesetCreated   = "changeset.created"
	EventChangesetUpdated   = "changeset.updated"
	EventChangesetApproved  = "changeset.approved"
	EventChangesetSubmitted = "changeset.submitted"
	EventChangesetAbandoned = "changeset.abandoned"
	EventCheckRunCompleted  = "check_run.completed"
)

// Event is something that happened. It holds references; consumers read the
// current objects when they build their payloads.
type Event struct {
	ID   string
	Kind string
	// The slice the event belongs to, when the producer knows it. Otherwise
	// it is the changeset's authoring slice.
	SliceID        string
	ActorSubjectID string
	ChangesetID    string
	PatchsetID     string
	CommitID       string
	TargetRef      string
	TagName        string
	CheckRunID     string
	ChangedPaths   []string
	// Data holds event details that have no object to read later, as JSON
	// (for example a reported check's name and status).
	Data      string
	CreatedAt time.Time
}

// EventStore keeps the historical-named webhook_events event log. The table
// is consumed by triggers and Git mirrors; its name remains for compatibility.
type EventStore interface {
	// AppendEvent records an event; it gets an id and time if unset.
	AppendEvent(ctx context.Context, event Event) (*Event, error)
	// ClaimEvents leases up to limit events that are not fanned out yet, oldest
	// first; another claimer skips them until the lease ends.
	ClaimEvents(ctx context.Context, limit int, lease time.Duration) ([]Event, error)
	// CompleteEvent marks an event fanned out after its consumers have been
	// notified. It is safe to repeat after another claimer's lease expires.
	CompleteEvent(ctx context.Context, eventID string) error
	// PruneEvents deletes completed events older than before.
	PruneEvents(ctx context.Context, before time.Time) error
}
