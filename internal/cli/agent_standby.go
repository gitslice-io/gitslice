package cli

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"time"

	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

// A checks-only daemon holds a stream open to the server all day, and a
// server that bills while it handles requests (Cloud Run) then bills all day
// for an idle daemon. So once nothing has run for a while, the daemon
// detaches: it tells the server, closes the stream, and polls with a short
// request every half minute until the server says a check is queued. Then it
// connects again, and the server delivers the queued runs as it always has.
// design/31_ci_daemon.md.
const (
	// defaultDetachAfter is how long a checks-only daemon stays connected
	// with nothing to do before it detaches.
	defaultDetachAfter = 2 * time.Minute
	// detachCheckEvery is how often the daemon looks whether it is idle.
	detachCheckEvery = 5 * time.Second
	// standbyPollDefault is the poll interval when the server gives none.
	standbyPollDefault = 30 * time.Second
	standbyPollMin     = 5 * time.Second
	standbyPollMax     = 5 * time.Minute
	standbyCallTimeout = 20 * time.Second
)

// errAgentDetached is the end of a connection the daemon closed on purpose.
var errAgentDetached = errors.New("agent daemon detached")

// checksIdle reports whether no check is running and none has updates the
// server has not acknowledged yet.
func (d *agentDaemon) checksIdle() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.checkRuns) == 0 && len(d.checkRunOutboxes) == 0
}

// runDetachWatch detaches the daemon after it has been idle for detachAfter.
// It returns when it detaches, when the server cannot take a detached daemon,
// or when the connection ends.
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
				idleSince = now // try again after another quiet period
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

// tryDetach asks the server whether it can take a detached daemon (one
// StandbyPoll, while still connected), then detaches. A server that predates
// StandbyPoll, or one that says no, leaves the daemon connected for good.
func (d *agentDaemon) tryDetach(ctx context.Context) detachResult {
	resp, err := d.standbyPollRPC(ctx)
	switch {
	case isStandbyUnsupported(err):
		d.standbyUnsupported.Store(true)
		d.say("the server does not take detached checks daemons; staying connected")
		return detachUnsupported
	case err != nil:
		return detachLater
	case resp.GetPending():
		return detachLater // work is on its way over the stream
	}
	d.detaching.Store(true)
	if err := d.sendDaemonMessage(ctx, detachDaemonMessage()); err != nil {
		d.detaching.Store(false)
		return detachLater
	}
	return detachDone
}

// standby waits, detached, until the server says check runs are queued. It
// returns nil to reconnect, and an error only when ctx ends. A poll that fails
// (a timeout on a network that drops idle connections, a server restarting)
// is only retried, on a fresh connection and with a growing wait: the stream
// is opened when the server reports work, never because a poll failed.
func (d *agentDaemon) standby(ctx context.Context) error {
	d.say(fmt.Sprintf("idle for %s: detached from the server; polling for queued checks (it connects again when one is queued)", d.detachAfter))
	failures := 0
	for {
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
			// The connection may be dead without saying so; the next poll
			// dials again.
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
		}
		// GITSLICE_AGENT_POLL_INTERVAL sets the interval outright, and the
		// first wait after a failure; later failures back off from it.
		if override, ok := standbyPollOverride(); ok {
			wait = override
			if failures > 1 {
				wait = min(override<<min(failures-1, 8), standbyPollMax)
			}
		}
		// Daemons that restart together should not poll in step.
		wait += time.Duration(rand.Int63n(int64(wait/10) + 1))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

// standbyRetryWait is how long to wait before polling again after failures
// polls in a row failed: the usual interval, doubling, up to standbyPollMax.
func standbyRetryWait(failures int) time.Duration {
	return min(standbyPollDefault<<min(max(failures-1, 0), 8), standbyPollMax)
}

// standbyPollOverride is GITSLICE_AGENT_POLL_INTERVAL, when it is a duration.
func standbyPollOverride() (time.Duration, bool) {
	value := strings.TrimSpace(os.Getenv("GITSLICE_AGENT_POLL_INTERVAL"))
	if value == "" {
		return 0, false
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed < 10*time.Millisecond {
		return 0, false
	}
	return parsed, true
}

func isStandbyUnsupported(err error) bool {
	switch grpcstatus.Code(err) {
	case codes.Unimplemented, codes.FailedPrecondition:
		return true
	}
	return false
}

// standbyPollRPC makes one StandbyPoll call.
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
		AllowHostExec:     true,
	})
}

// pollConnection is the connection standby polls share: made without waiting
// for the server, so a poll made while it is unreachable fails fast.
func (d *agentDaemon) pollConnection() (*grpc.ClientConn, error) {
	d.pollMu.Lock()
	defer d.pollMu.Unlock()
	if d.pollConn != nil {
		return d.pollConn, nil
	}
	conn, err := dialLazy(d.cfg.ServerAddr)
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

// say prints a status line to stderr unless the daemon runs quietly.
func (d *agentDaemon) say(line string) {
	if d.quiet || d.runner.stderr() == nil {
		return
	}
	fmt.Fprintln(d.runner.stderr(), line)
}

func detachDaemonMessage() *corev1.DaemonMessage {
	return &corev1.DaemonMessage{Payload: &corev1.DaemonMessage_Detach{Detach: &corev1.Detach{}}}
}

// detachAfterFor is how long a daemon stays connected, idle, before it
// detaches: the flag if given, else GITSLICE_AGENT_DETACH_AFTER, else
// defaultDetachAfter for a checks-only daemon. 0 never detaches.
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
