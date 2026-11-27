package server

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"gitslice.io/gitslice/internal/activity"
	"gitslice.io/gitslice/internal/analytics"
	"gitslice.io/gitslice/internal/auth/clerk"
	"gitslice.io/gitslice/internal/automations"
	"gitslice.io/gitslice/internal/bgloop"
	"gitslice.io/gitslice/internal/doorbell"
	"gitslice.io/gitslice/internal/events"
	"gitslice.io/gitslice/internal/gitcompat"
	"gitslice.io/gitslice/internal/gitmirror"
	"gitslice.io/gitslice/internal/indexworker"
	"gitslice.io/gitslice/internal/objectstore/cache"
	"gitslice.io/gitslice/internal/objectstore/filesystem"
	"gitslice.io/gitslice/internal/objectstore/latency"
	"gitslice.io/gitslice/internal/objectstore/packed"
	"gitslice.io/gitslice/internal/objectstore/r2"
	"gitslice.io/gitslice/internal/postgres"
	"gitslice.io/gitslice/internal/secretbox"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/treestore"
	"gitslice.io/gitslice/service"
	"google.golang.org/grpc"
	// Registers gzip and zstd, so clients can compress batch calls (ReadFiles,
	// UploadBlobs, ImportGitCommitBatch) and their responses.
	_ "gitslice.io/gitslice/internal/grpccomp"
	_ "google.golang.org/grpc/encoding/gzip"
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

type infra struct {
	db          *postgres.DB
	objectStore service.ObjectStore
	secrets     *secretbox.Box
}

func openInfra(ctx context.Context, cfg Config) (*infra, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	secrets, err := cfg.secretsBox()
	if err != nil {
		return nil, err
	}
	if secrets == nil {
		slog.Warn("GITSLICE_SECRETS_KEY is unset; secret storage and signed self-requests are disabled (set GITSLICE_REQUIRE_SECRETS_KEY=1 in production)")
	}
	db, err := postgres.Open(ctx, cfg.Storage.DatabaseURL)
	if err != nil {
		return nil, err
	}
	db.Slices().Secrets = secrets
	objectStore, err := newObjectStore(cfg)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	objectStore = packed.New(objectStore, db.PackedObjects())
	objectStore = cache.New(objectStore, cfg.Storage.ObjectCacheBytes, 4<<20)
	db.SetTreeStore(treestore.New(objectStore))
	return &infra{
		db:          db,
		objectStore: objectStore,
		secrets:     secrets,
	}, nil
}

func (i *infra) Close() error {
	if i == nil || i.db == nil {
		return nil
	}
	return i.db.Close()
}

type workerGroup struct {
	wg sync.WaitGroup
}

func (g *workerGroup) Go(fn func()) {
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		fn()
	}()
}

func (g *workerGroup) Wait() {
	g.wg.Wait()
}

// App owns the fully wired server runtime: infrastructure, domain services,
// background workers, and network listeners/servers.
type App struct {
	cfg      Config
	infra    *infra
	tracker  analytics.Client
	gate     *activity.Gate
	limiters *serverLimiters

	dispatcher  *events.Dispatcher
	indexWorker *indexworker.Worker
	pubRunner   *bgloop.Runner
	handlers    *service.Handlers

	grpcServer     *grpc.Server
	combinedServer *http.Server
	combinedLis    net.Listener
	gatewayServer  *http.Server
	gatewayLis     net.Listener
	gitHTTPServer  *http.Server
	gitHTTPLis     net.Listener

	workers      workerGroup
	workerCancel context.CancelFunc
	shutdownOnce sync.Once
	closeOnce    sync.Once
}

func Run(ctx context.Context, cfg Config) error {
	app, err := buildApp(ctx, cfg)
	if err != nil {
		return err
	}
	defer app.Close()

	workerCtx, cancelWorkers := context.WithCancel(ctx)
	app.workerCancel = cancelWorkers
	app.StartWorkers(workerCtx)

	return app.Serve(ctx)
}

