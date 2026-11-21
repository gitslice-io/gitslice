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
	"gitslice.io/gitslice/internal/automations"
	"gitslice.io/gitslice/internal/gitslicecfg"
	gspaths "gitslice.io/gitslice/internal/paths"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// maxAutomationFileBytes bounds a file a trigger reads through the API.
const maxAutomationFileBytes = 4 << 20

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
	reader := newGitsliceTreeReader(t.Repository, t.Objects, maxAutomationFileBytes, "", automations.ErrNotFound)
	data, err := reader.ReadFile(ctx, rootTreeID, p)
	if errors.Is(err, gitslicecfg.ErrFileTooLarge) {
		return nil, fmt.Errorf("%s is larger than %d MiB", p, maxAutomationFileBytes>>20)
	}
	return data, err
}

// AutomationService shows triggers and their runs, and runs them by hand.
type AutomationService struct {
	Auth        storage.AuthStore
	Changesets  storage.ChangesetStore
	Slices      storage.SliceStore
	Repository  storage.RepositoryStore
	Automations storage.AutomationStore
	Tree        TreeFiles
	Engine      *automations.Engine
}

func (s *AutomationService) ready() error {
	if s == nil || s.Automations == nil || s.Engine == nil {
		return status.Error(codes.Unavailable, "triggers are not enabled on this server")
	}
	return nil
}

func (s *AutomationService) ListAutomations(ctx context.Context, req *corev1.ListAutomationsRequest) (*corev1.ListAutomationsResponse, error) {
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
	if err := s.ensureAutomationFolderAccess(ctx, subjectID, account, folder, authz.ActionRead); err != nil {
		return nil, err
	}
	ref := strings.TrimSpace(req.GetRef())
	if err := requireDefaultTargetRef(ref); err != nil {
		return nil, err
	}
	if ref == "" {
		ref = automations.DefaultRef
	}
	head, err := s.Repository.GetRef(ctx, ref)
	if err != nil {
		return nil, grpcError(err)
	}
	tree, err := s.Repository.RootTreeForCommit(ctx, head.GetCommitId())
	if err != nil {
		return nil, grpcError(err)
	}
	matches, problems, err := automations.List(ctx, s.Tree, tree, account, folder)
	if err != nil {
		return nil, grpcError(err)
	}
	out := &corev1.ListAutomationsResponse{CommitId: head.GetCommitId()}
	for _, m := range matches {
		m = s.enrichAutomationMatch(ctx, account, head.GetCommitId(), tree, m)
		out.Automations = append(out.Automations, automationDefinitionProto(m))
	}
	for _, p := range problems {
		out.Problems = append(out.Problems, &corev1.AutomationFileProblem{Folder: p.Folder, File: p.File, Message: p.Message})
	}
	return out, nil
}

