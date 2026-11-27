package service

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"gitslice.io/gitslice/internal/checks"
	"gitslice.io/gitslice/internal/gitslicecfg"
	"gitslice.io/gitslice/internal/keylock"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type checkDispatcher struct {
	Auth        storage.RoleLookupStore
	Checks      storage.CheckStore
	Changesets  storage.ChangesetStore
	Repository  storage.RepositoryStore
	Slices      storage.SliceStore
	Agents      storage.AgentStore
	ObjectStore ObjectStore

	hub        *agentHub
	serverAddr string

	// wake tells a daemon waiting on standby that queued check runs are
	// waiting for it (the doorbell, design/32 layer 1); nil without one.
	wake func(ctx context.Context, daemonID string, queued int)
	// onDemand are daemons whose processes are started when work is queued
	// (Cloudflare runners, design/32 layer 2): they count as reachable
	// with no process running.
	onDemand map[string]bool

	claimLocks keylock.Locker

	checkResolversMu sync.Mutex
	checkResolvers   map[string]*gitslicecfg.Resolver[*checks.File]
}

// dispatchCIChecks sends a new patchset's checks to the slice's full-tree CI
// runner. Out-of-slice checks always go there, because the authoring agent
// cannot materialize them. In-slice checks normally arrive bundled with the
// patchset from the authoring agent (gs cs capture). When the slice has a CI
// daemon, it also runs the in-slice checks nobody bundled, so changesets made
// with gs create, gs modify or git push still get results for required checks.
func (d *checkDispatcher) dispatchCIChecks(ctx context.Context, cs *corev1.Changeset, slice *corev1.Slice, patchset *corev1.Patchset, bundled map[string]struct{}) {
	if d == nil || d.Checks == nil || cs == nil || slice == nil || patchset == nil {
		return
	}
	plan, err := d.resolveCheckPlan(ctx, slice, patchset)
	if err != nil {
		slog.Warn("failed to resolve check plan for dispatch", "changeset_id", cs.GetId(), "patchset_id", patchset.GetId(), "error", err)
		return
	}
	// A daemon runs checks when configured on the slice (exposed to the slice)
	// or inherited from the account's CI runner pool.
	pool, _ := d.effectiveCIDaemonPool(ctx, slice)
	hasDaemon := len(pool) > 0
	var runnable []checks.CheckSpec
	for _, spec := range plan.Runnable {
		if spec.OutOfSlice {
			runnable = append(runnable, spec)
			continue
		}
		// Never let client-supplied bundled check runs suppress required CI checks
		// on a slice configured with a CI daemon.
		if hasDaemon && (isRequiredSliceCheck(slice, spec.Name) || spec.Required) {
			runnable = append(runnable, spec)
			continue
		}
		if _, ok := bundled[spec.Name]; !ok && hasDaemon {
			runnable = append(runnable, spec)
		}
	}
	if len(runnable) == 0 {
		return
	}
	if _, err := d.sendChecks(ctx, cs, slice, patchset, runnable, ""); err != nil {
		slog.Warn("failed to dispatch ci checks", "changeset_id", cs.GetId(), "patchset_id", patchset.GetId(), "slice_id", slice.GetId(), "error", err)
	}
}

