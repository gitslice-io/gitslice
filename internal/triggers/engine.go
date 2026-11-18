package triggers

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"gitslice.io/gitslice/internal/outbound"
	"gitslice.io/gitslice/internal/storage"
)

const (
	// DefaultRef is where definitions are read from when an event names no
	// ref.
	DefaultRef = "refs/global/main"
	// ActorPrefix marks the subject of writes a trigger run makes, so the
	// events they cause can be traced back to it: "trigger:<run id>".
	ActorPrefix = "trigger:"
	// MaxDepth stops chains of triggers set off by triggers.
	MaxDepth = 3
	// MaxActivePerAccount bounds an account's running and waiting runs.
	MaxActivePerAccount = 20
	// RunLease is how long a started run may go without a sign of life.
	RunLease = 2 * time.Minute

	userAgent      = "Gitslice-Trigger/1.0"
	dueBatch       = 50
	postHold       = time.Minute
	sendWorkers    = 8
	filesCacheTTL  = 30 * time.Second
	pruneInterval  = time.Hour
	historyKeep    = 30 * 24 * time.Hour
	dispatchWindow = 10 * time.Second
	maxResponse    = 2 << 10
)

// Stores are what the engine reads and writes.
type Stores struct {
	Triggers   storage.TriggerStore
	Changesets storage.ChangesetStore
	Slices     storage.SliceStore
	Repository storage.RepositoryStore
	Checks     storage.CheckStore
}

// Options configure an Engine.
type Options struct {
	// Tree reads files of a tree at canonical paths.
	Tree TreeReader
	// DispatchURL is the Cloudflare dispatcher's origin; without it, worker
	// actions error.
	DispatchURL string
	// DispatchSecret authenticates the server and the dispatcher to each
	// other, and keys run tokens.
	DispatchSecret string
	// AllowPrivateTargets lets http actions reach private addresses (tests).
	AllowPrivateTargets bool
	// Client sends http actions; DispatchClient posts runs (tests).
	Client         *http.Client
	DispatchClient *http.Client
}

// RunSpec is what a run keeps of its trigger (trigger_runs.spec).
type RunSpec struct {
	Name    string            `json:"name"`
	Trigger Trigger           `json:"trigger"`
	Depth   int               `json:"depth"`
	Inputs  map[string]string `json:"inputs,omitempty"`
}

// CheckLauncher runs checks.yaml checks on a changeset's CI daemon for a
// check action, linked to the trigger run, and says how many runs it created.
type CheckLauncher interface {
	LaunchTriggerChecks(ctx context.Context, changesetID, patchsetID string, names []string, triggerRunID string) (int, error)
}

// Engine turns events into trigger runs and runs them (design/34).
type Engine struct {
	stores   Stores
	opts     Options
	key      []byte
	client   *http.Client
	dispatch *http.Client
	launcher CheckLauncher

	mu          sync.Mutex
	hasFiles    bool
	filesCached time.Time
	lastPrune   time.Time
}

// NewEngine returns an engine. Without a dispatch secret, run tokens are
// keyed by a random key that lasts as long as the process.
func NewEngine(stores Stores, opts Options) *Engine {
	key := []byte(opts.DispatchSecret)
	if len(key) == 0 {
		key = make([]byte, 32)
		_, _ = rand.Read(key)
	}
	client := opts.Client
	if client == nil {
		client = outbound.NewClient(opts.AllowPrivateTargets)
	}
	dispatch := opts.DispatchClient
	if dispatch == nil {
		dispatch = &http.Client{Timeout: dispatchWindow}
	}
	opts.DispatchURL = strings.TrimRight(opts.DispatchURL, "/")
	return &Engine{stores: stores, opts: opts, key: key, client: client, dispatch: dispatch}
}

// SetCheckLauncher gives the engine what runs check actions.
func (e *Engine) SetCheckLauncher(l CheckLauncher) {
	e.launcher = l
}

