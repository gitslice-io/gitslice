package service

import (
	"context"

	"gitslice.io/gitslice/internal/activity"
	"gitslice.io/gitslice/internal/analytics"
	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/automations"
	"gitslice.io/gitslice/internal/blame"
	"gitslice.io/gitslice/internal/doorbell"
	"gitslice.io/gitslice/internal/gitmirror"
	"gitslice.io/gitslice/internal/objectstore"
	"gitslice.io/gitslice/internal/storage"
)

type ObjectStore = objectstore.Store

// Options configures optional server integrations and runtime dependencies for
// service handlers, avoiding post-hoc field mutation after construction.
type Options struct {
	AutomationEngine   *automations.Engine
	Doorbell           *doorbell.Client
	CIRunners          []string
	FeedbackAccount    string
	OperatorSubjects   []string
	GitMirrors         *gitmirror.Runner
	AgentSignupEnabled bool
	ClerkUsers         ClerkUserDirectory
	ActivityGate       *activity.Gate
}

// WorkerFunc is a blocking background loop that exits when ctx is canceled.
type WorkerFunc func(ctx context.Context)

// WorkerOptions controls which background workers Handlers.Workers returns.
type WorkerOptions struct {
	DisableBlameWarmer bool
	GitBlobBackfill    bool
	// Gate overrides the Handlers' ActivityGate when non-nil.
	Gate *activity.Gate
}

type Handlers struct {
	Auth       *AuthService
	Repository *RepositoryService
	Blob       *BlobService
	Slice      *SliceService
	Workspace  *WorkspaceService
	Changeset  *ChangesetService
	Stack      *ChangesetStackService
	Agent      *AgentService
	Check      *CheckService
	Secret     *SecretService
	Feedback   *FeedbackService
	// Trigger and AutomationRun need an engine (design/34_triggers.md), which
	// the server sets.
	Automation    *AutomationService
	AutomationRun *AutomationRunService

	gate *activity.Gate
}

type Stores struct {
	Auth       storage.AuthStore
	Blobs      storage.BlobStore
	Changesets storage.ChangesetStore
	Repository storage.RepositoryStore
	Slices     storage.SliceStore
	Agents     storage.AgentStore
	Checks     storage.CheckStore
	// Feedback is optional: without it the FeedbackService is unavailable.
	Feedback storage.FeedbackStore
	// Blames is optional: without it every blame walks the file's history.
	Blames storage.BlameStore
	// Automations is optional: without it the trigger services are unavailable.
	Automations storage.AutomationStore
}

