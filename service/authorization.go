package service

import (
	"context"
	"errors"

	"gitslice.io/gitslice/internal/authz"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func authorize(ctx context.Context, auth storage.AuthStore, subjectID string, slice *corev1.Slice, action authz.Action) error {
	if err := authz.New(auth).Authorize(ctx, subjectID, slice, action); err != nil {
		return grpcError(err)
	}
	return nil
}

func authorizeAccount(ctx context.Context, auth storage.AuthStore, subjectID, account string, action authz.Action) error {
	if err := authz.New(auth).AuthorizeAccount(ctx, subjectID, account, action); err != nil {
		return grpcError(err)
	}
	return nil
}

func canAuthorizeAccount(ctx context.Context, auth storage.AuthStore, subjectID, account string, action authz.Action) (bool, error) {
	err := authz.New(auth).AuthorizeAccount(ctx, subjectID, account, action)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, storage.ErrUnauthorized) || errors.Is(err, storage.ErrNotFound) {
		return false, nil
	}
	return false, grpcError(err)
}

func resolveAuthorizedSlice(ctx context.Context, auth storage.AuthStore, slices storage.SliceStore, subjectID string, ref *corev1.SliceRef, action authz.Action) (*corev1.Slice, error) {
	slice, err := slices.Resolve(ctx, ref)
	if err != nil {
		return nil, grpcError(err)
	}
	if err := authorize(ctx, auth, subjectID, slice, action); err != nil {
		return nil, err
	}
	return slice, nil
}

// authorizeSliceOwner lets the owners and admins of the slice's account change
// what the slice is: its definition (paths, visibility, policy), CI daemon and
// secrets, or delete it. A slice admin from outside the account
// (design/26_slice_members.md) cannot: widening the paths or pointing the
// slice at their own daemon, which receives its secrets, would reach past the
// slice they were given.
func authorizeSliceOwner(ctx context.Context, auth storage.AuthStore, subjectID string, slice *corev1.Slice) error {
	return authorizeAccount(ctx, auth, subjectID, slice.GetRef().GetAccount(), authz.ActionAdmin)
}