func (s *AutomationService) ListAutomationRuns(ctx context.Context, req *corev1.ListAutomationRunsRequest) (*corev1.ListAutomationRunsResponse, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	subjectID := optionalSubject(ctx)
	filter := storage.AutomationRunFilter{
		Account:      strings.TrimSpace(req.GetAccount()),
		AutomationID: strings.TrimSpace(req.GetAutomationId()),
		ChangesetID:  strings.TrimSpace(req.GetChangesetId()),
		ParentRunID:  strings.TrimSpace(req.GetParentRunId()),
		Limit:        int(req.GetLimit()),
	}
	filterByFolder := false
	switch {
	case filter.ParentRunID != "":
		parent, err := s.authorizedRun(ctx, filter.ParentRunID, authz.ActionRead)
		if err != nil {
			return nil, err
		}
		filter.ParentRunID = parent.ID
		filter.Account = parent.Account
	case filter.ChangesetID != "":
		cs, err := s.authorizedChangeset(ctx, subjectID, filter.ChangesetID, authz.ActionRead)
		if err != nil {
			return nil, err
		}
		filter.Account = cs.GetAuthoringSlice().GetAccount()
	case filter.Account != "":
		if accountErr := authorizeAccount(ctx, s.Auth, subjectID, filter.Account, authz.ActionRead); accountErr != nil {
			if subjectID == "" {
				return nil, accountErr
			}
			if filter.AutomationID != "" {
				folder := automations.FolderOf(filter.Account, filter.AutomationID)
				if err := s.ensureAutomationFolderRunReadAccess(ctx, subjectID, filter.Account, folder); err != nil {
					return nil, err
				}
			} else {
				hasShared, err := s.hasSharedSliceInAccount(ctx, subjectID, filter.Account)
				if err != nil {
					return nil, err
				}
				if !hasShared {
					return nil, accountErr
				}
				filterByFolder = true
			}
		}
	default:
		return nil, status.Error(codes.InvalidArgument, "pass an account or a changeset")
	}
	runs, err := s.Automations.ListAutomationRuns(ctx, filter)
	if err != nil {
		return nil, grpcError(err)
	}
	allowedFolders := map[string]bool{}
	out := &corev1.ListAutomationRunsResponse{}
	for _, run := range runs {
		if filterByFolder {
			allowed, cached := allowedFolders[run.Folder]
			if !cached {
				allowed = s.ensureAutomationFolderRunReadAccess(ctx, subjectID, run.Account, run.Folder) == nil
				allowedFolders[run.Folder] = allowed
			}
			if !allowed {
				continue
			}
		}
		out.Runs = append(out.Runs, automationRunProto(run))
	}
	return out, nil
}

func (s *AutomationService) GetAutomationRun(ctx context.Context, req *corev1.GetAutomationRunRequest) (*corev1.AutomationRun, error) {
	run, err := s.authorizedRun(ctx, req.GetRunId(), authz.ActionRead)
	if err != nil {
		return nil, err
	}
	return automationRunProto(*run), nil
}

func (s *AutomationService) ListAutomationRunLogs(ctx context.Context, req *corev1.ListAutomationRunLogsRequest) (*corev1.ListAutomationRunLogsResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	run, err := s.authorizedRun(ctx, req.GetRunId(), authz.ActionRead)
	if err != nil {
		return nil, err
	}
	if err := s.ensureAutomationFolderAccess(ctx, subjectID, run.Account, run.Folder, authz.ActionRead); err != nil {
		return nil, err
	}
	logs, err := s.Automations.ListAutomationRunLogs(ctx, run.ID, req.GetAfterSeq(), 1000)
	if err != nil {
		return nil, grpcError(err)
	}
	out := &corev1.ListAutomationRunLogsResponse{}
	for _, log := range logs {
		out.Logs = append(out.Logs, &corev1.AutomationRunLog{Seq: log.Seq, Stream: log.Stream, Chunk: log.Chunk, CreatedAt: formatTime(log.CreatedAt)})
	}
	return out, nil
}

