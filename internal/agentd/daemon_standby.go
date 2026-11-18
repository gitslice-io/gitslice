package agentd

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/checkexec"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
)

type DetachResult int

const (
	DetachDone DetachResult = iota
	DetachLater
	DetachUnsupported
)

func (d *Daemon) ChecksIdle() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.checkRuns) == 0 && len(d.checkRunOutboxes) == 0
}

func (d *Daemon) runDetachWatch(ctx context.Context) {
	every := DetachCheckEvery
	if d.DetachAfter < every*2 {
		every = max(d.DetachAfter/2, time.Millisecond)
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	idleSince := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if !d.ChecksIdle() {
				idleSince = now
				continue
			}
			if now.Sub(idleSince) < d.DetachAfter {
				continue
			}
			switch d.TryDetach(ctx) {
			case DetachDone, DetachUnsupported:
				return
			default:
				idleSince = now
			}
		}
	}
}

func (d *Daemon) TryDetach(ctx context.Context) DetachResult {
	d.DoorbellOpen.Store(false)
	resp, err := d.standbyPollRPC(ctx)
	switch {
	case IsStandbyUnsupported(err):
		d.StandbyUnsupported.Store(true)
		d.say("the server does not take detached checks daemons; staying connected")
		return DetachUnsupported
	case err != nil:
		return DetachLater
	case resp.GetPending():
		return DetachLater
	}
	d.Detaching.Store(true)
	if err := d.sendDaemonMessage(ctx, DetachDaemonMessage()); err != nil {
		d.Detaching.Store(false)
		return DetachLater
	}
	return DetachDone
}

func (d *Daemon) Standby(ctx context.Context) error {
	d.say(fmt.Sprintf("idle for %s: detached from the server; polling for queued checks (it connects again when one is queued)", d.DetachAfter))
	failures := 0
	var bell *DoorbellListener
	defer func() { bell.Stop() }()
	for {
		d.DoorbellOpen.Store(bell.IsConnected())
		resp, err := d.standbyPollRPC(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		wait := StandbyPollDefault
		switch {
		case IsStandbyUnsupported(err):
			d.StandbyUnsupported.Store(true)
			d.say("the server does not take detached checks daemons: connecting")
			return nil
		case err != nil:
			failures++
			d.ClosePollConnection()
			wait = StandbyRetryWait(failures)
			d.say(fmt.Sprintf("standby poll failed: %v; trying again in %s", err, wait.Round(time.Second)))
		case resp.GetPending():
			d.say("a check is queued: connecting")
			return nil
		default:
			failures = 0
			if resp.GetPollAfterMs() > 0 {
				wait = time.Duration(resp.GetPollAfterMs()) * time.Millisecond
			}
			wait = min(max(wait, StandbyPollMin), StandbyPollMax)
			if url, token := resp.GetDoorbellUrl(), resp.GetDoorbellToken(); url != "" && token != "" && !DoorbellDisabled() {
				requireTLS := d.Host.usesNonLoopbackTLS(d.Host.ServerAddr)
				if bell == nil {
					bell = ListenDoorbellWithTLS(ctx, url, token, requireTLS, d.Host.insecureFromContext, d.say)
					bell.WaitReady(ctx, DoorbellInitialReadyWait)
				} else {
					bell.UpdateWithTLS(url, token, requireTLS)
				}
				d.DoorbellOpen.Store(bell.IsConnected())
				if bell.IsConnected() && resp.GetSafetyPollAfterMs() > 0 {
					wait = time.Duration(resp.GetSafetyPollAfterMs()) * time.Millisecond
				}
			}
		}
		if override, ok := StandbyPollOverride(); ok && !bell.IsConnected() {
			wait = override
			if failures > 1 {
				wait = min(override<<min(failures-1, 8), StandbyPollMax)
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

func (d *Daemon) standbyPollRPC(ctx context.Context) (*corev1.StandbyPollResponse, error) {
	if d.StandbyPollFn != nil {
		return d.StandbyPollFn(ctx)
	}
	conn, err := d.pollConnection()
	if err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithTimeout(d.Host.authContext(ctx), StandbyCallTimeout)
	defer cancel()
	version := strings.TrimSpace(d.Host.Version)
	if version == "" {
		version = "dev"
	}
	return corev1.NewAgentServiceClient(conn).StandbyPoll(callCtx, &corev1.StandbyPollRequest{
		Name:              ResolvedAgentName(d.Name),
		Runtime:           CIRuntimeName,
		Version:           version,
		ContainerRuntimes: DetectContainerRuntimes(),
		AllowHostExec:     !checkexec.RequireContainerChecks(),
		InstanceId:        d.InstanceID,
		DoorbellOpen:      d.DoorbellOpen.Load(),
	})
}

func (d *Daemon) pollConnection() (*grpc.ClientConn, error) {
	d.pollMu.Lock()
	defer d.pollMu.Unlock()
	if d.pollConn != nil {
		return d.pollConn, nil
	}
	conn, err := d.Host.dialLazy(d.Host.ServerAddr)
	if err != nil {
		return nil, err
	}
	d.pollConn = conn
	return conn, nil
}

func (d *Daemon) ClosePollConnection() {
	d.pollMu.Lock()
	defer d.pollMu.Unlock()
	if d.pollConn != nil {
		_ = d.pollConn.Close()
		d.pollConn = nil
	}
}

func (d *Daemon) say(line string) {
	if d.Quiet || d.Host.Stderr == nil {
		return
	}
	fmt.Fprintln(d.Host.Stderr, line)
}

func DetachAfterFor(ci, detachSet bool, detachAfter time.Duration) (time.Duration, error) {
	if detachSet {
		if detachAfter < 0 {
			return 0, NewError("invalid_args", "--detach-after must not be negative", "Use 0 to stay connected.")
		}
		if detachAfter > 0 && !ci {
			return 0, NewError("invalid_args", "--detach-after works with --ci only", "Only a checks-only daemon can detach; an agent daemon holds its stream for its conversations.")
		}
		return detachAfter, nil
	}
	if !ci {
		return 0, nil
	}
	if value := strings.TrimSpace(os.Getenv("GITSLICE_AGENT_DETACH_AFTER")); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed < 0 {
			return 0, NewError("invalid_args", "GITSLICE_AGENT_DETACH_AFTER is not a duration: "+value, "Use a value like 2m, or 0 to stay connected.")
		}
		return parsed, nil
	}
	return DefaultDetachAfter, nil
}
