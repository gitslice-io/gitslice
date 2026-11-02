package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"gitslice.io/gitslice/internal/checks"
	"gitslice.io/gitslice/internal/objectstore/filesystem"
	"gitslice.io/gitslice/internal/secretmask"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	checkDispatchSweepInterval = 10 * time.Second
	checkDispatchSweepMinAge   = 15 * time.Second
)

type checkTreeReader struct {
	repository storage.RepositoryStore
	objects    ObjectStore
	account    string
}

func (r checkTreeReader) ReadFile(ctx context.Context, rootTreeID, logicalPath string) ([]byte, error) {
	if r.repository == nil || r.objects == nil {
		return nil, fmt.Errorf("check tree reader is not configured")
	}
	entry, err := r.repository.GetFileAtTree(ctx, rootTreeID, r.storagePath(logicalPath))
	if errors.Is(err, storage.ErrNotFound) {
		return nil, checks.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rc, err := r.objects.Get(ctx, filesystem.BlobKey(entry.ContentHash), 0, 0)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func (r checkTreeReader) storagePath(logicalPath string) string {
	cleaned := path.Clean("/" + strings.TrimSpace(logicalPath))
	if cleaned == "/" {
		return "/" + r.account
	}
	return "/" + path.Join(r.account, strings.TrimPrefix(cleaned, "/"))
}

type checkDispatcher struct {
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

	claimMu    sync.Mutex
	claimLocks map[string]*sync.Mutex
}

func (d *checkDispatcher) resolveCheckPlan(ctx context.Context, slice *corev1.Slice, patchset *corev1.Patchset) (*checks.Plan, error) {
	if d == nil {
		return nil, fmt.Errorf("check dispatcher is not configured")
	}
	if slice == nil || slice.Ref == nil || slice.Definition == nil {
		return nil, fmt.Errorf("slice definition is required")
	}
	if patchset == nil {
		return nil, fmt.Errorf("patchset is required")
	}
	if strings.TrimSpace(patchset.ResultTreeId) == "" {
		return nil, fmt.Errorf("patchset result tree is required")
	}
	reader := checkTreeReader{
		repository: d.Repository,
		objects:    d.ObjectStore,
		account:    slice.Ref.Account,
	}
	return checks.ResolvePlan(
		ctx,
		reader,
		patchset.ResultTreeId,
		logicalPathsForAccount(slice.Ref.Account, patchset.ChangedPaths),
		logicalPathsForAccount(slice.Ref.Account, slice.Definition.IncludedPaths),
		slice.Definition.RequiredChecks,
	)
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
	// A daemon runs a slice's checks only while its owner keeps it exposed to
	// the slice. Otherwise the checks error (sendChecks), without the daemon
	// seeing the slice's secrets.
	hasDaemon := len(storage.CIDaemonPool(slice)) > 0
	var runnable []checks.CheckSpec
	for _, spec := range plan.Runnable {
		if spec.OutOfSlice {
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
			ChangesetID:  cs.Id,
			PatchsetID:   patchset.Id,
			CheckName:    spec.Name,
			DaemonID:     runDaemon,
			Provenance:   "ci",
			Status:       "queued",
			TriggerRunID: triggerRunID,
		})
		if err != nil {
			slog.Warn("failed to create ci check run", "changeset_id", cs.Id, "patchset_id", patchset.Id, "check", spec.Name, "error", err)
			continue
		}
		created++
		if daemonID == "" {
			if _, err := d.Checks.UpdateCheckRunStatus(ctx, run.Id, "errored", -1, "slice has no full-tree CI runner configured; set the slice CI daemon to run out-of-slice checks"); err != nil {
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

// pickDaemon chooses the daemon of the slice's pool that gets its check
// runs: the first one exposed to the slice that is online, else the first
// one exposed to it. With none exposed it returns the first daemon and why
// it may not run them; with no pool, "".
func (d *checkDispatcher) pickDaemon(ctx context.Context, slice *corev1.Slice) (string, string) {
	pool := storage.CIDaemonPool(slice)
	if len(pool) == 0 {
		return "", ""
	}
	firstRefusal, fallback := "", ""
	for _, daemonID := range pool {
		if refusal := d.ciDaemonRefusal(ctx, slice.GetId(), daemonID); refusal != "" {
			if firstRefusal == "" {
				firstRefusal = refusal
			}
			continue
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

func (d *checkDispatcher) claimLock(daemonID string) *sync.Mutex {
	d.claimMu.Lock()
	defer d.claimMu.Unlock()
	if d.claimLocks == nil {
		d.claimLocks = map[string]*sync.Mutex{}
	}
	lock := d.claimLocks[daemonID]
	if lock == nil {
		lock = &sync.Mutex{}
		d.claimLocks[daemonID] = lock
	}
	return lock
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
	lock := d.claimLock(daemonID)
	lock.Lock()
	defer lock.Unlock()
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
	if _, err := d.secretsForChecks(ctx, slice.GetRef().GetAccount(), []checks.CheckSpec{spec}); err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "CI secrets could not be loaded: %v", err)
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
	if prior.GetStatus() == "queued" || prior.GetStatus() == "running" {
		if _, err := d.Checks.UpdateCheckRunStatus(ctx, prior.GetId(), "canceled", -1, "superseded by rerun"); err != nil {
			slog.Warn("failed to cancel prior check run after rerun", "run_id", prior.GetId(), "replacement_run_id", run.GetId(), "error", err)
		} else {
			d.cancelCheckRun(prior)
		}
	}

	d.handOut(ctx, daemonID)
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

type changesetSubmitWithCheckStatuses interface {
	SubmitWithCheckStatuses(ctx context.Context, changesetID, expectedCurrentPatchsetID string, extraCheckStatuses map[string]string) (*corev1.SubmitChangesetResponse, error)
}

func (s *ChangesetService) skippedRequiredCheckStatuses(ctx context.Context, cs *corev1.Changeset) (map[string]string, error) {
	patchset := currentPatchset(cs)
	if patchset == nil {
		return nil, nil
	}
	slice, err := s.Slices.Resolve(ctx, cs.AuthoringSlice)
	if err != nil {
		return nil, err
	}
	if slice.Definition == nil || len(slice.Definition.RequiredChecks) == 0 {
		return nil, nil
	}
	if s.dispatcher == nil {
		return nil, fmt.Errorf("check dispatcher is not configured")
	}
	plan, err := s.dispatcher.resolveCheckPlan(ctx, slice, patchset)
	if err != nil {
		return nil, err
	}
	required := map[string]struct{}{}
	for _, name := range slice.Definition.RequiredChecks {
		name = strings.TrimSpace(name)
		if name != "" {
			required[name] = struct{}{}
		}
	}
	if len(required) == 0 {
		return nil, nil
	}
	out := map[string]string{}
	for _, skipped := range plan.Skipped {
		if _, ok := required[skipped.Name]; ok {
			out[skipped.Name] = storage.CheckStatusPass
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// replayDaemonCheckRuns sends a process that (re)connected the runs it
// holds claims on, then the daemon's unclaimed queued runs it has room for.
func (s *AgentService) replayDaemonCheckRuns(ctx context.Context, daemonID string, conn *daemonConn) {
	if s.Checks == nil || s.dispatcher == nil || conn == nil {
		return
	}
	if claimed, err := s.Checks.ListClaimedCheckRuns(ctx, conn.instance); err == nil {
		s.dispatcher.sendRuns(ctx, conn, claimed, false)
	}
	s.dispatcher.dispatchQueued(ctx, daemonID)
}

// checkRecoverEvery is how many sweeps pass between looks for runs whose
// process went away or whose daemon is gone (recoverCheckRuns).
const checkRecoverEvery = 3

func (s *AgentService) RunCheckDispatchSweep(ctx context.Context) {
	if s == nil || s.hub == nil || s.Checks == nil || s.dispatcher == nil {
		return
	}
	ticker := time.NewTicker(checkDispatchSweepInterval)
	defer ticker.Stop()
	for tick := 1; ; tick++ {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.runCheckDispatchSweepOnce(ctx, now)
			if tick%checkRecoverEvery == 0 {
				s.dispatcher.recoverCheckRuns(ctx)
			}
		}
	}
}

// runCheckDispatchSweepOnce sends each connected process again the queued
// runs it holds that it has not started (a message may have been lost), and
// hands out queued runs nobody holds.
func (s *AgentService) runCheckDispatchSweepOnce(ctx context.Context, now time.Time) {
	if s == nil || s.hub == nil || s.Checks == nil || s.dispatcher == nil {
		return
	}
	for _, daemonID := range s.hub.onlineDaemonIDs() {
		for _, conn := range s.hub.conns(daemonID) {
			if err := ctx.Err(); err != nil {
				return
			}
			runs, err := s.Checks.ListClaimedCheckRuns(ctx, conn.instance)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				slog.Warn("check dispatch sweep failed to list claimed runs", "daemon_id", daemonID, "error", err)
				continue
			}
			if stale := staleCheckRunsForDispatch(runs, now); len(stale) > 0 {
				s.dispatcher.sendRuns(ctx, conn, stale, true)
			}
		}
		s.dispatcher.dispatchQueued(ctx, daemonID)
	}
}

func staleCheckRunsForDispatch(runs []*corev1.CheckRun, now time.Time) []*corev1.CheckRun {
	stale := make([]*corev1.CheckRun, 0, len(runs))
	for _, run := range runs {
		if run == nil || run.Provenance != "ci" || run.Status != "queued" {
			continue
		}
		createdAt, err := time.Parse(time.RFC3339Nano, run.GetCreatedAt())
		if err != nil {
			slog.Warn("check dispatch sweep skipped run with invalid created_at", "run_id", run.GetId(), "created_at", run.GetCreatedAt(), "error", err)
			continue
		}
		if now.Sub(createdAt) < checkDispatchSweepMinAge {
			continue
		}
		stale = append(stale, run)
	}
	return stale
}

// strandedRunAfter is how long a queued run waits on a daemon that is gone
// before it moves to another daemon of the slice's pool.
var strandedRunAfter = 2 * time.Minute

// recoverCheckRuns looks after runs whose daemon process or daemon went
// away (design/32 layer 3): a queued run whose claim lapsed is handed out
// again, a running one is tried again (up to storage.MaxCheckRunAttempts),
// and a queued run whose daemon is gone moves to another daemon of the pool.
func (d *checkDispatcher) recoverCheckRuns(ctx context.Context) {
	if d == nil || d.Checks == nil {
		return
	}
	lapsed, err := d.Checks.TakeLapsedCheckRuns(ctx, 100)
	if err != nil {
		slog.Warn("failed to take lapsed check run claims", "error", err)
	}
	for _, run := range lapsed {
		if run.GetStatus() == "running" {
			d.retryLostRun(ctx, run)
			continue
		}
		d.handOut(ctx, run.GetDaemonId())
	}
	// Runners exit when idle; queued runs that waited for a free one get
	// another ring.
	for daemonID := range d.onDemand {
		d.handOut(ctx, daemonID)
	}
	stranded, err := d.Checks.ListStrandedCheckRuns(ctx, strandedRunAfter)
	if err != nil {
		slog.Warn("failed to list stranded check runs", "error", err)
	}
	for _, run := range stranded {
		d.moveStrandedRun(ctx, run)
	}
}

// runContext loads a run's changeset, patchset and slice.
func (d *checkDispatcher) runContext(ctx context.Context, run *corev1.CheckRun) (*corev1.Changeset, *corev1.Patchset, *corev1.Slice, error) {
	cs, err := d.Changesets.Get(ctx, run.GetChangesetId())
	if err != nil {
		return nil, nil, nil, err
	}
	patchset := patchsetByID(cs, run.GetPatchsetId())
	if patchset == nil {
		return nil, nil, nil, storage.ErrNotFound
	}
	slice, err := d.Slices.Resolve(ctx, cs.AuthoringSlice)
	if err != nil {
		return nil, nil, nil, err
	}
	return cs, patchset, slice, nil
}

// retryLostRun ends a running run whose daemon process stopped responding,
// and queues another attempt on the slice's pool unless it has had its
// tries.
func (d *checkDispatcher) retryLostRun(ctx context.Context, run *corev1.CheckRun) {
	lost := "the CI daemon process running it stopped responding"
	if run.GetAttempt() >= storage.MaxCheckRunAttempts {
		if _, err := d.Checks.UpdateCheckRunStatus(ctx, run.GetId(), "errored", -1, fmt.Sprintf("%s (tried %d times)", lost, run.GetAttempt())); err != nil && !errors.Is(err, storage.ErrConflict) {
			slog.Warn("failed to end a lost check run", "run_id", run.GetId(), "error", err)
		}
		return
	}
	_, _, slice, err := d.runContext(ctx, run)
	if err != nil {
		slog.Warn("failed to load a lost check run's slice", "run_id", run.GetId(), "error", err)
		return
	}
	daemonID, refusal := d.pickDaemon(ctx, slice)
	if daemonID == "" || refusal != "" {
		if _, err := d.Checks.UpdateCheckRunStatus(ctx, run.GetId(), "errored", -1, lost+"; "+firstNonEmptyString(refusal, "the slice has no CI daemon to try it again")); err != nil && !errors.Is(err, storage.ErrConflict) {
			slog.Warn("failed to end a lost check run", "run_id", run.GetId(), "error", err)
		}
		return
	}
	next, err := d.Checks.CreateCheckRun(ctx, storage.CheckRunInput{
		ChangesetID:  run.GetChangesetId(),
		PatchsetID:   run.GetPatchsetId(),
		CheckName:    run.GetCheckName(),
		DaemonID:     daemonID,
		Provenance:   "ci",
		Status:       "queued",
		TriggerRunID: run.GetTriggerRunId(),
	})
	if err != nil {
		slog.Warn("failed to queue another attempt of a lost check run", "run_id", run.GetId(), "error", err)
		return
	}
	if _, err := d.Checks.UpdateCheckRunStatus(ctx, run.GetId(), "canceled", -1, fmt.Sprintf("%s; tried again as attempt %d", lost, next.GetAttempt())); err != nil && !errors.Is(err, storage.ErrConflict) {
		slog.Warn("failed to end a lost check run", "run_id", run.GetId(), "error", err)
	}
	d.cancelCheckRun(run)
	d.handOut(ctx, daemonID)
}

// moveStrandedRun moves a queued run whose daemon is gone to another daemon
// of the slice's pool that is online.
func (d *checkDispatcher) moveStrandedRun(ctx context.Context, run *corev1.CheckRun) {
	_, _, slice, err := d.runContext(ctx, run)
	if err != nil {
		return
	}
	daemonID, refusal := d.pickDaemon(ctx, slice)
	if daemonID == "" || refusal != "" || daemonID == run.GetDaemonId() || !d.daemonReachable(ctx, daemonID) {
		return
	}
	moved, err := d.Checks.ReassignCheckRun(ctx, run.GetId(), run.GetDaemonId(), daemonID)
	if err != nil {
		slog.Warn("failed to move a stranded check run", "run_id", run.GetId(), "error", err)
		return
	}
	if moved {
		slog.Info("moved a check run off a daemon that is gone", "run_id", run.GetId(), "from", run.GetDaemonId(), "to", daemonID)
		d.handOut(ctx, daemonID)
	}
}

func (d *checkDispatcher) rebuildRunChecksMessage(ctx context.Context, runs []*corev1.CheckRun) (*corev1.ServerMessage, error) {
	if d == nil || d.Changesets == nil || d.Slices == nil {
		return nil, fmt.Errorf("check dispatcher is not configured")
	}
	first := runs[0]
	cs, err := d.Changesets.Get(ctx, first.ChangesetId)
	if err != nil {
		return nil, err
	}
	patchset := patchsetByID(cs, first.PatchsetId)
	if patchset == nil {
		return nil, storage.ErrNotFound
	}
	slice, err := d.Slices.Resolve(ctx, cs.AuthoringSlice)
	if err != nil {
		return nil, err
	}
	// Queued runs for a daemon that is no longer exposed to the slice are not
	// sent to it: they error, and the daemon never sees the slice's secrets.
	if refusal := d.ciDaemonRefusal(ctx, slice.Id, first.DaemonId); refusal != "" {
		for _, run := range runs {
			if _, err := d.Checks.UpdateCheckRunStatus(ctx, run.Id, "errored", -1, refusal); err != nil {
				slog.Warn("failed to mark queued ci check run errored for an unexposed daemon", "run_id", run.Id, "error", err)
			}
		}
		return nil, nil
	}
	plan, err := d.resolveCheckPlan(ctx, slice, patchset)
	if err != nil {
		return nil, err
	}
	byName := map[string]checks.CheckSpec{}
	for _, spec := range append(append([]checks.CheckSpec(nil), plan.Runnable...), plan.Triggered...) {
		byName[spec.Name] = spec
	}
	var queued []checks.CheckSpec
	for _, run := range runs {
		if spec, ok := byName[run.CheckName]; ok {
			queued = append(queued, spec)
		}
	}
	secrets, err := d.secretsForChecks(ctx, slice.GetRef().GetAccount(), queued)
	if err != nil {
		return nil, err
	}
	specs := make([]*corev1.CheckRunSpec, 0, len(runs))
	for _, run := range runs {
		spec, ok := byName[run.CheckName]
		if !ok {
			continue
		}
		specs = append(specs, checkRunSpecFromPlan(run.Id, spec, secrets[spec.Name]))
	}
	if len(specs) == 0 {
		return nil, nil
	}
	return &corev1.ServerMessage{Payload: &corev1.ServerMessage_RunChecks{RunChecks: &corev1.RunChecks{
		ChangesetId:  first.ChangesetId,
		PatchsetId:   first.PatchsetId,
		ResultTreeId: patchset.ResultTreeId,
		Slice:        slice.Ref,
		SliceId:      slice.Id,
		ServerAddr:   d.serverAddr,
		Checks:       specs,
	}}}, nil
}

func (s *AgentService) handleCheckRunUpdate(ctx context.Context, daemonID string, conn *daemonConn, update *corev1.CheckRunUpdate) {
	if s.Checks == nil || conn == nil || update == nil {
		return
	}
	ack := func() {
		conn.trySend(&corev1.ServerMessage{Payload: &corev1.ServerMessage_CheckAck{CheckAck: &corev1.CheckRunAck{
			RunId:          update.RunId,
			AckedClientSeq: update.ClientSeq,
		}}})
	}
	run, err := s.Checks.GetCheckRun(ctx, update.RunId)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			ack()
		}
		return
	}
	if run.DaemonId != daemonID {
		ack()
		return
	}
	// The process reporting on a run holds it (design/32 layer 3). A run
	// that ended without it (canceled, or tried again elsewhere after its
	// claim lapsed) is stopped there.
	if run.Status == "queued" || run.Status == "running" {
		_ = s.Checks.RenewCheckRunClaim(ctx, run.Id, conn.instance, storage.CheckRunLease)
	} else if update.Status != "" && update.Status != run.Status && !update.Final {
		conn.trySend(&corev1.ServerMessage{Payload: &corev1.ServerMessage_CancelCheck{CancelCheck: &corev1.CancelCheckRun{RunId: run.Id}}})
	}
	if s.dispatcher != nil && (update.Final || isFinalCheckStatus(update.Status)) {
		// A slot is free on that process.
		defer s.dispatcher.dispatchQueued(context.WithoutCancel(ctx), daemonID)
	}
	if update.LogChunk != "" || update.Summary != "" {
		masker := s.runSecretMasker(ctx, run)
		update.LogChunk = masker.Mask(update.LogChunk)
		update.Summary = masker.Mask(update.Summary)
	}
	if update.LogChunk != "" && update.ClientSeq > 0 {
		streamName := defaultCheckStream(update.Stream)
		inserted, err := s.Checks.AppendCheckRunLog(ctx, update.RunId, update.ClientSeq, streamName, update.LogChunk)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) || errors.Is(err, storage.ErrInvalid) || errors.Is(err, storage.ErrConflict) {
				ack()
			}
			return
		}
		if inserted && s.checkLogs != nil {
			log := &corev1.CheckRunLog{
				RunId:  update.RunId,
				Seq:    update.ClientSeq,
				Stream: streamName,
				Chunk:  update.LogChunk,
			}
			if stored := s.persistedCheckRunLog(ctx, update.RunId, update.ClientSeq); stored != nil {
				log = stored
			}
			s.checkLogs.publish(update.RunId, log)
		}
	}
	if update.Status != "" {
		if _, err := s.Checks.UpdateCheckRunStatus(ctx, update.RunId, update.Status, update.ExitCode, update.Summary); err != nil {
			if errors.Is(err, storage.ErrNotFound) || errors.Is(err, storage.ErrInvalid) || errors.Is(err, storage.ErrConflict) {
				ack()
			}
			return
		}
		if s.checkLogs != nil {
			s.checkLogs.publish(update.RunId, nil)
		}
	} else if update.Final {
		if _, err := s.Checks.UpdateCheckRunStatus(ctx, update.RunId, "errored", update.ExitCode, update.Summary); err != nil {
			if errors.Is(err, storage.ErrNotFound) || errors.Is(err, storage.ErrInvalid) || errors.Is(err, storage.ErrConflict) {
				ack()
			}
			return
		}
		if s.checkLogs != nil {
			s.checkLogs.publish(update.RunId, nil)
		}
	}
	ack()
}

// runSecretMasker hides the secrets the run's check was given (those at the
// folder that defines it) in what its daemon sends back, whatever the
// daemon's version: a check that prints a secret must not store it in a log
// that every reader of the changeset can open.
// Daemons mask too, with the values they were sent (CheckRunSpec.secret_env),
// which also covers a secret changed while the check ran.
func (s *AgentService) runSecretMasker(ctx context.Context, run *corev1.CheckRun) *secretmask.Masker {
	if s.Changesets == nil || s.Slices == nil || run == nil {
		return nil
	}
	cs, err := s.Changesets.Get(ctx, run.ChangesetId)
	if err != nil {
		slog.Warn("failed to load changeset to mask check secrets", "run_id", run.Id, "error", err)
		return nil
	}
	secrets, err := s.Slices.SecretValuesAt(ctx, checkSecretFolder(cs.GetAuthoringSlice().GetAccount(), run.CheckName))
	if err != nil {
		slog.Warn("failed to load secrets to mask check output", "run_id", run.Id, "check", run.CheckName, "error", err)
		return nil
	}
	values := make([]string, 0, len(secrets))
	for _, value := range secrets {
		values = append(values, value)
	}
	return secretmask.New(values)
}

func (s *AgentService) persistedCheckRunLog(ctx context.Context, runID string, seq int64) *corev1.CheckRunLog {
	logs, err := s.Checks.ListCheckRunLogs(ctx, runID, seq-1)
	if err != nil {
		return nil
	}
	for _, log := range logs {
		if log.GetSeq() == seq {
			return log
		}
	}
	return nil
}

// secretsForChecks returns, by check name, the secrets each check gets:
// those that apply at the folder of the checks.yaml that defines it
// (design/33_secrets.md). Each folder is read once.
func (d *checkDispatcher) secretsForChecks(ctx context.Context, account string, specs []checks.CheckSpec) (map[string]map[string]string, error) {
	if d == nil || d.Slices == nil {
		return nil, fmt.Errorf("slice store is not configured")
	}
	byFolder := map[string]map[string]string{}
	out := make(map[string]map[string]string, len(specs))
	for _, spec := range specs {
		folder := checkSecretFolder(account, spec.Name)
		values, ok := byFolder[folder]
		if !ok {
			var err error
			if values, err = d.Slices.SecretValuesAt(ctx, folder); err != nil {
				return nil, err
			}
			byFolder[folder] = values
		}
		out[spec.Name] = values
	}
	return out, nil
}

// checkSecretFolder is the folder whose secrets a check gets: the folder of
// the checks.yaml that defines it, read from its id ("<dir>/<name>", or
// "<name>" at the root) as a path in the account.
func checkSecretFolder(account, checkName string) string {
	dir := path.Dir("/" + strings.Trim(strings.TrimSpace(checkName), "/"))
	return checkTreeReader{account: account}.storagePath(dir)
}

func checkRunSpecFromPlan(runID string, spec checks.CheckSpec, secrets map[string]string) *corev1.CheckRunSpec {
	return &corev1.CheckRunSpec{
		RunId:            runID,
		Name:             spec.Name,
		Command:          spec.Run,
		Image:            spec.Image,
		Setup:            append([]string(nil), spec.Setup...),
		Cache:            append([]string(nil), spec.Cache...),
		WorkingDir:       spec.WorkingDir,
		MaterializePaths: append([]string(nil), spec.MaterializePaths...),
		Env:              mergeCheckEnv(spec.Env, secrets),
		Network:          spec.Network,
		Memory:           spec.Memory,
		Cpus:             spec.CPUs,
		TimeoutMs:        spec.Timeout.Milliseconds(),
		Build:            checkImageBuildToProto(spec.Build),
		Services:         checkServicesToProto(spec.Services),
		SecretEnv:        sortedKeys(secrets),
	}
}

func sortedKeys(values map[string]string) []string {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func checkImageBuildToProto(build *checks.ImageBuild) *corev1.CheckImageBuild {
	if build == nil {
		return nil
	}
	return &corev1.CheckImageBuild{
		ContextDir: build.ContextDir,
		Dockerfile: build.Dockerfile,
		Inputs:     append([]string(nil), build.Inputs...),
		TimeoutMs:  build.Timeout.Milliseconds(),
	}
}

func checkServicesToProto(services []checks.ServiceSpec) []*corev1.CheckServiceContainer {
	out := make([]*corev1.CheckServiceContainer, 0, len(services))
	for _, service := range services {
		out = append(out, &corev1.CheckServiceContainer{
			Name:           service.Name,
			Image:          service.Image,
			Env:            copyStringMap(service.Env),
			Ready:          service.Ready,
			ReadyTimeoutMs: service.ReadyTimeout.Milliseconds(),
			Memory:         service.Memory,
		})
	}
	return out
}

func mergeCheckEnv(checkEnv, secrets map[string]string) map[string]string {
	out := copyStringMap(checkEnv)
	if len(secrets) == 0 {
		return out
	}
	if out == nil {
		out = map[string]string{}
	}
	for key, value := range secrets {
		out[key] = value
	}
	return out
}

func logicalPathsForAccount(account string, in []string) []string {
	out := make([]string, 0, len(in))
	accountPrefix := "/" + strings.Trim(strings.TrimSpace(account), "/")
	for _, value := range in {
		cleaned := path.Clean("/" + strings.TrimSpace(value))
		switch {
		case cleaned == accountPrefix:
			out = append(out, "/")
		case strings.HasPrefix(cleaned, accountPrefix+"/"):
			out = append(out, strings.TrimPrefix(cleaned, accountPrefix+"/"))
		default:
			out = append(out, strings.TrimPrefix(cleaned, "/"))
		}
	}
	return out
}

func patchsetByID(cs *corev1.Changeset, patchsetID string) *corev1.Patchset {
	if cs == nil {
		return nil
	}
	for _, patchset := range cs.Patchsets {
		if patchset != nil && patchset.Id == patchsetID {
			return patchset
		}
	}
	return nil
}

// runnableCICheck finds a runnable check, in-slice or out-of-slice, that the
// slice's CI daemon can run for the patchset.
func runnableCICheck(plan *checks.Plan, checkName string) (checks.CheckSpec, bool) {
	return plan.Spec(checkName)
}

func erroredCheckReason(plan *checks.Plan, checkName string) string {
	if plan == nil {
		return ""
	}
	for _, errored := range plan.Errored {
		if errored.Name == checkName {
			return errored.Reason
		}
	}
	return ""
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func defaultCheckStream(stream string) string {
	stream = strings.ToLower(strings.TrimSpace(stream))
	if stream == "stderr" {
		return "stderr"
	}
	return "stdout"
}

func isFinalCheckStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "passed", "failed", "errored", "skipped", "canceled":
		return true
	}
	return false
}
