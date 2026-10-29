package triggers

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/secretmask"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/webhooks"
	corev1 "gitslice.io/gitslice/proto/core/v1"
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

// httpRetryDelays is the wait after each failed attempt of an http action,
// as for webhooks.
var httpRetryDelays = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour, 12 * time.Hour}

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
		client = webhooks.NewClient(opts.AllowPrivateTargets)
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

func (e *Engine) describe(ctx context.Context, event storage.WebhookEvent) (*eventInfo, error) {
	info := &eventInfo{ref: event.TargetRef, commitID: event.CommitID, changesetID: event.ChangesetID}
	if event.ChangesetID != "" {
		cs, err := e.stores.Changesets.Get(ctx, event.ChangesetID)
		if err != nil {
			return nil, fmt.Errorf("changeset %s: %w", event.ChangesetID, err)
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
		slice, err := e.stores.Slices.Get(ctx, event.SliceID)
		if err != nil {
			return nil, fmt.Errorf("slice %s: %w", event.SliceID, err)
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
func (e *Engine) FanOut(ctx context.Context, event storage.WebhookEvent, envelope []byte) {
	if event.Kind == storage.WebhookEventPing {
		return
	}
	if event.Kind == storage.WebhookEventCheckRunCompleted && event.CheckRunID != "" && e.stores.Checks != nil {
		// A check a run reported or launched finished: the run may be done.
		if check, err := e.stores.Checks.GetCheckRun(ctx, event.CheckRunID); err == nil && check.GetTriggerRunId() != "" {
			e.FinishIfDone(ctx, check.GetTriggerRunId())
		}
	}
	if err := e.fanOut(ctx, event, envelope); err != nil {
		slog.WarnContext(ctx, "trigger fan-out", "event", event.ID, "kind", event.Kind, "error", err)
	}
}

func (e *Engine) fanOut(ctx context.Context, event storage.WebhookEvent, envelope []byte) error {
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
	info, err := e.describe(ctx, event)
	if err != nil || info == nil {
		return err
	}
	kinds := []string{event.Kind}
	if event.Kind == storage.WebhookEventChangesetSubmitted {
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
// webhook envelope plus the trigger, the changes it matched, the commit its
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
		maxAttempts = len(httpRetryDelays) + 1
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

// OpenChecks lists a run's checks that are still queued or running.
func (e *Engine) OpenChecks(ctx context.Context, run storage.TriggerRun) []*corev1.CheckRun {
	if run.ChangesetID == "" || e.stores.Checks == nil {
		return nil
	}
	checks, err := e.stores.Checks.ListCheckRuns(ctx, run.ChangesetID, run.PatchsetID)
	if err != nil {
		return nil
	}
	var out []*corev1.CheckRun
	for _, check := range checks {
		if check.GetTriggerRunId() == run.ID && (check.GetStatus() == "queued" || check.GetStatus() == "running") {
			out = append(out, check)
		}
	}
	return out
}

// CloseChecks gives a run's open checks a final status. The run is the
// writer, so the events this causes never set its own trigger off again.
func (e *Engine) CloseChecks(ctx context.Context, run storage.TriggerRun, status, summary string) {
	ctx = authctx.WithSubjectID(ctx, ActorPrefix+run.ID)
	for _, check := range e.OpenChecks(ctx, run) {
		if _, err := e.stores.Checks.UpdateCheckRunStatus(ctx, check.GetId(), status, -1, summary); err != nil && !errors.Is(err, storage.ErrConflict) {
			slog.WarnContext(ctx, "close trigger check", "run", run.ID, "check", check.GetId(), "error", err)
		}
	}
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

// settleExpired ends or retries runs whose dispatcher went quiet, and times
// out runs that waited too long for callbacks.
func (e *Engine) settleExpired(ctx context.Context) {
	expired, err := e.stores.Triggers.ExpiredTriggerRuns(ctx, dueBatch)
	if err != nil {
		return
	}
	for _, run := range expired {
		switch run.Status {
		case storage.TriggerRunRunning:
			if run.Attempt < run.MaxAttempts {
				next := time.Now()
				if run.Action == ActionHTTP {
					next = next.Add(httpRetryDelays[min(run.Attempt-1, len(httpRetryDelays)-1)])
				}
				_, _ = e.stores.Triggers.UpdateTriggerRun(ctx, run.ID, []string{storage.TriggerRunRunning}, storage.TriggerRunUpdate{
					Status: storage.TriggerRunQueued, Error: "interrupted; trying again", NextAttemptAt: next,
				})
				continue
			}
			reason := "interrupted: the run stopped before it finished; rerun it if it is safe to repeat"
			if updated, err := e.stores.Triggers.UpdateTriggerRun(ctx, run.ID, []string{storage.TriggerRunRunning}, storage.TriggerRunUpdate{Status: storage.TriggerRunErrored, Error: reason}); err == nil {
				e.CloseChecks(ctx, *updated, "errored", "the trigger run was interrupted")
			}
		case storage.TriggerRunWaiting:
			reason := "timed out waiting for callbacks"
			if updated, err := e.stores.Triggers.UpdateTriggerRun(ctx, run.ID, []string{storage.TriggerRunWaiting}, storage.TriggerRunUpdate{Status: storage.TriggerRunTimedOut, Error: reason}); err == nil {
				e.CloseChecks(ctx, *updated, "errored", "the trigger run "+reason)
			}
		}
	}
}

// SecretsFor returns the values of the named secrets at a folder, and the
// names that have none.
func (e *Engine) SecretsFor(ctx context.Context, folder string, names []string) (map[string]string, []string, error) {
	out := map[string]string{}
	if len(names) == 0 {
		return out, nil, nil
	}
	values, err := e.stores.Slices.SecretValuesAt(ctx, folder)
	if err != nil {
		return nil, nil, err
	}
	var missing []string
	for _, name := range names {
		if value, ok := values[name]; ok {
			out[name] = value
		} else {
			missing = append(missing, name)
		}
	}
	return out, missing, nil
}

// launch runs a check action: its checks go to the changeset's CI daemon
// and the run waits for them.
func (e *Engine) launch(ctx context.Context, queued storage.TriggerRun) {
	run, err := e.stores.Triggers.ClaimTriggerRun(ctx, queued.ID, RunLease)
	if err != nil {
		return
	}
	fail := func(reason string) {
		if updated, err := e.stores.Triggers.UpdateTriggerRun(ctx, run.ID, []string{storage.TriggerRunRunning}, storage.TriggerRunUpdate{Status: storage.TriggerRunErrored, Error: reason}); err == nil {
			e.CloseChecks(ctx, *updated, "errored", reason)
		}
	}
	var spec RunSpec
	if err := json.Unmarshal(run.Spec, &spec); err != nil {
		fail("the run has an unreadable spec")
		return
	}
	if run.ChangesetID == "" || run.PatchsetID == "" {
		fail("a check action needs a changeset event")
		return
	}
	if e.launcher == nil {
		fail("this server cannot run check actions")
		return
	}
	names := make([]string, 0, len(spec.Trigger.Action.Check))
	for _, name := range spec.Trigger.Action.Check {
		names = append(names, CheckID(run.TriggerID, name))
	}
	created, err := e.launcher.LaunchTriggerChecks(ctx, run.ChangesetID, run.PatchsetID, names, run.ID)
	switch {
	case err != nil && created == 0:
		fail(err.Error())
		return
	case created == 0:
		// Nothing to run: every check was skipped or ran already.
		_, _ = e.stores.Triggers.UpdateTriggerRun(ctx, run.ID, []string{storage.TriggerRunRunning}, storage.TriggerRunUpdate{Status: storage.TriggerRunSucceeded})
		return
	}
	update := storage.TriggerRunUpdate{Status: storage.TriggerRunWaiting, Deadline: time.Now().Add(spec.Trigger.Timeout)}
	if err != nil {
		update.Error = err.Error()
	}
	if _, err := e.stores.Triggers.UpdateTriggerRun(ctx, run.ID, []string{storage.TriggerRunRunning}, update); err == nil {
		e.FinishIfDone(ctx, run.ID)
	}
}

// ChecksOutcome is how a run ends once its checks are done: failed if one of
// them failed or errored, else succeeded.
func (e *Engine) ChecksOutcome(ctx context.Context, run storage.TriggerRun) string {
	if run.ChangesetID == "" || e.stores.Checks == nil {
		return storage.TriggerRunSucceeded
	}
	checks, err := e.stores.Checks.ListCheckRuns(ctx, run.ChangesetID, run.PatchsetID)
	if err != nil {
		return storage.TriggerRunSucceeded
	}
	for _, check := range checks {
		if check.GetTriggerRunId() == run.ID && (check.GetStatus() == "failed" || check.GetStatus() == "errored") {
			return storage.TriggerRunFailed
		}
	}
	return storage.TriggerRunSucceeded
}

// FinishIfDone ends a waiting run once every check it reported or launched
// has a result.
func (e *Engine) FinishIfDone(ctx context.Context, runID string) {
	run, err := e.stores.Triggers.GetTriggerRun(ctx, runID)
	if err != nil || run.Status != storage.TriggerRunWaiting || len(e.OpenChecks(ctx, *run)) > 0 {
		return
	}
	_, _ = e.stores.Triggers.UpdateTriggerRun(ctx, run.ID, []string{storage.TriggerRunWaiting}, storage.TriggerRunUpdate{Status: e.ChecksOutcome(ctx, *run), Error: run.Error})
}

// sendHTTP makes one attempt of an http action.
func (e *Engine) sendHTTP(ctx context.Context, queued storage.TriggerRun) {
	run, err := e.stores.Triggers.ClaimTriggerRun(ctx, queued.ID, RunLease)
	if err != nil {
		return
	}
	status, errText, final := e.sendHTTPOnce(ctx, *run)
	update := storage.TriggerRunUpdate{ResponseStatus: status, Error: errText}
	switch {
	case errText == "":
		update.Status = storage.TriggerRunSucceeded
	case final || run.Attempt >= run.MaxAttempts:
		update.Status = storage.TriggerRunFailed
	default:
		update.Status = storage.TriggerRunQueued
		update.NextAttemptAt = time.Now().Add(httpRetryDelays[min(run.Attempt-1, len(httpRetryDelays)-1)])
	}
	if _, err := e.stores.Triggers.UpdateTriggerRun(ctx, run.ID, []string{storage.TriggerRunRunning}, update); err != nil && !errors.Is(err, storage.ErrConflict) {
		slog.Warn("record trigger http attempt", "run", run.ID, "error", err)
	}
}

func (e *Engine) sendHTTPOnce(ctx context.Context, run storage.TriggerRun) (int, string, bool) {
	var spec RunSpec
	if err := json.Unmarshal(run.Spec, &spec); err != nil || spec.Trigger.Action.HTTP == nil {
		return 0, "the run has no http action", true
	}
	action := spec.Trigger.Action.HTTP
	secrets, missing, err := e.SecretsFor(ctx, run.Folder, spec.Trigger.Secrets)
	if err != nil {
		return 0, "read secrets: " + err.Error(), false
	}
	if len(missing) > 0 {
		return 0, fmt.Sprintf("no secret %s applies at %s", strings.Join(missing, ", "), run.Folder), true
	}
	masker := secretmask.New(secretValues(secrets))
	expand := func(value string) string {
		return SecretRef.ReplaceAllStringFunc(value, func(ref string) string {
			return secrets[SecretRef.FindStringSubmatch(ref)[1]]
		})
	}
	target := expand(action.URL)
	if _, err := webhooks.ValidateURL(target, e.opts.AllowPrivateTargets); err != nil {
		return 0, masker.Mask(err.Error()), true
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(run.Payload))
	if err != nil {
		return 0, masker.Mask(err.Error()), true
	}
	for name, value := range action.Headers {
		req.Header.Set(name, expand(value))
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("X-Gitslice-Event", run.EventKind)
	req.Header.Set("X-Gitslice-Event-ID", run.EventID)
	req.Header.Set("X-Gitslice-Trigger", run.TriggerID)
	req.Header.Set("X-Gitslice-Trigger-Run", run.ID)
	if action.Sign != "" {
		req.Header.Set(webhooks.SignatureHeader, webhooks.Sign(secrets[action.Sign], run.Payload))
	}
	resp, err := e.client.Do(req)
	if err != nil {
		text, final := webhooks.DescribeSendError(err)
		return 0, masker.Mask(text), final
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, "the endpoint answered " + resp.Status, false
	}
	return resp.StatusCode, "", false
}

func secretValues(secrets map[string]string) []string {
	out := make([]string, 0, len(secrets))
	for _, value := range secrets {
		out = append(out, value)
	}
	return out
}

// post hands a worker run to the dispatcher, which claims it with StartRun.
// A post that is lost is posted again when its hold ends.
func (e *Engine) post(ctx context.Context, run storage.TriggerRun) {
	if !e.DispatchConfigured() {
		_, _ = e.stores.Triggers.UpdateTriggerRun(ctx, run.ID, []string{storage.TriggerRunQueued}, storage.TriggerRunUpdate{
			Status: storage.TriggerRunErrored, Error: "this server runs no trigger scripts: GITSLICE_TRIGGER_DISPATCH_URL is not set",
		})
		return
	}
	var spec RunSpec
	_ = json.Unmarshal(run.Spec, &spec)
	if spec.Trigger.Concurrency == ConcurrencySerial {
		open, err := e.stores.Triggers.OpenTriggerRuns(ctx, run.Account, run.TriggerID, "")
		if err != nil {
			return
		}
		for _, other := range open {
			if other.ID != run.ID && (other.Status == storage.TriggerRunRunning || other.Status == storage.TriggerRunWaiting || other.CreatedAt.Before(run.CreatedAt)) {
				return // its turn comes after the older runs
			}
		}
	}
	if active, err := e.stores.Triggers.CountActiveTriggerRuns(ctx, run.Account); err != nil || active >= MaxActivePerAccount {
		return
	}
	body, _ := json.Marshal(map[string]any{"run_id": run.ID, "ts": time.Now().Unix()})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.opts.DispatchURL+"/run", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("X-Gitslice-Dispatch-Signature", e.sign("dispatch", string(body)))
	resp, err := e.dispatch.Do(req)
	if err != nil {
		slog.Warn("post trigger run to dispatcher", "run", run.ID, "error", err)
		return
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if resp.StatusCode/100 != 2 {
		slog.Warn("dispatcher refused trigger run", "run", run.ID, "status", resp.StatusCode, "body", string(data))
	}
}

// sign is the hex HMAC of parts under the engine's key, by purpose.
func (e *Engine) sign(purpose string, parts ...string) string {
	mac := hmac.New(sha256.New, e.key)
	mac.Write([]byte(purpose))
	for _, part := range parts {
		mac.Write([]byte{0})
		mac.Write([]byte(part))
	}
	return hex.EncodeToString(mac.Sum(nil))
}

// CheckDispatcherKey says whether key is the dispatcher's.
func (e *Engine) CheckDispatcherKey(key string) bool {
	return e.opts.DispatchSecret != "" && subtle.ConstantTimeCompare([]byte(key), []byte(e.opts.DispatchSecret)) == 1
}

// RunToken is a run's bearer token: good for that run until expires.
func (e *Engine) RunToken(runID string, expires time.Time) string {
	exp := strconv.FormatInt(expires.Unix(), 10)
	return "gstr_" + runID + "." + exp + "." + e.sign("run-token", runID, exp)
}

// ParseRunToken returns the run a token is for, if it is genuine and has
// not expired.
func (e *Engine) ParseRunToken(token string) (string, error) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(token), "gstr_")
	parts := strings.Split(rest, ".")
	if !ok || len(parts) != 3 {
		return "", errors.New("not a run token")
	}
	runID, exp, sig := parts[0], parts[1], parts[2]
	if !hmac.Equal([]byte(sig), []byte(e.sign("run-token", runID, exp))) {
		return "", errors.New("run token is not genuine")
	}
	unix, err := strconv.ParseInt(exp, 10, 64)
	if err != nil || time.Now().Unix() > unix {
		return "", errors.New("run token expired")
	}
	return runID, nil
}

// CallbackURL is where outside systems call a run back.
func (e *Engine) CallbackURL(runID string) string {
	if e.opts.DispatchURL == "" {
		return ""
	}
	return e.opts.DispatchURL + "/cb/" + runID + "/" + e.CallbackSignature(runID)
}

// CallbackSignature signs a run id for its callback URL.
func (e *Engine) CallbackSignature(runID string) string {
	return e.sign("callback", runID)[:32]
}

// NewManualRun queues a run of one trigger by hand, at the head of ref,
// optionally about a changeset.
func (e *Engine) NewManualRun(ctx context.Context, account, triggerID, ref, changesetID string, inputs map[string]string, actor string, envelope []byte) (*storage.TriggerRun, error) {
	if ref == "" {
		ref = DefaultRef
	}
	info := &eventInfo{account: account, ref: ref, changesetID: changesetID}
	if changesetID != "" {
		cs, err := e.stores.Changesets.Get(ctx, changesetID)
		if err != nil {
			return nil, err
		}
		if cs.GetAuthoringSlice().GetAccount() != account {
			return nil, fmt.Errorf("%w: changeset %s is not in account %s", storage.ErrInvalid, changesetID, account)
		}
		info.patchsetID, info.commitID = cs.GetCurrentPatchsetId(), cs.GetCommitId()
	}
	treeID, headCommit, err := e.headTree(ctx, ref)
	if err != nil {
		return nil, err
	}
	matches, problems, err := Resolve(ctx, e.opts.Tree, treeID, Event{Kind: EventManual, Account: account, Ref: ref, Only: triggerID})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		for _, problem := range problems {
			return nil, fmt.Errorf("%w: %s: %s", storage.ErrInvalid, problem.File, problem.Message)
		}
		return nil, fmt.Errorf("%w: no trigger %s at the head of %s", storage.ErrNotFound, triggerID, ref)
	}
	if !contains(matches[0].Trigger.On, EventManual) {
		return nil, fmt.Errorf("%w: trigger %s does not listen for manual", storage.ErrInvalid, triggerID)
	}
	id, err := objectid.RandomID("evt_manual")
	if err != nil {
		return nil, err
	}
	run, err := e.newRun(info, matches[0], EventManual, id, headCommit, envelope, 0, actor, inputs)
	if err != nil {
		return nil, err
	}
	created, err := e.stores.Triggers.CreateTriggerRuns(ctx, []storage.TriggerRun{run})
	if err != nil {
		return nil, err
	}
	if len(created) == 0 {
		return nil, storage.ErrConflict
	}
	return &created[0], nil
}

// Rerun queues a finished run's trigger again for the same event, with the
// definition it ran with.
func (e *Engine) Rerun(ctx context.Context, run storage.TriggerRun) (*storage.TriggerRun, error) {
	if !storage.TriggerRunFinal(run.Status) {
		return nil, fmt.Errorf("%w: run %s has not finished", storage.ErrConflict, run.ID)
	}
	if run.Action == "" {
		return nil, fmt.Errorf("%w: run %s is a triggers file that does not parse; fix it and push again", storage.ErrInvalid, run.ID)
	}
	base, _, _ := strings.Cut(run.EventID, ".rerun.")
	runs, err := e.stores.Triggers.ListTriggerRuns(ctx, storage.TriggerRunFilter{Account: run.Account, TriggerID: run.TriggerID, Limit: 200})
	if err != nil {
		return nil, err
	}
	n := 1
	for _, other := range runs {
		if strings.HasPrefix(other.EventID, base+".rerun.") {
			n++
		}
	}
	again := run
	again.ID, again.Status, again.Error, again.Attempt, again.ResponseStatus, again.State = "", storage.TriggerRunQueued, "", 0, 0, nil
	again.EventID = base + ".rerun." + strconv.Itoa(n)
	created, err := e.stores.Triggers.CreateTriggerRuns(ctx, []storage.TriggerRun{again})
	if err != nil {
		return nil, err
	}
	if len(created) == 0 {
		return nil, storage.ErrConflict
	}
	return &created[0], nil
}
