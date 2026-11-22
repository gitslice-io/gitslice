package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/model"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

// pendingCheckPattern matches a submit blocked only because a required check
// has no result yet (model.EvaluateSubmitRequirements).
var pendingCheckPattern = regexp.MustCompile(`required check "([^"]+)" has no result`)

// submitRetryInterval is how often gs create --submit and gs submit retry
// while required checks have not reported.
var submitRetryInterval = 5 * time.Second

var defaultPublishPollSteps = []time.Duration{
	50 * time.Millisecond,
	100 * time.Millisecond,
	250 * time.Millisecond,
	500 * time.Millisecond,
	time.Second,
	2 * time.Second,
}

const defaultDraftPublishPollCap = 5 * time.Second

type changesetWaitConfig struct {
	publishSteps    []time.Duration
	draftPublishCap time.Duration
	checkRetryDelay time.Duration
	sleep           func(context.Context, time.Duration) error
}

func (r Runner) resolveChangesetWaitConfig() changesetWaitConfig {
	cfg := changesetWaitConfig{
		publishSteps:    defaultPublishPollSteps,
		draftPublishCap: defaultDraftPublishPollCap,
		checkRetryDelay: submitRetryInterval,
		sleep:           sleepWithContext,
	}
	if r.changesetWait == nil {
		return cfg
	}
	if len(r.changesetWait.publishSteps) > 0 {
		cfg.publishSteps = r.changesetWait.publishSteps
	}
	if r.changesetWait.draftPublishCap > 0 {
		cfg.draftPublishCap = r.changesetWait.draftPublishCap
	}
	if r.changesetWait.checkRetryDelay > 0 {
		cfg.checkRetryDelay = r.changesetWait.checkRetryDelay
	}
	if r.changesetWait.sleep != nil {
		cfg.sleep = r.changesetWait.sleep
	}
	return cfg
}

