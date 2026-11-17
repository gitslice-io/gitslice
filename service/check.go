package service

import (
	"context"
	"strings"

	"gitslice.io/gitslice/internal/authz"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type CheckService struct {
	Auth       storage.AuthStore
	Changesets storage.ChangesetStore
	Slices     storage.SliceStore
	Repository storage.RepositoryStore
	Checks     storage.CheckStore
	// Agents and hub say whether a run's daemon is connected, so a queued
	// run can say what it waits for.
	Agents storage.AgentStore

	checkLogs  *checkLogHub
	dispatcher *checkDispatcher
	hub        *agentHub
}

// annotateDaemons fills in whether each run's daemon is online and when it
// was last seen. A lookup that fails leaves the run as stored.
func (s *CheckService) annotateDaemons(ctx context.Context, runs ...*corev1.CheckRun) {
	if s == nil || s.Agents == nil {
		return
	}
	seen := map[string]*corev1.AgentDaemon{}
	for _, run := range runs {
		daemonID := strings.TrimSpace(run.GetDaemonId())
		if daemonID == "" {
			continue
		}
		daemon, ok := seen[daemonID]
		if !ok {
			daemon, _ = s.Agents.GetDaemon(ctx, daemonID)
			seen[daemonID] = daemon
		}
		if daemon == nil {
			continue
		}
		run.DaemonLastSeenAt = daemon.GetLastSeenAt()
		run.DaemonOnline = daemon.GetStatus() == "online"
		if s.hub != nil {
			if _, connected := s.hub.daemon(daemonID); connected {
				run.DaemonOnline = true
			}
		}
	}
}

func (s *CheckService) ListCheckRuns(ctx context.Context, req *corev1.ListCheckRunsRequest) (*corev1.ListCheckRunsResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	if req.ChangesetId == "" && req.PatchsetId == "" {
		return nil, status.Error(codes.InvalidArgument, "changeset_id or patchset_id is required")
	}
	if req.ChangesetId != "" {
		if _, err := s.authorizedChangeset(ctx, subjectID, req.ChangesetId, authz.ActionRead); err != nil {
			return nil, err
		}
		runs, err := s.Checks.ListCheckRuns(ctx, req.ChangesetId, req.PatchsetId)
		if err != nil {
			return nil, grpcError(err)
		}
		s.annotateDaemons(ctx, runs...)
		return &corev1.ListCheckRunsResponse{Runs: runs}, nil
	}

	runs, err := s.Checks.ListCheckRuns(ctx, "", req.PatchsetId)
	if err != nil {
		return nil, grpcError(err)
	}
	if len(runs) == 0 {
		return &corev1.ListCheckRunsResponse{}, nil
	}
	if _, err := s.authorizedChangeset(ctx, subjectID, runs[0].GetChangesetId(), authz.ActionRead); err != nil {
		return nil, err
	}
	s.annotateDaemons(ctx, runs...)
	return &corev1.ListCheckRunsResponse{Runs: runs}, nil
}

func (s *CheckService) GetCheckRun(ctx context.Context, req *corev1.GetCheckRunRequest) (*corev1.CheckRun, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	runID := strings.TrimSpace(req.RunId)
	if runID == "" {
		return nil, status.Error(codes.InvalidArgument, "run_id is required")
	}
	run, err := s.Checks.GetCheckRun(ctx, runID)
	if err != nil {
		return nil, grpcError(err)
	}
	if _, err := s.authorizedChangeset(ctx, subjectID, run.GetChangesetId(), authz.ActionRead); err != nil {
		return nil, err
	}
	s.annotateDaemons(ctx, run)
	return run, nil
}

func (s *CheckService) RerunCheck(ctx context.Context, req *corev1.RerunCheckRequest) (*corev1.CheckRun, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	runID := strings.TrimSpace(req.RunId)
	if runID == "" {
		return nil, status.Error(codes.InvalidArgument, "run_id is required")
	}
	run, err := s.Checks.GetCheckRun(ctx, runID)
	if err != nil {
		return nil, grpcError(err)
	}
	cs, slice, err := s.authorizedChangesetAndSlice(ctx, subjectID, run.GetChangesetId(), authz.ActionReview)
	if err != nil {
		return nil, err
	}
	patchset := patchsetByID(cs, run.GetPatchsetId())
	if patchset == nil {
		return nil, grpcError(storage.ErrNotFound)
	}
	return s.dispatcher.rerunCheck(ctx, cs, slice, patchset, run)
}

// AllowPatchsetSecrets explicitly authorizes folder/account secrets for a
// specific patchset's CI runs and re-queues CI checks on that patchset
// (design/36 §1).
func (s *CheckService) AllowPatchsetSecrets(ctx context.Context, req *corev1.AllowPatchsetSecretsRequest) (*corev1.AllowPatchsetSecretsResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	changesetID := strings.TrimSpace(req.GetChangesetId())
	if changesetID == "" {
		return nil, status.Error(codes.InvalidArgument, "changeset_id is required")
	}
	cs, slice, err := s.authorizedChangesetAndSlice(ctx, subjectID, changesetID, authz.ActionReview)
	if err != nil {
		return nil, err
	}
	if cs.GetStatus() == "submitted" || cs.GetStatus() == "abandoned" {
		return nil, status.Errorf(codes.FailedPrecondition, "cannot allow secrets on %s changeset", cs.GetStatus())
	}

	var patchset *corev1.Patchset
	if patchsetID := strings.TrimSpace(req.GetPatchsetId()); patchsetID != "" {
		patchset = patchsetByID(cs, patchsetID)
		if patchset == nil {
			return nil, grpcError(storage.ErrNotFound)
		}
	} else {
		patchset = currentPatchset(cs)
		if patchset == nil {
			return nil, status.Error(codes.FailedPrecondition, "changeset has no patchset")
		}
	}

	repo := s.Repository
	if repo == nil && s.dispatcher != nil {
		repo = s.dispatcher.Repository
	}
	if touched := patchsetTouchedGitslicePath(ctx, repo, patchset); touched != "" {
		if err := authorizeAccount(ctx, s.Auth, subjectID, slice.GetRef().GetAccount(), authz.ActionAdmin); err != nil {
			return nil, err
		}
	}

	allowance, created, err := s.Checks.AllowPatchsetSecrets(ctx, cs.GetId(), patchset.GetId(), subjectID)
	if err != nil {
		return nil, grpcError(err)
	}

	var rerunChecks []*corev1.CheckRun
	if s.dispatcher != nil {
		rerunChecks, err = s.dispatcher.dispatchAllowedPatchsetChecks(ctx, cs, slice, patchset, created)
		if err != nil {
			return nil, err
		}
		s.annotateDaemons(ctx, rerunChecks...)
	}
	return &corev1.AllowPatchsetSecretsResponse{
		ChangesetId:        allowance.ChangesetID,
		PatchsetId:         allowance.PatchsetID,
		AllowedBySubjectId: allowance.AllowedBySubjectID,
		AllowedAt:          formatTime(allowance.CreatedAt),
		RerunChecks:        rerunChecks,
	}, nil
}

func (s *CheckService) StreamCheckRun(req *corev1.StreamCheckRunRequest, stream corev1.CheckService_StreamCheckRunServer) error {
	return s.streamCheckRun(req, stream)
}

func (s *CheckService) streamCheckRun(req *corev1.StreamCheckRunRequest, stream checkRunLogStream) error {
	ctx := stream.Context()
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return err
	}
	runID := strings.TrimSpace(req.RunId)
	if runID == "" {
		return status.Error(codes.InvalidArgument, "run_id is required")
	}
	run, err := s.Checks.GetCheckRun(ctx, runID)
	if err != nil {
		return grpcError(err)
	}
	if _, err := s.authorizedChangeset(ctx, subjectID, run.GetChangesetId(), authz.ActionRead); err != nil {
		return err
	}

	lastSeq := req.AfterSeq
	var (
		ch     <-chan *corev1.CheckRunLog
		cancel func()
	)
	if s.checkLogs != nil {
		ch, cancel = s.checkLogs.subscribe(runID)
		defer cancel()
	}
	if err := s.sendPersistedCheckLogs(ctx, runID, &lastSeq, stream); err != nil {
		return err
	}
	run, err = s.Checks.GetCheckRun(ctx, runID)
	if err != nil {
		return grpcError(err)
	}
	if isTerminalServiceCheckRunStatus(run.GetStatus()) {
		return nil
	}
	if ch == nil {
		return status.Error(codes.FailedPrecondition, "check log stream hub is not configured")
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case _, ok := <-ch:
			if !ok {
				return nil
			}
			if err := s.sendPersistedCheckLogs(ctx, runID, &lastSeq, stream); err != nil {
				return err
			}
			run, err = s.Checks.GetCheckRun(ctx, runID)
			if err != nil {
				return grpcError(err)
			}
			if isTerminalServiceCheckRunStatus(run.GetStatus()) {
				return nil
			}
		}
	}
}