// sendChecks creates a run for each spec on a CI daemon of the slice's pool
// (pickDaemon) and hands the runs to it. Without a daemon, or with none
// exposed to the slice, the runs error. triggerRunID links the runs to the
// trigger run that asked for them. It returns how many runs it created.
func (d *checkDispatcher) sendChecks(ctx context.Context, cs *corev1.Changeset, slice *corev1.Slice, patchset *corev1.Patchset, specs []checks.CheckSpec, triggerRunID string) (int, error) {
	daemonID, refusal := d.pickDaemon(ctx, slice)
	created := 0
	for _, spec := range specs {
		runDaemon := daemonID
		if refusal != "" {
			runDaemon = "" // no daemon ran it
		}
		run, err := d.Checks.CreateCheckRun(ctx, storage.CheckRunInput{
			ChangesetID:     cs.Id,
			PatchsetID:      patchset.Id,
			CheckName:       spec.Name,
			DaemonID:        runDaemon,
			Provenance:      "ci",
			Status:          "queued",
			AutomationRunID: triggerRunID,
		})
		if err != nil {
			slog.Warn("failed to create ci check run", "changeset_id", cs.Id, "patchset_id", patchset.Id, "check", spec.Name, "error", err)
			continue
		}
		created++
		if daemonID == "" {
			if _, err := d.Checks.UpdateCheckRunStatus(ctx, run.Id, "errored", -1, "no CI runner configured for this account or slice; set an account CI runner or slice CI daemon"); err != nil {
				slog.Warn("failed to mark ci check run errored without runner", "run_id", run.Id, "changeset_id", cs.Id, "patchset_id", patchset.Id, "check", spec.Name, "error", err)
			}
			continue
		}
		if refusal != "" {
			if _, err := d.Checks.UpdateCheckRunStatus(ctx, run.Id, "errored", -1, refusal); err != nil {
				slog.Warn("failed to mark ci check run errored for an unexposed daemon", "run_id", run.Id, "changeset_id", cs.Id, "patchset_id", patchset.Id, "check", spec.Name, "error", err)
			}
			continue
		}
	}
	if created > 0 && daemonID != "" && refusal == "" {
		d.handOut(ctx, daemonID)
	}
	return created, nil
}

// handOut sends the daemon's queued runs to its processes connected here,
// and rings the doorbell for one waiting on standby. A process connected to
// another server instance gets them from that instance's sweep.
func (d *checkDispatcher) handOut(ctx context.Context, daemonID string) {
	d.dispatchQueued(ctx, daemonID)
	if d.wake == nil || d.Checks == nil {
		return
	}
	// Runs no process here had room for: wake the daemon's processes on
	// standby, or start more runners.
	queued, err := d.Checks.CountUnclaimedCheckRuns(ctx, daemonID)
	if err != nil {
		slog.Warn("failed to count queued check runs", "daemon_id", daemonID, "error", err)
		return
	}
	if queued > 0 {
		d.wake(ctx, daemonID, queued)
	}
}

// effectiveCIDaemonPool returns the slice's explicit CI daemon pool if set,
// else falls back to the account-level CI daemon pool.
func (d *checkDispatcher) effectiveCIDaemonPool(ctx context.Context, slice *corev1.Slice) ([]string, bool) {
	if pool := storage.CIDaemonPool(slice); len(pool) > 0 {
		return pool, false
	}
	if d == nil || d.Slices == nil || slice == nil {
		return nil, false
	}
	account := strings.TrimSpace(slice.GetRef().GetAccount())
	if account == "" {
		return nil, false
	}
	pool, _, err := d.Slices.AccountCIDaemons(ctx, account)
	if err != nil || len(pool) == 0 {
		return nil, false
	}
	return pool, true
}

// pickDaemon chooses the daemon of the effective CI pool (slice override or
// account pool) that gets check runs: the first eligible one that is online,
// else the first eligible one. With none eligible it returns the first daemon
// and why it may not run them; with no pool, "".
func (d *checkDispatcher) pickDaemon(ctx context.Context, slice *corev1.Slice) (string, string) {
	pool, fromAccount := d.effectiveCIDaemonPool(ctx, slice)
	if len(pool) == 0 {
		return "", ""
	}
	firstRefusal, fallback := "", ""
	for _, daemonID := range pool {
		if !fromAccount {
			if refusal := d.ciDaemonRefusal(ctx, slice.GetId(), daemonID); refusal != "" {
				if firstRefusal == "" {
					firstRefusal = refusal
				}
				continue
			}
		}
		if d.daemonReachable(ctx, daemonID) {
			return daemonID, ""
		}
		if fallback == "" {
			fallback = daemonID
		}
	}
	if fallback != "" {
		return fallback, ""
	}
	return pool[0], firstRefusal
}

// daemonReachable reports whether a daemon is connected here or counts as
// online (connected elsewhere, or waiting on standby).
func (d *checkDispatcher) daemonReachable(ctx context.Context, daemonID string) bool {
	if d.onDemand[daemonID] {
		return true
	}
	if d.hub != nil && len(d.hub.conns(daemonID)) > 0 {
		return true
	}
	return d.daemonOnline(ctx, daemonID)
}

