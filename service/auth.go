package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/analytics"
	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/authz"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type AuthService struct {
	Auth      storage.AuthStore
	Slices    storage.SliceStore
	Analytics analytics.Client
	// AgentSignupEnabled gates the unauthenticated RegisterAgent RPC. Off by
	// default; see design/20_agent_signup_and_claim.md.
	AgentSignupEnabled bool
	// ClerkUsers reads verified emails for Clerk-authenticated callers during
	// agent claims. Nil when the server has no Clerk secret key.
	ClerkUsers ClerkUserDirectory
	// OperatorSubjects may create organization accounts
	// (GITSLICE_OPERATOR_SUBJECTS).
	OperatorSubjects []string
	// ObjectStore holds uploaded account pictures. Without it, uploads are
	// refused and every account shows its generated picture.
	ObjectStore ObjectStore
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
	if err := requireUnrestrictedKey(ctx, subjectID); err != nil {
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
	username, err := newSubjectDirectory(s.Auth).username(ctx, subjectID)
	if err != nil {
		return nil, grpcError(err)
	}
	memberships, err := s.memberships(ctx, subjectID, username, accounts)
	if err != nil {
		return nil, err
	}
	return &corev1.GetAuthStatusResponse{SubjectId: subjectID, Accounts: accounts, NeedsUsername: username == "", Memberships: memberships}, nil
}