func buildApp(ctx context.Context, cfg Config) (*App, error) {
	cfg.ApplyDefaults()
	inf, err := openInfra(ctx, cfg)
	if err != nil {
		return nil, err
	}
	app := &App{
		cfg:   cfg,
		infra: inf,
		// Start background database loops idle until real user traffic arrives,
		// so a cold-booted instance poked only by health checks or metrics
		// scrapes lets Neon stay scaled to zero.
		gate: activity.NewIdle(activity.IdleAfter),
	}
	ok := false
	defer func() {
		if !ok {
			app.Close()
		}
	}()

	tracker, err := analytics.New(cfg.Integrations.PostHogAPIKey, cfg.Integrations.PostHogHost, cfg.Integrations.PostHogEnvironment)
	if err != nil {
		slog.Warn("failed to initialize analytics; using no-op client", "error", err)
		tracker, err = analytics.New("", "", "")
		if err != nil {
			slog.Warn("failed to initialize no-op analytics client", "error", err)
		}
	}
	app.tracker = tracker

	db := inf.db
	objectStore := inf.objectStore

	resolvePrincipal, err := newPrincipalResolver(db.Auth(), cfg)
	if err != nil {
		return nil, err
	}
	resolvePrincipal = cachedPrincipalResolver(resolvePrincipal, time.Now)

	if cfg.Storage.RunMigrations {
		if err := db.Migrate(ctx); err != nil {
			return nil, err
		}
	}

	// Trigger checks are written through the wrapped check store below, so
	// the events they cause are recorded too. All wiring completes before
	// StartWorkers launches any goroutine.
	triggerChecks := &lateCheckStore{}
	automationEngine := automations.NewEngine(automations.Stores{
		Automations: db.Automations(),
		Changesets:  db.Changesets(),
		Slices:      db.Slices(),
		Repository:  db.Repository(),
		Checks:      triggerChecks,
	}, automations.Options{
		Tree:                service.TreeFiles{Repository: db.Repository(), Objects: objectStore},
		DispatchURL:         cfg.Integrations.TriggerDispatchURL,
		DispatchSecret:      cfg.Integrations.TriggerDispatchSecret,
		AllowPrivateTargets: cfg.Network.OutboundAllowPrivateTargets,
	})
	// Slice Git mirrors (design/35_git_mirrors.md): the dispatcher's drains
	// queue and claim pushes; the projector (below) does them.
	gitMirrors := &gitmirror.Runner{
		Mirrors:             db.GitMirrors(),
		Slices:              db.Slices(),
		AllowPrivateTargets: cfg.Network.OutboundAllowPrivateTargets,
	}
	dispatcher := events.NewDispatcher(events.Stores{
		Events:     db.Events(),
		Slices:     db.Slices(),
		Changesets: db.Changesets(),
		Checks:     db.Checks(),
		Auth:       db.Auth(),
	}, events.Options{
		WebBaseURL:  cfg.webBaseURL(),
		Automations: automationEngine,
		Mirrors:     gitMirrors,
		Gate:        app.gate,
	})
	gitMirrors.Nudge = dispatcher.Nudge
	app.dispatcher = dispatcher

	if !cfg.Workers.DisableIndexWorker {
		iw := indexworker.New(db.Changesets(), cfg.Workers.IndexBatchSize, cfg.Workers.IndexInterval, cfg.Workers.IndexBackoffMax)
		iw.SetGate(app.gate)
		app.indexWorker = iw
	}
	if !cfg.Workers.DisableAsyncPublisher {
		// A landed commit records changeset.submitted in its transaction.
		nudge := dispatcher.Nudge
		if app.indexWorker != nil {
			nudge = func() {
				app.indexWorker.Nudge()
				dispatcher.Nudge()
			}
		}
		app.pubRunner = newPublisherRunner(
			db.Changesets(),
			cfg.Workers.PublishBatchSize,
			cfg.Workers.PublishInterval,
			cfg.Workers.PublishBackoffMax,
			nudge,
			app.gate,
		)
		db.Changesets().SetPendingPublishListener(app.pubRunner.Nudge)
	}

	stores := service.Stores{
		Auth:        db.Auth(),
		Blobs:       db.Blobs(),
		Changesets:  events.WrapChangesets(db.Changesets(), dispatcher),
		Repository:  db.Repository(),
		Slices:      events.WrapSlices(db.Slices(), dispatcher),
		Agents:      db.Agents(),
		Checks:      events.WrapChecks(db.Checks(), dispatcher),
		Feedback:    db.Feedback(),
		Blames:      db.Blames(),
		Automations: db.Automations(),
	}
	triggerChecks.CheckStore = stores.Checks

	dbell, err := doorbell.New(cfg.Integrations.DoorbellURL, cfg.Integrations.DoorbellSecret)
	if err != nil {
		return nil, err
	}
	var clerkUsers service.ClerkUserDirectory
	if users := clerk.NewUserClient(cfg.Auth.Clerk.SecretKey); users != nil {
		clerkUsers = users
	}
	handlers := service.NewWithOptions(stores, objectStore, tracker, service.Options{
		AutomationEngine:   automationEngine,
		Doorbell:           dbell,
		CIRunners:          cfg.Integrations.CIRunnerDaemons,
		FeedbackAccount:    cfg.Auth.FeedbackAccount,
		OperatorSubjects:   cfg.Auth.OperatorSubjects,
		GitMirrors:         gitMirrors,
		AgentSignupEnabled: cfg.Auth.AgentSignupEnabled,
		ClerkUsers:         clerkUsers,
		ActivityGate:       app.gate,
	})
	app.handlers = handlers

	projector, err := gitcompat.NewProjector(gitcompat.ProjectorStores{
		Auth:       db.Auth(),
		Repository: db.Repository(),
		Slices:     db.Slices(),
		Blobs:      db.Blobs(),
	}, objectStore, cfg.gitCacheRoot())
	if err != nil {
		return nil, err
	}
	gitcompatHandler := gitcompat.NewHandlerWithPrincipal(gitcompat.PrincipalResolver(resolvePrincipal), projector, handlers.Blob, handlers.Changeset)
	// Covers (internal/gitcompat/cover.go) are built in a request the server
	// sends itself, at its own URL; without one they are built in-process.
	if key := cfg.internalKey(); key != nil {
		gitcompatHandler.SetInternalKey(key)
		if cfg.Network.SelfURL != "" {
			projector.SetCompactRequester(gitcompat.SelfCompactRequester(cfg.Network.SelfURL, key))
		}
	}
	var mirrorRequest func(account, slice string)
	if key := cfg.internalKey(); key != nil && cfg.Network.SelfURL != "" {
		// Mirror pushes, like covers, run in a request to the server itself,
		// where Cloud Run gives them CPU.
		mirrorRequest = gitcompat.SelfMirrorRequester(cfg.Network.SelfURL, key)
	}
	// Wire mirror pusher BEFORE any worker starts so dispatcher never observes
	// a nil pusher.
	gitMirrors.SetPusher(service.GitMirrorPusher(projector.PushMirror), mirrorRequest)
	gitcompatHandler.SetMirrorRunner(gitMirrors.Run)

	limiters := newServerLimiters(cfg)
	app.limiters = limiters
	app.grpcServer = newPrincipalGRPCServer(resolvePrincipal, handlers, limiters)
	apiHandler := newPrincipalConnectHandler(resolvePrincipal, handlers, limiters, cfg.webBaseURL())
	gitHandler := limiters.httpIPMiddleware()(gitcompatHandler)
	gatewayMux := newHTTPHandler(apiHandler, gitHandler, cfg.Network.HTTPAllowedOrigin, cfg, true, limiters)

	lis, err := net.Listen("tcp", cfg.Network.GRPCAddr)
	if err != nil {
		return nil, err
	}
	app.combinedLis = lis
	app.combinedServer = &http.Server{
		Handler:           newCombinedServerHandler(app.grpcServer, gatewayMux, app.gate),
		ReadHeaderTimeout: gatewayReadHeaderTimeout,
		IdleTimeout:       gatewayIdleTimeout,
	}

	if cfg.Network.HTTPAddr != "" {
		gatewayLis, err := net.Listen("tcp", cfg.Network.HTTPAddr)
		if err != nil {
			return nil, err
		}
		app.gatewayLis = gatewayLis
		app.gatewayServer = &http.Server{
			Handler:           app.gate.Middleware(gatewayMux),
			ReadHeaderTimeout: gatewayReadHeaderTimeout,
			ReadTimeout:       gatewayReadTimeout,
			WriteTimeout:      gatewayWriteTimeout,
			IdleTimeout:       gatewayIdleTimeout,
		}
	}
	if cfg.Network.GitHTTPAddr != "" {
		gitHTTPLis, err := net.Listen("tcp", cfg.Network.GitHTTPAddr)
		if err != nil {
			return nil, err
		}
		app.gitHTTPLis = gitHTTPLis
		app.gitHTTPServer = &http.Server{
			Handler: app.gate.Middleware(gitHandler),
			// Git clone/fetch/push transfers can be large and slow, so only the
			// header-read deadline (slowloris protection) and the idle-keepalive
			// deadline are bounded here; body read/write are left to the request
			// context and the body-size cap in the handler.
			ReadHeaderTimeout: gitReadHeaderTimeout,
			IdleTimeout:       gitIdleTimeout,
		}
	}

	ok = true
	return app, nil
}