// dispatchQueued claims the daemon's queued runs for its processes
// connected to this server, each up to its free check slots, and sends them
// (design/32 layer 3). Claims make it safe for several server instances to
// do this at once: a run goes to one process.
func (d *checkDispatcher) dispatchQueued(ctx context.Context, daemonID string) {
	daemonID = strings.TrimSpace(daemonID)
	if d == nil || d.hub == nil || d.Checks == nil || daemonID == "" {
		return
	}
	unlock := d.claimLocks.Lock(daemonID)
	defer unlock()
	for _, conn := range d.hub.conns(daemonID) {
		limit := 0
		if conn.capacity > 0 {
			claimed, err := d.Checks.ListClaimedCheckRuns(ctx, conn.instance)
			if err != nil {
				slog.Warn("failed to count a daemon process's check runs", "daemon_id", daemonID, "error", err)
				continue
			}
			if limit = conn.capacity - len(claimed); limit <= 0 {
				continue
			}
		}
		runs, err := d.Checks.ClaimCheckRuns(ctx, daemonID, conn.instance, limit, storage.CheckRunLease)
		if err != nil {
			slog.Warn("failed to claim queued check runs", "daemon_id", daemonID, "error", err)
			continue
		}
		d.sendRuns(ctx, conn, runs, true)
	}
}

// sendRuns sends runs to a daemon process, one RunChecks per patchset.
func (d *checkDispatcher) sendRuns(ctx context.Context, conn *daemonConn, runs []*corev1.CheckRun, logErrors bool) bool {
	grouped := map[string][]*corev1.CheckRun{}
	for _, run := range runs {
		if run == nil || run.Provenance != "ci" {
			continue
		}
		key := run.ChangesetId + "\x00" + run.PatchsetId
		grouped[key] = append(grouped[key], run)
	}
	keys := make([]string, 0, len(grouped))
	for key := range grouped {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if ctx.Err() != nil {
			return false
		}
		group := grouped[key]
		msg, err := d.rebuildRunChecksMessage(ctx, group)
		if err != nil || msg == nil {
			if err != nil && logErrors {
				slog.Warn("failed to build a check dispatch", "daemon_id", conn.daemonID, "changeset_id", group[0].GetChangesetId(), "patchset_id", group[0].GetPatchsetId(), "error", err)
			}
			continue
		}
		if !conn.trySend(msg) {
			return false
		}
		for _, spec := range msg.GetRunChecks().GetChecks() {
			conn.trackRun(spec.GetRunId())
		}
	}
	return true
}

