package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gitslice-io/gitslice/internal/analytics"
	"github.com/gitslice-io/gitslice/internal/auth/clerk"
	"github.com/gitslice-io/gitslice/internal/auth/servicetoken"
	"github.com/gitslice-io/gitslice/internal/authctx"
	"github.com/gitslice-io/gitslice/internal/gitcompat"
	"github.com/gitslice-io/gitslice/internal/indexworker"
	"github.com/gitslice-io/gitslice/internal/objectstore/cache"
	"github.com/gitslice-io/gitslice/internal/objectstore/filesystem"
	"github.com/gitslice-io/gitslice/internal/objectstore/latency"
	"github.com/gitslice-io/gitslice/internal/objectstore/r2"
	"github.com/gitslice-io/gitslice/internal/postgres"
	"github.com/gitslice-io/gitslice/internal/rpclimits"
	"github.com/gitslice-io/gitslice/internal/storage"
	"github.com/gitslice-io/gitslice/internal/treestore"
	"github.com/gitslice-io/gitslice/proto/core/v1"
	"github.com/gitslice-io/gitslice/service"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	healthv1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// HTTP server deadlines. The Connect HTTP API carries small, fast requests, so
// all four deadlines are bounded. The Git smart-HTTP server can stream large,
// slow clone/fetch/push bodies, so only the header-read and idle deadlines are
// set (a body read/write deadline would abort legitimate large transfers).
const (
	gatewayReadHeaderTimeout = 10 * time.Second
	gatewayReadTimeout       = 30 * time.Second
	gatewayWriteTimeout      = 60 * time.Second
	gatewayIdleTimeout       = 120 * time.Second

	// serverShutdownTimeout bounds how long graceful HTTP drain waits before the
	// gRPC server is force-stopped; long-lived daemon Connect streams never go
	// idle on their own.
	serverShutdownTimeout = 5 * time.Second

	gitReadHeaderTimeout = 10 * time.Second
	gitIdleTimeout       = 120 * time.Second
)

