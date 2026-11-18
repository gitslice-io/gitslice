package service

import (
	"context"
	"errors"
	"path"
	"strings"
	"unicode/utf8"

	gspaths "gitslice.io/gitslice/internal/paths"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/triggers"
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

func (s *TriggerRunService) CreateCheck(ctx context.Context, req *corev1.CreateTriggerCheckRequest) (*corev1.CheckRun, error) {
	call, err := s.call(ctx, req.GetRunToken())
	if err != nil {
		return nil, err
	}
	run := call.run
	name := strings.TrimSpace(req.GetName())
	listed := false
	for _, check := range call.spec.Trigger.Checks {
		listed = listed || check == name
	}
	if !listed {
		return nil, status.Errorf(codes.PermissionDenied, "trigger %s does not list check %q in checks", run.TriggerID, name)
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
		ChangesetID: run.ChangesetID, PatchsetID: run.PatchsetID, CheckName: triggers.CheckID(run.TriggerID, name),
		Provenance: "trigger", Status: initial, TriggerRunID: run.ID,
	})
	if err != nil {
		return nil, grpcError(err)
	}
	summary, url := req.GetSummary(), req.GetDetailsUrl()
	return s.applyCheck(call, check, want, &summary, &url, req.GetAnnotations(), true)
}

func (s *TriggerRunService) UpdateCheck(ctx context.Context, req *corev1.UpdateTriggerCheckRequest) (*corev1.CheckRun, error) {
	call, err := s.call(ctx, req.GetRunToken())
	if err != nil {
		return nil, err
	}
	check, err := s.Checks.GetCheckRun(ctx, strings.TrimSpace(req.GetCheckRunId()))
	if err != nil || check.GetTriggerRunId() != call.run.ID {
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
func (s *TriggerRunService) applyCheck(call *runCall, check *corev1.CheckRun, want string, summary, url *string, annotations []*corev1.CheckAnnotation, setAnnotations bool) (*corev1.CheckRun, error) {
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
func (s *TriggerRunService) finishIfDone(ctx context.Context, run *storage.TriggerRun) {
	s.Engine.FinishIfDone(ctx, run.ID)
}

// eventTree is the tree a run's event is about: the patchset's result (or
// base, at "base"), else the commit, else the definition's commit.
func (s *TriggerRunService) eventTree(ctx context.Context, run *storage.TriggerRun, at string) (string, error) {
	if run.PatchsetID != "" {
		cs, err := s.Changesets.Get(ctx, run.ChangesetID)
		if err != nil {
			return "", err
		}
		for _, ps := range cs.GetPatchsets() {
			if ps.GetId() == run.PatchsetID {
				if at == "base" {
					return ps.GetBaseTreeId(), nil
				}
				return ps.GetResultTreeId(), nil
			}
		}
	}
	commit := run.CommitID
	if commit == "" || at == "base" {
		commit = run.DefinitionCommitID
	}
	return s.Repository.RootTreeForCommit(ctx, commit)
}

func (s *TriggerRunService) triggerPath(run *storage.TriggerRun, raw string) (string, error) {
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

func (s *TriggerRunService) ReadFile(ctx context.Context, req *corev1.TriggerReadFileRequest) (*corev1.TriggerReadFileResponse, error) {
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
		return nil, grpcError(err)
	}
	data, err := s.Tree.ReadFile(ctx, tree, p)
	if errors.Is(err, triggers.ErrNotFound) {
		return &corev1.TriggerReadFileResponse{}, nil
	}
	if err != nil {
		return nil, grpcError(err)
	}
	return &corev1.TriggerReadFileResponse{Found: true, Content: data}, nil
}

func (s *TriggerRunService) ListDirectory(ctx context.Context, req *corev1.TriggerListDirectoryRequest) (*corev1.TriggerListDirectoryResponse, error) {
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
		return nil, grpcError(err)
	}
	entries, err := s.Repository.ListDirectoryAtTree(ctx, tree, p)
	if errors.Is(err, storage.ErrNotFound) {
		return &corev1.TriggerListDirectoryResponse{}, nil
	}
	if err != nil {
		return nil, grpcError(err)
	}
	out := &corev1.TriggerListDirectoryResponse{Found: true}
	for _, entry := range entries {
		kind := "file"
		if entry.Kind == "tree" || entry.Kind == "dir" || entry.Kind == "directory" {
			kind = "directory"
		}
		full := entry.Path
		if full == "" {
			full = p + "/" + entry.Name
		}
		out.Entries = append(out.Entries, &corev1.TriggerDirectoryEntry{Name: entry.Name, Path: full, Kind: kind})
	}
	return out, nil
}

func (s *TriggerRunService) GetChangeset(ctx context.Context, req *corev1.TriggerRunTokenRequest) (*corev1.Changeset, error) {
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
	storage.PopulateChangesetHandles(cs)
	return cs, nil
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