// DispatchConfigured says whether worker actions can run.
func (e *Engine) DispatchConfigured() bool {
	return e.opts.DispatchURL != "" && e.opts.DispatchSecret != ""
}

// MayHaveTriggers says whether any triggers file exists, cached for 30 s.
func (e *Engine) MayHaveTriggers(ctx context.Context) bool {
	e.mu.Lock()
	if time.Since(e.filesCached) < filesCacheTTL {
		has := e.hasFiles
		e.mu.Unlock()
		return has
	}
	e.mu.Unlock()
	has, err := e.stores.Triggers.HasTriggerFiles(ctx)
	if err != nil {
		return true
	}
	e.mu.Lock()
	e.hasFiles, e.filesCached = has, time.Now()
	e.mu.Unlock()
	return has
}

// eventInfo is what an event is about, for choosing triggers.
type eventInfo struct {
	account     string
	ref         string
	changes     []Change
	folders     []string
	changesetID string
	patchsetID  string
	commitID    string
}

func (e *Engine) describe(ctx context.Context, event storage.Event, entities storage.EventEntities) (*eventInfo, error) {
	info := &eventInfo{ref: event.TargetRef, commitID: event.CommitID, changesetID: event.ChangesetID}
	if event.ChangesetID != "" {
		cs := entities.Changeset
		if cs == nil || cs.GetId() != event.ChangesetID {
			var err error
			cs, err = e.stores.Changesets.Get(ctx, event.ChangesetID)
			if err != nil {
				return nil, fmt.Errorf("changeset %s: %w", event.ChangesetID, err)
			}
		}
		info.account = cs.GetAuthoringSlice().GetAccount()
		if info.ref == "" {
			info.ref = cs.GetTargetRef()
		}
		if info.commitID == "" {
			info.commitID = cs.GetCommitId()
		}
		info.patchsetID = event.PatchsetID
		if info.patchsetID == "" {
			info.patchsetID = cs.GetCurrentPatchsetId()
		}
		for _, ps := range cs.GetPatchsets() {
			if ps.GetId() == info.patchsetID {
				info.changes = ChangesFromPatchset(ps)
			}
		}
		if len(info.changes) == 0 && len(event.ChangedPaths) > 0 {
			info.changes = ChangesFromPaths(event.ChangedPaths)
		}
	}
	if info.account == "" && event.SliceID != "" {
		slice := entities.Slice
		if slice == nil || slice.GetId() != event.SliceID {
			var err error
			slice, err = e.stores.Slices.Get(ctx, event.SliceID)
			if err != nil {
				return nil, fmt.Errorf("slice %s: %w", event.SliceID, err)
			}
		}
		info.account = slice.GetRef().GetAccount()
		info.folders = slice.GetDefinition().GetIncludedPaths()
	}
	if info.ref == "" {
		info.ref = DefaultRef
	}
	if info.account == "" {
		return nil, nil
	}
	return info, nil
}

// headTree is the root tree and commit at the head of a ref.
func (e *Engine) headTree(ctx context.Context, ref string) (string, string, error) {
	head, err := e.stores.Repository.GetRef(ctx, ref)
	if err != nil {
		return "", "", fmt.Errorf("ref %s: %w", ref, err)
	}
	tree, err := e.stores.Repository.RootTreeForCommit(ctx, head.GetCommitId())
	if err != nil {
		return "", "", fmt.Errorf("tree of %s: %w", head.GetCommitId(), err)
	}
	return tree, head.GetCommitId(), nil
}

