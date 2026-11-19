package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/authz"
	"gitslice.io/gitslice/internal/gitslicecfg"
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
	reader := newGitsliceTreeReader(t.Repository, t.Objects, maxTriggerFileBytes, "", triggers.ErrNotFound)
	data, err := reader.ReadFile(ctx, rootTreeID, p)
	if errors.Is(err, gitslicecfg.ErrFileTooLarge) {
		return nil, fmt.Errorf("%s is larger than %d MiB", p, maxTriggerFileBytes>>20)
	}
	return data, err
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
	if err := requireDefaultTargetRef(ref); err != nil {
		return nil, err
	}
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
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	run, err := s.authorizedRun(ctx, req.GetRunId(), authz.ActionRead)
	if err != nil {
		return nil, err
	}
	if err := s.ensureTriggerFolderAccess(ctx, subjectID, run.Account, run.Folder, authz.ActionRead); err != nil {
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
	triggerID := strings.TrimSpace(req.GetTriggerId())
	if account == "" || triggerID == "" {
		return nil, status.Error(codes.InvalidArgument, "account and trigger_id are required")
	}
	ref := strings.TrimSpace(req.GetRef())
	if err := requireDefaultTargetRef(ref); err != nil {
		return nil, err
	}
	folder := triggers.FolderOf(account, triggerID)
	if err := s.ensureTriggerFolderAccess(ctx, subjectID, account, folder, authz.ActionUseSecrets); err != nil {
		return nil, err
	}
	match, _, err := s.Engine.ResolveManual(ctx, account, triggerID, ref)
	if err != nil {
		return nil, grpcError(err)
	}
	if match.Folder != folder {
		if err := s.ensureTriggerFolderAccess(ctx, subjectID, account, match.Folder, authz.ActionUseSecrets); err != nil {
			return nil, err
		}
	}
	if match.Trigger.Manual != nil && !match.Trigger.Manual.AllowsWriter() {
		if err := authorizeAccount(ctx, s.Auth, subjectID, account, authz.ActionAdmin); err != nil {
			return nil, status.Errorf(codes.PermissionDenied, "automation %s requires an account admin to run manually", triggerID)
		}
	}
	triggeredBy, triggeredByKind := s.resolveActor(ctx, subjectID)
	if match.Trigger.Manual != nil && match.Trigger.Manual.RequireHuman && triggeredByKind == "agent" {
		return nil, status.Errorf(codes.PermissionDenied, "automation %s requires a human actor to run manually", triggerID)
	}
	envelope := map[string]any{"created_at": time.Now().UTC().Format(time.RFC3339)}
	if triggeredBy != "" {
		envelope["sender"] = map[string]any{"username": triggeredBy, "kind": triggeredByKind}
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
	run, err := s.Engine.NewManualRun(ctx, account, triggerID, ref, strings.TrimSpace(req.GetChangesetId()), req.GetInputs(), subjectID, triggeredBy, triggeredByKind, body)
	if err != nil {
		return nil, grpcError(err)
	}
	return triggerRunProto(*run), nil
}

func (s *TriggerService) resolveActor(ctx context.Context, subjectID string) (string, string) {
	kind := "human"
	if strings.HasPrefix(subjectID, "agent_") {
		kind = "agent"
	}
	username := ""
	if s.Auth != nil {
		if subjects, err := s.Auth.SubjectsByIDs(ctx, []string{subjectID}); err == nil {
			if subj, ok := subjects[subjectID]; ok {
				if subj.Kind == "agent" {
					kind = "agent"
				} else if subj.Kind != "" {
					kind = "human"
				}
			}
		}
	}
	username = newSubjectDirectory(s.Auth).bestEffortUsername(ctx, subjectID)
	if username == "" {
		username = subjectID
	}
	return username, kind
}

func (s *TriggerService) RerunTriggerRun(ctx context.Context, req *corev1.RerunTriggerRunRequest) (*corev1.TriggerRun, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	run, err := s.authorizedRun(ctx, req.GetRunId(), authz.ActionUseSecrets)
	if err != nil {
		return nil, err
	}
	if err := s.ensureTriggerFolderAccess(ctx, subjectID, run.Account, run.Folder, authz.ActionUseSecrets); err != nil {
		return nil, err
	}
	again, err := s.Engine.Rerun(ctx, *run)
	if err != nil {
		return nil, grpcError(err)
	}
	return triggerRunProto(*again), nil
}

func (s *TriggerService) CancelTriggerRun(ctx context.Context, req *corev1.CancelTriggerRunRequest) (*corev1.TriggerRun, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	run, err := s.authorizedRun(ctx, req.GetRunId(), authz.ActionUseSecrets)
	if err != nil {
		return nil, err
	}
	if err := s.ensureTriggerFolderAccess(ctx, subjectID, run.Account, run.Folder, authz.ActionUseSecrets); err != nil {
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

func (s *TriggerService) ensureTriggerFolderAccess(ctx context.Context, subjectID, account, folder string, action authz.Action) error {
	folder = strings.TrimSpace(folder)
	if folder == "" {
		return authorizeAccount(ctx, s.Auth, subjectID, account, action)
	}
	accountErr := authorizeAccount(ctx, s.Auth, subjectID, account, action)
	if accountErr == nil {
		return nil
	}
	if s.Slices == nil {
		return accountErr
	}
	covering, err := s.Slices.CoveringIDsByPath(ctx, []string{folder})
	if err != nil {
		return grpcError(err)
	}
	var archivedErr error
	for _, sliceID := range covering[folder] {
		slice, err := s.Slices.Get(ctx, sliceID)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				continue
			}
			return grpcError(err)
		}
		err = authorize(ctx, s.Auth, subjectID, slice, action)
		if err == nil {
			return nil
		}
		if status.Code(err) == codes.FailedPrecondition && archivedErr == nil {
			archivedErr = err
		}
	}
	if archivedErr != nil {
		return archivedErr
	}
	return accountErr
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
		Id: m.ID, Name: m.Name, Folder: m.Folder, Description: t.Description, On: t.On, Paths: t.Paths, Changes: t.Changes, Refs: t.Refs,
		Secrets: t.Secrets, Reveal: t.Reveal, Network: t.Network, Egress: t.Egress, Concurrency: t.Concurrency, TimeoutMs: t.Timeout.Milliseconds(),
		Retry: int32(t.Retry), Action: t.Action.Kind, Worker: t.Action.Worker,
	}
	for _, check := range t.Checks {
		out.Checks = append(out.Checks, triggers.CheckID(m.ID, check))
	}
	if t.Action.HTTP != nil {
		out.HttpUrl = t.Action.HTTP.URL
	}
	if t.Action.Bash != nil {
		b := t.Action.Bash
		out.Bash = &corev1.AutomationBashAction{
			Run: b.Run, Script: b.Script, Runtime: b.Runtime, Size: b.Size,
			Checkout: b.Checkout, Include: b.Include, WorkingDir: b.WorkingDir, Env: b.Env,
		}
	}
	if t.Manual != nil {
		out.Manual = &corev1.AutomationManualPolicy{
			Roles: t.Manual.Roles, RequireHuman: t.Manual.RequireHuman, Confirm: t.Manual.Confirm,
		}
	}
	if len(t.Inputs) > 0 {
		names := make([]string, 0, len(t.Inputs))
		for name := range t.Inputs {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			in := t.Inputs[name]
			out.Inputs = append(out.Inputs, &corev1.AutomationInputSpec{
				Name: name, Type: in.Type, Description: in.Description, Required: in.Required,
				DefaultValue: in.Default, Options: in.Options, Pattern: in.Pattern,
			})
		}
	}
	return out
}

func triggerRunProto(run storage.TriggerRun) *corev1.TriggerRun {
	var spec triggers.RunSpec
	if len(run.Spec) > 0 {
		_ = json.Unmarshal(run.Spec, &spec)
	}
	return &corev1.TriggerRun{
		Id: run.ID, Account: run.Account, TriggerId: run.TriggerID, Folder: run.Folder, Action: run.Action,
		EventId: run.EventID, EventKind: run.EventKind, TriggeredBy: run.TriggeredBy, TriggeredByKind: run.TriggeredByKind,
		Inputs: spec.Inputs, ChangesetId: run.ChangesetID, PatchsetId: run.PatchsetID,
		CommitId: run.CommitID, TargetRef: run.TargetRef, DefinitionCommitId: run.DefinitionCommitID,
		Status: run.Status, Attempt: int32(run.Attempt), MaxAttempts: int32(run.MaxAttempts), Error: run.Error,
		ResponseStatus: int32(run.ResponseStatus), CreatedAt: formatTime(run.CreatedAt), StartedAt: formatTime(run.StartedAt),
		FinishedAt: formatTime(run.FinishedAt), Deadline: formatTime(run.Deadline),
	}
}
