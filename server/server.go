package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/gitslice-io/gitslice/internal/authctx"
	"github.com/gitslice-io/gitslice/internal/gitcompat"
	"github.com/gitslice-io/gitslice/internal/objectstore/filesystem"
	"github.com/gitslice-io/gitslice/internal/postgres"
	"github.com/gitslice-io/gitslice/internal/treestore"
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
	if cfg.PublishBatchSize <= 0 {
		cfg.PublishBatchSize = 128
	}
	if cfg.PublishInterval <= 0 {
		cfg.PublishInterval = defaultPublishInterval
	}
	store, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer store.Close()
	objectStore, err := filesystem.New(cfg.ObjectStoreRoot)
	if err != nil {
		return err
	}
	store.SetTreeStore(treestore.New(objectStore))
	if cfg.RunMigrations {
		if err := store.Migrate(ctx); err != nil {
			return err
		}
	}
	if !cfg.DisableAsyncPublisher {
		go runPublisher(ctx, store, cfg.PublishBatchSize, cfg.PublishInterval)
	}
	lis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		return err
	}
	grpcServer := NewGRPCServer(store, service.New(store, objectStore))
	var gatewayServer *http.Server
	var gatewayLis net.Listener
	if cfg.HTTPAddr != "" {
		gatewayHandler, err := NewHTTPGateway(ctx, gatewayGRPCEndpoint(lis.Addr()))
		if err != nil {
			return err
		}
		gatewayLis, err = net.Listen("tcp", cfg.HTTPAddr)
		if err != nil {
			return err
		}
		gatewayServer = &http.Server{Handler: withCORS(gatewayHandler, cfg.HTTPAllowedOrigin)}
	}
	var gitHTTPServer *http.Server
	var gitHTTPLis net.Listener
	if cfg.GitHTTPAddr != "" {
		if cfg.GitCacheRoot == "" {
			cfg.GitCacheRoot = filepath.Join(cfg.ObjectStoreRoot, "git-cache")
		}
		projector, err := gitcompat.NewProjector(store, objectStore, cfg.GitCacheRoot)
		if err != nil {
			return err
		}
		gitHTTPLis, err = net.Listen("tcp", cfg.GitHTTPAddr)
		if err != nil {
			return err
		}
		gitHTTPServer = &http.Server{Handler: gitcompat.NewHandler(store, projector)}
	}
	errCh := make(chan error, 3)
	go func() {
		slog.Info("gitslice server listening", "grpc_addr", lis.Addr().String())
		errCh <- grpcServer.Serve(lis)
	}()
	if gatewayServer != nil {
		go func() {
			slog.Info("gitslice http gateway listening", "http_addr", gatewayLis.Addr().String())
			errCh <- gatewayServer.Serve(gatewayLis)
		}()
	}
	if gitHTTPServer != nil {
		go func() {
			slog.Info("gitslice git http listening", "git_http_addr", gitHTTPLis.Addr().String())
			errCh <- gitHTTPServer.Serve(gitHTTPLis)
		}()
	}
	select {
	case <-ctx.Done():
		grpcServer.GracefulStop()
		if gatewayServer != nil {
			_ = gatewayServer.Shutdown(context.Background())
		}
		if gitHTTPServer != nil {
			_ = gitHTTPServer.Shutdown(context.Background())
		}
		return ctx.Err()
	case err := <-errCh:
		if errors.Is(err, grpc.ErrServerStopped) {
			return nil
		}
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		grpcServer.GracefulStop()
		if gatewayServer != nil {
			_ = gatewayServer.Shutdown(context.Background())
		}
		if gitHTTPServer != nil {
			_ = gitHTTPServer.Shutdown(context.Background())
		}
		return err
	}
}

func NewGRPCServer(store *postgres.Store, handlers *service.Handlers) *grpc.Server {
	grpcServer := grpc.NewServer(
		grpc.UnaryInterceptor(authInterceptor(store)),
		grpc.StreamInterceptor(authStreamInterceptor(store)),
	)
	corev1.RegisterFakeAccountServiceServer(grpcServer, handlers.FakeAccount)
	corev1.RegisterRepositoryServiceServer(grpcServer, handlers.Repository)
	corev1.RegisterBlobServiceServer(grpcServer, handlers.Blob)
	corev1.RegisterSliceServiceServer(grpcServer, handlers.Slice)
	corev1.RegisterWorkspaceServiceServer(grpcServer, handlers.Workspace)
	corev1.RegisterChangesetServiceServer(grpcServer, handlers.Changeset)
	healthServer := health.NewServer()
	healthServer.SetServingStatus("", healthv1.HealthCheckResponse_SERVING)
	healthv1.RegisterHealthServer(grpcServer, healthServer)
	return grpcServer
}

func authStreamInterceptor(store *postgres.Store) grpc.StreamServerInterceptor {
	return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if isPublicMethod(info.FullMethod) {
			return handler(srv, stream)
		}
		token, err := bearerToken(stream.Context())
		if err != nil {
			return err
		}
		subject, err := store.SubjectForToken(stream.Context(), token)
		if err != nil {
			return grpcAuthError(err)
		}
		return handler(srv, &contextServerStream{
			ServerStream: stream,
			ctx:          authctx.WithSubjectID(stream.Context(), subject.ID),
		})
	}
}

type contextServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *contextServerStream) Context() context.Context {
	return s.ctx
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
