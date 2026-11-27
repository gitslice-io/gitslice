package service

import (
	"context"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/analytics"
	"gitslice.io/gitslice/internal/authz"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// CreateAgentInvite mints a short-lived, single-use join token ("gsi_...") that
// lets a coding agent register itself as a co-owned, controller-bound agent
// and optionally receive access to a slice in one step.
func (s *AuthService) CreateAgentInvite(ctx context.Context, req *corev1.CreateAgentInviteRequest) (*corev1.CreateAgentInviteResponse, error) {
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
		return nil, status.Error(codes.PermissionDenied, "only human users can create agent invites")
	}

	var (
		sliceID      string
		sliceAccount string
		sliceSlug    string
		sliceRole    string
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
		sliceAccount = slice.GetRef().GetAccount()
		sliceSlug = slice.GetRef().GetSlice()
		if sliceRole, err = storage.NormalizeDelegatedAgentSliceRole(sliceID, req.GetRole()); err != nil {
			return nil, grpcError(err)
		}
	} else if strings.TrimSpace(req.GetRole()) != "" {
		return nil, status.Error(codes.InvalidArgument, "role requires a slice")
	}

	ttl := storage.DefaultAgentInviteTTL
	if secs := req.GetExpiresInSeconds(); secs > 0 {
		ttl = time.Duration(secs) * time.Second
	} else if secs < 0 {
		return nil, status.Error(codes.InvalidArgument, "expires_in_seconds must be positive")
	}
	if ttl > storage.MaxAgentInviteTTL {
		return nil, status.Error(codes.InvalidArgument, "invite expiry cannot exceed 7 days")
	}

	res, err := s.Auth.CreateAgentInvite(ctx, storage.CreateAgentInviteInput{
		ActorID:           subjectID,
		SliceID:           sliceID,
		SliceAccount:      sliceAccount,
		SliceSlug:         sliceSlug,
		SliceRole:         sliceRole,
		SuggestedUsername: req.GetSuggestedUsername(),
		ExpiresAt:         time.Now().UTC().Add(ttl),
	})
	if err != nil {
		return nil, grpcError(err)
	}
	return &corev1.CreateAgentInviteResponse{
		Invite: agentInviteToProto(res.Invite),
		Token:  res.Token,
	}, nil
}

// ListAgentInvites lists agent join invites created by the signed-in human.
func (s *AuthService) ListAgentInvites(ctx context.Context, req *corev1.ListAgentInvitesRequest) (*corev1.ListAgentInvitesResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireUnrestrictedKey(ctx, subjectID); err != nil {
		return nil, err
	}
	invites, err := s.Auth.ListAgentInvites(ctx, subjectID)
	if err != nil {
		return nil, grpcError(err)
	}
	out := make([]*corev1.AgentInvite, 0, len(invites))
	for _, inv := range invites {
		out = append(out, agentInviteToProto(inv))
	}
	return &corev1.ListAgentInvitesResponse{Invites: out}, nil
}

// RevokeAgentInvite revokes an unused agent join invite.
func (s *AuthService) RevokeAgentInvite(ctx context.Context, req *corev1.RevokeAgentInviteRequest) (*corev1.Empty, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireUnrestrictedKey(ctx, subjectID); err != nil {
		return nil, err
	}
	if err := s.Auth.RevokeAgentInvite(ctx, subjectID, req.GetInviteId()); err != nil {
		return nil, grpcError(err)
	}
	return &corev1.Empty{}, nil
}

// JoinAgentInvite redeems a single-use human-minted join token ("gsi_...").
// Unauthenticated; protected by the server's per-IP signup rate limiter.
func (s *AuthService) JoinAgentInvite(ctx context.Context, req *corev1.JoinAgentInviteRequest) (*corev1.JoinAgentInviteResponse, error) {
	res, err := s.Auth.JoinAgentInvite(ctx, storage.JoinAgentInviteInput{
		Token:       req.GetToken(),
		Username:    req.GetUsername(),
		DisplayName: req.GetDisplayName(),
	})
	if err != nil {
		return nil, grpcError(err)
	}
	if s.Analytics != nil {
		s.Analytics.Capture(ctx, analytics.Event{Name: analytics.EventAgentRegistered, DistinctID: res.AgentSubjectID})
	}
	var sliceRef *corev1.SliceRef
	if res.SliceAccount != "" && res.SliceSlug != "" {
		sliceRef = &corev1.SliceRef{Account: res.SliceAccount, Slice: res.SliceSlug}
	}
	return &corev1.JoinAgentInviteResponse{
		AgentSubjectId: res.AgentSubjectID,
		Account:        res.Account,
		ApiKey:         res.APIKey,
		Slice:          sliceRef,
		Role:           res.SliceRole,
	}, nil
}

func agentInviteToProto(inv storage.AgentInvite) *corev1.AgentInvite {
	out := &corev1.AgentInvite{
		Id:                inv.ID,
		Role:              inv.SliceRole,
		SuggestedUsername: inv.SuggestedUsername,
		ExpiresAt:         formatTime(inv.ExpiresAt),
		CreatedAt:         formatTime(inv.CreatedAt),
		ConsumedAt:        formatTime(inv.ConsumedAt),
		RevokedAt:         formatTime(inv.RevokedAt),
		ConsumedByAccount: inv.ConsumedByAccount,
	}
	if inv.SliceAccount != "" && inv.SliceSlug != "" {
		out.Slice = &corev1.SliceRef{Account: inv.SliceAccount, Slice: inv.SliceSlug}
	}
	return out
}
