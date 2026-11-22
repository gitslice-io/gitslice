package service

import (
	"context"
	"errors"
	"path"
	"strings"
	"unicode/utf8"

	"gitslice.io/gitslice/internal/automations"
	gspaths "gitslice.io/gitslice/internal/paths"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var triggerCheckStatuses = map[string]bool{
	"queued":   true,
	"running":  true,
	"passed":   true,
	"failed":   true,
	"errored":  true,
	"canceled": true,
	"skipped":  true,
}

func terminalCheck(status string) bool {
	return status != "queued" && status != "running"
}

func (s *AutomationRunService) CreateCheck(ctx context.Context, req *corev1.CreateAutomationCheckRequest) (*corev1.CheckRun, error) {
	call, err := s.call(ctx, req.GetRunToken())
	if err != nil {
		return nil, err
	}
	run := call.run
	name := strings.TrimSpace(req.GetName())
	listed := false
	for _, check := range call.spec.Automation.Checks {
		listed = listed || check == name
	}
	if !listed {
		return nil, status.Errorf(codes.PermissionDenied, "trigger %s does not list check %q in checks", run.AutomationID, name)
	}
	if run.ChangesetID == "" || run.PatchsetID == "" {
		return nil, status.Errorf(codes.FailedPrecondition, "a %s event has no changeset to report checks on", run.EventKind)
	}
	want := strings.TrimSpace(req.GetStatus())
	if want == "" {
		want = "queued"
	}
	if !triggerCheckStatuses[want] {
		return nil, status.Errorf(codes.InvalidArgument, "check status %q", want)
	}
	initial := want
	if terminalCheck(want) {
		// Only an update records a result for the submit gate.
		initial = "running"
	}
	check, err := s.Checks.CreateCheckRun(call.ctx, storage.CheckRunInput{
		ChangesetID: run.ChangesetID, PatchsetID: run.PatchsetID, CheckName: automations.CheckID(run.AutomationID, name),
		Provenance: "trigger", Status: initial, AutomationRunID: run.ID,
	})
	if err != nil {
		return nil, grpcError(err)
	}
	summary, url := req.GetSummary(), req.GetDetailsUrl()
	return s.applyCheck(call, check, want, &summary, &url, req.GetAnnotations(), true)
}

func (s *AutomationRunService) UpdateCheck(ctx context.Context, req *corev1.UpdateAutomationCheckRequest) (*corev1.CheckRun, error) {
	call, err := s.call(ctx, req.GetRunToken())
	if err != nil {
		return nil, err
	}
	check, err := s.Checks.GetCheckRun(ctx, strings.TrimSpace(req.GetCheckRunId()))
	if err != nil || check.GetAutomationRunId() != call.run.ID {
		return nil, status.Errorf(codes.NotFound, "run %s reported no check %s", call.run.ID, req.GetCheckRunId())
	}
	want := strings.TrimSpace(req.GetStatus())
	if want != "" && !triggerCheckStatuses[want] {
		return nil, status.Errorf(codes.InvalidArgument, "check status %q", want)
	}
	var summary, url *string
	if req.Summary != nil {
		summary = req.Summary
	}
	if req.DetailsUrl != nil {
		url = req.DetailsUrl
	}
	return s.applyCheck(call, check, want, summary, url, req.GetAnnotations(), len(req.GetAnnotations()) > 0)
}

// applyCheck sets a check's details and status. Setting the status a check
// already has is a no-op, so repeated callbacks are harmless.
func (s *AutomationRunService) applyCheck(call *runCall, check *corev1.CheckRun, want string, summary, url *string, annotations []*corev1.CheckAnnotation, setAnnotations bool) (*corev1.CheckRun, error) {
	ctx := call.ctx
	if summary != nil || url != nil || setAnnotations {
		details := storage.CheckRunDetails{DetailsURL: check.GetDetailsUrl(), Summary: check.GetSummary()}
		for _, a := range check.GetAnnotations() {
			details.Annotations = append(details.Annotations, storage.CheckAnnotation{Path: a.Path, StartLine: int(a.StartLine), EndLine: int(a.EndLine), Level: a.Level, Message: a.Message})
		}
		if summary != nil {
			details.Summary = truncate(*summary, maxTriggerSummary)
		}
		if url != nil {
			u := strings.TrimSpace(*url)
			if u != "" && !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
				return nil, status.Error(codes.InvalidArgument, "details_url must be an http(s) URL")
			}
			details.DetailsURL = u
		}
		if setAnnotations {
			details.Annotations = nil
			for _, a := range annotations {
				p := strings.TrimSpace(a.GetPath())
				if p != "" && !strings.HasPrefix(p, "/") {
					p = call.run.Folder + "/" + p
				}
				level := a.GetLevel()
				if level != "notice" && level != "warning" {
					level = "failure"
				}
				details.Annotations = append(details.Annotations, storage.CheckAnnotation{
					Path: path.Clean(p), StartLine: int(a.GetStartLine()), EndLine: int(a.GetEndLine()), Level: level, Message: truncate(a.GetMessage(), 2000),
				})
			}
		}
		updated, err := s.Checks.SetCheckRunDetails(ctx, check.GetId(), details)
		if err != nil {
			return nil, grpcError(err)
		}
		check = updated
	}
	if want != "" && want != check.GetStatus() {
		if terminalCheck(check.GetStatus()) {
			return nil, status.Errorf(codes.FailedPrecondition, "check %s already %s", check.GetCheckName(), check.GetStatus())
		}
		exitCode := int32(-1)
		switch want {
		case "passed":
			exitCode = 0
		case "failed":
			exitCode = 1
		}
		updated, err := s.Checks.UpdateCheckRunStatus(ctx, check.GetId(), want, exitCode, check.GetSummary())
		if err != nil {
			return nil, grpcError(err)
		}
		check = updated
		// Details went first, but a terminal update writes the summary again.
		if check.GetDetailsUrl() == "" && url != nil {
			check.DetailsUrl = *url
		}
	}
	s.finishIfDone(ctx, call.run)
	return check, nil
}

