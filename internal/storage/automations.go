package storage

import (
	"context"
	"encoding/json"
	"time"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// Trigger run states (design/34_triggers.md).
const (
	AutomationRunQueued    = "queued"
	AutomationRunRunning   = "running"
	AutomationRunWaiting   = "waiting"
	AutomationRunSucceeded = "succeeded"
	AutomationRunFailed    = "failed"
	AutomationRunErrored   = "errored"
	AutomationRunCanceled  = "canceled"
	AutomationRunTimedOut  = "timed_out"
)

// AutomationRunFinal reports whether a run status is final.
func AutomationRunFinal(status string) bool {
	switch status {
	case AutomationRunSucceeded, AutomationRunFailed, AutomationRunErrored, AutomationRunCanceled, AutomationRunTimedOut:
		return true
	}
	return false
}

// AutomationRunOpen are the statuses of a run that has not finished.
var AutomationRunOpen = []string{AutomationRunQueued, AutomationRunRunning, AutomationRunWaiting}

// AutomationRun is one firing of one trigger for one event.
type AutomationRun struct {
	ID string
	// Account is the account's name; AutomationID is relative to its root.
	Account      string
	AutomationID string
	// Folder is the canonical folder that holds the trigger's .gitslice.
	Folder string
	// Action is "http", "worker", "check", or "bash".
	Action    string
	EventID   string
	EventKind string
	// For manual runs: actor username or subject id, and kind ("human" or "agent").
	TriggeredBy     string
	TriggeredByKind string
	// What the event is about, when it is about these.
	ChangesetID string
	PatchsetID  string
	CommitID    string
	TargetRef   string
	// DefinitionCommitID is the commit the trigger was read from.
	DefinitionCommitID string
	// ConcurrencyGroup is the trigger's concurrency_group: no two runs of
	// the account's triggers in one group run at once.
	ConcurrencyGroup string
	// DetailsURL is an outside page about the run (gs.setDetailsUrl).
	DetailsURL string
	// Reusable template provenance when executed via action.uses.
	TemplateID       string
	TemplateCommitID string
	// Multi-step pipeline linkage (set on child step runs).
	ParentRunID string
	StepIndex   int
	StepID      string
	// Outputs stores named key/value outputs emitted by this run.
	Outputs map[string]string
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

// AutomationRunUpdate changes a run's status. Zero times are left as they are.
type AutomationRunUpdate struct {
	Status         string
	Error          string
	ResponseStatus int
	NextAttemptAt  time.Time
	Deadline       time.Time
}

// AutomationRunFilter selects runs to list, newest first (or by step_index
// ascending when ParentRunID is set).
type AutomationRunFilter struct {
	Account      string
	AutomationID string
	ChangesetID  string
	ParentRunID  string
	Limit        int
}

// AutomationRunLog is one chunk of a run's log.
type AutomationRunLog struct {
	RunID     string
	Seq       int64
	Stream    string
	Chunk     string
	CreatedAt time.Time
}

// AutomationStore keeps trigger runs and their logs.
type AutomationStore interface {
	// CreateAutomationRuns inserts runs, skipping any whose (account, trigger,
	// event) already exists, and returns the ones it inserted.
	CreateAutomationRuns(ctx context.Context, runs []AutomationRun) ([]AutomationRun, error)
	GetAutomationRun(ctx context.Context, id string) (*AutomationRun, error)
	ListAutomationRuns(ctx context.Context, filter AutomationRunFilter) ([]AutomationRun, error)
	// ClaimAutomationRun moves a queued run to running, counts an attempt and
	// leases it; ErrConflict when it is not queued.
	ClaimAutomationRun(ctx context.Context, id string, lease time.Duration) (*AutomationRun, error)
	// ExtendAutomationRunLease pushes the lease of a running run forward;
	// ErrConflict when it is not running.
	ExtendAutomationRunLease(ctx context.Context, id string, lease time.Duration) error
	// UpdateAutomationRun applies an update when the run's status is one of
	// from; ErrConflict otherwise. A final status sets finished_at, and any
	// status but running ends the lease.
	UpdateAutomationRun(ctx context.Context, id string, from []string, update AutomationRunUpdate) (*AutomationRun, error)
	// ClaimDueAutomationRuns returns up to limit queued runs whose next attempt
	// is due, oldest first, and moves their next attempt hold later so
	// other drains skip them meanwhile.
	ClaimDueAutomationRuns(ctx context.Context, limit int, hold time.Duration) ([]AutomationRun, error)
	// ExpiredAutomationRuns returns running runs whose lease ended and waiting
	// runs past their deadline.
	ExpiredAutomationRuns(ctx context.Context, limit int) ([]AutomationRun, error)
	// OpenAutomationRuns lists the account's queued, running and waiting runs
	// of a trigger, for one changeset when changesetID is set.
	OpenAutomationRuns(ctx context.Context, account, triggerID, changesetID string) ([]AutomationRun, error)
	// OpenAutomationRunsInGroup lists the account's queued, running and waiting
	// runs of every trigger in a concurrency group, oldest first.
	OpenAutomationRunsInGroup(ctx context.Context, account, group string) ([]AutomationRun, error)
	// SetAutomationRunDetailsURL sets the details URL of an unfinished run.
	SetAutomationRunDetailsURL(ctx context.Context, id, url string) error
	// SetAutomationRunOutput sets a named output key/value on an unfinished run.
	SetAutomationRunOutput(ctx context.Context, id, key, value string) error
	// CountActiveAutomationRuns counts the account's running and waiting runs.
	CountActiveAutomationRuns(ctx context.Context, account string) (int, error)
	// CountActiveAutomationRunsByAction counts the account's running and waiting
	// runs of one action kind (for example, "bash").
	CountActiveAutomationRunsByAction(ctx context.Context, account, action string) (int, error)
	// SetAutomationRunState replaces the state of an unfinished run.
	SetAutomationRunState(ctx context.Context, id string, state []byte) error
	AppendAutomationRunLog(ctx context.Context, id, stream, chunk string) (int64, error)
	ListAutomationRunLogs(ctx context.Context, id string, afterSeq int64, limit int) ([]AutomationRunLog, error)
	// HasAutomationFiles reports whether any .gitslice/automations.yaml exists at
	// the head of any ref.
	HasAutomationFiles(ctx context.Context) (bool, error)
	// PruneAutomationRuns deletes finished runs older than before.
	PruneAutomationRuns(ctx context.Context, before time.Time) error
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
