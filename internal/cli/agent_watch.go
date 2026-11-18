package cli

import (
	"context"
	"fmt"
	"time"

	"gitslice.io/gitslice/internal/agentd"
)

type agentInboundState = agentd.InboundState

var (
	newAgentInboundState = agentd.NewInboundState
	inboundStale         = agentd.InboundStale
)

func (d *agentDaemon) markActivity() {
	d.idleMu.Lock()
	d.lastActivity = time.Now()
	d.idleMu.Unlock()
}

func (d *agentDaemon) beginWork() {
	d.idleMu.Lock()
	d.activeWork++
	d.lastActivity = time.Now()
	d.idleMu.Unlock()
}

func (d *agentDaemon) endWork() {
	d.idleMu.Lock()
	if d.activeWork > 0 {
		d.activeWork--
	}
	d.lastActivity = time.Now()
	d.idleMu.Unlock()
}

func (d *agentDaemon) isIdle(now time.Time, timeout time.Duration) bool {
	d.idleMu.Lock()
	defer d.idleMu.Unlock()
	return timeout > 0 && d.activeWork == 0 && !d.lastActivity.IsZero() && now.Sub(d.lastActivity) > timeout
}

func (d *agentDaemon) requestIdleShutdown() {
	d.idleMu.Lock()
	d.idleShutdown = true
	d.idleMu.Unlock()
}

func (d *agentDaemon) idleShutdownRequested() bool {
	d.idleMu.Lock()
	defer d.idleMu.Unlock()
	return d.idleShutdown
}

func (d *agentDaemon) runHeartbeat(ctx context.Context, cancel context.CancelFunc) {
	ticker := time.NewTicker(agentHeartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := d.sendDaemonMessage(ctx, heartbeatDaemonMessage()); err != nil {
				cancel()
				return
			}
		}
	}
}

func (d *agentDaemon) runInboundWatchdog(ctx context.Context, cancel context.CancelFunc, inbound *agentInboundState) {
	if inbound == nil {
		return
	}
	ticker := time.NewTicker(agentInboundStaleCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if ctx.Err() != nil {
				return
			}
			last := inbound.LastSeen()
			now := time.Now()
			if inboundStale(last, now, agentInboundStaleTimeout) {
				fmt.Fprintf(d.runner.stderr(), "no server message in %s; assuming half-open stream, reconnecting\n", now.Sub(last).Round(time.Second))
				cancel()
				return
			}
		}
	}
}

func (d *agentDaemon) runIdleMonitor(ctx context.Context, cancel context.CancelFunc, timeout time.Duration) {
	if timeout <= 0 {
		return
	}
	interval := timeout
	if interval > time.Minute {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if ctx.Err() != nil {
				return
			}
			if d.isIdle(time.Now(), timeout) {
				fmt.Fprintf(d.runner.stderr(), "no conversation activity for %s; disconnecting and exiting (run 'gs agent start' to resume)\n", timeout)
				d.requestIdleShutdown()
				cancel()
				return
			}
		}
	}
}
