package storage

import (
	"context"
	"encoding/json"
	"time"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// Trigger run states (design/34_triggers.md).
const (
	TriggerRunQueued    = "queued"
	TriggerRunRunning   = "running"
	TriggerRunWaiting   = "waiting"
	TriggerRunSucceeded = "succeeded"
	TriggerRunFailed    = "failed"
	TriggerRunErrored   = "errored"
	TriggerRunCanceled  = "canceled"
	TriggerRunTimedOut  = "timed_out"
)

// TriggerRunFinal reports whether a run status is final.
func TriggerRunFinal(status string) bool {
	switch status {
	case TriggerRunSucceeded, TriggerRunFailed, TriggerRunErrored, TriggerRunCanceled, TriggerRunTimedOut:
		return true
	}
	return false
}

// TriggerRunOpen are the statuses of a run that has not finished.
var TriggerRunOpen = []string{TriggerRunQueued, TriggerRunRunning, TriggerRunWaiting}

// TriggerRun is one firing of one trigger for one event.
type TriggerRun struct {
	ID string
	// Account is the account's name; TriggerID is relative to its root.
	Account   string
	TriggerID string
	// Folder is the canonical folder that holds the trigger's .gitslice.
	Folder string
	// Action is "http" or "worker".
	Action    string
	EventID   string
	EventKind string
	// What the event is about, when it is about these.
	ChangesetID string
	PatchsetID  string
	CommitID    string
	TargetRef   string
	// DefinitionCommitID is the commit the trigger was read from.
	DefinitionCommitID string
	// Spec is the trigger as resolved (JSON), Payload the event envelope
	// (JSON), State what the script kept between calls (JSON or nil).
	Spec    []byte
	Payload []byte
	State   []byte
	Status  string
	// Attempt counts starts; MaxAttempts is 1 + the trigger's retry (or the
	// HTTP retry schedule's length).
	Attempt        int
	MaxAttempts    int
	Error          string
	ResponseStatus int
	LeaseUntil     time.Time
	NextAttemptAt  time.Time
	Deadline       time.Time
	CreatedAt      time.Time
	StartedAt      time.Time
	FinishedAt     time.Time
	UpdatedAt      time.Time
}

// TriggerRunUpdate changes a run's status. Zero times are left as they are.
type TriggerRunUpdate struct {
	Status         string
	Error          string
	ResponseStatus int
	NextAttemptAt  time.Time
	Deadline       time.Time
}

// TriggerRunFilter selects runs to list, newest first.
type TriggerRunFilter struct {
	Account     string
	TriggerID   string
	ChangesetID string
	Limit       int
}

// TriggerRunLog is one chunk of a run's log.
type TriggerRunLog struct {
	RunID     string
	Seq       int64
	Stream    string
	Chunk     string
	CreatedAt time.Time
}

// TriggerStore keeps trigger runs and their logs.
type TriggerStore interface {
	// CreateTriggerRuns inserts runs, skipping any whose (account, trigger,
	// event) already exists, and returns the ones it inserted.
	CreateTriggerRuns(ctx context.Context, runs []TriggerRun) ([]TriggerRun, error)
	GetTriggerRun(ctx context.Context, id string) (*TriggerRun, error)
	ListTriggerRuns(ctx context.Context, filter TriggerRunFilter) ([]TriggerRun, error)
	// ClaimTriggerRun moves a queued run to running, counts an attempt and
	// leases it; ErrConflict when it is not queued.
	ClaimTriggerRun(ctx context.Context, id string, lease time.Duration) (*TriggerRun, error)
	// ExtendTriggerRunLease pushes the lease of a running run forward;
	// ErrConflict when it is not running.
	ExtendTriggerRunLease(ctx context.Context, id string, lease time.Duration) error
	// UpdateTriggerRun applies an update when the run's status is one of
	// from; ErrConflict otherwise. A final status sets finished_at, and any
	// status but running ends the lease.
	UpdateTriggerRun(ctx context.Context, id string, from []string, update TriggerRunUpdate) (*TriggerRun, error)
	// ClaimDueTriggerRuns returns up to limit queued runs whose next attempt
	// is due, oldest first, and moves their next attempt hold later so
	// other drains skip them meanwhile.
	ClaimDueTriggerRuns(ctx context.Context, limit int, hold time.Duration) ([]TriggerRun, error)
	// ExpiredTriggerRuns returns running runs whose lease ended and waiting
	// runs past their deadline.
	ExpiredTriggerRuns(ctx context.Context, limit int) ([]TriggerRun, error)
	// OpenTriggerRuns lists the account's queued, running and waiting runs
	// of a trigger, for one changeset when changesetID is set.
	OpenTriggerRuns(ctx context.Context, account, triggerID, changesetID string) ([]TriggerRun, error)
	// CountActiveTriggerRuns counts the account's running and waiting runs.
	CountActiveTriggerRuns(ctx context.Context, account string) (int, error)
	// SetTriggerRunState replaces the state of an unfinished run.
	SetTriggerRunState(ctx context.Context, id string, state []byte) error
	AppendTriggerRunLog(ctx context.Context, id, stream, chunk string) (int64, error)
	ListTriggerRunLogs(ctx context.Context, id string, afterSeq int64, limit int) ([]TriggerRunLog, error)
	// HasTriggerFiles reports whether any .gitslice/triggers.yaml exists at
	// the head of any ref.
	HasTriggerFiles(ctx context.Context) (bool, error)
	// PruneTriggerRuns deletes finished runs older than before.
	PruneTriggerRuns(ctx context.Context, before time.Time) error
}

// CheckAnnotation points a check's finding at lines of a file.
type CheckAnnotation struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
	// Level is notice, warning or failure.
	Level   string `json:"level,omitempty"`
	Message string `json:"message"`
}

// CheckRunDetails are what a trigger adds to a check run beyond its status.
type CheckRunDetails struct {
	DetailsURL  string
	Summary     string
	Annotations []CheckAnnotation
}

// MaxCheckAnnotations bounds the annotations of one check run.
const MaxCheckAnnotations = 200

// MarshalCheckAnnotations stores annotations as JSON (nil for none).
func MarshalCheckAnnotations(annotations []CheckAnnotation) ([]byte, error) {
	if len(annotations) == 0 {
		return nil, nil
	}
	if len(annotations) > MaxCheckAnnotations {
		annotations = annotations[:MaxCheckAnnotations]
	}
	return json.Marshal(annotations)
}

// CheckAnnotationsProto reads stored annotations for the API.
func CheckAnnotationsProto(data []byte) []*corev1.CheckAnnotation {
	if len(data) == 0 {
		return nil
	}
	var annotations []CheckAnnotation
	if err := json.Unmarshal(data, &annotations); err != nil {
		return nil
	}
	out := make([]*corev1.CheckAnnotation, 0, len(annotations))
	for _, a := range annotations {
		out = append(out, &corev1.CheckAnnotation{Path: a.Path, StartLine: int32(a.StartLine), EndLine: int32(a.EndLine), Level: a.Level, Message: a.Message})
	}
	return out
}
