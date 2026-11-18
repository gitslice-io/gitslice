package cli

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"gitslice.io/gitslice/internal/postgres"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

// pendingCheckPattern matches a submit blocked only because a required check
// has no result yet (storage.EvaluateSubmitRequirements).
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
// check with no result yet, waits up to timeout and tries again.
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
	if !opts.Quiet && !opts.jsonOutput() {
		daemons = newCheckDaemonWatch(r.stderr())
	}
	for {
		check, checkChangeset, err := attempt(time.Until(deadline))
		if err != nil {
			return err
		}
		remaining := time.Until(deadline)
		if check == "" || remaining <= 0 {
			return nil
		}
		if check != waitingFor && !opts.Quiet && !opts.jsonOutput() {
			fmt.Fprintf(r.stderr(), "waiting for required check %q to pass...\n", check)
		}
		daemons.look(ctx, conn, cfg, checkChangeset, check, check != waitingFor)
		waitingFor = check
		if err := waitCfg.sleep(ctx, min(waitCfg.checkRetryDelay, remaining)); err != nil {
			return err
		}
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
			ref, err := repoClient.GetRef(authContext(waitCtx, cfg), &corev1.GetRefRequest{RefName: postgres.DefaultTargetRef})
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
