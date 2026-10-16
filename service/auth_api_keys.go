package service

import (
	"context"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// ListApiKeys lists an agent's API keys, never their secrets. The caller is the
// agent itself or an owner of the agent's account.
func (s *AuthService) ListApiKeys(ctx context.Context, req *corev1.ListApiKeysRequest) (*corev1.ListApiKeysResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	agentID, account, keys, err := s.Auth.ListAPIKeys(ctx, subjectID, req.GetAgent())
	if err != nil {
		return nil, grpcError(err)
	}
	out := make([]*corev1.ApiKey, 0, len(keys))
	for _, key := range keys {
		out = append(out, apiKeyToProto(key))
	}
	return &corev1.ListApiKeysResponse{AgentSubjectId: agentID, Account: account, Keys: out}, nil
}

// CreateApiKey issues a new API key for an agent. Its secret is in the
// response and nowhere else. This is how a lost key is replaced.
func (s *AuthService) CreateApiKey(ctx context.Context, req *corev1.CreateApiKeyRequest) (*corev1.CreateApiKeyResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	key, secret, agentID, account, err := s.Auth.CreateAPIKey(ctx, subjectID, req.GetAgent(), req.GetName())
	if err != nil {
		return nil, grpcError(err)
	}
	return &corev1.CreateApiKeyResponse{Key: apiKeyToProto(key), Secret: secret, AgentSubjectId: agentID, Account: account}, nil
}

// RevokeApiKey ends an agent's API key at once.
func (s *AuthService) RevokeApiKey(ctx context.Context, req *corev1.RevokeApiKeyRequest) (*corev1.RevokeApiKeyResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	key, err := s.Auth.RevokeAPIKey(ctx, subjectID, req.GetAgent(), req.GetKeyId())
	if err != nil {
		return nil, grpcError(err)
	}
	return &corev1.RevokeApiKeyResponse{Key: apiKeyToProto(key)}, nil
}

func apiKeyToProto(key storage.APIKey) *corev1.ApiKey {
	return &corev1.ApiKey{
		Id:         key.ID,
		Name:       key.Name,
		CreatedAt:  formatOptionalTime(key.CreatedAt),
		LastUsedAt: formatOptionalTime(key.LastUsedAt),
		ExpiresAt:  formatOptionalTime(key.ExpiresAt),
		RevokedAt:  formatOptionalTime(key.RevokedAt),
	}
}