// finishIfDone ends a waiting run once every check it reported has a result.
func (s *AutomationRunService) finishIfDone(ctx context.Context, run *storage.AutomationRun) {
	s.Engine.FinishIfDone(ctx, run.ID)
}

// eventTree is the tree a run's event is about: the patchset's result (at
// "", "event", or "patchset"), its base tree (at "base"), or the current head
// of the run's target ref (at "main", default refs/global/main).
func (s *AutomationRunService) eventTree(ctx context.Context, run *storage.AutomationRun, at string) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(at))
	switch mode {
	case "", "event", "patchset", "base", "main":
	default:
		return "", status.Errorf(codes.InvalidArgument, "at %q (use event, patchset, base or main)", at)
	}
	if mode == "main" {
		ref := strings.TrimSpace(run.TargetRef)
		if ref == "" {
			ref = automations.DefaultRef
		}
		head, err := s.Repository.GetRef(ctx, ref)
		if err != nil {
			return "", grpcError(err)
		}
		tree, err := s.Repository.RootTreeForCommit(ctx, head.GetCommitId())
		if err != nil {
			return "", grpcError(err)
		}
		return tree, nil
	}
	patchsetID := strings.TrimSpace(run.PatchsetID)
	if run.ChangesetID != "" && (patchsetID != "" || mode == "patchset" || mode == "base") {
		cs, err := s.Changesets.Get(ctx, run.ChangesetID)
		if err != nil {
			return "", grpcError(err)
		}
		if patchsetID == "" {
			patchsetID = strings.TrimSpace(cs.GetCurrentPatchsetId())
		}
		for _, ps := range cs.GetPatchsets() {
			if ps.GetId() == patchsetID {
				if mode == "base" {
					if ps.GetBaseTreeId() != "" {
						return ps.GetBaseTreeId(), nil
					}
					if ps.GetBaseCommitId() != "" {
						tree, err := s.Repository.RootTreeForCommit(ctx, ps.GetBaseCommitId())
						if err != nil {
							return "", grpcError(err)
						}
						return tree, nil
					}
					break
				}
				if ps.GetResultTreeId() != "" {
					return ps.GetResultTreeId(), nil
				}
				break
			}
		}
	}
	commit := run.CommitID
	if commit == "" || mode == "base" {
		commit = run.DefinitionCommitID
	}
	tree, err := s.Repository.RootTreeForCommit(ctx, commit)
	if err != nil {
		return "", grpcError(err)
	}
	return tree, nil
}

func (s *AutomationRunService) triggerPath(run *storage.AutomationRun, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = "."
	}
	if !strings.HasPrefix(raw, "/") {
		raw = run.Folder + "/" + raw
	}
	p, err := gspaths.CanonicalPrefix(raw)
	if err != nil {
		return "", status.Errorf(codes.InvalidArgument, "path: %v", err)
	}
	if !gspaths.Contains("/"+run.Account, p) {
		return "", status.Errorf(codes.PermissionDenied, "%s is outside account %s", p, run.Account)
	}
	return p, nil
}

func (s *AutomationRunService) ReadFile(ctx context.Context, req *corev1.AutomationReadFileRequest) (*corev1.AutomationReadFileResponse, error) {
	call, err := s.call(ctx, req.GetRunToken())
	if err != nil {
		return nil, err
	}
	p, err := s.triggerPath(call.run, req.GetPath())
	if err != nil {
		return nil, err
	}
	tree, err := s.eventTree(ctx, call.run, req.GetAt())
	if err != nil {
		return nil, err
	}
	data, err := s.Tree.ReadFile(ctx, tree, p)
	if errors.Is(err, automations.ErrNotFound) {
		return &corev1.AutomationReadFileResponse{}, nil
	}
	if err != nil {
		return nil, grpcError(err)
	}
	return &corev1.AutomationReadFileResponse{Found: true, Content: data}, nil
}

