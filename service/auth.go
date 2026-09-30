package service

import (
	"context"
	"strings"
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
	// ClerkUsers reads verified emails for Clerk-authenticated callers during
	// agent claims. Nil when the server has no Clerk secret key.
	ClerkUsers ClerkUserDirectory
}

// ClerkUserDirectory looks up a Clerk user's verified email addresses.
type ClerkUserDirectory interface {
	VerifiedEmails(ctx context.Context, userID string) ([]string, error)
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
	// A claimed agent account is a membership but not the caller's own personal
	// account, so "needs username" asks the store for the personal account.
	usernames, err := s.Auth.UsernamesForSubjects(ctx, []string{subjectID})
	if err != nil {
		return nil, grpcError(err)
	}
	return &corev1.GetAuthStatusResponse{SubjectId: subjectID, Accounts: accounts, NeedsUsername: usernames[subjectID] == ""}, nil
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

// ListPendingClaims refreshes the caller's verified emails from their identity
// provider, then lists the agent registrations they may claim.
func (s *AuthService) ListPendingClaims(ctx context.Context, req *corev1.ListPendingClaimsRequest) (*corev1.ListPendingClaimsResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.refreshVerifiedEmails(ctx, subjectID); err != nil {
		return nil, err
	}
	claims, err := s.Auth.ListPendingClaims(ctx, subjectID)
	if err != nil {
		return nil, grpcError(err)
	}
	out := make([]*corev1.PendingClaim, 0, len(claims))
	for _, claim := range claims {
		out = append(out, &corev1.PendingClaim{
			AgentSubjectId:   claim.AgentSubjectID,
			AgentDisplayName: claim.AgentDisplayName,
			Account:          claim.Account,
			OwnerEmail:       claim.OwnerEmail,
			CreatedAt:        claim.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	return &corev1.ListPendingClaimsResponse{Claims: out}, nil
}

// AcceptClaim re-checks the caller's verified emails and makes them an owner of
// the agent's account. Agents cannot claim: they have no verified emails.
func (s *AuthService) AcceptClaim(ctx context.Context, req *corev1.AcceptClaimRequest) (*corev1.AcceptClaimResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	agentSubjectID := strings.TrimSpace(req.GetAgentSubjectId())
	if agentSubjectID == "" {
		return nil, status.Error(codes.InvalidArgument, "agent_subject_id is required")
	}
	if err := s.refreshVerifiedEmails(ctx, subjectID); err != nil {
		return nil, err
	}
	account, err := s.Auth.AcceptClaim(ctx, subjectID, agentSubjectID)
	if err != nil {
		return nil, grpcError(err)
	}
	captureAnalytics(ctx, s.Analytics, analytics.EventAgentClaimed, nil)
	return &corev1.AcceptClaimResponse{Account: account}, nil
}

// refreshVerifiedEmails syncs the caller's verified emails from Clerk. Service
// token subjects record theirs at sign-in; other subjects have none.
func (s *AuthService) refreshVerifiedEmails(ctx context.Context, subjectID string) error {
	provider, externalID, err := s.Auth.ExternalIdentity(ctx, subjectID)
	if err != nil {
		return grpcError(err)
	}
	if provider != storage.ProviderClerk || externalID == "" {
		return nil
	}
	if s.ClerkUsers == nil {
		return status.Error(codes.FailedPrecondition, "agent claims need CLERK_SECRET_KEY on the server to verify your email")
	}
	emails, err := s.ClerkUsers.VerifiedEmails(ctx, externalID)
	if err != nil {
		return status.Error(codes.Unavailable, "could not verify your email with the identity provider")
	}
	if err := s.Auth.SetVerifiedEmails(ctx, subjectID, storage.ProviderClerk, emails); err != nil {
		return grpcError(err)
	}
	return nil
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
