package server

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/activity"
	"gitslice.io/gitslice/internal/analytics"
	"gitslice.io/gitslice/internal/auth/clerk"
	"gitslice.io/gitslice/internal/events"
	"gitslice.io/gitslice/internal/gitcompat"
	"gitslice.io/gitslice/internal/indexworker"
	"gitslice.io/gitslice/internal/objectstore/cache"
	"gitslice.io/gitslice/internal/objectstore/filesystem"
	"gitslice.io/gitslice/internal/objectstore/latency"
	"gitslice.io/gitslice/internal/objectstore/r2"
	"gitslice.io/gitslice/internal/postgres"
	"gitslice.io/gitslice/internal/rpclimits"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/treestore"
	"gitslice.io/gitslice/internal/triggers"
	"gitslice.io/gitslice/proto/core/v1"
	"gitslice.io/gitslice/service"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
	// Registers gzip, so clients can compress batch calls (ReadFiles,
	// UploadBlobs) and their responses.
	_ "google.golang.org/grpc/encoding/gzip"
	"google.golang.org/grpc/health"
	healthv1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
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
	if secrets == nil {
		slog.Warn("GITSLICE_SECRETS_KEY is unset; secret storage and signed self-requests are disabled (set GITSLICE_REQUIRE_SECRETS_KEY=1 in production)")
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
	// Background workers (publisher, outbox/index worker, check sweep) run on
	// ctx. Cancel it on every return path, before db.Close runs, so a startup
	// failure such as a port already in use does not leave them spinning on a
	// closed database.
	ctx, cancelWorkers := context.WithCancel(ctx)
	defer cancelWorkers()
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
	resolvePrincipal, err := newPrincipalResolver(db.Auth(), cfg)
	if err != nil {
		return err
	}
	resolvePrincipal = cachedPrincipalResolver(resolvePrincipal, time.Now)
	db.SetTreeStore(treestore.New(objectStore))
	if cfg.RunMigrations {
		if err := db.Migrate(ctx); err != nil {
			return err
		}
	}
	// Trigger checks are written through the wrapped check store below, so
	// the events they cause are recorded too.
	triggerChecks := &lateCheckStore{}
	triggerEngine := triggers.NewEngine(triggers.Stores{
		Triggers:   db.Triggers(),
		Changesets: db.Changesets(),
		Slices:     db.Slices(),
		Repository: db.Repository(),
		Checks:     triggerChecks,
	}, triggers.Options{
		Tree:                service.TreeFiles{Repository: db.Repository(), Objects: objectStore},
		DispatchURL:         cfg.TriggerDispatchURL,
		DispatchSecret:      cfg.TriggerDispatchSecret,
		AllowPrivateTargets: cfg.OutboundAllowPrivateTargets,
	})
	// Slice Git mirrors (design/35_git_mirrors.md): the dispatcher's drains
	// queue and claim pushes; the projector (below) does them.
	gitMirrors := &service.GitMirrors{
		Mirrors:             db.GitMirrors(),
		Slices:              db.Slices(),
		AllowPrivateTargets: cfg.OutboundAllowPrivateTargets,
	}
	dispatcher := events.NewDispatcher(events.Stores{
		Events:     db.Events(),
		Slices:     db.Slices(),
		Changesets: db.Changesets(),
		Checks:     db.Checks(),
		Auth:       db.Auth(),
	}, events.Options{
		WebBaseURL: cfg.webBaseURL(),
		Triggers:   triggerEngine,
		Mirrors:    gitMirrors,
	})
	// Start background database loops idle until real user traffic arrives, so
	// a cold-booted instance poked only by health checks or metrics scrapes lets
	// Neon stay scaled to zero.
	activity.Default.ResetIdle()

	gitMirrors.Nudge = dispatcher.Nudge
	go dispatcher.Run(ctx)
	pubNudge := newPublishNudger()
	db.Changesets().SetPendingPublishListener(pubNudge.Nudge)
	var indexWorker *indexworker.Worker
	if !cfg.DisableIndexWorker {
		indexWorker = indexworker.New(db.Changesets(), cfg.IndexBatchSize, cfg.IndexInterval, cfg.IndexBackoffMax)
		go indexWorker.Run(ctx)
	}
	if !cfg.DisableAsyncPublisher {
		// A landed commit records changeset.submitted in its transaction.
		nudge := dispatcher.Nudge
		if indexWorker != nil {
			nudge = func() {
				indexWorker.Nudge()
				dispatcher.Nudge()
			}
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
		Changesets: events.WrapChangesets(db.Changesets(), dispatcher),
		Repository: db.Repository(),
		Slices:     events.WrapSlices(db.Slices(), dispatcher),
		Agents:     db.Agents(),
		Checks:     events.WrapChecks(db.Checks(), dispatcher),
		Feedback:   db.Feedback(),
		Blames:     db.Blames(),
		Triggers:   db.Triggers(),
	}
	triggerChecks.CheckStore = stores.Checks
	handlers := service.New(stores, objectStore, tracker)
	handlers.SetTriggerEngine(triggerEngine)
	doorbell, err := service.NewDoorbell(cfg.DoorbellURL, cfg.DoorbellSecret)
	if err != nil {
		return err
	}
	handlers.SetDoorbell(doorbell)
	handlers.SetCIRunners(cfg.CIRunnerDaemons)
	handlers.Feedback.Account = cfg.FeedbackAccount
	handlers.Feedback.OperatorSubjects = cfg.OperatorSubjects
	handlers.Slice.GitMirrors = gitMirrors
	handlers.Auth.AgentSignupEnabled = cfg.AgentSignupEnabled
	handlers.Auth.OperatorSubjects = cfg.OperatorSubjects
	if users := clerk.NewUserClient(cfg.Clerk.SecretKey); users != nil {
		handlers.Auth.ClerkUsers = users
	}
	if handlers.Agent != nil {
		go handlers.Agent.RunCheckDispatchSweep(ctx)
	}
	if !cfg.DisableBlameWarmer && handlers.Repository != nil {
		// Blames of the files each landing changed, stored for whoever opens
		// them next (service/blame_warm.go).
		go handlers.Repository.RunBlameWarmer(ctx)
	}
	if cfg.GitBlobBackfill && handlers.Blob != nil {
		go handlers.Blob.RunGitBlobBackfill(ctx)
	}
	limiters := newServerLimiters(cfg)
	grpcServer := newPrincipalGRPCServer(resolvePrincipal, handlers, limiters)
	apiHandler := newPrincipalConnectHandler(resolvePrincipal, handlers, limiters)
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
		Blobs:      db.Blobs(),
	}, objectStore, gitCacheRoot)
	if err != nil {
		return err
	}
	gitcompatHandler := gitcompat.NewHandlerWithPrincipal(gitcompat.PrincipalResolver(resolvePrincipal), projector, handlers.Blob, handlers.Changeset)
	// Covers (internal/gitcompat/cover.go) are built in a request the server
	// sends itself, at its own URL; without one they are built in-process.
	if key := cfg.internalKey(); key != nil {
		gitcompatHandler.SetInternalKey(key)
		if cfg.SelfURL != "" {
			projector.SetCompactRequester(gitcompat.SelfCompactRequester(cfg.SelfURL, key))
		}
	}
	var mirrorRequest func(account, slice string)
	if key := cfg.internalKey(); key != nil && cfg.SelfURL != "" {
		// Mirror pushes, like covers, run in a request to the server itself,
		// where Cloud Run gives them CPU.
		mirrorRequest = gitcompat.SelfMirrorRequester(cfg.SelfURL, key)
	}
	gitMirrors.SetPusher(projector.PushMirror, mirrorRequest)
	gitcompatHandler.SetMirrorRunner(gitMirrors.Run)
	var gitHandler http.Handler = gitcompatHandler
	gitHandler = limiters.httpIPMiddleware()(gitHandler)
	gatewayMux := newHTTPHandler(apiHandler, gitHandler, cfg.HTTPAllowedOrigin, cfg, true, limiters)
	combinedServer := &http.Server{
		Handler:           newCombinedServerHandler(grpcServer, gatewayMux, activity.Default),
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
			Handler:           activity.Default.Middleware(gatewayMux),
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
			Handler: activity.Default.Middleware(gitHandler),
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
	if secrets == nil {
		slog.Warn("GITSLICE_SECRETS_KEY is unset; secret storage and signed self-requests are disabled (set GITSLICE_REQUIRE_SECRETS_KEY=1 in production)")
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
	return newCombinedServerHandler(grpcServer, gateway, nil)
}

func newCombinedServerHandler(grpcServer *grpc.Server, gateway http.Handler, gate *activity.Gate) http.Handler {
	boundedGateway := withNonStreamingDeadlines(gateway, gatewayReadTimeout, gatewayWriteTimeout)
	inner := http.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			grpcServer.ServeHTTP(w, r)
			return
		}
		boundedGateway.ServeHTTP(w, r)
	}))
	// Apply activity tracking inside h2c.NewHandler so each HTTP/2 stream has
	// its individual path/method inspected (rather than holding the connection's
	// PRI * HTTP/2.0 upgrade request in-flight for the lifetime of the socket).
	if gate != nil {
		inner = gate.Middleware(inner)
	}
	return h2c.NewHandler(inner, &http2.Server{
		// Receive windows for uploads (UploadBlobs, blob streams); the
		// defaults (1 MiB) cap a batch upload at a window per round trip.
		MaxUploadBufferPerStream:     4 << 20,
		MaxUploadBufferPerConnection: 16 << 20,
	})
}

