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
)

// Doorbell signs tokens for daemons and rings the doorbell Worker.
type Doorbell = doorbell.Client

// NewDoorbell returns a doorbell for the Worker at baseURL (https), or nil
// without a URL and secret.
func NewDoorbell(baseURL, secret string) (*Doorbell, error) {
	return doorbell.New(baseURL, secret)
}

// ringDaemon rings for the daemon in the background, trying a few times. A
// daemon nobody hears loses its standby, so it counts as offline and its
// queued runs can move to another daemon of the pool.
func (s *AgentService) ringDaemon(ctx context.Context, daemonID string, queued int) {
	if s.doorbell == nil || daemonID == "" {
		return
	}
	ctx = context.WithoutCancel(ctx)
	go func() {
		var err error
		for attempt, wait := range []time.Duration{0, time.Second, 4 * time.Second} {
			if wait > 0 {
				time.Sleep(wait)
			}
			var listening int
			if listening, err = s.doorbell.Ring(ctx, daemonID, queued); err == nil {
				if listening == 0 && len(s.hub.conns(daemonID)) == 0 && !s.dispatcher.onDemand[daemonID] {
					_ = s.Agents.SetDaemonStandby(ctx, daemonID, time.Time{})
				}
				return
			}
			slog.Debug("doorbell ring failed", "daemon_id", daemonID, "attempt", attempt+1, "error", err)
		}
		slog.Warn("could not ring the doorbell; the daemon finds its runs at its next poll", "daemon_id", daemonID, "error", err)
	}()
}

// SetCIRunners names the daemons whose processes the doorbell starts on
// demand (Cloudflare runners, design/32 layer 2).
func (h *Handlers) SetCIRunners(daemonIDs []string) {
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

// SetDoorbell gives the server a doorbell (nil for none).
func (h *Handlers) SetDoorbell(bell *Doorbell) {
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