// LaunchTriggerChecks runs checks.yaml checks that a trigger's check action
// names (design/34_triggers.md) on the changeset's slice CI daemon, for the
// patchset, linked to the trigger run. A check whose paths do not match the
// patchset is skipped, and one that already has a run on the patchset is
// left alone. It returns how many runs it created.
func (d *checkDispatcher) LaunchTriggerChecks(ctx context.Context, changesetID, patchsetID string, names []string, triggerRunID string) (int, error) {
	if d == nil || d.Checks == nil {
		return 0, fmt.Errorf("check dispatcher is not configured")
	}
	cs, err := d.Changesets.Get(ctx, changesetID)
	if err != nil {
		return 0, err
	}
	patchset := patchsetByID(cs, patchsetID)
	if patchset == nil {
		return 0, fmt.Errorf("%w: patchset %s", storage.ErrNotFound, patchsetID)
	}
	slice, err := d.Slices.Resolve(ctx, cs.AuthoringSlice)
	if err != nil {
		return 0, err
	}
	plan, err := d.resolveCheckPlan(ctx, slice, patchset)
	if err != nil {
		return 0, err
	}
	existing, err := d.Checks.ListCheckRuns(ctx, cs.Id, patchset.Id)
	if err != nil {
		return 0, err
	}
	ran := map[string]bool{}
	for _, run := range existing {
		ran[run.GetCheckName()] = true
	}
	var specs []checks.CheckSpec
	var problems []string
	for _, name := range names {
		if ran[name] {
			continue
		}
		if spec, ok := plan.Spec(name); ok {
			specs = append(specs, spec)
			continue
		}
		if reason := erroredCheckReason(plan, name); reason != "" {
			problems = append(problems, name+": "+reason)
			continue
		}
		skipped := false
		for _, s := range plan.Skipped {
			skipped = skipped || s.Name == name
		}
		if !skipped {
			problems = append(problems, name+": not defined in a checks.yaml on the patchset's paths")
		}
	}
	created, err := d.sendChecks(ctx, cs, slice, patchset, specs, triggerRunID)
	if err != nil {
		return created, err
	}
	if len(problems) > 0 {
		return created, fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return created, nil
}

func (s *ChangesetService) dispatchCIChecks(ctx context.Context, cs *corev1.Changeset, slice *corev1.Slice, patchset *corev1.Patchset, bundled []*corev1.BundledCheckRun) {
	if s == nil || s.dispatcher == nil {
		return
	}
	names := make(map[string]struct{}, len(bundled))
	for _, run := range bundled {
		if run != nil {
			names[strings.TrimSpace(run.Name)] = struct{}{}
		}
	}
	s.dispatcher.dispatchCIChecks(ctx, cs, slice, patchset, names)
}

// ciDaemonRefusal is why a daemon may not run a slice's checks, or "" when it
// may: its owner has exposed it to the slice. When that cannot be checked it
// says so, and the checks do not run.
func (d *checkDispatcher) ciDaemonRefusal(ctx context.Context, sliceID, daemonID string) string {
	daemonID = strings.TrimSpace(daemonID)
	if d == nil || d.Agents == nil || daemonID == "" {
		return ""
	}
	exposed, err := d.Agents.DaemonExposedTo(ctx, daemonID, sliceID)
	if err != nil {
		slog.Warn("failed to check that a CI daemon is exposed to a slice", "daemon_id", daemonID, "slice_id", sliceID, "error", err)
		return "could not check that the slice's CI daemon is exposed to it, so its checks did not run: " + err.Error()
	}
	if exposed {
		return ""
	}
	return "the slice's CI daemon is not exposed to this slice (its owner may have withdrawn it); choose another CI daemon in the slice's settings, or ask the daemon's owner to expose it again"
}

// ciDaemonRefusalForSlice checks whether daemonID is still authorized to run
// checks for slice: either explicitly exposed to slice (when slice sets its own
// pool or has an exposure) or still listed in the account's CI runner pool.
func (d *checkDispatcher) ciDaemonRefusalForSlice(ctx context.Context, slice *corev1.Slice, daemonID string) string {
	daemonID = strings.TrimSpace(daemonID)
	if d == nil || daemonID == "" || slice == nil {
		return ""
	}
	if len(storage.CIDaemonPool(slice)) > 0 {
		return d.ciDaemonRefusal(ctx, slice.GetId(), daemonID)
	}
	if d.Slices != nil {
		if account := strings.TrimSpace(slice.GetRef().GetAccount()); account != "" {
			if pool, _, err := d.Slices.AccountCIDaemons(ctx, account); err == nil {
				for _, id := range pool {
					if id == daemonID {
						return ""
					}
				}
			}
		}
	}
	if d.Agents != nil {
		if exposed, err := d.Agents.DaemonExposedTo(ctx, daemonID, slice.GetId()); err == nil && exposed {
			return ""
		}
	}
	return "the CI runner is no longer configured for this account or slice; choose a CI runner in account settings or slice settings"
}

func (d *checkDispatcher) cancelOpenCheckRunsBeforePatchset(ctx context.Context, changesetID, currentPatchsetID string) {
	if d == nil || d.Checks == nil {
		return
	}
	runs, err := d.Checks.CancelOpenCheckRunsBeforePatchset(ctx, changesetID, currentPatchsetID)
	if err != nil {
		slog.Warn("failed to cancel superseded check runs", "changeset_id", changesetID, "current_patchset_id", currentPatchsetID, "error", err)
		return
	}
	for _, run := range runs {
		d.cancelCheckRun(run)
	}
}

func (d *checkDispatcher) cancelCheckRun(run *corev1.CheckRun) {
	if d == nil || d.hub == nil || run == nil || strings.TrimSpace(run.GetDaemonId()) == "" || strings.TrimSpace(run.GetId()) == "" {
		return
	}
	// Whichever process has it stops it; the others do not know the run.
	for _, conn := range d.hub.conns(strings.TrimSpace(run.GetDaemonId())) {
		conn.trySend(&corev1.ServerMessage{Payload: &corev1.ServerMessage_CancelCheck{CancelCheck: &corev1.CancelCheckRun{
			RunId: run.GetId(),
		}}})
	}
}

func (d *checkDispatcher) rerunCheck(ctx context.Context, cs *corev1.Changeset, slice *corev1.Slice, patchset *corev1.Patchset, prior *corev1.CheckRun) (*corev1.CheckRun, error) {
	if d == nil || d.Checks == nil || d.hub == nil {
		return nil, status.Error(codes.FailedPrecondition, "check dispatcher is not configured")
	}
	if cs == nil || slice == nil || patchset == nil {
		return nil, status.Error(codes.FailedPrecondition, "check run context is incomplete")
	}
	if prior == nil {
		return nil, status.Error(codes.FailedPrecondition, "check run context is incomplete")
	}
	checkName := strings.TrimSpace(prior.GetCheckName())
	if checkName == "" {
		return nil, status.Error(codes.FailedPrecondition, "check run has no check name")
	}
	daemonID, refusal := d.pickDaemon(ctx, slice)
	if daemonID == "" {
		return nil, status.Error(codes.FailedPrecondition, "slice has no CI daemon configured")
	}
	if refusal != "" {
		return nil, status.Error(codes.FailedPrecondition, refusal)
	}
	// A daemon that detached to wait on standby has no stream, but it is
	// online and picks queued runs up when it is woken or next polls.
	if !d.daemonReachable(ctx, daemonID) {
		return nil, status.Error(codes.FailedPrecondition, "slice CI daemon is not online")
	}

	plan, err := d.resolveCheckPlan(ctx, slice, patchset)
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "check plan could not be resolved: %v", err)
	}
	spec, ok := runnableCICheck(plan, checkName)
	if !ok {
		if reason := erroredCheckReason(plan, checkName); reason != "" {
			return nil, status.Errorf(codes.FailedPrecondition, "check %q has no resolvable definition in this revision: %s", checkName, reason)
		}
		return nil, status.Errorf(codes.FailedPrecondition, "check %q has no runnable CI definition in this revision", checkName)
	}
	run, err := d.enqueueCheckRun(ctx, cs, slice, patchset, spec, daemonID, prior, "superseded by rerun")
	if err != nil {
		return nil, err
	}
	d.handOut(ctx, daemonID)
	return run, nil
}

