package authz

import (
	"context"
	"errors"
	"strings"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

type Action string

const (
	ActionRead  Action = "read"
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
	switch action {
	case ActionRead, ActionWrite, ActionAdmin:
	default:
		return storage.ErrInvalid
	}
	// The account's role decides first; a role on the slice itself
	// (design/26_slice_members.md) can only add to it.
	role, ok, err := a.accountRole(ctx, subjectID, slice.Ref.Account)
	if err != nil {
		return err
	}
	if ok && roleAllows(role, action) {
		return nil
	}
	sliceRole, ok, err := a.SliceRole(ctx, subjectID, slice)
	if err != nil {
		return err
	}
	if ok && roleAllows(sliceRole, action) {
		return nil
	}
	return storage.ErrUnauthorized
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

func roleAllows(role string, action Action) bool {
	switch action {
	case ActionRead:
		return true
	case ActionWrite:
		return roleCanWrite(role)
	case ActionAdmin:
		return roleCanAdmin(role)
	}
	return false
}

func (a Authorizer) AuthorizeAccount(ctx context.Context, subjectID, account string, action Action) error {
	if strings.TrimSpace(subjectID) == "" {
		return storage.ErrUnauthenticated
	}
	account = strings.TrimSpace(account)
	if account == "" {
		return storage.ErrInvalid
	}
	role, ok, err := a.accountRole(ctx, subjectID, account)
	if err != nil {
		return err
	}
	switch action {
	case ActionRead:
		if !ok {
			return storage.ErrUnauthorized
		}
		return nil
	case ActionWrite:
		if !ok || !roleCanWrite(role) {
			return storage.ErrUnauthorized
		}
		return nil
	case ActionAdmin:
		if !ok || !roleCanAdmin(role) {
			return storage.ErrUnauthorized
		}
		return nil
	default:
		return storage.ErrInvalid
	}
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

func roleCanWrite(role string) bool {
	switch role {
	case "owner", "admin", "member", "writer":
		return true
	default:
		return false
	}
}

func roleCanAdmin(role string) bool {
	return role == "owner" || role == "admin"
}