// New builds all RPC handlers with optional integrations and background worker
// dependencies wired upfront.
func New(stores Stores, objectStore ObjectStore, analyticsClient analytics.Client, options ...Options) *Handlers {
	var opts Options
	if len(options) > 0 {
		opts = options[0]
	}
	validator := diffValidator{
		Blobs:      stores.Blobs,
		Repository: stores.Repository,
		Slices:     stores.Slices,
	}
	hub := newAgentHub()
	checkLogs := newCheckLogHub()
	checkDispatcher := &checkDispatcher{
		Auth:        stores.Auth,
		Checks:      stores.Checks,
		Changesets:  stores.Changesets,
		Repository:  stores.Repository,
		Slices:      stores.Slices,
		Agents:      stores.Agents,
		ObjectStore: objectStore,
		hub:         hub,
	}
	blobService := &BlobService{
		Auth:        stores.Auth,
		Blobs:       stores.Blobs,
		Slices:      stores.Slices,
		ObjectStore: objectStore,
		gate:        opts.ActivityGate,
	}
	csCore := &changesetCore{
		Auth:        stores.Auth,
		Blobs:       stores.Blobs,
		Changesets:  stores.Changesets,
		Repository:  stores.Repository,
		Slices:      stores.Slices,
		Agents:      stores.Agents,
		Checks:      stores.Checks,
		ObjectStore: objectStore,
		Analytics:   analyticsClient,
		validator:   validator,
		dispatcher:  checkDispatcher,
		blobService: blobService,
	}
	changesetSvc := &ChangesetService{changesetCore: csCore}
	h := &Handlers{
		Auth: &AuthService{
			Auth:               stores.Auth,
			Slices:             stores.Slices,
			Analytics:          analyticsClient,
			AgentSignupEnabled: opts.AgentSignupEnabled,
			ClerkUsers:         opts.ClerkUsers,
			OperatorSubjects:   opts.OperatorSubjects,
			ObjectStore:        objectStore,
		},
		Repository: &RepositoryService{
			Auth:           stores.Auth,
			Blobs:          stores.Blobs,
			Changesets:     stores.Changesets,
			Repository:     stores.Repository,
			Slices:         stores.Slices,
			ObjectStore:    objectStore,
			Blames:         stores.Blames,
			validator:      validator,
			changesetsCore: csCore,
			gate:           opts.ActivityGate,
		},
		Blob: blobService,
		Slice: &SliceService{
			Auth:            stores.Auth,
			Repository:      stores.Repository,
			Slices:          stores.Slices,
			Agents:          stores.Agents,
			Analytics:       analyticsClient,
			gitMirrorRunner: opts.GitMirrors,
		},
		Workspace: &WorkspaceService{
			Auth:        stores.Auth,
			Repository:  stores.Repository,
			Slices:      stores.Slices,
			ObjectStore: objectStore,
			validator:   validator,
		},
		Changeset: changesetSvc,
		Stack:     &ChangesetStackService{changesetCore: csCore},
		Agent: &AgentService{
			Auth:        stores.Auth,
			Slices:      stores.Slices,
			Agents:      stores.Agents,
			Changesets:  stores.Changesets,
			Repository:  stores.Repository,
			Checks:      stores.Checks,
			ObjectStore: objectStore,
			hub:         hub,
			checkLogs:   checkLogs,
			dispatcher:  checkDispatcher,
			gate:        opts.ActivityGate,
		},
		Check: &CheckService{
			Auth:       stores.Auth,
			Changesets: stores.Changesets,
			Slices:     stores.Slices,
			Repository: stores.Repository,
			Checks:     stores.Checks,
			Agents:     stores.Agents,
			checkLogs:  checkLogs,
			dispatcher: checkDispatcher,
			hub:        hub,
		},
		Secret: &SecretService{Auth: stores.Auth, Slices: stores.Slices},
		Feedback: &FeedbackService{
			Auth:             stores.Auth,
			Feedback:         stores.Feedback,
			Account:          opts.FeedbackAccount,
			OperatorSubjects: opts.OperatorSubjects,
		},
		Automation: &AutomationService{
			Auth: stores.Auth, Changesets: stores.Changesets, Slices: stores.Slices, Repository: stores.Repository,
			Automations: stores.Automations, Tree: TreeFiles{Repository: stores.Repository, Objects: objectStore},
		},
		AutomationRun: &AutomationRunService{
			Changesets: stores.Changesets, Blobs: stores.Blobs, Slices: stores.Slices, Repository: stores.Repository, Checks: stores.Checks,
			Automations: stores.Automations, Tree: TreeFiles{Repository: stores.Repository, Objects: objectStore},
			ChangesetService: changesetSvc,
		},
		gate: opts.ActivityGate,
	}
	if opts.AutomationEngine != nil {
		h.setAutomationEngine(opts.AutomationEngine)
	}
	if opts.Doorbell != nil {
		h.setDoorbell(opts.Doorbell)
	}
	if len(opts.CIRunners) > 0 {
		h.setCIRunners(opts.CIRunners)
	}
	return h
}

func (h *Handlers) resolveGate(override *activity.Gate) *activity.Gate {
	if override != nil {
		return override
	}
	if h != nil && h.gate != nil {
		return h.gate
	}
	return activity.Default
}

