package service

import (
	"context"
	"errors"

	"gitslice.io/gitslice/internal/storage"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// requireDefaultTargetRef enforces the single-global-ref model: clients may omit
// the target ref (it defaults to refs/global/main) but may not select a
// different one. Multiple target refs (branches) are explicitly future work in
// design/02_storage.md §3.1.
func requireDefaultTargetRef(ref string) error {
	if ref != "" && ref != storage.DefaultTargetRef {
		return status.Errorf(codes.InvalidArgument, "target ref %q is not supported; only %s is available", ref, storage.DefaultTargetRef)
	}
	return nil
}

func grpcError(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := status.FromError(err); ok {
		return err
	}
	switch {
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, err.Error())
	case errors.Is(err, storage.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, storage.ErrAlreadyExists):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, storage.ErrConflict):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, storage.ErrInvalid):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, storage.ErrResourceExhausted):
		return status.Error(codes.ResourceExhausted, err.Error())
	case errors.Is(err, storage.ErrUnauthenticated):
		return status.Error(codes.Unauthenticated, err.Error())
	case errors.Is(err, storage.ErrUnauthorized):
		return status.Error(codes.PermissionDenied, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}