func withNonStreamingDeadlines(next http.Handler, readTimeout, writeTimeout time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isStreamingHTTPRequest(r) {
			rc := http.NewResponseController(w)
			now := time.Now()
			_ = rc.SetReadDeadline(now.Add(readTimeout))
			_ = rc.SetWriteDeadline(now.Add(writeTimeout))
		}
		next.ServeHTTP(w, r)
	})
}

func isStreamingHTTPRequest(r *http.Request) bool {
	if strings.HasPrefix(r.URL.Path, "/git/") {
		return true
	}
	ct := strings.ToLower(r.Header.Get("Content-Type"))
	if strings.HasPrefix(ct, "application/grpc") || strings.HasPrefix(ct, "application/connect+") {
		return true
	}
	if strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/event-stream") {
		return true
	}
	return streamingProcedures[r.URL.Path]
}

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

func NewGRPCServer(resolve subjectResolver, handlers *service.Handlers, cfgs ...Config) *grpc.Server {
	return NewPrincipalGRPCServer(toPrincipalResolver(resolve), handlers, cfgs...)
}

func NewPrincipalGRPCServer(resolve principalResolver, handlers *service.Handlers, cfgs ...Config) *grpc.Server {
	var cfg Config
	if len(cfgs) > 0 {
		cfg = cfgs[0]
	}
	return newPrincipalGRPCServer(resolve, handlers, newServerLimiters(cfg))
}

