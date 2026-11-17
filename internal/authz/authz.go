package authz

import (
	"context"
	"errors"
	"fmt"
	"strings"

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

func (a Authorizer) authorize(ctx context.Context, subjectID string, slice *corev1.Slice, action Action, allowArchived bool) error {
	if slice == nil || slice.Ref == nil || slice.Ref.Account == "" || slice.Definition == nil {
		return storage.ErrInvalid
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
	if !validAction(action) {
		return storage.ErrInvalid
	}
	// The account's role decides first; a role on the slice itself
	// (design/26_slice_members.md) can only add to it.
	allowed := false
	role, ok, err := a.accountRole(ctx, subjectID, slice.Ref.Account)
	if err != nil {
		return err
	}
	if ok && roleAllows(role, action) {
		allowed = true
	}
	if !allowed {
		sliceRole, ok, err := a.SliceRole(ctx, subjectID, slice)
		if err != nil {
			return err
		}
		if ok && roleAllows(sliceRole, action) {
			allowed = true
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

// EffectiveRole returns the subject's higher role across the slice's account
// and the slice itself, or ("", false, nil) when it holds neither.
func (a Authorizer) EffectiveRole(ctx context.Context, subjectID string, slice *corev1.Slice) (string, bool, error) {
	if slice == nil || strings.TrimSpace(subjectID) == "" {
		return "", false, nil
	}
	if a.Auth == nil {
		return "", false, storage.ErrInvalid
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
	switch {
	case !acctOK && !sliceOK:
		return "", false, nil
	case acctOK && !sliceOK:
		return acctRole, true, nil
	case !acctOK && sliceOK:
		return sliceRole, true, nil
	default:
		return higherRole(acctRole, sliceRole), true, nil
	}
}

// SliceRole returns the role the subject was given on the slice itself, if
// any (not its role in the slice's account).
func (a Authorizer) SliceRole(ctx context.Context, subjectID string, slice *corev1.Slice) (string, bool, error) {
	if a.Auth == nil || slice == nil || strings.TrimSpace(slice.Id) == "" || strings.TrimSpace(subjectID) == "" {
		return "", false, nil
	}
	role, err := a.Auth.SliceRole(ctx, subjectID, slice.Id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) || errors.Is(err, storage.ErrUnauthorized) {
			return "", false, nil
		}
		return "", false, err
	}
	return strings.ToLower(strings.TrimSpace(role)), true, nil
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