// NewBlameWarmer constructs a standalone blame warmer worker for these handlers.
func (h *Handlers) NewBlameWarmer(gate *activity.Gate) *blame.Warmer {
	if h == nil || h.Repository == nil {
		return nil
	}
	return h.Repository.NewBlameWarmer(h.resolveGate(gate))
}

// CheckDispatchSweeper runs periodic CI check dispatch sweeps and recovery.
type CheckDispatchSweeper struct {
	agent *AgentService
	gate  *activity.Gate
}

// NewCheckDispatchSweeper constructs a standalone check dispatch sweep worker.
func (h *Handlers) NewCheckDispatchSweeper(gate *activity.Gate) *CheckDispatchSweeper {
	if h == nil || h.Agent == nil {
		return nil
	}
	return &CheckDispatchSweeper{agent: h.Agent, gate: h.resolveGate(gate)}
}

// Run executes the check dispatch sweep loop until ctx is canceled.
func (w *CheckDispatchSweeper) Run(ctx context.Context) {
	if w == nil || w.agent == nil {
		return
	}
	w.agent.runCheckDispatchSweepWithGate(ctx, w.gate)
}

// GitBlobBackfillWorker backfills Git blob IDs for previously uploaded blobs.
type GitBlobBackfillWorker struct {
	blob *BlobService
	gate *activity.Gate
}

// NewGitBlobBackfillWorker constructs a standalone Git blob ID backfill worker.
func (h *Handlers) NewGitBlobBackfillWorker(gate *activity.Gate) *GitBlobBackfillWorker {
	if h == nil || h.Blob == nil {
		return nil
	}
	return &GitBlobBackfillWorker{blob: h.Blob, gate: h.resolveGate(gate)}
}

// Run executes the Git blob ID backfill loop until ctx is canceled.
func (w *GitBlobBackfillWorker) Run(ctx context.Context) {
	if w == nil || w.blob == nil {
		return
	}
	w.blob.runGitBlobBackfillWithGate(ctx, w.gate)
}

// Workers returns the enabled background worker loops for these handlers.
func (h *Handlers) Workers(opts WorkerOptions) []WorkerFunc {
	if h == nil {
		return nil
	}
	gate := h.resolveGate(opts.Gate)
	var workers []WorkerFunc
	if sweeper := h.NewCheckDispatchSweeper(gate); sweeper != nil {
		workers = append(workers, sweeper.Run)
	}
	if !opts.DisableBlameWarmer {
		if warmer := h.NewBlameWarmer(gate); warmer != nil {
			workers = append(workers, warmer.Run)
		}
	}
	if opts.GitBlobBackfill {
		if backfill := h.NewGitBlobBackfillWorker(gate); backfill != nil {
			workers = append(workers, backfill.Run)
		}
	}
	return workers
}

// setAutomationEngine gives the automation services their engine, and the engine
// what runs check actions on CI daemons.
func (h *Handlers) setAutomationEngine(engine *automations.Engine) {
	h.Automation.Engine = engine
	h.AutomationRun.Engine = engine
	if h.Changeset != nil && h.Changeset.dispatcher != nil {
		engine.SetCheckLauncher(h.Changeset.dispatcher)
	}
	if engine != nil && engine.TemplateLookup() == nil {
		engine.SetTemplateLookup(&sliceTemplateLookup{
			Repository: h.Automation.Repository,
			Slices:     h.Automation.Slices,
			Auth:       h.Automation.Auth,
		})
	}
}

func captureAnalytics(ctx context.Context, tracker analytics.Client, eventName string, props map[string]any) {
	if tracker == nil {
		return
	}
	subjectID, ok := authctx.SubjectID(ctx)
	if !ok || subjectID == "" {
		return
	}
	tracker.Capture(ctx, analytics.Event{
		Name:       eventName,
		DistinctID: subjectID,
		Props:      props,
	})
}