func Run(ctx context.Context, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	secrets, err := cfg.secretsBox()
	if err != nil {
		return err
	}
	if cfg.PublishBatchSize <= 0 {
		cfg.PublishBatchSize = 128
	}
	if cfg.PublishInterval <= 0 {
		cfg.PublishInterval = defaultPublishInterval
	}
	if cfg.PublishBackoffMax <= 0 {
		cfg.PublishBackoffMax = defaultPublishMaxInterval
	}
	if cfg.IndexBatchSize <= 0 {
		cfg.IndexBatchSize = 128
	}
	if cfg.IndexInterval <= 0 {
		cfg.IndexInterval = defaultPublishInterval
	}
	if cfg.IndexBackoffMax <= 0 {
		cfg.IndexBackoffMax = 60 * time.Second
	}
	db, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	db.Slices().Secrets = secrets
	objectStore, err := newObjectStore(cfg)
	if err != nil {
		return err
	}
	objectStore = cache.New(objectStore, cfg.ObjectCacheBytes, 4<<20)
	tracker, err := analytics.New(cfg.PostHogAPIKey, cfg.PostHogHost, cfg.PostHogEnvironment)
	if err != nil {
		slog.Warn("failed to initialize analytics; using no-op client", "error", err)
		tracker, err = analytics.New("", "", "")
		if err != nil {
			slog.Warn("failed to initialize no-op analytics client", "error", err)
		}
	}
	defer func() {
		if tracker == nil {
			return
		}
		if err := tracker.Close(); err != nil {
			slog.Warn("failed to close analytics client", "error", err)
		}
	}()
	resolveSubject, err := newSubjectResolver(db.Auth(), cfg)
	if err != nil {
		return err
	}
	db.SetTreeStore(treestore.New(objectStore))
	if cfg.RunMigrations {
		if err := db.Migrate(ctx); err != nil {
			return err
		}
	}
	pubNudge := newPublishNudger()
	db.Changesets().SetPendingPublishListener(pubNudge.Nudge)
	var indexWorker *indexworker.Worker
	if !cfg.DisableIndexWorker {
		indexWorker = indexworker.New(db.Changesets(), cfg.IndexBatchSize, cfg.IndexInterval, cfg.IndexBackoffMax)
		go indexWorker.Run(ctx)
	}
	if !cfg.DisableAsyncPublisher {
		var nudge func()
		if indexWorker != nil {
			nudge = indexWorker.Nudge
		}
		go runPublisher(ctx, db.Changesets(), cfg.PublishBatchSize, cfg.PublishInterval, nudge, pubNudge.ch, cfg.PublishBackoffMax)
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
		Agents:     db.Agents(),
		Checks:     db.Checks(),
	}
	handlers := service.New(stores, objectStore, tracker)
	if handlers.Agent != nil {
		go handlers.Agent.RunCheckDispatchSweep(ctx)
	}
	grpcServer := NewGRPCServer(resolveSubject, handlers, cfg)
	apiHandler := NewConnectHandler(resolveSubject, handlers)
	gitCacheRoot := cfg.GitCacheRoot
	if gitCacheRoot == "" {
		if cfg.ObjectStoreRoot != "" {
			gitCacheRoot = filepath.Join(cfg.ObjectStoreRoot, "git-cache")
		} else {
			gitCacheRoot = filepath.Join(os.TempDir(), "gitslice-git-cache")
		}
	}
	projector, err := gitcompat.NewProjector(gitcompat.ProjectorStores{
		Auth:       db.Auth(),
		Repository: db.Repository(),
		Slices:     db.Slices(),
	}, objectStore, gitCacheRoot)
	if err != nil {
		return err
	}
	var gitHandler http.Handler = gitcompat.NewHandler(gitcompat.SubjectResolver(resolveSubject), projector, handlers.Blob, handlers.Changeset)
	gitHandler = newHTTPRateLimitMiddleware(cfg)(gitHandler)
	combinedServer := &http.Server{
		Handler:           NewCombinedGRPCGatewayHandler(grpcServer, NewHTTPHandler(apiHandler, gitHandler, cfg.HTTPAllowedOrigin, cfg)),
		ReadHeaderTimeout: gatewayReadHeaderTimeout,
		IdleTimeout:       gatewayIdleTimeout,
	}
	var gatewayServer *http.Server
	var gatewayLis net.Listener
	if cfg.HTTPAddr != "" {
		gatewayLis, err = net.Listen("tcp", cfg.HTTPAddr)
		if err != nil {
			return err
		}
		gatewayServer = &http.Server{
			Handler:           NewHTTPHandler(apiHandler, gitHandler, cfg.HTTPAllowedOrigin, cfg),
			ReadHeaderTimeout: gatewayReadHeaderTimeout,
			ReadTimeout:       gatewayReadTimeout,
			WriteTimeout:      gatewayWriteTimeout,
			IdleTimeout:       gatewayIdleTimeout,
		}
	}
	var gitHTTPServer *http.Server
	var gitHTTPLis net.Listener
	if cfg.GitHTTPAddr != "" {
		gitHTTPLis, err = net.Listen("tcp", cfg.GitHTTPAddr)
		if err != nil {
			return err
		}
		gitHTTPServer = &http.Server{
			Handler: gitHandler,
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
		errCh <- combinedServer.Serve(lis)
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
	shutdown := func() {
		// Bound the HTTP drain: long-lived AgentService.Connect streams would
		// otherwise keep the combined server from ever going idle.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), serverShutdownTimeout)
		defer cancel()
		_ = combinedServer.Shutdown(shutdownCtx)
		// gRPC is multiplexed over HTTP via grpcServer.ServeHTTP; that
		// serverHandlerTransport does not implement Drain(), so GracefulStop()
		// panics whenever a server stream is still open at shutdown (as the
		// daemon Connect stream always is). Stop() closes the handler
		// transports without draining, which is correct here because
		// combinedServer.Shutdown already drained in-flight HTTP/gRPC requests.
		grpcServer.Stop()
		if gatewayServer != nil {
			_ = gatewayServer.Shutdown(shutdownCtx)
		}
		if gitHTTPServer != nil {
			_ = gitHTTPServer.Shutdown(shutdownCtx)
		}
	}
	select {
	case <-ctx.Done():
		shutdown()
		return ctx.Err()
	case err := <-errCh:
		if errors.Is(err, grpc.ErrServerStopped) {
			return nil
		}
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		shutdown()
		return err
	}
}

func Migrate(ctx context.Context, cfg Config) (err error) {
	if err := cfg.Validate(); err != nil {
		return err
	}
	secrets, err := cfg.secretsBox()
	if err != nil {
		return err
	}
	db, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := db.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	db.Slices().Secrets = secrets
	objectStore, err := newObjectStore(cfg)
	if err != nil {
		return err
	}
	objectStore = cache.New(objectStore, cfg.ObjectCacheBytes, 4<<20)
	db.SetTreeStore(treestore.New(objectStore))
	return db.Migrate(ctx)
}

func NewCombinedGRPCGatewayHandler(grpcServer *grpc.Server, gateway http.Handler) http.Handler {
	return h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			grpcServer.ServeHTTP(w, r)
			return
		}
		gateway.ServeHTTP(w, r)
	}), &http2.Server{})
}

