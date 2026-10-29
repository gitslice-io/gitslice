package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/authz"
	"gitslice.io/gitslice/internal/objectstore/filesystem"
	gspaths "gitslice.io/gitslice/internal/paths"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/triggers"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// maxTriggerFileBytes bounds a file a trigger reads through the API.
const maxTriggerFileBytes = 4 << 20

// TreeFiles reads files of a tree at canonical paths (/account/dir/file),
// for triggers (design/34_triggers.md).
type TreeFiles struct {
	Repository storage.RepositoryStore
	Objects    ObjectStore
}

func (t TreeFiles) ReadFile(ctx context.Context, rootTreeID, p string) ([]byte, error) {
	if t.Repository == nil || t.Objects == nil {
		return nil, fmt.Errorf("tree reader is not configured")
	}
	entry, err := t.Repository.GetFileAtTree(ctx, rootTreeID, p)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, triggers.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if entry.Size > maxTriggerFileBytes {
		return nil, fmt.Errorf("%s is larger than %d MiB", p, maxTriggerFileBytes>>20)
	}
	rc, err := t.Objects.Get(ctx, filesystem.BlobKey(entry.ContentHash), 0, 0)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, maxTriggerFileBytes))
}

// TriggerService shows triggers and their runs, and runs them by hand.
type TriggerService struct {
	Auth       storage.AuthStore
	Changesets storage.ChangesetStore
	Slices     storage.SliceStore
	Repository storage.RepositoryStore
	Triggers   storage.TriggerStore
	Tree       TreeFiles
	Engine     *triggers.Engine
}

func (s *TriggerService) ready() error {
	if s == nil || s.Triggers == nil || s.Engine == nil {
		return status.Error(codes.Unavailable, "triggers are not enabled on this server")
	}
	return nil
}

func (s *TriggerService) ListTriggers(ctx context.Context, req *corev1.ListTriggersRequest) (*corev1.ListTriggersResponse, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	folder, err := gspaths.CanonicalPrefix(strings.TrimSpace(req.GetPath()))
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "path: %v", err)
	}
	account := strings.Split(strings.Trim(folder, "/"), "/")[0]
	if err := authorizeAccount(ctx, s.Auth, subjectID, account, authz.ActionRead); err != nil {
		return nil, err
	}
	ref := strings.TrimSpace(req.GetRef())
	if ref == "" {
		ref = triggers.DefaultRef
	}
	head, err := s.Repository.GetRef(ctx, ref)
	if err != nil {
		return nil, grpcError(err)
	}
	tree, err := s.Repository.RootTreeForCommit(ctx, head.GetCommitId())
	if err != nil {
		return nil, grpcError(err)
	}
	matches, problems, err := triggers.List(ctx, s.Tree, tree, account, folder)
	if err != nil {
		return nil, grpcError(err)
	}
	out := &corev1.ListTriggersResponse{CommitId: head.GetCommitId()}
	for _, m := range matches {
		out.Triggers = append(out.Triggers, triggerDefinitionProto(m))
	}
	for _, p := range problems {
		out.Problems = append(out.Problems, &corev1.TriggerFileProblem{Folder: p.Folder, File: p.File, Message: p.Message})
	}
	return out, nil
}

