package authz

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

type Action string

const (
	ActionRead              Action = "read"
	ActionPropose           Action = "propose"
	ActionSubmit            Action = "submit"
	ActionReview            Action = "review"
	ActionTag               Action = "tag"
	ActionUseSecrets        Action = "use_secrets"
	ActionManageMembers     Action = "manage_members"
	ActionActOnAnyChangeset Action = "act_on_any_changeset"

	// ActionWrite and ActionAdmin remain for compatibility with existing callers;
	// prefer the named capabilities above for new call sites.
	ActionWrite Action = "write"
	ActionAdmin Action = "admin"
)

type Authorizer struct {
	Auth storage.AuthStore
}

func New(auth storage.AuthStore) Authorizer {
	return Authorizer{Auth: auth}
}

func (a Authorizer) Authorize(ctx context.Context, subjectID string, slice *corev1.Slice, action Action) error {
	return a.authorize(ctx, subjectID, slice, action, false)
}

func (a Authorizer) AuthorizeAllowArchived(ctx context.Context, subjectID string, slice *corev1.Slice, action Action) error {
	return a.authorize(ctx, subjectID, slice, action, true)
}

func keyGrantFor(ctx context.Context, subjectID string) (authctx.Principal, bool) {
	p, ok := authctx.PrincipalFrom(ctx)
	if !ok || p.KeyID == "" {
		return authctx.Principal{}, false
	}
	if strings.TrimSpace(p.SubjectID) != strings.TrimSpace(subjectID) {
		return authctx.Principal{}, false
	}
	return p, true
}

func (a Authorizer) authorize(ctx context.Context, subjectID string, slice *corev1.Slice, action Action, allowArchived bool) error {
	if slice == nil || slice.Ref == nil || slice.Ref.Account == "" || slice.Definition == nil {
		return storage.ErrInvalid
	}
	if !validAction(action) {
		return storage.ErrInvalid
	}
	grant, hasGrant := keyGrantFor(ctx, subjectID)
	if hasGrant {
		if !grant.AllowsSlice(slice.Id) {
			return storage.ErrUnauthorized
		}
		if grant.MaxRole != "" && !roleAllows(grant.MaxRole, action) {
			return storage.ErrUnauthorized
		}
	}
	visibility := strings.TrimSpace(slice.Definition.Visibility)
	if visibility == "" {
		visibility = "private"
	}
	if action == ActionRead && visibility == "public" {
		return nil
	}
	if strings.TrimSpace(subjectID) == "" {
		return storage.ErrUnauthenticated
	}
	// The account's role decides first; a role on the slice itself
	// (design/26_slice_members.md) can only add to it.
	allowed := false
	role, ok, err := a.accountRole(ctx, subjectID, slice.Ref.Account)
	if err != nil {
		return err
	}
	if ok {
		if hasGrant && grant.MaxRole != "" {
			role = minRole(role, grant.MaxRole)
		}
		if roleAllows(role, action) {
			allowed = true
		}
	}
	if !allowed {
		sliceRole, ok, err := a.SliceRole(ctx, subjectID, slice)
		if err != nil {
			return err
		}
		if ok {
			if hasGrant && grant.MaxRole != "" {
				sliceRole = minRole(sliceRole, grant.MaxRole)
			}
			if roleAllows(sliceRole, action) {
				allowed = true
			}
		}
	}
	if !allowed {
		return storage.ErrUnauthorized
	}
	if !allowArchived && action != ActionRead && slice.Archived {
		return fmt.Errorf("%w: slice %s/%s is archived and read-only; unarchive it to change it", storage.ErrConflict, slice.Ref.Account, slice.Ref.Slice)
	}
	return nil
}

// AuthorizeDelegated authorizes actorSubjectID on slice for action, falling
// back to delegatedSubjectID when actorSubjectID lacks direct slice access,
// while always enforcing actorSubjectID's credential grant as an upper bound.
func (a Authorizer) AuthorizeDelegated(ctx context.Context, actorSubjectID, delegatedSubjectID string, slice *corev1.Slice, action Action) error {
	if slice == nil || slice.Ref == nil || slice.Ref.Account == "" || slice.Definition == nil {
		return storage.ErrInvalid
	}
	if !validAction(action) {
		return storage.ErrInvalid
	}
	if grant, hasGrant := keyGrantFor(ctx, actorSubjectID); hasGrant {
		if !grant.AllowsSlice(slice.Id) {
			return storage.ErrUnauthorized
		}
		if grant.MaxRole != "" && !roleAllows(grant.MaxRole, action) {
			return storage.ErrUnauthorized
		}
	}
	actorErr := a.Authorize(ctx, actorSubjectID, slice, action)
	if actorErr == nil || !errors.Is(actorErr, storage.ErrUnauthorized) {
		return actorErr
	}
	return a.Authorize(ctx, delegatedSubjectID, slice, action)
}