func newGRPCServer(resolve subjectResolver, handlers *service.Handlers, limiters *serverLimiters) *grpc.Server {
	return newPrincipalGRPCServer(toPrincipalResolver(resolve), handlers, limiters)
}

func newPrincipalGRPCServer(resolve principalResolver, handlers *service.Handlers, limiters *serverLimiters) *grpc.Server {
	grpcServer := grpc.NewServer(
		grpc.MaxRecvMsgSize(rpclimits.MaxUnaryMessageBytes),
		grpc.MaxSendMsgSize(rpclimits.MaxUnaryMessageBytes),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             10 * time.Second,
			PermitWithoutStream: true,
		}),
		grpc.ChainUnaryInterceptor(
			requestIDUnaryInterceptor(),
			grpcMetricsUnaryInterceptor(),
			limiters.grpcPreAuthUnaryInterceptor(),
			principalAuthInterceptor(resolve),
			limiters.grpcSubjectUnaryInterceptor(),
			limiters.agentSignupUnaryInterceptor(),
			limiters.triggerRunFailureUnaryInterceptor(),
		),
		grpc.ChainStreamInterceptor(
			requestIDStreamInterceptor(),
			grpcMetricsStreamInterceptor(),
			limiters.grpcPreAuthStreamInterceptor(),
			principalAuthStreamInterceptor(resolve),
			limiters.grpcSubjectStreamInterceptor(),
		),
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
	corev1.RegisterSecretServiceServer(grpcServer, handlers.Secret)
	corev1.RegisterFeedbackServiceServer(grpcServer, handlers.Feedback)
	corev1.RegisterTriggerServiceServer(grpcServer, handlers.Trigger)
	corev1.RegisterTriggerRunServiceServer(grpcServer, handlers.TriggerRun)
	healthServer := health.NewServer()
	healthServer.SetServingStatus("", healthv1.HealthCheckResponse_SERVING)
	healthv1.RegisterHealthServer(grpcServer, healthServer)
	return grpcServer
}

// lateCheckStore is a check store set after it is handed out: the trigger
// engine needs the check store wrapped by the event dispatcher, which needs
// the engine.
type lateCheckStore struct {
	storage.CheckStore
}
