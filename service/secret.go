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

// SecretService manages an account's secrets (design/33_secrets.md). Only the
// account's owners and admins see or change them: a secret reaches the CI
// daemons of the slices in its scope, and whoever chooses the scope chooses
// who can read it.
type SecretService struct {
	Auth   storage.AuthStore
	Slices storage.SliceStore
}

func (s *SecretService) ListSecrets(ctx context.Context, req *corev1.ListSecretsRequest) (*corev1.ListSecretsResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	account, err := normalizeServiceSlug(req.GetAccount(), "account")
	if err != nil {
		return nil, err
	}
	if err := authorizeAccount(ctx, s.Auth, subjectID, account, authz.ActionAdmin); err != nil {
		return nil, err
	}
	secrets, err := s.Slices.ListSecrets(ctx, account)
	if err != nil {
		return nil, grpcError(err)
	}
	var subjects []string
	for _, secret := range secrets {
		subjects = append(subjects, secret.CreatedBy, secret.UpdatedBy)
	}
	names := s.usernames(ctx, subjects)
	out := &corev1.ListSecretsResponse{Secrets: make([]*corev1.Secret, 0, len(secrets))}
	for _, secret := range secrets {
		out.Secrets = append(out.Secrets, secretProto(secret, names))
	}
	return out, nil
}

func (s *SecretService) GetSecret(ctx context.Context, req *corev1.GetSecretRequest) (*corev1.Secret, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	secret, err := s.adminSecret(ctx, subjectID, req.GetId())
	if err != nil {
		return nil, err
	}
	return s.secretProto(ctx, *secret), nil
}

func (s *SecretService) CreateSecret(ctx context.Context, req *corev1.CreateSecretRequest) (*corev1.Secret, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	account, err := normalizeServiceSlug(req.GetAccount(), "account")
	if err != nil {
		return nil, err
	}
	if err := authorizeAccount(ctx, s.Auth, subjectID, account, authz.ActionAdmin); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.GetName())
	if !storage.ValidSliceSecretName(name) {
		return nil, status.Error(codes.InvalidArgument, "secret name must match ^[A-Z_][A-Z0-9_]*$, like CI_TOKEN")
	}
	if req.GetScope() == nil {
		return nil, status.Error(codes.InvalidArgument, "scope is required: all slices, or at least one slice")
	}
	scope, err := s.resolveScope(ctx, account, req.GetScope())
	if err != nil {
		return nil, err
	}
	secret, err := s.Slices.CreateSecret(ctx, account, name, req.GetValue(), scope, subjectID)
	if err != nil {
		return nil, grpcError(err)
	}
	return s.secretProto(ctx, *secret), nil
}

func (s *SecretService) UpdateSecret(ctx context.Context, req *corev1.UpdateSecretRequest) (*corev1.Secret, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	secret, err := s.adminSecret(ctx, subjectID, req.GetId())
	if err != nil {
		return nil, err
	}
	var scope *storage.SecretScope
	if req.GetScope() != nil {
		resolved, err := s.resolveScope(ctx, secret.Account, req.GetScope())
		if err != nil {
			return nil, err
		}
		scope = &resolved
	}
	updated, err := s.Slices.UpdateSecret(ctx, secret.ID, req.Value, scope, subjectID)
	if err != nil {
		return nil, grpcError(err)
	}
	return s.secretProto(ctx, *updated), nil
}

func (s *SecretService) DeleteSecret(ctx context.Context, req *corev1.DeleteSecretRequest) (*corev1.Empty, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	secret, err := s.adminSecret(ctx, subjectID, req.GetId())
	if err != nil {
		return nil, err
	}
	if err := s.Slices.DeleteSecret(ctx, secret.ID); err != nil {
		return nil, grpcError(err)
	}
	return &corev1.Empty{}, nil
}