func (s *CheckService) sendPersistedCheckLogs(ctx context.Context, runID string, lastSeq *int64, stream checkRunLogStream) error {
	logs, err := s.Checks.ListCheckRunLogs(ctx, runID, *lastSeq)
	if err != nil {
		return grpcError(err)
	}
	for _, log := range logs {
		if log.GetSeq() <= *lastSeq {
			continue
		}
		if err := stream.Send(log); err != nil {
			return grpcError(err)
		}
		*lastSeq = log.GetSeq()
	}
	return nil
}

func (s *CheckService) authorizedChangeset(ctx context.Context, subjectID, changesetID string, action authz.Action) (*corev1.Changeset, error) {
	cs, _, err := s.authorizedChangesetAndSlice(ctx, subjectID, changesetID, action)
	return cs, err
}

func (s *CheckService) authorizedChangesetAndSlice(ctx context.Context, subjectID, changesetID string, action authz.Action) (*corev1.Changeset, *corev1.Slice, error) {
	cs, err := s.Changesets.Get(ctx, changesetID)
	if err != nil {
		return nil, nil, grpcError(err)
	}
	if cs.GetAuthoringSlice() == nil {
		return nil, nil, status.Error(codes.FailedPrecondition, "changeset has no authoring slice")
	}
	slice, err := resolveAuthoringSlice(ctx, s.Slices, cs.GetAuthoringSlice(), action)
	if err != nil {
		return nil, nil, grpcError(err)
	}
	if err := authorize(ctx, s.Auth, subjectID, slice, action); err != nil {
		return nil, nil, err
	}
	return cs, slice, nil
}

type checkRunLogStream interface {
	Context() context.Context
	Send(*corev1.CheckRunLog) error
}

func isTerminalServiceCheckRunStatus(status string) bool {
	switch status {
	case "passed", "failed", "errored", "skipped", "canceled":
		return true
	default:
		return false
	}
}