func (c changesetWaitConfig) publishPollDelay(step int, status string) time.Duration {
	steps := c.publishSteps
	if len(steps) == 0 {
		steps = defaultPublishPollSteps
	}
	if step < 0 {
		step = 0
	}
	var delay time.Duration
	if step < len(steps) {
		delay = steps[step]
	} else {
		delay = steps[len(steps)-1]
		if status == "draft" && c.draftPublishCap > delay {
			delay = c.draftPublishCap
		}
	}
	if status == "draft" && c.draftPublishCap > 0 && delay > c.draftPublishCap {
		delay = c.draftPublishCap
	}
	return delay
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func pendingRequiredCheckName(reason string) string {
	match := pendingCheckPattern.FindStringSubmatch(reason)
	if len(match) < 2 {
		return ""
	}
	return match[1]
}

// retrySubmitWhileChecksPending runs attempt and, while it reports a required
// check with no result yet, waits up to timeout and tries again. It streams
// check state transitions and fails fast if any required check fails.
func (r Runner) retrySubmitWhileChecksPending(
	ctx context.Context,
	opts commandOptions,
	conn *grpc.ClientConn,
	cfg UserConfig,
	timeout time.Duration,
	attempt func(remaining time.Duration) (pendingCheck, pendingChangesetID string, err error),
) error {
	waitCfg := r.resolveChangesetWaitConfig()
	deadline := time.Now().Add(timeout)
	waitingFor := ""
	var daemons *checkDaemonWatch
	var progressOut io.Writer
	if !opts.Quiet && !opts.jsonOutput() {
		progressOut = r.stderr()
		daemons = newCheckDaemonWatch(progressOut)
	}
	progress := newCheckProgressWatch(progressOut)
	for {
		check, checkChangeset, err := attempt(time.Until(deadline))
		if err != nil {
			return err
		}
		remaining := time.Until(deadline)
		if check == "" || remaining <= 0 {
			return nil
		}
		if check != waitingFor && progressOut != nil {
			fmt.Fprintf(progressOut, "waiting for required check %q to pass...\n", check)
		}
		if err := progress.poll(ctx, conn, cfg, checkChangeset, check); err != nil {
			return err
		}
		daemons.look(ctx, conn, cfg, checkChangeset, check, check != waitingFor)
		waitingFor = check
		if err := waitCfg.sleep(ctx, min(waitCfg.checkRetryDelay, remaining)); err != nil {
			return err
		}
	}
}

type watchedChangesetChecks struct {
	patchsetID string
	required   map[string]bool
	loaded     bool
}

type checkProgressWatch struct {
	out  io.Writer
	meta map[string]*watchedChangesetChecks
	seen map[string]map[string]string // changesetID -> checkName -> last status
}

func newCheckProgressWatch(out io.Writer) *checkProgressWatch {
	return &checkProgressWatch{
		out:  out,
		meta: map[string]*watchedChangesetChecks{},
		seen: map[string]map[string]string{},
	}
}

func (w *checkProgressWatch) poll(ctx context.Context, conn *grpc.ClientConn, cfg UserConfig, changesetID, pendingCheck string) error {
	if w == nil || conn == nil || strings.TrimSpace(changesetID) == "" {
		return nil
	}
	callCtx := authContext(ctx, cfg)
	meta := w.meta[changesetID]
	if meta == nil {
		meta = &watchedChangesetChecks{required: map[string]bool{}}
		w.meta[changesetID] = meta
	}
	if !meta.loaded {
		meta.loaded = true
		if cs, err := corev1.NewChangesetServiceClient(conn).GetChangeset(callCtx, &corev1.GetChangesetRequest{ChangesetId: changesetID}); err == nil && cs != nil {
			meta.patchsetID = cs.GetCurrentPatchsetId()
			if req := currentPatchsetSubmitRequirements(cs); req != nil {
				for _, name := range req.GetRequiredChecks() {
					if trimmed := strings.TrimSpace(name); trimmed != "" {
						meta.required[trimmed] = true
					}
				}
			}
		}
	}
	res, err := corev1.NewCheckServiceClient(conn).ListCheckRuns(callCtx, &corev1.ListCheckRunsRequest{
		ChangesetId: changesetID,
		PatchsetId:  meta.patchsetID,
	})
	if err != nil {
		return nil
	}
	return w.observe(changesetID, pendingCheck, meta.required, LatestPatchsetCheckRuns(res.GetRuns(), meta.patchsetID))
}

func (w *checkProgressWatch) observe(changesetID, pendingCheck string, required map[string]bool, runs []*corev1.CheckRun) error {
	if len(runs) == 0 {
		return nil
	}
	seen := w.seen[changesetID]
	if seen == nil {
		seen = map[string]string{}
		w.seen[changesetID] = seen
	}
	var firstFailure *corev1.CheckRun
	for _, run := range runs {
		if run == nil {
			continue
		}
		name := strings.TrimSpace(run.GetCheckName())
		st := strings.ToLower(strings.TrimSpace(run.GetStatus()))
		if name == "" || st == "" {
			continue
		}
		prev := seen[name]
		if st != prev {
			seen[name] = st
			if w.out != nil && !(prev == "" && st == "queued") {
				detail := ""
				if isFailedCheckStatus(st) {
					parts := []string{}
					if id := strings.TrimSpace(run.GetId()); id != "" {
						parts = append(parts, id)
					}
					if summary := strings.TrimSpace(run.GetSummary()); summary != "" {
						parts = append(parts, summary)
					}
					if len(parts) > 0 {
						detail = " (" + strings.Join(parts, "; ") + ")"
					}
				}
				fmt.Fprintf(w.out, "check %s: %s%s\n", name, st, detail)
			}
		}
		if firstFailure == nil && isFailedCheckStatus(st) && (required[name] || name == pendingCheck) {
			firstFailure = run
		}
	}
	if firstFailure != nil {
		name := strings.TrimSpace(firstFailure.GetCheckName())
		st := strings.ToLower(strings.TrimSpace(firstFailure.GetStatus()))
		shortCS := firstNonEmpty(model.ShortChangesetID(changesetID), changesetID)
		hint := fmt.Sprintf("Run gs check rerun %s %s to retry.", shortCS, name)
		if runID := strings.TrimSpace(firstFailure.GetId()); runID != "" {
			hint = fmt.Sprintf("Run gs check logs %s to inspect, or gs check rerun %s %s to retry.", runID, shortCS, name)
		}
		msg := fmt.Sprintf("required check %q %s", name, st)
		if summary := strings.TrimSpace(firstFailure.GetSummary()); summary != "" {
			msg += " (" + summary + ")"
		}
		return userError("required_check_failed", msg, hint)
	}
	return nil
}

func isFailedCheckStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "failed", "errored", "canceled":
		return true
	default:
		return false
	}
}

