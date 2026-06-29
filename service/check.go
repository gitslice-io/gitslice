package service

import (
	"context"
	"time"

	"github.com/gitslice-io/gitslice/internal/authz"
	"github.com/gitslice-io/gitslice/internal/storage"
	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const checkRunPollInterval = 500 * time.Millisecond

type CheckService struct {
	Auth       storage.AuthStore
	Changesets storage.ChangesetStore
	Slices     storage.SliceStore
	Checks     storage.CheckStore
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
		if _, err := s.authorizedChangeset(ctx, subjectID, req.ChangesetId); err != nil {
			return nil, err
		}
		runs, err := s.Checks.ListCheckRuns(ctx, req.ChangesetId, req.PatchsetId)
		if err != nil {
			return nil, grpcError(err)
		}
		return &corev1.ListCheckRunsResponse{Runs: runs}, nil
	}

	runs, err := s.Checks.ListCheckRuns(ctx, "", req.PatchsetId)
	if err != nil {
		return nil, grpcError(err)
	}
	if len(runs) == 0 {
		return &corev1.ListCheckRunsResponse{}, nil
	}
	if _, err := s.authorizedChangeset(ctx, subjectID, runs[0].GetChangesetId()); err != nil {
		return nil, err
	}
	return &corev1.ListCheckRunsResponse{Runs: runs}, nil
}

func (s *CheckService) GetCheckRun(ctx context.Context, req *corev1.GetCheckRunRequest) (*corev1.CheckRun, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	run, err := s.Checks.GetCheckRun(ctx, req.RunId)
	if err != nil {
		return nil, grpcError(err)
	}
	if _, err := s.authorizedChangeset(ctx, subjectID, run.GetChangesetId()); err != nil {
		return nil, err
	}
	return run, nil
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
	run, err := s.Checks.GetCheckRun(ctx, req.RunId)
	if err != nil {
		return grpcError(err)
	}
	if _, err := s.authorizedChangeset(ctx, subjectID, run.GetChangesetId()); err != nil {
		return err
	}

	lastSeq := req.AfterSeq
	ticker := time.NewTicker(checkRunPollInterval)
	defer ticker.Stop()
	for {
		logs, err := s.Checks.ListCheckRunLogs(ctx, req.RunId, lastSeq)
		if err != nil {
			return grpcError(err)
		}
		for _, log := range logs {
			if err := stream.Send(log); err != nil {
				return grpcError(err)
			}
			lastSeq = log.GetSeq()
		}

		run, err = s.Checks.GetCheckRun(ctx, req.RunId)
		if err != nil {
			return grpcError(err)
		}
		if isTerminalServiceCheckRunStatus(run.GetStatus()) {
			return nil
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (s *CheckService) authorizedChangeset(ctx context.Context, subjectID, changesetID string) (*corev1.Changeset, error) {
	cs, err := s.Changesets.Get(ctx, changesetID)
	if err != nil {
		return nil, grpcError(err)
	}
	if cs.GetAuthoringSlice() == nil {
		return nil, status.Error(codes.FailedPrecondition, "changeset has no authoring slice")
	}
	if _, err := resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, cs.GetAuthoringSlice(), authz.ActionRead); err != nil {
		return nil, err
	}
	return cs, nil
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