// subjectResolver maps a verified bearer token to an internal subject ID. It
// abstracts over the dev/fake session store and external providers such as Clerk.
type subjectResolver func(ctx context.Context, token string) (string, error)

// newObjectStore selects the object store backend from config: Cloudflare R2 when
// OBJECT_STORE_TYPE=r2, otherwise the prototype filesystem store. An optional
// benchmark latency wrapper remains inside the cache installed by callers.
func newObjectStore(cfg Config) (service.ObjectStore, error) {
	var objectStore service.ObjectStore
	if cfg.usesR2() {
		store, err := r2.New(cfg.R2)
		if err != nil {
			return nil, err
		}
		objectStore = store
	} else {
		store, err := filesystem.New(cfg.ObjectStoreRoot)
		if err != nil {
			return nil, err
		}
		objectStore = store
	}
	if cfg.ObjectStoreLatency > 0 {
		objectStore = latency.New(objectStore, cfg.ObjectStoreLatency)
	}
	return objectStore, nil
}

// newSubjectResolver builds the token->subject resolver for the configured auth
// provider. With AUTH_PROVIDER=clerk it verifies Clerk session JWTs and
// JIT-provisions a subject; otherwise it resolves dev/fake session tokens.
func newSubjectResolver(auth storage.AuthStore, cfg Config) (subjectResolver, error) {
	// Optional asymmetric-key service-token path for automated testing and
	// service accounts. Disabled unless GITSLICE_SERVICE_JWT_PUBLIC_KEY is set.
	serviceVerifier, err := servicetoken.NewVerifier(cfg.ServiceToken)
	if err != nil {
		return nil, err
	}

	var primary subjectResolver
	if cfg.AuthProvider == "clerk" {
		clerkVerifier, err := clerk.NewVerifier(cfg.Clerk)
		if err != nil {
			return nil, err
		}
		primary = func(ctx context.Context, token string) (string, error) {
			if subject, err := auth.SubjectForToken(ctx, token); err == nil {
				return subject.ID, nil
			} else if !errors.Is(err, storage.ErrUnauthenticated) {
				return "", err
			}
			claims, err := clerkVerifier.Verify(ctx, token)
			if err != nil {
				return "", fmt.Errorf("%w: %v", storage.ErrUnauthenticated, err)
			}
			return auth.EnsureExternalSubject(ctx, claims.Subject, claims.Email)
		}
	} else {
		primary = func(ctx context.Context, token string) (string, error) {
			subject, err := auth.SubjectForToken(ctx, token)
			if err != nil {
				return "", err
			}
			return subject.ID, nil
		}
	}

	if serviceVerifier == nil {
		return primary, nil
	}
	// Try the strict service-token verifier first; a token not signed by the
	// configured key simply falls through to the primary provider.
	return func(ctx context.Context, token string) (string, error) {
		if claims, err := serviceVerifier.Verify(ctx, token); err == nil {
			return auth.EnsureExternalSubject(ctx, claims.Subject, claims.Email)
		}
		return primary(ctx, token)
	}, nil
}

