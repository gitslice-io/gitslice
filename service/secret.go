package service

import (
	"context"
	"log/slog"
	"strings"

	"gitslice.io/gitslice/internal/authz"
	"gitslice.io/gitslice/internal/paths"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SecretService manages an account's secrets (design/33_secrets.md). Only the
// account's owners and admins see or change them: a secret reaches the checks
// defined in its folders, and whoever chooses the folders chooses who can
// read it.
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
		return nil, status.Error(codes.InvalidArgument, "scope is required: at least one folder, such as /"+account)
	}
	secretPaths, err := s.resolveScope(ctx, account, req.GetScope())
	if err != nil {
		return nil, err
	}
	secret, err := s.Slices.CreateSecret(ctx, account, name, req.GetValue(), secretPaths, subjectID)
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
	var secretPaths []string
	if req.GetScope() != nil {
		if secretPaths, err = s.resolveScope(ctx, secret.Account, req.GetScope()); err != nil {
			return nil, err
		}
	}
	updated, err := s.Slices.UpdateSecret(ctx, secret.ID, req.Value, secretPaths, subjectID)
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

// maxRevealedSecrets bounds one reveal call.
const maxRevealedSecrets = 100

// RevealSecrets returns the values of the secrets that apply at a folder to
// the owners and admins of its account, the people who can already set them,
// so they can fill a local env file. Writers and slice admins only ever see
// values inside a check. Every call is logged with who asked, the folder and
// the names.
func (s *SecretService) RevealSecrets(ctx context.Context, req *corev1.RevealSecretsRequest) (*corev1.RevealSecretsResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	names, err := revealNames(req.GetNames())
	if err != nil {
		return nil, err
	}
	folder, err := paths.CanonicalPrefix(strings.TrimSpace(req.GetPath()))
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "path: %v", err)
	}
	account := strings.Split(strings.Trim(folder, "/"), "/")[0]
	if err := authorizeAccount(ctx, s.Auth, subjectID, account, authz.ActionAdmin); err != nil {
		return nil, err
	}
	values, err := s.Slices.SecretValuesAt(ctx, folder)
	if err != nil {
		return nil, grpcError(err)
	}
	out := &corev1.RevealSecretsResponse{Values: map[string]string{}}
	for _, name := range names {
		if value, ok := values[name]; ok {
			out.Values[name] = value
		} else {
			out.Missing = append(out.Missing, name)
		}
	}
	slog.InfoContext(ctx, "secrets revealed", "subject_id", subjectID, "path", folder, "names", strings.Join(names, ","), "missing", strings.Join(out.Missing, ","))
	return out, nil
}

// RevealSliceSecrets is RevealSecrets at the slice's included paths, for
// CLIs from before secrets moved to folders. A name whose value differs
// between the slice's paths is refused, since there is no single answer.
func (s *SecretService) RevealSliceSecrets(ctx context.Context, req *corev1.RevealSliceSecretsRequest) (*corev1.RevealSliceSecretsResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	ref, err := normalizeServiceSliceRef(req.GetSlice())
	if err != nil {
		return nil, err
	}
	names, err := revealNames(req.GetNames())
	if err != nil {
		return nil, err
	}
	slice, err := s.Slices.Resolve(ctx, ref)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := authorizeSliceOwner(ctx, s.Auth, subjectID, slice); err != nil {
		return nil, err
	}
	values := map[string]string{}
	from := map[string]string{}
	for _, folder := range slice.GetDefinition().GetIncludedPaths() {
		at, err := s.Slices.SecretValuesAt(ctx, folder)
		if err != nil {
			return nil, grpcError(err)
		}
		for name, value := range at {
			if prev, ok := values[name]; ok && prev != value {
				return nil, status.Errorf(codes.FailedPrecondition, "secret %s has different values at %s and %s; ask for one folder with gs secret env --path", name, from[name], folder)
			}
			values[name], from[name] = value, folder
		}
	}
	out := &corev1.RevealSliceSecretsResponse{Values: map[string]string{}}
	for _, name := range names {
		if value, ok := values[name]; ok {
			out.Values[name] = value
		} else {
			out.Missing = append(out.Missing, name)
		}
	}
	slog.InfoContext(ctx, "secrets revealed", "subject_id", subjectID, "slice", slice.GetRef().GetAccount()+"/"+slice.GetRef().GetSlice(), "names", strings.Join(names, ","), "missing", strings.Join(out.Missing, ","))
	return out, nil
}