// adminSecret loads a secret of an account the caller administers. To anyone
// else it does not exist.
func (s *SecretService) adminSecret(ctx context.Context, subjectID, secretID string) (*storage.Secret, error) {
	secretID = strings.TrimSpace(secretID)
	if secretID == "" {
		return nil, status.Error(codes.InvalidArgument, "secret id is required")
	}
	secret, err := s.Slices.GetSecret(ctx, secretID)
	if err != nil {
		return nil, grpcError(err)
	}
	admin, err := canAuthorizeAccount(ctx, s.Auth, subjectID, secret.Account, authz.ActionAdmin)
	if err != nil {
		return nil, err
	}
	if !admin {
		return nil, status.Errorf(codes.NotFound, "secret %s not found", secretID)
	}
	return secret, nil
}

// resolveScope turns slice refs into the ids of slices of the account. A ref
// with no account means the secret's own.
func (s *SecretService) resolveScope(ctx context.Context, account string, scope *corev1.SecretScope) (storage.SecretScope, error) {
	if scope.GetAllSlices() {
		return storage.SecretScope{AllSlices: true}, nil
	}
	if len(scope.GetSlices()) == 0 {
		return storage.SecretScope{}, status.Error(codes.InvalidArgument, "choose all slices, or at least one slice")
	}
	out := storage.SecretScope{}
	for _, ref := range scope.GetSlices() {
		ref = &corev1.SliceRef{Account: ref.GetAccount(), Slice: ref.GetSlice()}
		if strings.TrimSpace(ref.Account) == "" {
			ref.Account = account
		}
		ref, err := normalizeServiceSliceRef(ref)
		if err != nil {
			return storage.SecretScope{}, err
		}
		if ref.Account != account {
			return storage.SecretScope{}, status.Errorf(codes.InvalidArgument, "slice %s/%s is not in account %s: a secret applies only to its own account's slices", ref.Account, ref.Slice, account)
		}
		slice, err := s.Slices.Resolve(ctx, ref)
		if err != nil {
			if status.Code(grpcError(err)) == codes.NotFound {
				return storage.SecretScope{}, status.Errorf(codes.InvalidArgument, "there is no slice %s/%s", ref.Account, ref.Slice)
			}
			return storage.SecretScope{}, grpcError(err)
		}
		out.SliceIDs = append(out.SliceIDs, slice.Id)
	}
	return out, nil
}

func (s *SecretService) usernames(ctx context.Context, subjects []string) map[string]string {
	if len(subjects) == 0 || s.Auth == nil {
		return nil
	}
	names, err := s.Auth.UsernamesForSubjects(ctx, subjects)
	if err != nil {
		return nil
	}
	return names
}

func (s *SecretService) secretProto(ctx context.Context, secret storage.Secret) *corev1.Secret {
	return secretProto(secret, s.usernames(ctx, []string{secret.CreatedBy, secret.UpdatedBy}))
}

func secretProto(secret storage.Secret, names map[string]string) *corev1.Secret {
	scope := &corev1.SecretScope{AllSlices: secret.AllSlices}
	if !secret.AllSlices {
		for _, slice := range secret.Slices {
			scope.Slices = append(scope.Slices, &corev1.SliceRef{Account: secret.Account, Slice: slice.Name})
		}
	}
	return &corev1.Secret{
		Id:        secret.ID,
		Account:   secret.Account,
		Name:      secret.Name,
		Scope:     scope,
		CreatedBy: names[secret.CreatedBy],
		UpdatedBy: names[secret.UpdatedBy],
		CreatedAt: webhookTime(secret.CreatedAt),
		UpdatedAt: webhookTime(secret.UpdatedAt),
	}
}

func sliceSecretProto(account string, secret storage.SliceSecret) *corev1.SliceSecret {
	out := &corev1.SliceSecret{Name: secret.Name, SecretId: secret.SecretID, AllSlices: secret.AllSlices}
	for _, slice := range secret.SharedWith {
		out.SharedWith = append(out.SharedWith, &corev1.SliceRef{Account: account, Slice: slice.Name})
	}
	return out
}