func (s *TriggerService) ListTriggerRuns(ctx context.Context, req *corev1.ListTriggerRunsRequest) (*corev1.ListTriggerRunsResponse, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	subjectID := optionalSubject(ctx)
	filter := storage.TriggerRunFilter{
		Account:     strings.TrimSpace(req.GetAccount()),
		TriggerID:   strings.TrimSpace(req.GetTriggerId()),
		ChangesetID: strings.TrimSpace(req.GetChangesetId()),
		Limit:       int(req.GetLimit()),
	}
	switch {
	case filter.ChangesetID != "":
		cs, err := s.authorizedChangeset(ctx, subjectID, filter.ChangesetID, authz.ActionRead)
		if err != nil {
			return nil, err
		}
		filter.Account = cs.GetAuthoringSlice().GetAccount()
	case filter.Account != "":
		if err := authorizeAccount(ctx, s.Auth, subjectID, filter.Account, authz.ActionRead); err != nil {
			return nil, err
		}
	default:
		return nil, status.Error(codes.InvalidArgument, "pass an account or a changeset")
	}
	runs, err := s.Triggers.ListTriggerRuns(ctx, filter)
	if err != nil {
		return nil, grpcError(err)
	}
	out := &corev1.ListTriggerRunsResponse{}
	for _, run := range runs {
		out.Runs = append(out.Runs, triggerRunProto(run))
	}
	return out, nil
}

func (s *TriggerService) GetTriggerRun(ctx context.Context, req *corev1.GetTriggerRunRequest) (*corev1.TriggerRun, error) {
	run, err := s.authorizedRun(ctx, req.GetRunId(), authz.ActionRead)
	if err != nil {
		return nil, err
	}
	return triggerRunProto(*run), nil
}

func (s *TriggerService) ListTriggerRunLogs(ctx context.Context, req *corev1.ListTriggerRunLogsRequest) (*corev1.ListTriggerRunLogsResponse, error) {
	run, err := s.authorizedRun(ctx, req.GetRunId(), authz.ActionRead)
	if err != nil {
		return nil, err
	}
	logs, err := s.Triggers.ListTriggerRunLogs(ctx, run.ID, req.GetAfterSeq(), 1000)
	if err != nil {
		return nil, grpcError(err)
	}
	out := &corev1.ListTriggerRunLogsResponse{}
	for _, log := range logs {
		out.Logs = append(out.Logs, &corev1.TriggerRunLog{Seq: log.Seq, Stream: log.Stream, Chunk: log.Chunk, CreatedAt: formatTime(log.CreatedAt)})
	}
	return out, nil
}

func (s *TriggerService) RunTrigger(ctx context.Context, req *corev1.RunTriggerRequest) (*corev1.TriggerRun, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	account := strings.TrimSpace(req.GetAccount())
	if account == "" || strings.TrimSpace(req.GetTriggerId()) == "" {
		return nil, status.Error(codes.InvalidArgument, "account and trigger_id are required")
	}
	if err := authorizeAccount(ctx, s.Auth, subjectID, account, authz.ActionWrite); err != nil {
		return nil, err
	}
	envelope := map[string]any{"created_at": time.Now().UTC().Format(time.RFC3339)}
	if names, err := s.Auth.UsernamesForSubjects(ctx, []string{subjectID}); err == nil && names[subjectID] != "" {
		envelope["sender"] = map[string]any{"username": names[subjectID]}
	}
	if id := strings.TrimSpace(req.GetChangesetId()); id != "" {
		cs, err := s.authorizedChangeset(ctx, subjectID, id, authz.ActionRead)
		if err != nil {
			return nil, err
		}
		envelope["changeset"] = map[string]any{"id": cs.GetId(), "handle": storage.ShortChangesetID(cs.GetId()), "title": cs.GetTitle(),
			"status": cs.GetStatus(), "target_ref": cs.GetTargetRef(), "patchset_id": cs.GetCurrentPatchsetId(), "commit_id": cs.GetCommitId()}
	}
	body, _ := json.Marshal(envelope)
	run, err := s.Engine.NewManualRun(ctx, account, strings.TrimSpace(req.GetTriggerId()), strings.TrimSpace(req.GetRef()), strings.TrimSpace(req.GetChangesetId()), req.GetInputs(), subjectID, body)
	if err != nil {
		return nil, grpcError(err)
	}
	return triggerRunProto(*run), nil
}

