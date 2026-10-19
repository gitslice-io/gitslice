package service

import (
	"context"
	"time"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// standbyPollAfter is how long a detached daemon waits between polls. It sets
// how soon a queued check starts on an idle daemon, and it must stay well
// under storage.DaemonStaleAfter so a polling daemon keeps counting as online.
const standbyPollAfter = 30 * time.Second

// StandbyPoll keeps a detached checks-only daemon registered and online, and
// says whether check runs are queued for it. The daemon then connects, and
// Connect delivers them.
//
// A daemon that holds Connect open keeps the server busy around the clock, so
// a server that bills while it handles requests (Cloud Run) never gets to
// idle. A daemon that detaches when it has nothing to do costs one short
// request every poll.
func (s *AgentService) StandbyPoll(ctx context.Context, req *corev1.StandbyPollRequest) (*corev1.StandbyPollResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetRuntime() != ciDaemonRuntime {
		return nil, status.Error(codes.FailedPrecondition, "only a checks-only daemon (gs agent start --ci) can wait on standby; stay connected instead")
	}
	account := ""
	if accounts, err := s.Auth.ListSubjectAccountSlugs(ctx, subjectID); err == nil && len(accounts) > 0 {
		account = accounts[0]
	}
	daemon, err := s.Agents.RegisterDaemon(ctx, storage.AgentDaemonInput{
		SubjectID: subjectID,
		Account:   account,
		Name:      req.GetName(),
		Runtime:   req.GetRuntime(),
		Version:   req.GetVersion(),
	})
	if err != nil {
		return nil, grpcError(err)
	}
	pending := false
	if s.Checks != nil {
		for _, runStatus := range []string{"queued", "running"} {
			runs, err := s.Checks.ListRunsByDaemonStatus(ctx, daemon.Id, runStatus)
			if err != nil {
				return nil, grpcError(err)
			}
			if len(runs) > 0 {
				pending = true
				break
			}
		}
	}
	return &corev1.StandbyPollResponse{
		DaemonId:    daemon.Id,
		Pending:     pending,
		PollAfterMs: standbyPollAfter.Milliseconds(),
	}, nil
}
