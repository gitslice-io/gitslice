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
	"time"

	"github.com/gitslice-io/gitslice/internal/authctx"
	"github.com/gitslice-io/gitslice/internal/gitcompat"
	"github.com/gitslice-io/gitslice/internal/indexworker"
	"github.com/gitslice-io/gitslice/internal/objectstore/filesystem"
	"github.com/gitslice-io/gitslice/internal/postgres"
	"github.com/gitslice-io/gitslice/internal/rpclimits"
	"github.com/gitslice-io/gitslice/internal/storage"
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

// HTTP server deadlines. The JSON gateway carries small, fast requests, so all
// four deadlines are bounded. The Git smart-HTTP server can stream large, slow
// clone/fetch/push bodies, so only the header-read and idle deadlines are set
// (a body read/write deadline would abort legitimate large transfers).
const (
	gatewayReadHeaderTimeout = 10 * time.Second
	gatewayReadTimeout       = 30 * time.Second
	gatewayWriteTimeout      = 60 * time.Second
	gatewayIdleTimeout       = 120 * time.Second

	gitReadHeaderTimeout = 10 * time.Second
	gitIdleTimeout       = 120 * time.Second
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
	if cfg.IndexBatchSize <= 0 {
		cfg.IndexBatchSize = 128
	}
	if cfg.IndexInterval <= 0 {
		cfg.IndexInterval = defaultPublishInterval
	}
	db, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	objectStore, err := filesystem.New(cfg.ObjectStoreRoot)
	if err != nil {
		return err
	}
	db.SetTreeStore(treestore.New(objectStore))
	if cfg.RunMigrations {
		if err := db.Migrate(ctx); err != nil {
			return err
		}
	}
	var indexWorker *indexworker.Worker
	if !cfg.DisableIndexWorker {
		indexWorker = indexworker.New(db.Changesets(), cfg.IndexBatchSize, cfg.IndexInterval)
		go indexWorker.Run(ctx)
	}
	if !cfg.DisableAsyncPublisher {
		var nudge func()
		if indexWorker != nil {
			nudge = indexWorker.Nudge
		}
		go runPublisher(ctx, db.Changesets(), cfg.PublishBatchSize, cfg.PublishInterval, nudge)
	}
	lis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		return err
	}
	stores := service.Stores{
		Auth:       db.Auth(),
		Blobs:      db.Blobs(),
		Changesets: db.Changesets(),
		Repository: db.Repository(),
		Slices:     db.Slices(),
	}
	handlers := service.New(stores, objectStore)
	grpcServer := NewGRPCServer(db.Auth(), handlers, cfg.DevMode)
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
		gatewayServer = &http.Server{
			Handler:           NewHTTPHandler(gatewayHandler, cfg.HTTPAllowedOrigin, cfg.DevMode),
			ReadHeaderTimeout: gatewayReadHeaderTimeout,
			ReadTimeout:       gatewayReadTimeout,
			WriteTimeout:      gatewayWriteTimeout,
			IdleTimeout:       gatewayIdleTimeout,
		}
	}
	var gitHTTPServer *http.Server
	var gitHTTPLis net.Listener
	if cfg.GitHTTPAddr != "" {
		if cfg.GitCacheRoot == "" {
			cfg.GitCacheRoot = filepath.Join(cfg.ObjectStoreRoot, "git-cache")
		}
		projector, err := gitcompat.NewProjector(gitcompat.ProjectorStores{
			Auth:       db.Auth(),
			Repository: db.Repository(),
			Slices:     db.Slices(),
		}, objectStore, cfg.GitCacheRoot)
		if err != nil {
			return err
		}
		gitHTTPLis, err = net.Listen("tcp", cfg.GitHTTPAddr)
		if err != nil {
			return err
		}
		gitHTTPServer = &http.Server{
			Handler: gitcompat.NewHandler(db.Auth(), projector, handlers.Blob, handlers.Changeset),
			// Git clone/fetch/push transfers can be large and slow, so only the
			// header-read deadline (slowloris protection) and the idle-keepalive
			// deadline are bounded here; body read/write are left to the request
			// context and the body-size cap in the handler.
			ReadHeaderTimeout: gitReadHeaderTimeout,
			IdleTimeout:       gitIdleTimeout,
		}
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

func NewGRPCServer(auth storage.AuthStore, handlers *service.Handlers, devMode bool) *grpc.Server {
	grpcServer := grpc.NewServer(
		grpc.MaxRecvMsgSize(rpclimits.MaxUnaryMessageBytes),
		grpc.MaxSendMsgSize(rpclimits.MaxUnaryMessageBytes),
		grpc.ChainUnaryInterceptor(requestIDUnaryInterceptor(), grpcMetricsUnaryInterceptor(), authInterceptor(auth)),
		grpc.ChainStreamInterceptor(requestIDStreamInterceptor(), grpcMetricsStreamInterceptor(), authStreamInterceptor(auth)),
	)
	// FakeAccountService mints session tokens with no credential check (dev
	// login and self-serve signup). It is a prototype affordance and must never
	// be exposed in a non-dev deployment, so it is only registered in dev mode.
	if devMode {
		corev1.RegisterFakeAccountServiceServer(grpcServer, handlers.FakeAccount)
	}
	corev1.RegisterAuthServiceServer(grpcServer, handlers.Auth)
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

func authStreamInterceptor(auth storage.AuthStore) grpc.StreamServerInterceptor {
	return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if isPublicMethod(info.FullMethod) {
			return handler(srv, stream)
		}
		token, err := bearerToken(stream.Context())
		if err != nil {
			return err
		}
		subject, err := auth.SubjectForToken(stream.Context(), token)
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

func authInterceptor(auth storage.AuthStore) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if isPublicMethod(info.FullMethod) {
			return handler(ctx, req)
		}
		token, err := bearerToken(ctx)
		if err != nil {
			return nil, err
		}
		subject, err := auth.SubjectForToken(ctx, token)
		if err != nil {
			return nil, grpcAuthError(err)
		}
		return handler(authctx.WithSubjectID(ctx, subject.ID), req)
	}
}

func isPublicMethod(method string) bool {
	return method == "/gitslice.core.v1.FakeAccountService/Login" ||
		method == "/gitslice.core.v1.FakeAccountService/ApproveSignup" ||
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
	if errors.Is(err, storage.ErrUnauthenticated) {
		return status.Error(codes.Unauthenticated, err.Error())
	}
	return status.Error(codes.Internal, fmt.Sprintf("auth lookup failed: %v", err))
}