func (r Runner) waitForChangesetPublished(ctx context.Context, conn *grpc.ClientConn, cfg UserConfig, changesetID, changesetLabel string, timeout time.Duration, progress bool) (string, string, error) {
	cs, refCommitID, err := r.waitForChangesetSubmitted(ctx, conn, cfg, changesetID, changesetLabel, nil, timeout, progress, true)
	if err != nil {
		return "", "", err
	}
	return cs.GetCommitId(), refCommitID, nil
}

func (r Runner) waitForChangesetSubmitted(
	ctx context.Context,
	conn *grpc.ClientConn,
	cfg UserConfig,
	changesetID, changesetLabel string,
	initial *corev1.Changeset,
	timeout time.Duration,
	progress bool,
	fetchRef bool,
) (*corev1.Changeset, string, error) {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	waitCfg := r.resolveChangesetWaitConfig()
	changesetClient := corev1.NewChangesetServiceClient(conn)
	repoClient := corev1.NewRepositoryServiceClient(conn)
	lastStatus := ""
	prevPollStatus := ""
	step := 0
	cs := initial
	for {
		if cs == nil {
			var err error
			cs, err = changesetClient.GetChangeset(authContext(waitCtx, cfg), &corev1.GetChangesetRequest{ChangesetId: changesetID})
			if err != nil {
				if ctx.Err() == nil && (errors.Is(waitCtx.Err(), context.DeadlineExceeded) || grpcstatus.Code(err) == codes.DeadlineExceeded) {
					return nil, "", userError("publish_timeout", "changeset accepted but not published before timeout", "Run gs cs status to check publish progress.")
				}
				return nil, "", err
			}
		}
		if progress && cs.Status != lastStatus {
			fmt.Fprintf(r.stderr(), "changeset %s status: %s\n", firstNonEmpty(changesetLabel, displayChangesetID(cs), changesetID), cs.Status)
			lastStatus = cs.Status
		}
		if cs.Status == "submitted" && cs.CommitId != "" {
			if !fetchRef {
				return cs, "", nil
			}
			ref, err := repoClient.GetRef(authContext(waitCtx, cfg), &corev1.GetRefRequest{RefName: model.DefaultTargetRef})
			if err != nil {
				return nil, "", err
			}
			return cs, ref.CommitId, nil
		}
		if cs.Status != "pending_publish" && cs.Status != "draft" {
			return nil, "", userError("publish_failed", "changeset publish failed with status "+cs.Status, "Run gs cs status for details.")
		}
		if prevPollStatus != "" && cs.Status != prevPollStatus {
			step = 0
		}
		prevPollStatus = cs.Status
		delay := waitCfg.publishPollDelay(step, cs.Status)
		step++
		cs = nil
		if err := waitCfg.sleep(waitCtx, delay); err != nil {
			if errors.Is(ctx.Err(), context.Canceled) {
				return nil, "", ctx.Err()
			}
			return nil, "", userError("publish_timeout", "changeset accepted but not published before timeout", "Run gs cs status to check publish progress.")
		}
	}
}
