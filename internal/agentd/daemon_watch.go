package agentd

import (
	"context"
	"fmt"
	"time"
)

func (d *Daemon) markActivity() {
	d.idleMu.Lock()
	d.lastActivity = time.Now()
	d.idleMu.Unlock()
}

func (d *Daemon) beginWork() {
	d.idleMu.Lock()
	d.activeWork++
	d.lastActivity = time.Now()
	d.idleMu.Unlock()
}

func (d *Daemon) endWork() {
	d.idleMu.Lock()
	if d.activeWork > 0 {
		d.activeWork--
	}
	d.lastActivity = time.Now()
	d.idleMu.Unlock()
}

func (d *Daemon) isIdle(now time.Time, timeout time.Duration) bool {
	d.idleMu.Lock()
	defer d.idleMu.Unlock()
	return timeout > 0 && d.activeWork == 0 && !d.lastActivity.IsZero() && now.Sub(d.lastActivity) > timeout
}

func (d *Daemon) requestIdleShutdown() {
	d.idleMu.Lock()
	d.idleShutdown = true
	d.idleMu.Unlock()
}

func (d *Daemon) IdleShutdownRequested() bool {
	d.idleMu.Lock()
	defer d.idleMu.Unlock()
	return d.idleShutdown
}

func (d *Daemon) runHeartbeat(ctx context.Context, cancel context.CancelFunc) {
	ticker := time.NewTicker(HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := d.sendDaemonMessage(ctx, HeartbeatDaemonMessage()); err != nil {
				cancel()
				return
			}
		}
	}
}

func (d *Daemon) runInboundWatchdog(ctx context.Context, cancel context.CancelFunc, inbound *InboundState) {
	if inbound == nil {
		return
	}
	ticker := time.NewTicker(InboundStaleCheckEvery)
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
			if InboundStale(last, now, InboundStaleTimeout) {
				fmt.Fprintf(d.Host.stderr(), "no server message in %s; assuming half-open stream, reconnecting\n", now.Sub(last).Round(time.Second))
				cancel()
				return
			}
		}
	}
}

func (d *Daemon) runIdleMonitor(ctx context.Context, cancel context.CancelFunc, timeout time.Duration) {
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
				fmt.Fprintf(d.Host.stderr(), "no conversation activity for %s; disconnecting and exiting (run 'gs agent start' to resume)\n", timeout)
				d.requestIdleShutdown()
				cancel()
				return
			}
		}
	}
}