// FanOut creates the runs an event sets off. A landed changeset is also a
// push. It is safe to repeat: a trigger runs at most once per event.
func (e *Engine) FanOut(ctx context.Context, event storage.Event, entities storage.EventEntities, envelope []byte) {
	if event.Kind == storage.EventCheckRunCompleted && event.CheckRunID != "" {
		// A check a run reported or launched finished: the run may be done.
		check := entities.CheckRun
		if (check == nil || check.GetId() != event.CheckRunID) && e.stores.Checks != nil {
			check, _ = e.stores.Checks.GetCheckRun(ctx, event.CheckRunID)
		}
		if check != nil && check.GetTriggerRunId() != "" {
			e.FinishIfDone(ctx, check.GetTriggerRunId())
		}
	}
	if err := e.fanOut(ctx, event, entities, envelope); err != nil {
		slog.WarnContext(ctx, "trigger fan-out", "event", event.ID, "kind", event.Kind, "error", err)
	}
}

func (e *Engine) fanOut(ctx context.Context, event storage.Event, entities storage.EventEntities, envelope []byte) error {
	for _, p := range event.ChangedPaths {
		if strings.HasSuffix(p, "/"+FileName) {
			// A triggers file landed: stop trusting a cached "none".
			e.mu.Lock()
			e.filesCached = time.Time{}
			e.mu.Unlock()
			break
		}
	}
	depth, parentTrigger := 0, ""
	if runID, ok := strings.CutPrefix(event.ActorSubjectID, ActorPrefix); ok {
		parent, err := e.stores.Triggers.GetTriggerRun(ctx, runID)
		if err == nil {
			var spec RunSpec
			_ = json.Unmarshal(parent.Spec, &spec)
			depth, parentTrigger = spec.Depth+1, parent.Account+":"+parent.TriggerID
		}
	}
	if depth > MaxDepth {
		return nil
	}
	info, err := e.describe(ctx, event, entities)
	if err != nil || info == nil {
		return err
	}
	kinds := []string{event.Kind}
	if event.Kind == storage.EventChangesetSubmitted {
		kinds = append(kinds, EventPush)
	}
	treeID, headCommit, err := e.headTree(ctx, info.ref)
	if err != nil {
		return err
	}
	for _, kind := range kinds {
		eventID := event.ID
		if kind == EventPush {
			eventID = event.ID + ".push"
		}
		ev := Event{Kind: kind, Account: info.account, Ref: info.ref, Changes: info.changes, Folders: info.folders}
		matches, problems, err := Resolve(ctx, e.opts.Tree, treeID, ev)
		if err != nil {
			return err
		}
		var runs []storage.TriggerRun
		for _, problem := range problems {
			runs = append(runs, storage.TriggerRun{
				Account: info.account, TriggerID: ID("/"+info.account, problem.Folder, ".gitslice/triggers.yaml"),
				Folder: problem.Folder, Action: "", EventID: eventID, EventKind: kind,
				ChangesetID: info.changesetID, PatchsetID: info.patchsetID, CommitID: info.commitID,
				TargetRef: info.ref, DefinitionCommitID: headCommit, Status: storage.TriggerRunErrored,
				Error: problem.Message, Spec: []byte("{}"), Payload: envelope,
			})
		}
		for _, m := range matches {
			if info.account+":"+m.ID == parentTrigger {
				continue // a trigger never sets itself off
			}
			run, err := e.newRun(info, m, kind, eventID, headCommit, envelope, depth, event.ActorSubjectID, nil)
			if err != nil {
				return err
			}
			if m.Trigger.Concurrency == ConcurrencyCancelSuperseded && info.changesetID != "" {
				e.cancelSuperseded(ctx, info.account, m.ID, info.changesetID, info.patchsetID)
			}
			runs = append(runs, run)
		}
		if _, err := e.stores.Triggers.CreateTriggerRuns(ctx, runs); err != nil {
			return err
		}
	}
	return nil
}

