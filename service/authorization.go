package service

import (
	"context"
	"errors"

	"github.com/gitslice-io/gitslice/internal/authz"
	"github.com/gitslice-io/gitslice/internal/storage"
	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
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