func NewGRPCServer(resolve subjectResolver, handlers *service.Handlers, cfgs ...Config) *grpc.Server {
	var cfg Config
	if len(cfgs) > 0 {
		cfg = cfgs[0]
	}
	grpcLimiter := newGRPCRateLimiter(cfg)
	grpcServer := grpc.NewServer(
		grpc.MaxRecvMsgSize(rpclimits.MaxUnaryMessageBytes),
		grpc.MaxSendMsgSize(rpclimits.MaxUnaryMessageBytes),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             10 * time.Second,
			PermitWithoutStream: true,
		}),
		grpc.ChainUnaryInterceptor(requestIDUnaryInterceptor(), grpcMetricsUnaryInterceptor(), authInterceptor(resolve), grpcRateLimitUnaryInterceptor(grpcLimiter)),
		grpc.ChainStreamInterceptor(requestIDStreamInterceptor(), grpcMetricsStreamInterceptor(), authStreamInterceptor(resolve), grpcRateLimitStreamInterceptor(grpcLimiter)),
	)
	corev1.RegisterAuthServiceServer(grpcServer, handlers.Auth)
	corev1.RegisterRepositoryServiceServer(grpcServer, handlers.Repository)
	corev1.RegisterBlobServiceServer(grpcServer, handlers.Blob)
	corev1.RegisterSliceServiceServer(grpcServer, handlers.Slice)
	corev1.RegisterWorkspaceServiceServer(grpcServer, handlers.Workspace)
	corev1.RegisterChangesetServiceServer(grpcServer, handlers.Changeset)
	corev1.RegisterChangesetStackServiceServer(grpcServer, handlers.Stack)
	corev1.RegisterAgentServiceServer(grpcServer, handlers.Agent)
	corev1.RegisterCheckServiceServer(grpcServer, handlers.Check)
	healthServer := health.NewServer()
	healthServer.SetServingStatus("", healthv1.HealthCheckResponse_SERVING)
	healthv1.RegisterHealthServer(grpcServer, healthServer)
	return grpcServer
}

func authStreamInterceptor(resolve subjectResolver) grpc.StreamServerInterceptor {
	return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if isPublicMethod(info.FullMethod) {
			return handler(srv, stream)
		}
		ctx, err := authenticatedContext(stream.Context(), resolve)
		if err != nil {
			return err
		}
		return handler(srv, &contextServerStream{
			ServerStream: stream,
			ctx:          ctx,
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

func authInterceptor(resolve subjectResolver) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if isPublicMethod(info.FullMethod) {
			return handler(ctx, req)
		}
		ctx, err := authenticatedContext(ctx, resolve)
		if err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

func isPublicMethod(method string) bool {
	return method == "/gitslice.core.v1.AuthService/StartCliLogin" ||
		method == "/gitslice.core.v1.AuthService/PollCliLogin" ||
		strings.HasPrefix(method, "/grpc.health.v1.Health/")
}

func bearerToken(ctx context.Context) (string, error) {
	token, ok, err := optionalBearerToken(ctx)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", status.Error(codes.Unauthenticated, "missing authorization bearer token")
	}
	return token, nil
}

func authenticatedContext(ctx context.Context, resolve subjectResolver) (context.Context, error) {
	token, ok, err := optionalBearerToken(ctx)
	if err != nil {
		return nil, err
	}
	if !ok {
		return ctx, nil
	}
	subjectID, err := resolve(ctx, token)
	if err != nil {
		return nil, grpcAuthError(err)
	}
	return authctx.WithSubjectID(ctx, subjectID), nil
}

func optionalBearerToken(ctx context.Context) (string, bool, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", false, nil
	}
	values := md.Get("authorization")
	if len(values) == 0 {
		return "", false, nil
	}
	return optionalBearerTokenValue(values[0])
}

func optionalBearerTokenValue(value string) (string, bool, error) {
	const prefix = "bearer "
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false, nil
	}
	if !strings.HasPrefix(strings.ToLower(value), prefix) {
		return "", false, status.Error(codes.Unauthenticated, "authorization must be a bearer token")
	}
	token := strings.TrimSpace(value[len(prefix):])
	if token == "" {
		return "", false, status.Error(codes.Unauthenticated, "empty bearer token")
	}
	return token, true, nil
}

func grpcAuthError(err error) error {
	if errors.Is(err, storage.ErrUnauthenticated) {
		return status.Error(codes.Unauthenticated, err.Error())
	}
	return status.Error(codes.Internal, fmt.Sprintf("auth lookup failed: %v", err))
}
