package service

import (
	"errors"

	"github.com/gitslice-io/gitslice/internal/storage"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func grpcError(err error) error {
	switch {
	case errors.Is(err, storage.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, storage.ErrConflict):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, storage.ErrInvalid):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, storage.ErrUnauthenticated):
		return status.Error(codes.Unauthenticated, err.Error())
	case errors.Is(err, storage.ErrUnauthorized):
		return status.Error(codes.PermissionDenied, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}