func (d *checkDispatcher) enqueueCheckRun(ctx context.Context, cs *corev1.Changeset, slice *corev1.Slice, patchset *corev1.Patchset, spec checks.CheckSpec, daemonID string, prior *corev1.CheckRun, cancelReason string) (*corev1.CheckRun, error) {
	if d.patchsetApprovedForGitsliceSecrets(ctx, cs, slice, patchset) {
		if _, err := d.secretsForChecks(ctx, slice.GetRef().GetAccount(), []checks.CheckSpec{spec}); err != nil {
			return nil, status.Errorf(codes.FailedPrecondition, "CI secrets could not be loaded: %v", err)
		}
	}

	run, err := d.Checks.CreateCheckRun(ctx, storage.CheckRunInput{
		ChangesetID: cs.Id,
		PatchsetID:  patchset.Id,
		CheckName:   spec.Name,
		DaemonID:    daemonID,
		Provenance:  "ci",
		Status:      "queued",
	})
	if err != nil {
		return nil, grpcError(err)
	}
	if prior != nil && (prior.GetStatus() == "queued" || prior.GetStatus() == "running") {
		if _, err := d.Checks.UpdateCheckRunStatus(ctx, prior.GetId(), "canceled", -1, cancelReason); err != nil {
			slog.Warn("failed to cancel prior check run after rerun", "run_id", prior.GetId(), "replacement_run_id", run.GetId(), "error", err)
		} else {
			d.cancelCheckRun(prior)
		}
	}
	return run, nil
}

// daemonOnline reports whether the store counts the daemon as online: seen
// within storage.DaemonStaleAfter, connected or on standby.
func (d *checkDispatcher) daemonOnline(ctx context.Context, daemonID string) bool {
	if d == nil || d.Agents == nil {
		return false
	}
	daemon, err := d.Agents.GetDaemon(ctx, daemonID)
	return err == nil && daemon.GetStatus() == "online"
}
