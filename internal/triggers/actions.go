package triggers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/outbound"
	"gitslice.io/gitslice/internal/secretmask"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

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

// FinishIfDone ends a waiting or callback-claimed run once every check it
// reported or launched has a result.
func (e *Engine) FinishIfDone(ctx context.Context, runID string) {
	run, err := e.stores.Triggers.GetTriggerRun(ctx, runID)
	if err != nil {
		return
	}
	inCallback := run.Status == storage.TriggerRunRunning && !run.Deadline.IsZero()
	if (run.Status != storage.TriggerRunWaiting && !inCallback) || len(e.OpenChecks(ctx, *run)) > 0 {
		return
	}
	_, _ = e.stores.Triggers.UpdateTriggerRun(ctx, run.ID, []string{storage.TriggerRunWaiting, storage.TriggerRunRunning}, storage.TriggerRunUpdate{Status: e.ChecksOutcome(ctx, *run), Error: run.Error})
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
		update.NextAttemptAt = time.Now().Add(outbound.RetryDelay(run.Attempt))
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
	if _, err := outbound.ValidateURL(target, e.opts.AllowPrivateTargets); err != nil {
		return 0, masker.Mask(err.Error()), true
	}
	headers := make(map[string]string, len(action.Headers)+4)
	for name, value := range action.Headers {
		headers[name] = expand(value)
	}
	headers["X-Gitslice-Event"] = run.EventKind
	headers["X-Gitslice-Event-ID"] = run.EventID
	headers["X-Gitslice-Trigger"] = run.TriggerID
	headers["X-Gitslice-Trigger-Run"] = run.ID

	signSecret := ""
	if action.Sign != "" {
		signSecret = secrets[action.Sign]
	}
	res, err := outbound.PostSignedJSON(ctx, outbound.PostJSONRequest{
		Client:     e.client,
		URL:        target,
		Body:       run.Payload,
		UserAgent:  userAgent,
		Headers:    headers,
		SignSecret: signSecret,
	})
	if err != nil {
		text, final := outbound.DescribeSendError(err)
		return 0, masker.Mask(text), final
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return res.StatusCode, "the endpoint answered " + res.Status, false
	}
	return res.StatusCode, "", false
}

func secretValues(secrets map[string]string) []string {
	out := make([]string, 0, len(secrets))
	for _, value := range secrets {
		out = append(out, value)
	}
	return out
}

