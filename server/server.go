package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"

	"github.com/gitslice-io/gitslice/internal/authctx"
	"github.com/gitslice-io/gitslice/internal/objectstore/filesystem"
	"github.com/gitslice-io/gitslice/internal/postgres"
	"github.com/gitslice-io/gitslice/proto/core/v1"
	"github.com/gitslice-io/gitslice/service"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	healthv1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func Run(ctx context.Context, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	store, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer store.Close()
	if cfg.RunMigrations {
		if err := store.Migrate(ctx); err != nil {
			return err
		}
	}
	objectStore, err := filesystem.New(cfg.ObjectStoreRoot)
	if err != nil {
		return err
	}
	lis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		return err
	}
	grpcServer := NewGRPCServer(store, service.New(store, objectStore))
	errCh := make(chan error, 1)
	go func() {
		slog.Info("gitslice server listening", "grpc_addr", lis.Addr().String())
		errCh <- grpcServer.Serve(lis)
	}()
	select {
	case <-ctx.Done():
		grpcServer.GracefulStop()
		return ctx.Err()
	case err := <-errCh:
		if errors.Is(err, grpc.ErrServerStopped) {
			return nil
		}
		return err
	}
}

func NewGRPCServer(store *postgres.Store, services *service.Services) *grpc.Server {
	grpcServer := grpc.NewServer(grpc.UnaryInterceptor(authInterceptor(store)))
	corev1.RegisterFakeAccountServiceServer(grpcServer, services)
	corev1.RegisterRepositoryServiceServer(grpcServer, services)
	corev1.RegisterBlobServiceServer(grpcServer, services)
	corev1.RegisterSliceServiceServer(grpcServer, services)
	corev1.RegisterWorkspaceServiceServer(grpcServer, services)
	corev1.RegisterChangesetServiceServer(grpcServer, services)
	healthServer := health.NewServer()
	healthServer.SetServingStatus("", healthv1.HealthCheckResponse_SERVING)
	healthv1.RegisterHealthServer(grpcServer, healthServer)
	return grpcServer
}

func authInterceptor(store *postgres.Store) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if isPublicMethod(info.FullMethod) {
			return handler(ctx, req)
		}
		token, err := bearerToken(ctx)
		if err != nil {
			return nil, err
		}
		subject, err := store.SubjectForToken(ctx, token)
		if err != nil {
			return nil, grpcAuthError(err)
		}
		return handler(authctx.WithSubjectID(ctx, subject.ID), req)
	}
}

func isPublicMethod(method string) bool {
	return method == "/gitslice.core.v1.FakeAccountService/Login" ||
		strings.HasPrefix(method, "/grpc.health.v1.Health/")
}

func bearerToken(ctx context.Context) (string, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", status.Error(codes.Unauthenticated, "missing metadata")
	}
	values := md.Get("authorization")
	if len(values) == 0 {
		return "", status.Error(codes.Unauthenticated, "missing authorization bearer token")
	}
	const prefix = "bearer "
	value := strings.TrimSpace(values[0])
	if !strings.HasPrefix(strings.ToLower(value), prefix) {
		return "", status.Error(codes.Unauthenticated, "authorization must be a bearer token")
	}
	token := strings.TrimSpace(value[len(prefix):])
	if token == "" {
		return "", status.Error(codes.Unauthenticated, "empty bearer token")
	}
	return token, nil
}

func grpcAuthError(err error) error {
	if errors.Is(err, postgres.ErrUnauthenticated) {
		return status.Error(codes.Unauthenticated, err.Error())
	}
	return status.Error(codes.Internal, fmt.Sprintf("auth lookup failed: %v", err))
}
