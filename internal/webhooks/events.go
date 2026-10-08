// Package webhooks sends Gitslice events to the HTTPS endpoints that slices
// register (design/24_webhooks.md).
//
// Events come from thin wrappers around the stores (this file), so every path
// that creates a changeset, a patchset, a tag or a check result emits one,
// including Git pushes. A published commit's event is written by the store in
// the publishing transaction. The Dispatcher fans events out to the webhooks
// that want them and delivers them, signed and retried.
package webhooks

import (
	"context"
	"encoding/json"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// Emitter records an event for delivery. Flush delivers events that were
// recorded elsewhere, such as in a store's transaction.
type Emitter interface {
	Emit(ctx context.Context, event storage.WebhookEvent)
	Flush(ctx context.Context)
}

func actor(ctx context.Context, fallback string) string {
	if fallback != "" {
		return fallback
	}
	subject, _ := authctx.SubjectID(ctx)
	return subject
}

func data(fields map[string]string) string {
	raw, err := json.Marshal(fields)
	if err != nil {
		return ""
	}
	return string(raw)
}

// WrapChangesets emits changeset events around a changeset store.
func WrapChangesets(inner storage.ChangesetStore, emitter Emitter) storage.ChangesetStore {
	wrapped := &changesetStore{ChangesetStore: inner, emitter: emitter}
	// The repository service drains the derived indexes through the same
	// value; keep that working.
	if indexes, ok := inner.(storage.DerivedIndexStore); ok {
		return &changesetStoreWithIndexes{changesetStore: wrapped, DerivedIndexStore: indexes}
	}
	return wrapped
}

type changesetStore struct {
	storage.ChangesetStore
	emitter Emitter
}

type changesetStoreWithIndexes struct {
	*changesetStore
	storage.DerivedIndexStore
}

func (s *changesetStore) Create(ctx context.Context, subjectID string, req *corev1.CreateChangesetRequest) (*corev1.Changeset, error) {
	cs, err := s.ChangesetStore.Create(ctx, subjectID, req)
	if err == nil && cs != nil {
		s.emitter.Emit(ctx, storage.WebhookEvent{Kind: storage.WebhookEventChangesetCreated, ChangesetID: cs.Id, ActorSubjectID: actor(ctx, subjectID)})
	}
	return cs, err
}

func (s *changesetStore) AddPatchset(ctx context.Context, changesetID, expectedCurrentPatchsetID string, patchset *corev1.Patchset) (*corev1.Patchset, error) {
	added, err := s.ChangesetStore.AddPatchset(ctx, changesetID, expectedCurrentPatchsetID, patchset)
	if err == nil && added != nil {
		s.emitter.Emit(ctx, storage.WebhookEvent{Kind: storage.WebhookEventChangesetUpdated, ChangesetID: changesetID, PatchsetID: added.Id, ActorSubjectID: actor(ctx, "")})
	}
	return added, err
}

func (s *changesetStore) Approve(ctx context.Context, changesetID, subjectID string) (*corev1.ApproveChangesetResponse, error) {
	res, err := s.ChangesetStore.Approve(ctx, changesetID, subjectID)
	if err == nil {
		s.emitter.Emit(ctx, storage.WebhookEvent{Kind: storage.WebhookEventChangesetApproved, ChangesetID: changesetID, ActorSubjectID: actor(ctx, subjectID)})
	}
	return res, err
}

func (s *changesetStore) ReportCheckResult(ctx context.Context, changesetID, subjectID, checkName, status string) (*corev1.ReportCheckResultResponse, error) {
	res, err := s.ChangesetStore.ReportCheckResult(ctx, changesetID, subjectID, checkName, status)
	if err == nil && terminalCheckStatus(status) {
		s.emitter.Emit(ctx, storage.WebhookEvent{
			Kind:           storage.WebhookEventCheckRunCompleted,
			ChangesetID:    changesetID,
			ActorSubjectID: actor(ctx, subjectID),
			Data:           data(map[string]string{"name": checkName, "status": status}),
		})
	}
	return res, err
}

// SubmitWithCheckStatuses keeps the store's optional submit path (the
// changeset service looks for it).
func (s *changesetStore) SubmitWithCheckStatuses(ctx context.Context, changesetID, expectedCurrentPatchsetID string, extraCheckStatuses map[string]string) (*corev1.SubmitChangesetResponse, error) {
	if submitter, ok := s.ChangesetStore.(interface {
		SubmitWithCheckStatuses(context.Context, string, string, map[string]string) (*corev1.SubmitChangesetResponse, error)
	}); ok {
		return submitter.SubmitWithCheckStatuses(ctx, changesetID, expectedCurrentPatchsetID, extraCheckStatuses)
	}
	return s.ChangesetStore.Submit(ctx, changesetID, expectedCurrentPatchsetID)
}

// PublishPending lands submitted changesets. The store records their
// changeset.submitted events in the publishing transaction; deliver them.
func (s *changesetStore) PublishPending(ctx context.Context, limit int) (int, error) {
	published, err := s.ChangesetStore.PublishPending(ctx, limit)
	if published > 0 {
		s.emitter.Flush(ctx)
	}
	return published, err
}

func (s *changesetStore) Abandon(ctx context.Context, changesetID string) error {
	err := s.ChangesetStore.Abandon(ctx, changesetID)
	if err == nil {
		s.emitter.Emit(ctx, storage.WebhookEvent{Kind: storage.WebhookEventChangesetAbandoned, ChangesetID: changesetID, ActorSubjectID: actor(ctx, "")})
	}
	return err
}

// terminalCheckStatus reports whether a check status is final: reported
// results use pass/fail, check runs passed/failed/errored/skipped/canceled.
func terminalCheckStatus(status string) bool {
	switch status {
	case "pass", "fail", "passed", "failed", "errored", "skipped", "canceled", "success", "failure":
		return true
	}
	return false
}

// WrapChecks emits check_run.completed when a check run finishes.
func WrapChecks(inner storage.CheckStore, emitter Emitter) storage.CheckStore {
	return &checkStore{CheckStore: inner, emitter: emitter}
}

type checkStore struct {
	storage.CheckStore
	emitter Emitter
}

func (s *checkStore) UpdateCheckRunStatus(ctx context.Context, runID, status string, exitCode int32, summary string) (*corev1.CheckRun, error) {
	run, err := s.CheckStore.UpdateCheckRunStatus(ctx, runID, status, exitCode, summary)
	if err == nil && run != nil && terminalCheckStatus(run.Status) {
		s.emitter.Emit(ctx, storage.WebhookEvent{
			Kind:           storage.WebhookEventCheckRunCompleted,
			CheckRunID:     run.Id,
			ChangesetID:    run.ChangesetId,
			PatchsetID:     run.PatchsetId,
			ActorSubjectID: actor(ctx, ""),
		})
	}
	return run, err
}

// WrapSlices emits tag.created around a slice store.
func WrapSlices(inner storage.SliceStore, emitter Emitter) storage.SliceStore {
	return &sliceStore{SliceStore: inner, emitter: emitter}
}

type sliceStore struct {
	storage.SliceStore
	emitter Emitter
}

func (s *sliceStore) CreateTag(ctx context.Context, tag storage.SliceTag) (*storage.SliceTag, bool, error) {
	created, isNew, err := s.SliceStore.CreateTag(ctx, tag)
	// Creating an existing tag again (same commit) is not a new event.
	if err == nil && isNew && created != nil {
		s.emitter.Emit(ctx, storage.WebhookEvent{
			Kind:           storage.WebhookEventTagCreated,
			SliceID:        created.SliceID,
			TagName:        created.Name,
			CommitID:       created.CommitID,
			ActorSubjectID: actor(ctx, created.CreatedBy),
			Data:           data(map[string]string{"message": created.Message}),
		})
	}
	return created, isNew, err
}

// discard is an Emitter that drops events.
type discard struct{}

func (discard) Emit(context.Context, storage.WebhookEvent) {}
func (discard) Flush(context.Context)                      {}

// Discard drops every event.
var Discard Emitter = discard{}