func (s *AutomationRunService) ListDirectory(ctx context.Context, req *corev1.AutomationListDirectoryRequest) (*corev1.AutomationListDirectoryResponse, error) {
	call, err := s.call(ctx, req.GetRunToken())
	if err != nil {
		return nil, err
	}
	p, err := s.triggerPath(call.run, req.GetPath())
	if err != nil {
		return nil, err
	}
	tree, err := s.eventTree(ctx, call.run, req.GetAt())
	if err != nil {
		return nil, err
	}
	entries, err := s.Repository.ListDirectoryAtTree(ctx, tree, p)
	if errors.Is(err, storage.ErrNotFound) {
		return &corev1.AutomationListDirectoryResponse{}, nil
	}
	if err != nil {
		return nil, grpcError(err)
	}
	out := &corev1.AutomationListDirectoryResponse{Found: true}
	for _, entry := range entries {
		kind := "file"
		if entry.Kind == "tree" || entry.Kind == "dir" || entry.Kind == "directory" {
			kind = "directory"
		}
		full := entry.Path
		if full == "" {
			full = p + "/" + entry.Name
		}
		out.Entries = append(out.Entries, &corev1.AutomationDirectoryEntry{Name: entry.Name, Path: full, Kind: kind})
	}
	return out, nil
}

func (s *AutomationRunService) GetChangeset(ctx context.Context, req *corev1.AutomationRunTokenRequest) (*corev1.Changeset, error) {
	call, err := s.call(ctx, req.GetRunToken())
	if err != nil {
		return nil, err
	}
	if call.run.ChangesetID == "" {
		return nil, status.Errorf(codes.FailedPrecondition, "a %s event is not about a changeset", call.run.EventKind)
	}
	cs, err := s.Changesets.Get(ctx, call.run.ChangesetID)
	if err != nil {
		return nil, grpcError(err)
	}
	if csAccount := strings.TrimSpace(cs.GetAuthoringSlice().GetAccount()); csAccount != "" && !strings.EqualFold(csAccount, call.run.Account) {
		return nil, status.Errorf(codes.PermissionDenied, "changeset %s is outside account %s", cs.GetId(), call.run.Account)
	}
	storage.PopulateChangesetHandles(cs)
	if s.ChangesetService != nil {
		if err := s.ChangesetService.resolveAuthors(ctx, cs); err != nil {
			return nil, grpcError(err)
		}
	}
	return cs, nil
}

func (s *AutomationRunService) DiffChangeset(ctx context.Context, req *corev1.AutomationDiffChangesetRequest) (*corev1.DiffChangesetResponse, error) {
	call, err := s.call(ctx, req.GetRunToken())
	if err != nil {
		return nil, err
	}
	if call.run.ChangesetID == "" {
		return nil, status.Errorf(codes.FailedPrecondition, "a %s event is not about a changeset", call.run.EventKind)
	}
	cs, err := s.Changesets.Get(ctx, call.run.ChangesetID)
	if err != nil {
		return nil, grpcError(err)
	}
	if csAccount := strings.TrimSpace(cs.GetAuthoringSlice().GetAccount()); csAccount != "" && !strings.EqualFold(csAccount, call.run.Account) {
		return nil, status.Errorf(codes.PermissionDenied, "changeset %s is outside account %s", cs.GetId(), call.run.Account)
	}
	var requestedPaths []string
	if len(req.GetPaths()) > 0 {
		requestedPaths = make([]string, 0, len(req.GetPaths()))
		for _, raw := range req.GetPaths() {
			p, pErr := s.triggerPath(call.run, raw)
			if pErr != nil {
				return nil, pErr
			}
			requestedPaths = append(requestedPaths, p)
		}
	}
	toSelector := strings.TrimSpace(call.run.PatchsetID)
	if toSelector == "" {
		toSelector = strings.TrimSpace(cs.GetCurrentPatchsetId())
	}
	diffSvc := s.ChangesetService
	if diffSvc == nil {
		diffSvc = &ChangesetService{
			changesetCore: &changesetCore{
				Changesets:  s.Changesets,
				Blobs:       s.Blobs,
				Repository:  s.Repository,
				Slices:      s.Slices,
				ObjectStore: s.Tree.Objects,
			},
		}
	}
	accountRoot := "/" + strings.Trim(call.run.Account, "/")
	return diffSvc.diffLoadedChangeset(ctx, cs, req.GetFromPatchset(), toSelector, requestedPaths, accountRoot, maxTriggerDiffBytes)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
