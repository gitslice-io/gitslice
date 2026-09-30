package service

import (
	"context"
	"time"

	"github.com/gitslice-io/gitslice/internal/analytics"
	"github.com/gitslice-io/gitslice/internal/authctx"
	"github.com/gitslice-io/gitslice/internal/storage"
	"github.com/gitslice-io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type AuthService struct {
	Auth      storage.AuthStore
	Analytics analytics.Client
	// AgentSignupEnabled gates the unauthenticated RegisterAgent RPC. Off by
	// default; see design/20_agent_signup_and_claim.md.
	AgentSignupEnabled bool
}

func (s *AuthService) StartCliLogin(ctx context.Context, req *corev1.StartCliLoginRequest) (*corev1.StartCliLoginResponse, error) {
	code, expiresAt, err := s.Auth.StartCliLogin(ctx)
	if err != nil {
		return nil, grpcError(err)
	}
	return &corev1.StartCliLoginResponse{
		Code:                code,
		ExpiresAt:           expiresAt.UTC().Format(time.RFC3339),
		PollIntervalSeconds: 2,
	}, nil
}

func (s *AuthService) PollCliLogin(ctx context.Context, req *corev1.PollCliLoginRequest) (*corev1.PollCliLoginResponse, error) {
	loginStatus, token, subjectID, err := s.Auth.PollCliLogin(ctx, req.Code)
	if err != nil {
		return nil, grpcError(err)
	}
	return &corev1.PollCliLoginResponse{
		Status:    loginStatus,
		Token:     token,
		SubjectId: subjectID,
	}, nil
}

func (s *AuthService) CompleteCliLogin(ctx context.Context, req *corev1.CompleteCliLoginRequest) (*corev1.CompleteCliLoginResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.Auth.CompleteCliLogin(ctx, req.Code, subjectID); err != nil {
		return nil, grpcError(err)
	}
	captureAnalytics(ctx, s.Analytics, analytics.EventCLILoginCompleted, nil)
	return &corev1.CompleteCliLoginResponse{SubjectId: subjectID}, nil
}

func (s *AuthService) GetAuthStatus(ctx context.Context, req *corev1.GetAuthStatusRequest) (*corev1.GetAuthStatusResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	accounts, err := s.Auth.ListSubjectAccountSlugs(ctx, subjectID)
	if err != nil {
		return nil, grpcError(err)
	}
	return &corev1.GetAuthStatusResponse{SubjectId: subjectID, Accounts: accounts, NeedsUsername: len(accounts) == 0}, nil
}

func (s *AuthService) CheckUsernameAvailable(ctx context.Context, req *corev1.CheckUsernameAvailableRequest) (*corev1.CheckUsernameAvailableResponse, error) {
	if _, err := requireSubject(ctx); err != nil {
		return nil, err
	}
	available, normalized, reason, err := s.Auth.UsernameAvailable(ctx, req.Username)
	if err != nil {
		return nil, grpcError(err)
	}
	return &corev1.CheckUsernameAvailableResponse{
		Available:  available,
		Normalized: normalized,
		Reason:     reason,
	}, nil
}

func (s *AuthService) ChooseUsername(ctx context.Context, req *corev1.ChooseUsernameRequest) (*corev1.ChooseUsernameResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	account, err := s.Auth.ChooseUsername(ctx, subjectID, req.Username)
	if err != nil {
		return nil, grpcError(err)
	}
	return &corev1.ChooseUsernameResponse{SubjectId: subjectID, Account: account}, nil
}

// RegisterAgent is unauthenticated: it creates a new agent subject rather than
// acting as an existing one. The server applies a dedicated per-IP limit.
func (s *AuthService) RegisterAgent(ctx context.Context, req *corev1.RegisterAgentRequest) (*corev1.RegisterAgentResponse, error) {
	if !s.AgentSignupEnabled {
		return nil, status.Error(codes.FailedPrecondition, "agent sign-up is disabled on this server")
	}
	agent, err := s.Auth.RegisterAgent(ctx, storage.RegisterAgentInput{
		Username:    req.GetUsername(),
		OwnerEmail:  req.GetOwnerEmail(),
		DisplayName: req.GetDisplayName(),
	})
	if err != nil {
		return nil, grpcError(err)
	}
	if s.Analytics != nil {
		s.Analytics.Capture(ctx, analytics.Event{Name: analytics.EventAgentRegistered, DistinctID: agent.SubjectID})
	}
	return &corev1.RegisterAgentResponse{
		SubjectId: agent.SubjectID,
		Account:   agent.Account,
		ApiKey:    agent.APIKey,
	}, nil
}

func requireSubject(ctx context.Context) (string, error) {
	subjectID, ok := authctx.SubjectID(ctx)
	if !ok {
		return "", status.Error(codes.Unauthenticated, "missing subject")
	}
	return subjectID, nil
}

func optionalSubject(ctx context.Context) string {
	subjectID, _ := authctx.SubjectID(ctx)
	return subjectID
}
