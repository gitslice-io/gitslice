package cli

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/agentd"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
)

const (
	defaultDetachAfter = agentd.DefaultDetachAfter
	detachCheckEvery   = agentd.DetachCheckEvery
	standbyPollDefault = agentd.StandbyPollDefault
	standbyPollMin     = agentd.StandbyPollMin
	standbyPollMax     = agentd.StandbyPollMax
	standbyCallTimeout = agentd.StandbyCallTimeout
)

var (
	errAgentDetached     = agentd.ErrAgentDetached
	standbyRetryWait     = agentd.StandbyRetryWait
	standbyPollOverride  = agentd.StandbyPollOverride
	isStandbyUnsupported = agentd.IsStandbyUnsupported
	detachDaemonMessage  = agentd.DetachDaemonMessage
)

func (d *agentDaemon) checksIdle() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.checkRuns) == 0 && len(d.checkRunOutboxes) == 0
}

func (d *agentDaemon) runDetachWatch(ctx context.Context) {
	every := detachCheckEvery
	if d.detachAfter < every*2 {
		every = max(d.detachAfter/2, time.Millisecond)
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	idleSince := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if !d.checksIdle() {
				idleSince = now
				continue
			}
			if now.Sub(idleSince) < d.detachAfter {
				continue
			}
			switch d.tryDetach(ctx) {
			case detachDone, detachUnsupported:
				return
			default:
				idleSince = now
			}
		}
	}
}

type detachResult int

const (
	detachDone detachResult = iota
	detachLater
	detachUnsupported
)

func (d *agentDaemon) tryDetach(ctx context.Context) detachResult {
	d.doorbellOpen.Store(false)
	resp, err := d.standbyPollRPC(ctx)
	switch {
	case isStandbyUnsupported(err):
		d.standbyUnsupported.Store(true)
		d.say("the server does not take detached checks daemons; staying connected")
		return detachUnsupported
	case err != nil:
		return detachLater
	case resp.GetPending():
		return detachLater
	}
	d.detaching.Store(true)
	if err := d.sendDaemonMessage(ctx, detachDaemonMessage()); err != nil {
		d.detaching.Store(false)
		return detachLater
	}
	return detachDone
}

func (d *agentDaemon) standby(ctx context.Context) error {
	d.say(fmt.Sprintf("idle for %s: detached from the server; polling for queued checks (it connects again when one is queued)", d.detachAfter))
	failures := 0
	var bell *doorbellListener
	defer func() { bell.Stop() }()
	for {
		d.doorbellOpen.Store(bell.IsConnected())
		resp, err := d.standbyPollRPC(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		wait := standbyPollDefault
		switch {
		case isStandbyUnsupported(err):
			d.standbyUnsupported.Store(true)
			d.say("the server does not take detached checks daemons: connecting")
			return nil
		case err != nil:
			failures++
			d.closePollConnection()
			wait = standbyRetryWait(failures)
			d.say(fmt.Sprintf("standby poll failed: %v; trying again in %s", err, wait.Round(time.Second)))
		case resp.GetPending():
			d.say("a check is queued: connecting")
			return nil
		default:
			failures = 0
			if resp.GetPollAfterMs() > 0 {
				wait = time.Duration(resp.GetPollAfterMs()) * time.Millisecond
			}
			wait = min(max(wait, standbyPollMin), standbyPollMax)
			if url, token := resp.GetDoorbellUrl(), resp.GetDoorbellToken(); url != "" && token != "" && !doorbellDisabled() {
				requireTLS := serverUsesNonLoopbackTLS(d.cfg.ServerAddr)
				if bell == nil {
					bell = listenDoorbellWithTLS(ctx, url, token, requireTLS, d.say)
					bell.WaitReady(ctx, doorbellInitialReadyWait)
				} else {
					bell.UpdateWithTLS(url, token, requireTLS)
				}
				d.doorbellOpen.Store(bell.IsConnected())
				if bell.IsConnected() && resp.GetSafetyPollAfterMs() > 0 {
					wait = time.Duration(resp.GetSafetyPollAfterMs()) * time.Millisecond
				}
			}
		}
		if override, ok := standbyPollOverride(); ok && !bell.IsConnected() {
			wait = override
			if failures > 1 {
				wait = min(override<<min(failures-1, 8), standbyPollMax)
			}
		}
		wait += time.Duration(rand.Int63n(int64(wait/10) + 1))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		case <-bell.Woken():
			d.say("the doorbell rang: polling")
		case <-bell.Dropped():
		}
	}
}

func (d *agentDaemon) standbyPollRPC(ctx context.Context) (*corev1.StandbyPollResponse, error) {
	if d.standbyPollFn != nil {
		return d.standbyPollFn(ctx)
	}
	conn, err := d.pollConnection()
	if err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithTimeout(authContext(ctx, d.cfg), standbyCallTimeout)
	defer cancel()
	version := cliVersionInfo().Version
	if strings.TrimSpace(version) == "" {
		version = "dev"
	}
	return corev1.NewAgentServiceClient(conn).StandbyPoll(callCtx, &corev1.StandbyPollRequest{
		Name:              resolvedAgentName(d.name),
		Runtime:           ciAgentRuntime,
		Version:           version,
		ContainerRuntimes: detectContainerRuntimes(),
		AllowHostExec:     !requireContainerChecks(),
		InstanceId:        d.instanceID,
		DoorbellOpen:      d.doorbellOpen.Load(),
	})
}

func (d *agentDaemon) pollConnection() (*grpc.ClientConn, error) {
	d.pollMu.Lock()
	defer d.pollMu.Unlock()
	if d.pollConn != nil {
		return d.pollConn, nil
	}
	conn, err := dialLazy(d.cfg.ServerAddr, d.runner.Insecure)
	if err != nil {
		return nil, err
	}
	d.pollConn = conn
	return conn, nil
}

func (d *agentDaemon) closePollConnection() {
	d.pollMu.Lock()
	defer d.pollMu.Unlock()
	if d.pollConn != nil {
		_ = d.pollConn.Close()
		d.pollConn = nil
	}
}

func (d *agentDaemon) say(line string) {
	if d.quiet || d.runner.stderr() == nil {
		return
	}
	fmt.Fprintln(d.runner.stderr(), line)
}

func detachAfterFor(in agentStartOptions) (time.Duration, error) {
	if in.DetachSet {
		if in.DetachAfter < 0 {
			return 0, userError("invalid_args", "--detach-after must not be negative", "Use 0 to stay connected.")
		}
		if in.DetachAfter > 0 && !in.CI {
			return 0, userError("invalid_args", "--detach-after works with --ci only", "Only a checks-only daemon can detach; an agent daemon holds its stream for its conversations.")
		}
		return in.DetachAfter, nil
	}
	if !in.CI {
		return 0, nil
	}
	if value := strings.TrimSpace(os.Getenv("GITSLICE_AGENT_DETACH_AFTER")); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed < 0 {
			return 0, userError("invalid_args", "GITSLICE_AGENT_DETACH_AFTER is not a duration: "+value, "Use a value like 2m, or 0 to stay connected.")
		}
		return parsed, nil
	}
	return defaultDetachAfter, nil
}
