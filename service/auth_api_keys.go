package service

import (
	"context"
	"fmt"
	"math"
	"slices"
	"time"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/authz"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ListApiKeys lists a user's or agent's API keys, never their secrets. When
// --agent is omitted, the caller lists its own keys (filtered to self and
// descendants if calling with an attenuated or child key).
func (s *AuthService) ListApiKeys(ctx context.Context, req *corev1.ListApiKeysRequest) (*corev1.ListApiKeysResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	agentID, account, keys, err := s.Auth.ListAPIKeys(ctx, subjectID, req.GetAgent())
	if err != nil {
		return nil, grpcError(err)
	}
	refs := s.resolveKeySliceRefs(ctx, keys, nil)
	out := make([]*corev1.ApiKey, 0, len(keys))
	for _, key := range keys {
		out = append(out, apiKeyToProto(key, refs))
	}
	return &corev1.ListApiKeysResponse{AgentSubjectId: agentID, Account: account, Keys: out}, nil
}

// CreateApiKey issues a new API key for the caller or a managed agent. Its
// secret is in the response and nowhere else. When the caller authenticates
// with an API key, the new key is recorded as a child key that cannot exceed its
// parent's scope, role, or expiry.
func (s *AuthService) CreateApiKey(ctx context.Context, req *corev1.CreateApiKeyRequest) (*corev1.CreateApiKeyResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	const maxAPIKeyExpirySeconds = math.MaxInt64 / int64(time.Second)
	if req.GetExpiresInSeconds() < 0 {
		return nil, status.Error(codes.InvalidArgument, "expires_in_seconds must be positive")
	}
	if req.GetExpiresInSeconds() > maxAPIKeyExpirySeconds {
		return nil, status.Errorf(codes.InvalidArgument, "expires_in_seconds must be %d or less", maxAPIKeyExpirySeconds)
	}
	maxRole, err := storage.NormalizeAPIKeyMaxRole(req.GetMaxRole())
	if err != nil {
		return nil, grpcError(err)
	}
	var expiresAt time.Time
	if req.GetExpiresInSeconds() > 0 {
		expiresAt = time.Now().UTC().Add(time.Duration(req.GetExpiresInSeconds()) * time.Second)
	}
	sliceIDs, hasSliceScope, preloadedRefs, err := s.resolveRequestedKeySlices(ctx, subjectID, req.GetSlices())
	if err != nil {
		return nil, err
	}
	parentKeyID := ""
	if p, ok := authctx.PrincipalFrom(ctx); ok && p.KeyID != "" && p.SubjectID == subjectID {
		parentKeyID = p.KeyID
	}
	key, secret, agentID, account, err := s.Auth.CreateAPIKey(ctx, storage.CreateAPIKeyInput{
		ActorID:        subjectID,
		AgentAccount:   req.GetAgent(),
		Name:           req.GetName(),
		HasSliceScope:  hasSliceScope,
		SliceIDs:       sliceIDs,
		MaxRole:        maxRole,
		ExpiresAt:      expiresAt,
		ParentKeyID:    parentKeyID,
		Purpose:        req.GetPurpose(),
		DaemonID:       req.GetDaemonId(),
		ConversationID: req.GetConversationId(),
	})
	if err != nil {
		return nil, grpcError(err)
	}
	refs := s.resolveKeySliceRefs(ctx, []storage.APIKey{key}, preloadedRefs)
	return &corev1.CreateApiKeyResponse{Key: apiKeyToProto(key, refs), Secret: secret, AgentSubjectId: agentID, Account: account}, nil
}

// RevokeApiKey ends an agent's or user's API key (and all descendant keys) at once.
func (s *AuthService) RevokeApiKey(ctx context.Context, req *corev1.RevokeApiKeyRequest) (*corev1.RevokeApiKeyResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	key, err := s.Auth.RevokeAPIKey(ctx, subjectID, req.GetAgent(), req.GetKeyId())
	if err != nil {
		return nil, grpcError(err)
	}
	refs := s.resolveKeySliceRefs(ctx, []storage.APIKey{key}, nil)
	return &corev1.RevokeApiKeyResponse{Key: apiKeyToProto(key, refs)}, nil
}

func (s *AuthService) resolveRequestedKeySlices(ctx context.Context, subjectID string, rawSlices []string) ([]string, bool, map[string]string, error) {
	if len(rawSlices) == 0 {
		return nil, false, nil, nil
	}
	if s.Slices == nil {
		return nil, false, nil, status.Error(codes.Internal, "slice store is not configured")
	}
	// Resolve using the caller's underlying identity so unreadable private slices
	// still return NotFound, while slices readable to the subject but excluded
	// from an attenuated parent API key fall through to PermissionDenied in store
	// attenuation.
	unattenuatedCtx := authctx.WithPrincipal(ctx, authctx.Principal{SubjectID: subjectID})
	sliceIDs := make([]string, 0, len(rawSlices))
	refs := make(map[string]string, len(rawSlices))
	for _, raw := range rawSlices {
		account, slug, ok := storage.SplitSliceHandle(raw)
		if !ok {
			return nil, false, nil, status.Errorf(codes.InvalidArgument, "invalid slice %q: expected <account>/<slice>", raw)
		}
		ref, err := normalizeServiceSliceRef(&corev1.SliceRef{Account: account, Slice: slug})
		if err != nil {
			return nil, false, nil, err
		}
		slice, err := resolveAuthorizedSlice(unattenuatedCtx, s.Auth, s.Slices, subjectID, ref, authz.ActionRead)
		if err != nil {
			if status.Code(err) == codes.PermissionDenied {
				return nil, false, nil, status.Errorf(codes.NotFound, "slice %s/%s not found", ref.Account, ref.Slice)
			}
			return nil, false, nil, err
		}
		sliceIDs = append(sliceIDs, slice.GetId())
		refs[slice.GetId()] = fmt.Sprintf("%s/%s", slice.GetRef().GetAccount(), slice.GetRef().GetSlice())
	}
	return sliceIDs, true, refs, nil
}

func (s *AuthService) resolveKeySliceRefs(ctx context.Context, keys []storage.APIKey, preloaded map[string]string) map[string]string {
	refs := make(map[string]string, len(preloaded))
	for id, ref := range preloaded {
		refs[id] = ref
	}
	if s.Slices == nil {
		return refs
	}
	for _, key := range keys {
		for _, id := range key.SliceIDs {
			if id == "" {
				continue
			}
			if _, ok := refs[id]; ok {
				continue
			}
			slice, err := s.Slices.Get(ctx, id)
			if err != nil || slice.GetRef() == nil {
				refs[id] = ""
				continue
			}
			account, slug := slice.GetRef().GetAccount(), slice.GetRef().GetSlice()
			if account != "" && slug != "" {
				refs[id] = fmt.Sprintf("%s/%s", account, slug)
			} else {
				refs[id] = ""
			}
		}
	}
	return refs
}

func apiKeyToProto(key storage.APIKey, sliceRefs map[string]string) *corev1.ApiKey {
	sliceIDs := slices.Clone(key.SliceIDs)
	var sliceNames []string
	if len(sliceIDs) > 0 {
		sliceNames = make([]string, 0, len(sliceIDs))
		for _, id := range sliceIDs {
			if ref := sliceRefs[id]; ref != "" {
				sliceNames = append(sliceNames, ref)
			}
		}
	}
	return &corev1.ApiKey{
		Id:             key.ID,
		Name:           key.Name,
		CreatedAt:      formatTime(key.CreatedAt),
		LastUsedAt:     formatTime(key.LastUsedAt),
		ExpiresAt:      formatTime(key.ExpiresAt),
		RevokedAt:      formatTime(key.RevokedAt),
		SliceIds:       sliceIDs,
		Slices:         sliceNames,
		MaxRole:        key.MaxRole,
		ParentKeyId:    key.ParentKeyID,
		Purpose:        key.Purpose,
		DaemonId:       key.DaemonID,
		ConversationId: key.ConversationID,
	}
}
