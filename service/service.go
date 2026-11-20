package service

import (
	"context"
	"io"

	"gitslice.io/gitslice/internal/analytics"
	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/automations"
	"gitslice.io/gitslice/internal/storage"
)

type ObjectStore interface {
	Put(context.Context, string, io.Reader) error
	Get(context.Context, string, int64, int64) (io.ReadCloser, error)
	Delete(context.Context, string) error
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
	// Triggers is optional: without it the trigger services are unavailable.
	Automations storage.AutomationStore
}

func New(stores Stores, objectStore ObjectStore, analyticsClient analytics.Client) *Handlers {
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
	blobService := &BlobService{Auth: stores.Auth, Blobs: stores.Blobs, Slices: stores.Slices, ObjectStore: objectStore}
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
	return &Handlers{
		Auth: &AuthService{Auth: stores.Auth, Slices: stores.Slices, Analytics: analyticsClient, ObjectStore: objectStore},
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
		},
		Blob: blobService,
		Slice: &SliceService{
			Auth:       stores.Auth,
			Repository: stores.Repository,
			Slices:     stores.Slices,
			Agents:     stores.Agents,
			Analytics:  analyticsClient,
		},
		Workspace: &WorkspaceService{
			Auth:        stores.Auth,
			Repository:  stores.Repository,
			Slices:      stores.Slices,
			ObjectStore: objectStore,
			validator:   validator,
		},
		Changeset: &ChangesetService{changesetCore: csCore},
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
		// The server sets who reads feedback (Account, OperatorSubjects).
		Feedback: &FeedbackService{Auth: stores.Auth, Feedback: stores.Feedback},
		Automation: &AutomationService{
			Auth: stores.Auth, Changesets: stores.Changesets, Slices: stores.Slices, Repository: stores.Repository,
			Automations: stores.Automations, Tree: TreeFiles{Repository: stores.Repository, Objects: objectStore},
		},
		AutomationRun: &AutomationRunService{
			Changesets: stores.Changesets, Slices: stores.Slices, Repository: stores.Repository, Checks: stores.Checks,
			Automations: stores.Automations, Tree: TreeFiles{Repository: stores.Repository, Objects: objectStore},
		},
	}
}

// SetAutomationEngine gives the trigger services their engine, and the engine
// what runs check actions on CI daemons.
func (h *Handlers) SetAutomationEngine(engine *automations.Engine) {
	h.Automation.Engine = engine
	h.AutomationRun.Engine = engine
	if h.Changeset != nil && h.Changeset.dispatcher != nil {
		engine.SetCheckLauncher(h.Changeset.dispatcher)
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