// StartWorkers launches all background workers under app.workers so Shutdown
// can cancel and drain them cleanly before the database is closed.
func (a *App) StartWorkers(ctx context.Context) {
	if a.dispatcher != nil {
		a.workers.Go(func() { a.dispatcher.Run(ctx) })
	}
	if a.indexWorker != nil {
		a.workers.Go(func() { a.indexWorker.Run(ctx) })
	}
	if a.pubRunner != nil {
		a.workers.Go(func() { a.pubRunner.Run(ctx) })
	}
	if a.handlers != nil {
		for _, worker := range a.handlers.Workers(service.WorkerOptions{
			DisableBlameWarmer: a.cfg.Workers.DisableBlameWarmer,
			GitBlobBackfill:    a.cfg.Workers.GitBlobBackfill,
			Gate:               a.gate,
		}) {
			worker := worker
			a.workers.Go(func() { worker(ctx) })
		}
	}
}

// Serve runs the network servers until ctx is cancelled or a listener fails,
// then performs a graceful Shutdown before returning.
func (a *App) Serve(ctx context.Context) error {
	errCh := make(chan error, 3)
	go func() {
		slog.Info("gitslice server listening", "grpc_addr", a.combinedLis.Addr().String())
		errCh <- a.combinedServer.Serve(a.combinedLis)
	}()
	if a.gatewayServer != nil {
		go func() {
			slog.Info("gitslice http gateway listening", "http_addr", a.gatewayLis.Addr().String())
			errCh <- a.gatewayServer.Serve(a.gatewayLis)
		}()
	}
	if a.gitHTTPServer != nil {
		go func() {
			slog.Info("gitslice git http listening", "git_http_addr", a.gitHTTPLis.Addr().String())
			errCh <- a.gitHTTPServer.Serve(a.gitHTTPLis)
		}()
	}

	select {
	case <-ctx.Done():
		a.Shutdown()
		return ctx.Err()
	case err := <-errCh:
		a.Shutdown()
		if errors.Is(err, grpc.ErrServerStopped) || errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// Shutdown drains HTTP/gRPC servers, cancels background workers, and waits for
// every worker goroutine to exit before returning.
func (a *App) Shutdown() {
	a.shutdownOnce.Do(func() {
		// Bound the HTTP drain: long-lived AgentService.Connect streams would
		// otherwise keep the combined server from ever going idle.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), serverShutdownTimeout)
		defer cancel()
		if a.combinedServer != nil {
			_ = a.combinedServer.Shutdown(shutdownCtx)
		}
		// gRPC is multiplexed over HTTP via grpcServer.ServeHTTP; that
		// serverHandlerTransport does not implement Drain(), so GracefulStop()
		// panics whenever a server stream is still open at shutdown (as the
		// daemon Connect stream always is). Stop() closes the handler
		// transports without draining, which is correct here because
		// combinedServer.Shutdown already drained in-flight HTTP/gRPC requests.
		if a.grpcServer != nil {
			a.grpcServer.Stop()
		}
		if a.gatewayServer != nil {
			_ = a.gatewayServer.Shutdown(shutdownCtx)
		}
		if a.gitHTTPServer != nil {
			_ = a.gitHTTPServer.Shutdown(shutdownCtx)
		}
		if a.workerCancel != nil {
			a.workerCancel()
		}
		a.workers.Wait()
	})
}

// Close shuts down servers and workers, closes any unstarted listeners, flushes
// analytics, and closes the database pool.
func (a *App) Close() {
	a.closeOnce.Do(func() {
		a.Shutdown()
		if a.combinedLis != nil {
			_ = a.combinedLis.Close()
		}
		if a.gatewayLis != nil {
			_ = a.gatewayLis.Close()
		}
		if a.gitHTTPLis != nil {
			_ = a.gitHTTPLis.Close()
		}
		if a.tracker != nil {
			if err := a.tracker.Close(); err != nil {
				slog.Warn("failed to close analytics client", "error", err)
			}
		}
		if a.infra != nil {
			if err := a.infra.Close(); err != nil {
				slog.Warn("failed to close database", "error", err)
			}
		}
	})
}

func Migrate(ctx context.Context, cfg Config) (err error) {
	inf, err := openInfra(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := inf.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	return inf.db.Migrate(ctx)
}

// newObjectStore selects the object store backend from config: Cloudflare R2 when
// OBJECT_STORE_TYPE=r2, otherwise the prototype filesystem store. An optional
// benchmark latency wrapper remains inside the cache installed by callers.
func newObjectStore(cfg Config) (service.ObjectStore, error) {
	var objectStore service.ObjectStore
	if cfg.usesR2() {
		store, err := r2.New(cfg.Storage.R2)
		if err != nil {
			return nil, err
		}
		objectStore = store
	} else {
		store, err := filesystem.New(cfg.Storage.ObjectStoreRoot)
		if err != nil {
			return nil, err
		}
		objectStore = store
	}
	if cfg.Storage.ObjectStoreLatency > 0 {
		objectStore = latency.New(objectStore, cfg.Storage.ObjectStoreLatency)
	}
	return objectStore, nil
}

// lateCheckStore breaks the 3-way cycle between automations.Engine,
// events.Dispatcher, and events.WrapChecks; its CheckStore is populated during
// buildApp before StartWorkers launches any goroutine.
type lateCheckStore struct {
	storage.CheckStore
}