// memberships describes each of the subject's accounts: the subject's own
// personal account, an organization, or the personal account of an agent the
// subject claimed; and the subject's role in it. An account that vanished
// between the two reads is left out.
func (s *AuthService) memberships(ctx context.Context, subjectID, username string, accounts []string) ([]*corev1.AccountMembership, error) {
	out := make([]*corev1.AccountMembership, 0, len(accounts))
	for _, account := range accounts {
		kind, err := s.Auth.AccountKind(ctx, account)
		if errors.Is(err, storage.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, grpcError(err)
		}
		role, err := s.Auth.AccountRole(ctx, subjectID, account)
		if errors.Is(err, storage.ErrNotFound) || errors.Is(err, storage.ErrUnauthorized) {
			continue
		}
		if err != nil {
			return nil, grpcError(err)
		}
		if kind == storage.AccountKindPersonal && account != username {
			kind = "agent"
		}
		out = append(out, &corev1.AccountMembership{Account: account, Kind: kind, Role: strings.ToLower(role)})
	}
	return out, nil
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
	if err := requireUnrestrictedKey(ctx, subjectID); err != nil {
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
	var reqSliceAccount, reqSliceSlug, reqSliceRole string
	if req.GetRequestedSlice() != nil {
		ref, err := normalizeServiceSliceRef(req.GetRequestedSlice())
		if err != nil {
			return nil, err
		}
		reqSliceAccount = ref.GetAccount()
		reqSliceSlug = ref.GetSlice()
		roleInput := strings.TrimSpace(req.GetRequestedRole())
		if roleInput == "" {
			roleInput = "writer"
		}
		role, err := storage.NormalizeDelegatedAgentSliceRole("requested", roleInput)
		if err != nil {
			return nil, grpcError(err)
		}
		reqSliceRole = role
	} else if strings.TrimSpace(req.GetRequestedRole()) != "" {
		return nil, status.Error(codes.InvalidArgument, "requested_role requires requested_slice")
	}

	agent, err := s.Auth.RegisterAgent(ctx, storage.RegisterAgentInput{
		Username:              req.GetUsername(),
		OwnerEmail:            req.GetOwnerEmail(),
		DisplayName:           req.GetDisplayName(),
		RegistrationToken:     req.GetRegistrationToken(),
		RequestedSliceAccount: reqSliceAccount,
		RequestedSliceSlug:    reqSliceSlug,
		RequestedSliceRole:    reqSliceRole,
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
	if err := requireUnrestrictedKey(ctx, subjectID); err != nil {
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
		pc := &corev1.PendingClaim{
			AgentSubjectId:   claim.AgentSubjectID,
			AgentDisplayName: claim.AgentDisplayName,
			Account:          claim.Account,
			OwnerEmail:       claim.OwnerEmail,
			CreatedAt:        claim.CreatedAt.UTC().Format(time.RFC3339),
			RequestedRole:    claim.RequestedSliceRole,
		}
		if claim.RequestedSliceAccount != "" && claim.RequestedSliceSlug != "" {
			pc.RequestedSlice = &corev1.SliceRef{
				Account: claim.RequestedSliceAccount,
				Slice:   claim.RequestedSliceSlug,
			}
		}
		out = append(out, pc)
	}
	emails, err := s.Auth.VerifiedEmails(ctx, subjectID)
	if err != nil {
		return nil, grpcError(err)
	}
	return &corev1.ListPendingClaimsResponse{Claims: out, VerifiedEmails: emails}, nil
}

// AcceptClaim re-checks the caller's verified emails and makes them an owner of
// the agent's account. When grant_requested_slice is set and the claim named a
// slice, it also grants that slice role if the caller can manage its members.
func (s *AuthService) AcceptClaim(ctx context.Context, req *corev1.AcceptClaimRequest) (*corev1.AcceptClaimResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireUnrestrictedKey(ctx, subjectID); err != nil {
		return nil, err
	}
	agentSubjectID := strings.TrimSpace(req.GetAgentSubjectId())
	if agentSubjectID == "" {
		return nil, status.Error(codes.InvalidArgument, "agent_subject_id is required")
	}
	if err := s.refreshVerifiedEmails(ctx, subjectID); err != nil {
		return nil, err
	}

	var (
		grantSlice *corev1.Slice
		grantRole  string
	)
	if req.GetGrantRequestedSlice() {
		claims, err := s.Auth.ListPendingClaims(ctx, subjectID)
		if err != nil {
			return nil, grpcError(err)
		}
		for _, claim := range claims {
			if claim.AgentSubjectID != agentSubjectID {
				continue
			}
			if claim.RequestedSliceAccount != "" && claim.RequestedSliceSlug != "" {
				if s.Slices == nil {
					return nil, status.Error(codes.Internal, "slice store is not configured")
				}
				ref := &corev1.SliceRef{Account: claim.RequestedSliceAccount, Slice: claim.RequestedSliceSlug}
				slice, err := resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, ref, authz.ActionManageMembers)
				if err != nil {
					return nil, err
				}
				role := claim.RequestedSliceRole
				if role == "" {
					role = "writer"
				}
				grantSlice = slice
				grantRole = role
			}
			break
		}
	}

	account, err := s.Auth.AcceptClaim(ctx, subjectID, agentSubjectID)
	if err != nil {
		return nil, grpcError(err)
	}
	var grantedRef *corev1.SliceRef
	var grantedRole string
	if grantSlice != nil {
		if _, err := s.Slices.SetMember(ctx, grantSlice.GetId(), agentSubjectID, grantRole, subjectID); err != nil {
			return nil, grpcError(err)
		}
		grantedRef = grantSlice.GetRef()
		grantedRole = grantRole
	}
	captureAnalytics(ctx, s.Analytics, analytics.EventAgentClaimed, nil)
	return &corev1.AcceptClaimResponse{
		Account:      account,
		GrantedSlice: grantedRef,
		GrantedRole:  grantedRole,
	}, nil
}

// ListOwnedAgents lists the agents whose accounts the caller owns.
func (s *AuthService) ListOwnedAgents(ctx context.Context, req *corev1.ListOwnedAgentsRequest) (*corev1.ListOwnedAgentsResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	agents, err := s.Auth.ListOwnedAgents(ctx, subjectID)
	if err != nil {
		return nil, grpcError(err)
	}
	out := make([]*corev1.OwnedAgent, 0, len(agents))
	for _, agent := range agents {
		out = append(out, &corev1.OwnedAgent{
			AgentSubjectId:   agent.AgentSubjectID,
			AgentDisplayName: agent.AgentDisplayName,
			Account:          agent.Account,
			RegisteredAt:     formatOptionalTime(agent.RegisteredAt),
			ClaimedAt:        formatOptionalTime(agent.ClaimedAt),
			LastActiveAt:     formatOptionalTime(agent.LastActiveAt),
		})
	}
	return &corev1.ListOwnedAgentsResponse{Agents: out}, nil
}

// CreateDelegatedAgent creates an agent owned and controlled by the signed-in
// human caller, optionally adding it to a slice, and returns its initial API
// key once.
func (s *AuthService) CreateDelegatedAgent(ctx context.Context, req *corev1.CreateDelegatedAgentRequest) (*corev1.CreateDelegatedAgentResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireUnrestrictedKey(ctx, subjectID); err != nil {
		return nil, err
	}
	subjects, err := s.Auth.SubjectsByIDs(ctx, []string{subjectID})
	if err != nil {
		return nil, grpcError(err)
	}
	subj, ok := subjects[subjectID]
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "subject not found")
	}
	if subj.Kind != "user" {
		return nil, status.Error(codes.PermissionDenied, "only human users can create delegated agents")
	}

	var (
		sliceID   string
		sliceRef  *corev1.SliceRef
		sliceRole string
	)
	if req.GetSlice() != nil {
		if s.Slices == nil {
			return nil, status.Error(codes.Internal, "slice store is not configured")
		}
		ref, err := normalizeServiceSliceRef(req.GetSlice())
		if err != nil {
			return nil, err
		}
		slice, err := resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, ref, authz.ActionManageMembers)
		if err != nil {
			return nil, err
		}
		sliceID = slice.GetId()
		sliceRef = slice.GetRef()
		if sliceRole, err = storage.NormalizeDelegatedAgentSliceRole(sliceID, req.GetRole()); err != nil {
			return nil, grpcError(err)
		}
	} else if strings.TrimSpace(req.GetRole()) != "" {
		return nil, status.Error(codes.InvalidArgument, "role requires a slice")
	}

	res, err := s.Auth.CreateDelegatedAgent(ctx, storage.CreateDelegatedAgentInput{
		ActorID:     subjectID,
		Username:    req.GetUsername(),
		DisplayName: req.GetDisplayName(),
		SliceID:     sliceID,
		SliceRole:   sliceRole,
	})
	if err != nil {
		return nil, grpcError(err)
	}
	return &corev1.CreateDelegatedAgentResponse{
		AgentSubjectId: res.AgentSubjectID,
		Account:        res.Account,
		Key:            apiKeyToProto(res.Key, nil),
		Secret:         res.Secret,
		Slice:          sliceRef,
		Role:           res.Role,
	}, nil
}

func formatOptionalTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
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

func hasRestrictedKey(ctx context.Context, subjectID string) bool {
	p, ok := authctx.PrincipalFrom(ctx)
	return ok && strings.TrimSpace(p.SubjectID) == strings.TrimSpace(subjectID) && p.HasKeyGrant()
}

func requireUnrestrictedKey(ctx context.Context, subjectID string) error {
	if hasRestrictedKey(ctx, subjectID) {
		return status.Error(codes.PermissionDenied, "scoped or role-capped API keys cannot perform account-wide actions")
	}
	return nil
}