func (s *TriggerService) RerunTriggerRun(ctx context.Context, req *corev1.RerunTriggerRunRequest) (*corev1.TriggerRun, error) {
	run, err := s.authorizedRun(ctx, req.GetRunId(), authz.ActionWrite)
	if err != nil {
		return nil, err
	}
	again, err := s.Engine.Rerun(ctx, *run)
	if err != nil {
		return nil, grpcError(err)
	}
	return triggerRunProto(*again), nil
}

func (s *TriggerService) CancelTriggerRun(ctx context.Context, req *corev1.CancelTriggerRunRequest) (*corev1.TriggerRun, error) {
	run, err := s.authorizedRun(ctx, req.GetRunId(), authz.ActionWrite)
	if err != nil {
		return nil, err
	}
	canceled, err := s.Engine.Cancel(ctx, *run, "canceled by hand")
	if errors.Is(err, storage.ErrConflict) {
		return nil, status.Errorf(codes.FailedPrecondition, "run %s has already finished", run.ID)
	}
	if err != nil {
		return nil, grpcError(err)
	}
	return triggerRunProto(*canceled), nil
}

// authorizedRun loads a run the caller may act on: through its changeset's
// slice when it has one, else through its account.
func (s *TriggerService) authorizedRun(ctx context.Context, runID string, action authz.Action) (*storage.TriggerRun, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	subjectID := optionalSubject(ctx)
	run, err := s.Triggers.GetTriggerRun(ctx, strings.TrimSpace(runID))
	if err != nil {
		return nil, grpcError(err)
	}
	if run.ChangesetID != "" {
		if _, err := s.authorizedChangeset(ctx, subjectID, run.ChangesetID, action); err != nil {
			return nil, err
		}
		return run, nil
	}
	if err := authorizeAccount(ctx, s.Auth, subjectID, run.Account, action); err != nil {
		return nil, err
	}
	return run, nil
}

func (s *TriggerService) authorizedChangeset(ctx context.Context, subjectID, changesetID string, action authz.Action) (*corev1.Changeset, error) {
	cs, err := s.Changesets.Get(ctx, changesetID)
	if err != nil {
		return nil, grpcError(err)
	}
	if _, err := resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, cs.GetAuthoringSlice(), action); err != nil {
		return nil, err
	}
	return cs, nil
}

func triggerDefinitionProto(m triggers.Match) *corev1.TriggerDefinition {
	t := m.Trigger
	out := &corev1.TriggerDefinition{
		Id: m.ID, Name: m.Name, Folder: m.Folder, On: t.On, Paths: t.Paths, Changes: t.Changes, Refs: t.Refs,
		Secrets: t.Secrets, Network: t.Network, Concurrency: t.Concurrency, TimeoutMs: t.Timeout.Milliseconds(),
		Retry: int32(t.Retry), Action: t.Action.Kind, Worker: t.Action.Worker,
	}
	for _, check := range t.Checks {
		out.Checks = append(out.Checks, triggers.CheckID(m.ID, check))
	}
	if t.Action.HTTP != nil {
		out.HttpUrl = t.Action.HTTP.URL
	}
	return out
}

func triggerRunProto(run storage.TriggerRun) *corev1.TriggerRun {
	return &corev1.TriggerRun{
		Id: run.ID, Account: run.Account, TriggerId: run.TriggerID, Folder: run.Folder, Action: run.Action,
		EventId: run.EventID, EventKind: run.EventKind, ChangesetId: run.ChangesetID, PatchsetId: run.PatchsetID,
		CommitId: run.CommitID, TargetRef: run.TargetRef, DefinitionCommitId: run.DefinitionCommitID,
		Status: run.Status, Attempt: int32(run.Attempt), MaxAttempts: int32(run.MaxAttempts), Error: run.Error,
		ResponseStatus: int32(run.ResponseStatus), CreatedAt: formatTime(run.CreatedAt), StartedAt: formatTime(run.StartedAt),
		FinishedAt: formatTime(run.FinishedAt), Deadline: formatTime(run.Deadline),
	}
}
