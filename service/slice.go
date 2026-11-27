package service

import (
	"context"
	"strings"

	"gitslice.io/gitslice/internal/analytics"
	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/authz"
	"gitslice.io/gitslice/internal/gitmirror"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type SliceService struct {
	Auth       storage.AuthStore
	Repository storage.RepositoryStore
	Slices     storage.SliceStore
	Agents     storage.AgentStore
	Analytics  analytics.Client
	// GitMirrors is optional: without it the Git mirror calls are
	// unavailable (design/35_git_mirrors.md).
	GitMirrors      *GitMirrors
	gitMirrorRunner *gitmirror.Runner
}

func (s *SliceService) CreateSlice(ctx context.Context, req *corev1.CreateSliceRequest) (*corev1.Slice, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	ref, err := normalizeServiceSliceRef(req.Ref)
	if err != nil {
		return nil, err
	}
	if err := authorizeAccount(ctx, s.Auth, subjectID, ref.Account, authz.ActionAdmin); err != nil {
		return nil, err
	}
	def, err := s.Slices.ValidateSliceDefinition(ref, &corev1.SliceDefinition{
		IncludedPaths:          req.IncludedPaths,
		Visibility:             req.Visibility,
		RequiredApprovals:      req.RequiredApprovals,
		RequiredHumanApprovals: req.RequiredHumanApprovals,
		RequiredChecks:         req.RequiredChecks,
		Approvers:              req.Approvers,
	})
	if err != nil {
		return nil, grpcError(err)
	}
	if err := s.validateSliceApprovers(ctx, subjectID, ref.Account, def.Approvers); err != nil {
		return nil, err
	}
	if err := s.validateIncludedPathsExist(ctx, ref, def.IncludedPaths); err != nil {
		return nil, err
	}
	description, err := normalizeSliceDescription(req.Description)
	if err != nil {
		return nil, err
	}
	slice, err := s.Slices.CreateDefinition(ctx, subjectID, ref, def)
	if err != nil {
		return nil, grpcError(err)
	}
	if slice != nil && description != "" {
		if slice, err = s.Slices.SetDescription(ctx, slice.Id, description); err != nil {
			return nil, grpcError(err)
		}
	}
	if slice != nil {
		captureAnalytics(ctx, s.Analytics, analytics.EventSliceCreated, map[string]any{
			analytics.PropSliceID: slice.Id,
		})
	}
	return slice, nil
}

func (s *SliceService) ResolveSlice(ctx context.Context, req *corev1.ResolveSliceRequest) (*corev1.Slice, error) {
	subjectID := optionalSubject(ctx)
	ref, err := normalizeServiceSliceRef(req.Ref)
	if err != nil {
		return nil, err
	}
	return resolveAuthorizedSlice(ctx, s.Auth, s.Slices, subjectID, ref, authz.ActionRead)
}

func (s *SliceService) GetSlice(ctx context.Context, req *corev1.GetSliceRequest) (*corev1.Slice, error) {
	subjectID := optionalSubject(ctx)
	slice, err := s.Slices.Get(ctx, req.SliceId)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := authorize(ctx, s.Auth, subjectID, slice, authz.ActionRead); err != nil {
		return nil, err
	}
	return slice, nil
}

const (
	defaultSlicePageSize = 50
	maxSlicePageSize     = 100
)

// ListSlices lists an account's slices in name order, a page at a time (the
// cursor is the last name of the previous page). A member of the account sees
// all of them; anyone else, signed in or not, sees the public ones, which they
// could open anyway (an account page lists them). An account that does not
// exist is not found.
func (s *SliceService) ListSlices(ctx context.Context, req *corev1.ListSlicesRequest) (*corev1.ListSlicesResponse, error) {
	subjectID := optionalSubject(ctx)
	account, err := normalizeServiceSlug(req.Account, "account")
	if err != nil {
		return nil, err
	}
	kind, err := s.Auth.AccountKind(ctx, account)
	if err != nil {
		return nil, grpcError(err)
	}
	p, hasPrincipal := authctx.PrincipalFrom(ctx)
	scoped := hasPrincipal && p.KeyID != "" && p.SubjectID == subjectID && p.HasSliceScope
	member := false
	if subjectID != "" {
		memberCtx := ctx
		if scoped {
			// A slice-scoped key may only list slices inside its scope that the
			// underlying subject is allowed to read (including private slices of its
			// own account), never unrelated slices of the account.
			memberCtx = authctx.WithPrincipal(ctx, authctx.Principal{SubjectID: subjectID})
		}
		if member, err = canAuthorizeAccount(memberCtx, s.Auth, subjectID, account, authz.ActionRead); err != nil {
			return nil, err
		}
	}
	visibilityMode := "public"
	switch {
	case member:
		visibilityMode = "all"
	case subjectID != "":
		// Outside the account: the public slices and any shared with them.
		visibilityMode = "visible"
	}
	sortOrder, err := storage.NormalizeSliceSort(req.GetSort())
	if err != nil {
		return nil, grpcError(err)
	}
	pageSize := int(req.PageSize)
	if pageSize <= 0 {
		pageSize = defaultSlicePageSize
	}
	pageSize = min(pageSize, maxSlicePageSize)
	if scoped {
		if len(p.SliceIDs) == 0 {
			return &corev1.ListSlicesResponse{AccountKind: kind}, nil
		}
		authorizer := authz.New(s.Auth)
		var out []*corev1.Slice
		next := ""
		after := req.Cursor
		batchLimit := maxSlicePageSize
		for {
			batch, batchNext, err := s.Slices.ListSorted(ctx, account, visibilityMode, subjectID, after, batchLimit, req.IncludeArchived, sortOrder)
			if err != nil {
				return nil, grpcError(err)
			}
			for _, sl := range batch {
				if p.AllowsSlice(sl.GetId()) && authorizer.Authorize(ctx, subjectID, sl, authz.ActionRead) == nil {
					out = append(out, sl)
					if len(out) > pageSize {
						out = out[:pageSize]
						// Keep cursor from store when available; for name order slug is the cursor.
						if sortOrder == storage.SliceSortName {
							next = out[pageSize-1].GetRef().GetSlice()
						} else {
							next = batchNext
						}
						return &corev1.ListSlicesResponse{Slices: out, NextCursor: next, AccountKind: kind}, nil
					}
				}
			}
			if batchNext == "" || batchNext == after {
				break
			}
			after = batchNext
		}
		return &corev1.ListSlicesResponse{Slices: out, NextCursor: next, AccountKind: kind}, nil
	}
	slices, next, err := s.Slices.ListSorted(ctx, account, visibilityMode, subjectID, req.Cursor, pageSize, req.IncludeArchived, sortOrder)
	if err != nil {
		return nil, grpcError(err)
	}
	return &corev1.ListSlicesResponse{Slices: slices, NextCursor: next, AccountKind: kind}, nil
}

func (s *SliceService) ListSliceDefinitionVersions(ctx context.Context, req *corev1.ListSliceDefinitionVersionsRequest) (*corev1.ListSliceDefinitionVersionsResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	slice, err := s.Slices.Get(ctx, req.SliceId)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := authorize(ctx, s.Auth, subjectID, slice, authz.ActionRead); err != nil {
		return nil, err
	}
	versions, err := s.Slices.ListDefinitionVersions(ctx, req.SliceId, int(req.PageSize))
	if err != nil {
		return nil, grpcError(err)
	}
	return &corev1.ListSliceDefinitionVersionsResponse{Versions: versions}, nil
}

func (s *SliceService) UpdateSliceDefinition(ctx context.Context, req *corev1.UpdateSliceDefinitionRequest) (*corev1.SliceDefinition, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	current, err := s.Slices.Get(ctx, req.SliceId)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := authorizeSliceOwner(ctx, s.Auth, subjectID, current); err != nil {
		return nil, err
	}
	if req.Definition == nil {
		return nil, status.Error(codes.InvalidArgument, "slice definition is required")
	}
	def, err := s.Slices.ValidateSliceDefinition(current.Ref, req.Definition)
	if err != nil {
		return nil, grpcError(err)
	}
	if def.Approvers != "" && def.Approvers != current.GetDefinition().GetApprovers() {
		if err := s.validateSliceApprovers(ctx, subjectID, current.Ref.Account, def.Approvers); err != nil {
			return nil, err
		}
	}
	// The home slice's included paths are managed by account provisioning and
	// must not be edited; other definition fields (e.g. visibility) may change.
	if isHomeSlice(current.Ref) &&
		!sameStringSet(current.Definition.GetIncludedPaths(), def.IncludedPaths) {
		return nil, status.Error(codes.InvalidArgument, "the home slice's included paths cannot be changed")
	}
	if err := s.validateIncludedPathsExist(ctx, current.Ref, addedIncludedPaths(current.Definition, def.IncludedPaths)); err != nil {
		return nil, err
	}
	definition, err := s.Slices.UpdateDefinition(ctx, subjectID, req.SliceId, req.ExpectedDefinitionHash, def)
	if err != nil {
		return nil, grpcError(err)
	}
	return definition, nil
}

func (s *SliceService) SetSliceCIDaemon(ctx context.Context, req *corev1.SetSliceCIDaemonRequest) (*corev1.Slice, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	ref, err := normalizeServiceSliceRef(req.Slice)
	if err != nil {
		return nil, err
	}
	current, err := s.Slices.Resolve(ctx, ref)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := authorizeSliceOwner(ctx, s.Auth, subjectID, current); err != nil {
		return nil, err
	}
	// The slice's pool of CI daemons (design/32 layer 3): daemon_id alone
	// replaces it, add appends to it and remove takes one out.
	daemonID := strings.TrimSpace(req.DaemonId)
	pool := storage.CIDaemonPool(current)
	inPool := map[string]bool{}
	for _, id := range pool {
		inPool[id] = true
	}
	var next []string
	switch {
	case req.Add && req.Remove:
		return nil, status.Error(codes.InvalidArgument, "add and remove cannot both be set")
	case (req.Add || req.Remove) && daemonID == "":
		return nil, status.Error(codes.InvalidArgument, "daemon_id is required to add or remove a CI daemon")
	case req.Add:
		next = append(append(next, pool...), daemonID)
	case req.Remove:
		for _, id := range pool {
			if id != daemonID {
				next = append(next, id)
			}
		}
	case daemonID != "":
		next = []string{daemonID}
	}
	// A CI daemon runs the slice's checks and receives its secrets, so its
	// owner decides which slices may name it. The people who may name one are
	// those who manage it (they registered it, or own or administer the account
	// it is registered under), and anyone else for a daemon its owner exposed to
	// this slice. For any other daemon they get NotFound, as for an id that does
	// not exist. Removing one, or naming a daemon already in the pool, needs
	// nothing more.
	if daemonID != "" && !req.Remove && !inPool[daemonID] {
		if s.Agents == nil {
			return nil, status.Error(codes.FailedPrecondition, "agent daemons are not available")
		}
		_, _, err := manageableDaemon(ctx, s.Auth, s.Agents, subjectID, daemonID)
		switch {
		case err == nil:
			// Its owner is choosing it for their own slice: that exposes it.
			if err := s.Agents.ExposeDaemon(ctx, daemonID, current.Id, subjectID); err != nil {
				return nil, grpcError(err)
			}
		case status.Code(err) == codes.NotFound:
			// Not theirs: it must be exposed to this slice by its owner.
			exposed, exposedErr := s.Agents.DaemonExposedTo(ctx, daemonID, current.Id)
			if exposedErr != nil {
				return nil, grpcError(exposedErr)
			}
			if !exposed {
				return nil, err
			}
		default:
			return nil, err
		}
	}
	updated, err := s.Slices.SetCIDaemons(ctx, current.Id, next)
	if err != nil {
		return nil, grpcError(err)
	}
	return updated, nil
}

func (s *SliceService) GetAccountCIDaemons(ctx context.Context, req *corev1.GetAccountCIDaemonsRequest) (*corev1.AccountCIDaemonsResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	account := strings.ToLower(strings.TrimSpace(req.GetAccount()))
	if account == "" {
		return nil, status.Error(codes.InvalidArgument, "account is required")
	}
	if err := authorizeAccount(ctx, s.Auth, subjectID, account, authz.ActionRead); err != nil {
		return nil, err
	}
	ids, names, err := s.Slices.AccountCIDaemons(ctx, account)
	if err != nil {
		return nil, grpcError(err)
	}
	return &corev1.AccountCIDaemonsResponse{
		Account:     account,
		DaemonIds:   ids,
		DaemonNames: names,
	}, nil
}

func (s *SliceService) SetAccountCIDaemon(ctx context.Context, req *corev1.SetAccountCIDaemonRequest) (*corev1.AccountCIDaemonsResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireUnrestrictedKey(ctx, subjectID); err != nil {
		return nil, err
	}
	account := strings.ToLower(strings.TrimSpace(req.GetAccount()))
	if account == "" {
		return nil, status.Error(codes.InvalidArgument, "account is required")
	}
	if err := authorizeAccount(ctx, s.Auth, subjectID, account, authz.ActionAdmin); err != nil {
		return nil, err
	}
	pool, _, err := s.Slices.AccountCIDaemons(ctx, account)
	if err != nil {
		return nil, grpcError(err)
	}
	inPool := map[string]bool{}
	for _, id := range pool {
		inPool[id] = true
	}
	daemonID := strings.TrimSpace(req.GetDaemonId())
	var next []string
	switch {
	case req.GetAdd() && req.GetRemove():
		return nil, status.Error(codes.InvalidArgument, "add and remove cannot both be set")
	case (req.GetAdd() || req.GetRemove()) && daemonID == "":
		return nil, status.Error(codes.InvalidArgument, "daemon_id is required to add or remove a CI daemon")
	case req.GetAdd():
		next = append(append(next, pool...), daemonID)
	case req.GetRemove():
		for _, id := range pool {
			if id != daemonID {
				next = append(next, id)
			}
		}
	case daemonID != "":
		next = []string{daemonID}
	}
	if daemonID != "" && !req.GetRemove() && !inPool[daemonID] {
		if s.Agents == nil {
			return nil, status.Error(codes.FailedPrecondition, "agent daemons are not available")
		}
		if _, _, err := manageableDaemon(ctx, s.Auth, s.Agents, subjectID, daemonID); err != nil {
			return nil, err
		}
	}
	ids, names, err := s.Slices.SetAccountCIDaemons(ctx, account, next)
	if err != nil {
		return nil, grpcError(err)
	}
	return &corev1.AccountCIDaemonsResponse{
		Account:     account,
		DaemonIds:   ids,
		DaemonNames: names,
	}, nil
}

func (s *SliceService) SetSliceSecret(ctx context.Context, req *corev1.SetSliceSecretRequest) (*corev1.Empty, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	ref, err := normalizeServiceSliceRef(req.Slice)
	if err != nil {
		return nil, err
	}
	current, err := s.Slices.Resolve(ctx, ref)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := authorizeSliceOwner(ctx, s.Auth, subjectID, current); err != nil {
		return nil, err
	}
	if err := s.Slices.SetSliceSecret(ctx, current.Id, req.Name, req.Value, subjectID); err != nil {
		return nil, grpcError(err)
	}
	return &corev1.Empty{}, nil
}

func (s *SliceService) DeleteSliceSecret(ctx context.Context, req *corev1.DeleteSliceSecretRequest) (*corev1.Empty, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	ref, err := normalizeServiceSliceRef(req.Slice)
	if err != nil {
		return nil, err
	}
	current, err := s.Slices.Resolve(ctx, ref)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := authorizeSliceOwner(ctx, s.Auth, subjectID, current); err != nil {
		return nil, err
	}
	if err := s.Slices.DeleteSliceSecret(ctx, current.Id, req.Name, subjectID); err != nil {
		return nil, grpcError(err)
	}
	return &corev1.Empty{}, nil
}

func (s *SliceService) ListSliceSecrets(ctx context.Context, req *corev1.ListSliceSecretsRequest) (*corev1.ListSliceSecretsResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	ref, err := normalizeServiceSliceRef(req.Slice)
	if err != nil {
		return nil, err
	}
	current, err := s.Slices.Resolve(ctx, ref)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := authorizeAllowArchived(ctx, s.Auth, subjectID, current, authz.ActionUseSecrets); err != nil {
		return nil, err
	}
	secrets, err := s.Slices.ListSliceSecrets(ctx, current.Id)
	if err != nil {
		return nil, grpcError(err)
	}
	out := &corev1.ListSliceSecretsResponse{Names: make([]string, 0, len(secrets))}
	for _, secret := range secrets {
		out.Names = append(out.Names, secret.Name)
	}
	// Where each comes from (secret ids, the other slices) is for the
	// account's owners and admins, who manage them.
	admin, err := canAuthorizeAccount(ctx, s.Auth, subjectID, current.GetRef().GetAccount(), authz.ActionAdmin)
	if err != nil {
		return nil, err
	}
	if admin {
		for _, secret := range secrets {
			out.Secrets = append(out.Secrets, sliceSecretProto(secret))
		}
	}
	return out, nil
}

func (s *SliceService) DeleteSlice(ctx context.Context, req *corev1.DeleteSliceRequest) (*corev1.DeleteSliceResponse, error) {
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return nil, err
	}
	slice, err := s.Slices.Get(ctx, req.SliceId)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := authorizeSliceOwnerAllowArchived(ctx, s.Auth, subjectID, slice); err != nil {
		return nil, err
	}
	total, _, err := s.Slices.ChangesetCounts(ctx, req.SliceId)
	if err != nil {
		return nil, grpcError(err)
	}
	if total == 0 {
		if err := s.Slices.Delete(ctx, req.SliceId); err != nil {
			return nil, grpcError(err)
		}
		return &corev1.DeleteSliceResponse{SliceId: req.SliceId}, nil
	}
	if err := s.retireSlice(ctx, slice); err != nil {
		return nil, err
	}
	return &corev1.DeleteSliceResponse{SliceId: req.SliceId, HistoryKept: true}, nil
}