// post hands a worker or bash run to the dispatcher, which claims it with
// StartRun. A post that is lost is posted again when its hold ends.
func (e *Engine) post(ctx context.Context, run storage.TriggerRun) {
	if !e.DispatchConfigured() {
		_, _ = e.stores.Triggers.UpdateTriggerRun(ctx, run.ID, []string{storage.TriggerRunQueued}, storage.TriggerRunUpdate{
			Status: storage.TriggerRunErrored, Error: "this server runs no automation workers or containers: GITSLICE_TRIGGER_DISPATCH_URL is not set",
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
	if run.Action == ActionBash {
		if activeBash, err := e.stores.Triggers.CountActiveTriggerRunsByAction(ctx, run.Account, ActionBash); err != nil || activeBash >= MaxActiveBashPerAccount {
			return
		}
	}
	body, _ := json.Marshal(map[string]any{"run_id": run.ID, "action": run.Action, "ts": time.Now().Unix()})
	res, err := outbound.PostSignedJSON(ctx, outbound.PostJSONRequest{
		Client:           e.dispatch,
		URL:              e.opts.DispatchURL + "/run",
		Body:             body,
		UserAgent:        userAgent,
		SignHeader:       "X-Gitslice-Dispatch-Signature",
		Signature:        e.sign("dispatch", string(body)),
		MaxResponseBytes: maxResponse,
	})
	if err != nil {
		slog.Warn("post automation run to dispatcher", "run", run.ID, "action", run.Action, "error", err)
		return
	}
	if res.StatusCode/100 != 2 {
		slog.Warn("dispatcher refused automation run", "run", run.ID, "action", run.Action, "status", res.StatusCode, "body", string(res.Body))
	}
}

func (e *Engine) postCancel(runID string) {
	ctx, cancel := context.WithTimeout(context.Background(), dispatchWindow)
	defer cancel()
	body, _ := json.Marshal(map[string]any{"run_id": runID, "ts": time.Now().Unix()})
	_, err := outbound.PostSignedJSON(ctx, outbound.PostJSONRequest{
		Client:           e.dispatch,
		URL:              e.opts.DispatchURL + "/cancel",
		Body:             body,
		UserAgent:        userAgent,
		SignHeader:       "X-Gitslice-Dispatch-Signature",
		Signature:        e.sign("dispatch", string(body)),
		MaxResponseBytes: maxResponse,
	})
	if err != nil {
		slog.Warn("post automation cancel to dispatcher", "run", runID, "error", err)
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
			if !run.Deadline.IsZero() {
				if !run.Deadline.After(time.Now()) {
					reason := "timed out waiting for callbacks"
					if updated, err := e.stores.Triggers.UpdateTriggerRun(ctx, run.ID, []string{storage.TriggerRunRunning}, storage.TriggerRunUpdate{Status: storage.TriggerRunTimedOut, Error: reason}); err == nil {
						e.CloseChecks(ctx, *updated, "errored", "the trigger run "+reason)
					}
					continue
				}
				_, _ = e.stores.Triggers.UpdateTriggerRun(ctx, run.ID, []string{storage.TriggerRunRunning}, storage.TriggerRunUpdate{
					Status: storage.TriggerRunWaiting, Deadline: run.Deadline,
				})
				continue
			}
			if run.Attempt < run.MaxAttempts {
				next := time.Now()
				if run.Action == ActionHTTP {
					next = next.Add(outbound.RetryDelay(run.Attempt))
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

// ResolveManual finds a manual-capable automation at the head of ref and
// returns its definition and definition commit id.
func (e *Engine) ResolveManual(ctx context.Context, account, triggerID, ref string) (Match, string, error) {
	if ref == "" {
		ref = DefaultRef
	}
	treeID, headCommit, err := e.headTree(ctx, ref)
	if err != nil {
		return Match{}, "", err
	}
	matches, problems, err := Resolve(ctx, e.opts.Tree, treeID, Event{Kind: EventManual, Account: account, Ref: ref, Only: triggerID})
	if err != nil {
		return Match{}, "", err
	}
	if len(matches) == 0 {
		for _, problem := range problems {
			return Match{}, "", fmt.Errorf("%w: %s: %s", storage.ErrInvalid, problem.File, problem.Message)
		}
		return Match{}, "", fmt.Errorf("%w: no automation %s at the head of %s", storage.ErrNotFound, triggerID, ref)
	}
	if !contains(matches[0].Trigger.On, EventManual) {
		return Match{}, "", fmt.Errorf("%w: automation %s does not listen for manual", storage.ErrInvalid, triggerID)
	}
	return matches[0], headCommit, nil
}

// NewManualRun queues a run of one automation by hand, at the head of ref,
// optionally about a changeset.
func (e *Engine) NewManualRun(ctx context.Context, account, triggerID, ref, changesetID string, inputs map[string]string, actor, triggeredBy, triggeredByKind string, envelope []byte) (*storage.TriggerRun, error) {
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
	match, headCommit, err := e.ResolveManual(ctx, account, triggerID, ref)
	if err != nil {
		return nil, err
	}
	validatedInputs, err := ValidateInputs(match.Trigger.Inputs, inputs)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", storage.ErrInvalid, err)
	}
	id, err := objectid.RandomID("evt_manual")
	if err != nil {
		return nil, err
	}
	run, err := e.newRun(info, match, EventManual, id, headCommit, envelope, 0, actor, triggeredBy, triggeredByKind, validatedInputs)
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
