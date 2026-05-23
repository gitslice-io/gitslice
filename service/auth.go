package service

import (
	"context"

	"github.com/gitslice-io/gitslice/internal/authctx"
	"github.com/gitslice-io/gitslice/internal/postgres"
	"github.com/gitslice-io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type FakeAccountService struct {
	Auth *postgres.AuthStore
}

func (s *FakeAccountService) Login(ctx context.Context, req *corev1.LoginRequest) (*corev1.LoginResponse, error) {
	token, subjectID, err := s.Auth.LoginDevUser(ctx, req.DevUser)
	if err != nil {
		return nil, grpcError(err)
	}
	return &corev1.LoginResponse{Token: token, SubjectId: subjectID}, nil
}

func requireSubject(ctx context.Context) (string, error) {
	subjectID, ok := authctx.SubjectID(ctx)
	if !ok {
		return "", status.Error(codes.Unauthenticated, "missing subject")
	}
	return subjectID, nil
}