// newRun builds a queued run of a matched trigger, with its payload: the
// event payload plus the trigger, the changes it matched, the commit its
// definition was read from, and how deep in a chain of triggers it is.
func (e *Engine) newRun(info *eventInfo, m Match, kind, eventID, headCommit string, envelope []byte, depth int, actor string, inputs map[string]string) (storage.TriggerRun, error) {
	payload := map[string]any{}
	if len(envelope) > 0 {
		if err := json.Unmarshal(envelope, &payload); err != nil {
			return storage.TriggerRun{}, err
		}
	}
	payload["id"] = eventID
	payload["event"] = kind
	payload["trigger"] = map[string]string{"id": m.ID, "name": m.Name, "folder": m.Folder}
	changes := m.Changes
	if changes == nil {
		changes = []Change{}
	}
	payload["changes"] = changes
	payload["definition_commit"] = headCommit
	sender, _ := payload["sender"].(map[string]any)
	if sender == nil {
		sender = map[string]any{}
	}
	sender["kind"] = "user"
	if strings.HasPrefix(actor, ActorPrefix) {
		sender["kind"] = "trigger"
	}
	sender["depth"] = depth
	payload["sender"] = sender
	if inputs != nil {
		payload["inputs"] = inputs
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return storage.TriggerRun{}, err
	}
	spec, err := json.Marshal(RunSpec{Name: m.Name, Trigger: m.Trigger, Depth: depth, Inputs: inputs})
	if err != nil {
		return storage.TriggerRun{}, err
	}
	maxAttempts := 1 + m.Trigger.Retry
	switch m.Trigger.Action.Kind {
	case ActionHTTP:
		maxAttempts = len(outbound.DefaultRetryDelays) + 1
	case ActionCheck:
		maxAttempts = 1
	}
	return storage.TriggerRun{
		Account: info.account, TriggerID: m.ID, Folder: m.Folder, Action: m.Trigger.Action.Kind,
		EventID: eventID, EventKind: kind, ChangesetID: info.changesetID, PatchsetID: info.patchsetID,
		CommitID: info.commitID, TargetRef: info.ref, DefinitionCommitID: headCommit,
		Spec: spec, Payload: body, Status: storage.TriggerRunQueued, MaxAttempts: maxAttempts,
	}, nil
}

func (e *Engine) cancelSuperseded(ctx context.Context, account, triggerID, changesetID, patchsetID string) {
	open, err := e.stores.Triggers.OpenTriggerRuns(ctx, account, triggerID, changesetID)
	if err != nil {
		return
	}
	for _, run := range open {
		if run.PatchsetID != patchsetID {
			e.Cancel(ctx, run, "superseded by a newer patchset")
		}
	}
}

// Cancel ends an unfinished run and the checks it left open.
func (e *Engine) Cancel(ctx context.Context, run storage.TriggerRun, reason string) (*storage.TriggerRun, error) {
	updated, err := e.stores.Triggers.UpdateTriggerRun(ctx, run.ID, storage.TriggerRunOpen, storage.TriggerRunUpdate{Status: storage.TriggerRunCanceled, Error: reason})
	if err != nil {
		return nil, err
	}
	e.CloseChecks(ctx, *updated, "canceled", reason)
	return updated, nil
}

// Drain settles expired runs, sends due http actions, posts due worker runs
// to the dispatcher, and prunes old runs hourly.
func (e *Engine) Drain(ctx context.Context) {
	e.settleExpired(ctx)
	due, err := e.stores.Triggers.ClaimDueTriggerRuns(ctx, dueBatch, postHold)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("claim due trigger runs", "error", err)
		}
		return
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, sendWorkers)
	for _, run := range due {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() {
				<-sem
				wg.Done()
			}()
			switch run.Action {
			case ActionHTTP:
				e.sendHTTP(ctx, run)
			case ActionWorker:
				e.post(ctx, run)
			case ActionCheck:
				e.launch(ctx, run)
			}
		}()
	}
	wg.Wait()
	e.mu.Lock()
	prune := time.Since(e.lastPrune) > pruneInterval
	if prune {
		e.lastPrune = time.Now()
	}
	e.mu.Unlock()
	if prune {
		if err := e.stores.Triggers.PruneTriggerRuns(ctx, time.Now().Add(-historyKeep)); err != nil && ctx.Err() == nil {
			slog.Warn("prune trigger runs", "error", err)
		}
	}
}
