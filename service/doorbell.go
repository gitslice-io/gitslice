package service

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/doorbell"
)

const (
	doorbellSafetyPoll   = doorbell.SafetyPoll
	doorbellStandbyGrace = doorbell.StandbyGrace
	doorbellTokenLife    = doorbell.TokenLife
	doorbellRingTimeout  = 30 * time.Second
)

// ringDaemon rings for the daemon in the background, trying a few times. A
// daemon nobody hears loses its standby, so it counts as offline and its
// queued runs can move to another daemon of the pool.
func (s *AgentService) ringDaemon(ctx context.Context, daemonID string, queued int) {
	if s.doorbell == nil || daemonID == "" {
		return
	}
	ringCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), doorbellRingTimeout)
	go func() {
		defer cancel()
		var err error
		for attempt, wait := range []time.Duration{0, time.Second, 4 * time.Second} {
			if wait > 0 {
				timer := time.NewTimer(wait)
				select {
				case <-ringCtx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
			var listening int
			if listening, err = s.doorbell.Ring(ringCtx, daemonID, queued); err == nil {
				if listening == 0 && len(s.hub.Conns(daemonID)) == 0 && !s.dispatcher.onDemand[daemonID] {
					_ = s.Agents.SetDaemonStandby(ringCtx, daemonID, time.Time{})
				}
				return
			}
			slog.Debug("doorbell ring failed", "daemon_id", daemonID, "attempt", attempt+1, "error", err)
		}
		slog.Warn("could not ring the doorbell; the daemon finds its runs at its next poll", "daemon_id", daemonID, "error", err)
	}()
}

// setCIRunners names the daemons whose processes the doorbell starts on
// demand (Cloudflare runners, design/32 layer 2).
func (h *Handlers) setCIRunners(daemonIDs []string) {
	if h.Agent.dispatcher == nil {
		return
	}
	h.Agent.dispatcher.onDemand = map[string]bool{}
	for _, id := range daemonIDs {
		if id = strings.TrimSpace(id); id != "" {
			h.Agent.dispatcher.onDemand[id] = true
		}
	}
}

// setDoorbell gives the server a doorbell (nil for none).
func (h *Handlers) setDoorbell(bell *doorbell.Client) {
	h.Agent.doorbell = bell
	if h.Agent.dispatcher == nil {
		return
	}
	if bell == nil {
		h.Agent.dispatcher.wake = nil
		return
	}
	h.Agent.dispatcher.wake = h.Agent.ringDaemon
}