// EffectiveRole returns the subject's higher role across the slice's account
// and the slice itself, bounded by any key grant in ctx, or ("", false, nil)
// when it holds neither or the key does not cover the slice.
func (a Authorizer) EffectiveRole(ctx context.Context, subjectID string, slice *corev1.Slice) (string, bool, error) {
	if slice == nil || strings.TrimSpace(subjectID) == "" {
		return "", false, nil
	}
	if a.Auth == nil {
		return "", false, storage.ErrInvalid
	}
	grant, hasGrant := keyGrantFor(ctx, subjectID)
	if hasGrant && !grant.AllowsSlice(slice.Id) {
		return "", false, nil
	}
	var acctRole string
	var acctOK bool
	if slice.Ref != nil && strings.TrimSpace(slice.Ref.Account) != "" {
		var err error
		acctRole, acctOK, err = a.accountRole(ctx, subjectID, slice.Ref.Account)
		if err != nil {
			return "", false, err
		}
	}
	sliceRole, sliceOK, err := a.SliceRole(ctx, subjectID, slice)
	if err != nil {
		return "", false, err
	}
	var effective string
	switch {
	case !acctOK && !sliceOK:
		return "", false, nil
	case acctOK && !sliceOK:
		effective = acctRole
	case !acctOK && sliceOK:
		effective = sliceRole
	default:
		effective = higherRole(acctRole, sliceRole)
	}
	if hasGrant && grant.MaxRole != "" {
		effective = minRole(effective, grant.MaxRole)
		if effective == "" {
			return "", false, nil
		}
	}
	return effective, true, nil
}

// SliceRole returns the highest role the subject was given on the slice itself
// directly or via an account group (not its role in the slice's account).
func (a Authorizer) SliceRole(ctx context.Context, subjectID string, slice *corev1.Slice) (string, bool, error) {
	if a.Auth == nil || slice == nil || strings.TrimSpace(slice.Id) == "" || strings.TrimSpace(subjectID) == "" {
		return "", false, nil
	}
	var best string
	var hasBest bool
	role, err := a.Auth.SliceRole(ctx, subjectID, slice.Id)
	if err != nil {
		if !errors.Is(err, storage.ErrNotFound) && !errors.Is(err, storage.ErrUnauthorized) {
			return "", false, err
		}
	} else {
		best = strings.ToLower(strings.TrimSpace(role))
		hasBest = best != ""
	}
	groupRole, err := a.Auth.SliceGroupRole(ctx, subjectID, slice.Id)
	if err != nil {
		if !errors.Is(err, storage.ErrNotFound) && !errors.Is(err, storage.ErrUnauthorized) {
			return "", false, err
		}
	} else {
		groupRole = strings.ToLower(strings.TrimSpace(groupRole))
		if groupRole != "" && (!hasBest || roleTier(groupRole) > roleTier(best)) {
			best = groupRole
			hasBest = true
		}
	}
	if !hasBest {
		return "", false, nil
	}
	return best, true, nil
}

func validAction(action Action) bool {
	switch action {
	case ActionRead,
		ActionPropose,
		ActionSubmit,
		ActionReview,
		ActionTag,
		ActionUseSecrets,
		ActionManageMembers,
		ActionActOnAnyChangeset,
		ActionWrite,
		ActionAdmin:
		return true
	default:
		return false
	}
}

func roleAllows(role string, action Action) bool {
	tier := roleTier(role)
	if tier == 0 {
		return false
	}
	switch action {
	case ActionRead:
		return tier >= 1
	case ActionPropose:
		return tier >= 2
	case ActionSubmit, ActionReview, ActionTag, ActionUseSecrets, ActionWrite:
		return tier >= 3
	case ActionManageMembers, ActionActOnAnyChangeset, ActionAdmin:
		return tier >= 4
	default:
		return false
	}
}

func (a Authorizer) AuthorizeAccount(ctx context.Context, subjectID, account string, action Action) error {
	if strings.TrimSpace(subjectID) == "" {
		return storage.ErrUnauthenticated
	}
	account = strings.TrimSpace(account)
	if account == "" {
		return storage.ErrInvalid
	}
	if !validAction(action) {
		return storage.ErrInvalid
	}
	if grant, hasGrant := keyGrantFor(ctx, subjectID); hasGrant {
		if grant.HasSliceScope {
			return storage.ErrUnauthorized
		}
		if grant.MaxRole != "" && !roleAllows(grant.MaxRole, action) {
			return storage.ErrUnauthorized
		}
	}
	role, ok, err := a.accountRole(ctx, subjectID, account)
	if err != nil {
		return err
	}
	if !ok || !roleAllows(role, action) {
		return storage.ErrUnauthorized
	}
	return nil
}

func (a Authorizer) accountRole(ctx context.Context, subjectID, account string) (string, bool, error) {
	if a.Auth == nil {
		return "", false, storage.ErrInvalid
	}
	role, err := a.Auth.AccountRole(ctx, subjectID, account)
	if err != nil {
		if errors.Is(err, storage.ErrUnauthorized) || errors.Is(err, storage.ErrNotFound) {
			return "", false, nil
		}
		return "", false, err
	}
	return strings.ToLower(strings.TrimSpace(role)), true, nil
}

func roleTier(role string) int {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "owner":
		return 5
	case "admin":
		return 4
	case "writer", "member":
		return 3
	case "contributor":
		return 2
	case "reader":
		return 1
	default:
		return 0
	}
}

func higherRole(a, b string) string {
	ta, tb := roleTier(a), roleTier(b)
	if ta != tb {
		if ta > tb {
			return a
		}
		return b
	}
	if storage.AccountRoleRank(b) < storage.AccountRoleRank(a) {
		return b
	}
	return a
}

func minRole(a, b string) string {
	ta, tb := roleTier(a), roleTier(b)
	if ta == 0 || tb == 0 {
		return ""
	}
	if ta <= tb {
		return strings.ToLower(strings.TrimSpace(a))
	}
	return strings.ToLower(strings.TrimSpace(b))
}