func (s *AutomationService) RunAutomation(ctx context.Context, req *corev1.RunAutomationRequest) (*corev1.AutomationRun, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	account := strings.TrimSpace(req.GetAccount())
	triggerID := strings.TrimSpace(req.GetAutomationId())
	if account == "" || triggerID == "" {
		return nil, status.Error(codes.InvalidArgument, "account and trigger_id are required")
	}
	ref := strings.TrimSpace(req.GetRef())
	if err := requireDefaultTargetRef(ref); err != nil {
		return nil, err
	}
	folder := automations.FolderOf(account, triggerID)
	if err := s.ensureAutomationFolderAccess(ctx, subjectID, account, folder, authz.ActionUseSecrets); err != nil {
		return nil, err
	}
	// Trying a definition from a changeset before it lands.
	definitionTree, definitionPatchset := "", ""
	if req.GetDefinitionFromChangeset() {
		id := strings.TrimSpace(req.GetChangesetId())
		if id == "" {
			return nil, status.Error(codes.InvalidArgument, "definition_from_changeset needs changeset_id")
		}
		cs, err := s.authorizedChangeset(ctx, subjectID, id, authz.ActionRead)
		if err != nil {
			return nil, err
		}
		switch cs.GetStatus() {
		case "submitted", "pending_publish", "abandoned":
			return nil, status.Errorf(codes.FailedPrecondition, "changeset %s is %s; only an open changeset's definitions can be tried", storage.ShortChangesetID(cs.GetId()), cs.GetStatus())
		}
		for _, ps := range cs.GetPatchsets() {
			if ps.GetId() == cs.GetCurrentPatchsetId() {
				definitionTree, definitionPatchset = ps.GetResultTreeId(), ps.GetId()
			}
		}
		if definitionTree == "" {
			return nil, status.Errorf(codes.FailedPrecondition, "changeset %s has no result tree for its current patchset", storage.ShortChangesetID(cs.GetId()))
		}
	}
	match, _, err := s.Engine.ResolveManual(ctx, account, triggerID, ref, definitionTree)
	if err != nil {
		return nil, grpcError(err)
	}
	if definitionTree != "" && (len(match.Automation.Secrets) > 0 || len(match.Automation.Reveal) > 0) {
		// An unreviewed script with secrets could send them anywhere; only
		// those who can read the secrets anyway may run one.
		if err := authorizeAccount(ctx, s.Auth, subjectID, account, authz.ActionAdmin); err != nil {
			return nil, status.Errorf(codes.PermissionDenied, "automation %s lists secrets; only an account admin can run it from an unlanded changeset", triggerID)
		}
	}
	if match.Folder != folder {
		if err := s.ensureAutomationFolderAccess(ctx, subjectID, account, match.Folder, authz.ActionUseSecrets); err != nil {
			return nil, err
		}
	}
	if match.Automation.Manual != nil && !match.Automation.Manual.AllowsWriter() {
		if err := authorizeAccount(ctx, s.Auth, subjectID, account, authz.ActionAdmin); err != nil {
			return nil, status.Errorf(codes.PermissionDenied, "automation %s requires an account admin to run manually", triggerID)
		}
	}
	triggeredBy, triggeredByKind := s.resolveActor(ctx, subjectID)
	if match.Automation.Manual != nil && match.Automation.Manual.RequireHuman && triggeredByKind == "agent" {
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
	if definitionPatchset != "" {
		envelope["definition"] = map[string]any{"changeset_id": strings.TrimSpace(req.GetChangesetId()), "patchset_id": definitionPatchset}
	}
	body, _ := json.Marshal(envelope)
	run, err := s.Engine.NewManualRun(ctx, account, triggerID, ref, strings.TrimSpace(req.GetChangesetId()), definitionTree, req.GetInputs(), subjectID, triggeredBy, triggeredByKind, body)
	if err != nil {
		return nil, grpcError(err)
	}
	return automationRunProto(*run), nil
}

func (s *AutomationService) resolveActor(ctx context.Context, subjectID string) (string, string) {
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

func (s *AutomationService) RerunAutomationRun(ctx context.Context, req *corev1.RerunAutomationRunRequest) (*corev1.AutomationRun, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	run, err := s.authorizedRun(ctx, req.GetRunId(), authz.ActionUseSecrets)
	if err != nil {
		return nil, err
	}
	if err := s.ensureAutomationFolderAccess(ctx, subjectID, run.Account, run.Folder, authz.ActionUseSecrets); err != nil {
		return nil, err
	}
	again, err := s.Engine.Rerun(ctx, *run)
	if err != nil {
		return nil, grpcError(err)
	}
	return automationRunProto(*again), nil
}

func (s *AutomationService) CancelAutomationRun(ctx context.Context, req *corev1.CancelAutomationRunRequest) (*corev1.AutomationRun, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	run, err := s.authorizedRun(ctx, req.GetRunId(), authz.ActionUseSecrets)
	if err != nil {
		return nil, err
	}
	if err := s.ensureAutomationFolderAccess(ctx, subjectID, run.Account, run.Folder, authz.ActionUseSecrets); err != nil {
		return nil, err
	}
	canceled, err := s.Engine.Cancel(ctx, *run, "canceled by hand")
	if errors.Is(err, storage.ErrConflict) {
		return nil, status.Errorf(codes.FailedPrecondition, "run %s has already finished", run.ID)
	}
	if err != nil {
		return nil, grpcError(err)
	}
	return automationRunProto(*canceled), nil
}

func (s *AutomationService) ensureAutomationFolderAccess(ctx context.Context, subjectID, account, folder string, action authz.Action) error {
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

// ensureAutomationFolderRunReadAccess authorizes reading non-changeset automation
// runs (and their logs) for folder: account readers pass directly, and shared
// slice members covering folder pass — requiring ActionUseSecrets (explicit
// writer/admin) on public slices so unshared outsiders cannot inspect manual or
// tag automation runs just because the slice is public.
func (s *AutomationService) ensureAutomationFolderRunReadAccess(ctx context.Context, subjectID, account, folder string) error {
	folder = strings.TrimSpace(folder)
	accountErr := authorizeAccount(ctx, s.Auth, subjectID, account, authz.ActionRead)
	if accountErr == nil {
		return nil
	}
	if folder == "" || subjectID == "" || s.Slices == nil {
		return accountErr
	}
	covering, err := s.Slices.CoveringIDsByPath(ctx, []string{folder})
	if err != nil {
		return grpcError(err)
	}
	for _, sliceID := range covering[folder] {
		slice, err := s.Slices.Get(ctx, sliceID)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				continue
			}
			return grpcError(err)
		}
		need := authz.ActionRead
		if strings.EqualFold(strings.TrimSpace(slice.GetDefinition().GetVisibility()), "public") {
			need = authz.ActionUseSecrets
		}
		if err := authorize(ctx, s.Auth, subjectID, slice, need); err == nil {
			return nil
		}
	}
	return accountErr
}

func (s *AutomationService) hasSharedSliceInAccount(ctx context.Context, subjectID, account string) (bool, error) {
	if subjectID == "" || s.Slices == nil {
		return false, nil
	}
	shared, err := s.Slices.ListShared(ctx, subjectID)
	if err != nil {
		return false, grpcError(err)
	}
	account = strings.TrimSpace(account)
	for _, entry := range shared {
		if entry.Slice != nil && strings.EqualFold(strings.TrimSpace(entry.Slice.GetRef().GetAccount()), account) {
			return true, nil
		}
	}
	return false, nil
}

// authorizedRun loads a run the caller may act on: through its changeset's
// slice when it has one, else through its account or covering shared slice.
func (s *AutomationService) authorizedRun(ctx context.Context, runID string, action authz.Action) (*storage.AutomationRun, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	subjectID := optionalSubject(ctx)
	run, err := s.Automations.GetAutomationRun(ctx, strings.TrimSpace(runID))
	if err != nil {
		return nil, grpcError(err)
	}
	if run.ChangesetID != "" {
		if _, err := s.authorizedChangeset(ctx, subjectID, run.ChangesetID, action); err != nil {
			return nil, err
		}
		return run, nil
	}
	if action == authz.ActionRead {
		if err := s.ensureAutomationFolderRunReadAccess(ctx, subjectID, run.Account, run.Folder); err != nil {
			return nil, err
		}
		return run, nil
	}
	if err := s.ensureAutomationFolderAccess(ctx, subjectID, run.Account, run.Folder, action); err != nil {
		return nil, err
	}
	return run, nil
}

func (s *AutomationService) authorizedChangeset(ctx context.Context, subjectID, changesetID string, action authz.Action) (*corev1.Changeset, error) {
	cs, err := s.Changesets.Get(ctx, changesetID)
	if err != nil {
		return nil, grpcError(err)
	}
	if _, err := resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, cs.GetAuthoringSlice(), action); err != nil {
		return nil, err
	}
	return cs, nil
}

