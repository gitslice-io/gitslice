package service

import (
	"errors"

	"github.com/gitslice-io/gitslice/internal/postgres"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func grpcError(err error) error {
	switch {
	case errors.Is(err, postgres.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, postgres.ErrConflict):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, postgres.ErrUnauthenticated):
		return status.Error(codes.Unauthenticated, err.Error())
	case errors.Is(err, postgres.ErrUnauthorized):
		return status.Error(codes.PermissionDenied, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}