// revealNames checks and dedupes the names a reveal call asks for.
func revealNames(in []string) ([]string, error) {
	var names []string
	seen := map[string]bool{}
	for _, name := range in {
		name = strings.TrimSpace(name)
		if !storage.ValidSliceSecretName(name) {
			return nil, status.Errorf(codes.InvalidArgument, "secret name %q must match ^[A-Z_][A-Z0-9_]*$", name)
		}
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil, status.Error(codes.InvalidArgument, "name at least one secret")
	}
	if len(names) > maxRevealedSecrets {
		return nil, status.Errorf(codes.InvalidArgument, "at most %d secrets at a time", maxRevealedSecrets)
	}
	return names, nil
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

// resolveScope turns a scope into the secret's folders: paths as given
// (absolute only), the account root for all_slices, and each listed slice's included paths. The
// store checks that they are canonical and inside the account.
func (s *SecretService) resolveScope(ctx context.Context, account string, scope *corev1.SecretScope) ([]string, error) {
	var out []string
	for _, p := range scope.GetPaths() {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// "gitslice/web" could mean /gitslice/web or a folder of the account,
		// so only absolute paths are taken.
		if !strings.HasPrefix(p, "/") {
			return nil, status.Errorf(codes.InvalidArgument, "secret path %q must be absolute, such as /%s/%s", p, account, strings.TrimPrefix(p, account+"/"))
		}
		out = append(out, p)
	}
	if scope.GetAllSlices() {
		out = append(out, "/"+account)
	}
	for _, ref := range scope.GetSlices() {
		ref = &corev1.SliceRef{Account: ref.GetAccount(), Slice: ref.GetSlice()}
		if strings.TrimSpace(ref.Account) == "" {
			ref.Account = account
		}
		ref, err := normalizeServiceSliceRef(ref)
		if err != nil {
			return nil, err
		}
		if ref.Account != account {
			return nil, status.Errorf(codes.InvalidArgument, "slice %s/%s is not in account %s: a secret applies only to its own account's folders", ref.Account, ref.Slice, account)
		}
		slice, err := s.Slices.Resolve(ctx, ref)
		if err != nil {
			if status.Code(grpcError(err)) == codes.NotFound {
				return nil, status.Errorf(codes.InvalidArgument, "there is no slice %s/%s", ref.Account, ref.Slice)
			}
			return nil, grpcError(err)
		}
		out = append(out, slice.GetDefinition().GetIncludedPaths()...)
	}
	if len(out) == 0 {
		return nil, status.Errorf(codes.InvalidArgument, "choose at least one folder, such as /%s", account)
	}
	normalized, err := storage.NormalizeSecretPaths(account, out)
	if err != nil {
		return nil, grpcError(err)
	}
	return normalized, nil
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
	return &corev1.Secret{
		Id:        secret.ID,
		Account:   secret.Account,
		Name:      secret.Name,
		Scope:     &corev1.SecretScope{Paths: append([]string(nil), secret.Paths...)},
		CreatedBy: names[secret.CreatedBy],
		UpdatedBy: names[secret.UpdatedBy],
		CreatedAt: webhookTime(secret.CreatedAt),
		UpdatedAt: webhookTime(secret.UpdatedAt),
	}
}

func sliceSecretProto(secret storage.SliceSecret) *corev1.SliceSecret {
	return &corev1.SliceSecret{Name: secret.Name, SecretId: secret.SecretID, Paths: append([]string(nil), secret.Paths...)}
}