func automationDefinitionProto(m automations.Match) *corev1.AutomationDefinition {
	t := m.Automation
	out := &corev1.AutomationDefinition{
		Id: m.ID, Name: m.Name, Folder: m.Folder, Description: t.Description, On: t.On, Paths: t.Paths, Changes: t.Changes, Refs: t.Refs,
		Secrets: t.Secrets, Reveal: t.Reveal, Network: t.Network, Egress: t.Egress, Concurrency: t.Concurrency, TimeoutMs: t.Timeout.Milliseconds(),
		Retry: int32(t.Retry), Action: t.Action.Kind, Worker: t.Action.Worker,
	}
	for _, check := range t.Checks {
		out.Checks = append(out.Checks, automations.CheckID(m.ID, check))
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
	if t.Action.Uses != nil {
		out.Uses = &corev1.AutomationUsesAction{
			Ref:  t.Action.Uses.Ref,
			With: t.Action.Uses.With,
		}
	}
	if len(t.Action.Steps) > 0 {
		for _, step := range t.Action.Steps {
			sd := &corev1.AutomationStepDefinition{
				Id:        step.ID,
				Name:      step.Name,
				Condition: step.If,
				Action:    step.Action.Kind,
				Worker:    step.Action.Worker,
				Checks:    step.Action.Check,
				TimeoutMs: step.Timeout.Milliseconds(),
			}
			if step.Action.Uses != nil {
				sd.Uses = &corev1.AutomationUsesAction{Ref: step.Action.Uses.Ref, With: step.Action.Uses.With}
			}
			if step.Action.Bash != nil {
				b := step.Action.Bash
				sd.Bash = &corev1.AutomationBashAction{
					Run: b.Run, Script: b.Script, Runtime: b.Runtime, Size: b.Size,
					Checkout: b.Checkout, Include: b.Include, WorkingDir: b.WorkingDir, Env: b.Env,
				}
			}
			if step.Action.HTTP != nil {
				sd.HttpUrl = step.Action.HTTP.URL
			}
			out.Steps = append(out.Steps, sd)
		}
	}
	if len(t.SecretBindings) > 0 {
		out.SecretBindings = t.SecretBindings
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

func automationRunProto(run storage.AutomationRun) *corev1.AutomationRun {
	var spec automations.RunSpec
	if len(run.Spec) > 0 {
		_ = json.Unmarshal(run.Spec, &spec)
	}
	return &corev1.AutomationRun{
		Id: run.ID, Account: run.Account, AutomationId: run.AutomationID, Folder: run.Folder, Action: run.Action,
		EventId: run.EventID, EventKind: run.EventKind, TriggeredBy: run.TriggeredBy, TriggeredByKind: run.TriggeredByKind,
		Inputs: spec.Inputs, ChangesetId: run.ChangesetID, PatchsetId: run.PatchsetID,
		CommitId: run.CommitID, TargetRef: run.TargetRef, DefinitionCommitId: run.DefinitionCommitID,
		Status: run.Status, Attempt: int32(run.Attempt), MaxAttempts: int32(run.MaxAttempts), Error: run.Error,
		ResponseStatus: int32(run.ResponseStatus), CreatedAt: formatTime(run.CreatedAt), StartedAt: formatTime(run.StartedAt),
		FinishedAt: formatTime(run.FinishedAt), Deadline: formatTime(run.Deadline),
		DetailsUrl: run.DetailsURL,
		TemplateId: run.TemplateID, TemplateCommitId: run.TemplateCommitID,
		ParentRunId: run.ParentRunID, StepIndex: int32(run.StepIndex), StepId: run.StepID,
		Outputs: run.Outputs,
	}
}
