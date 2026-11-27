package events

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/activity"
	"gitslice.io/gitslice/internal/storage"
)

const (
	inlineWait    = 2 * time.Second
	inlineDrains  = 4
	eventLease    = time.Minute
	batchSize     = 50
	maxBatches    = 10
	pollInterval  = 30 * time.Second
	pruneInterval = time.Hour
	historyKeep   = 30 * 24 * time.Hour
	drainWindow   = 30 * time.Second
)

// Stores are what the dispatcher reads to fan out and describe events.
type Stores struct {
	Events     storage.EventStore
	Slices     storage.SliceStore
	Changesets storage.ChangesetStore
	Checks     storage.CheckStore
	Auth       storage.AuthStore
}

// Options configure a Dispatcher.
type Options struct {
	// WebBaseURL is the web app's origin, for links in trigger payloads.
	WebBaseURL string
	// Triggers, when set, turns events into trigger runs too.
	Automations Automations
	// Mirrors, when set, queues and pushes slice Git mirrors.
	Mirrors Mirrors
	// Gate pauses timer-driven polls while the server is idle.
	// Defaults to activity.Default when nil.
	Gate *activity.Gate
}

// Mirrors queues Git mirror pushes for recorded events and runs due ones.
type Mirrors interface {
	FanOut(ctx context.Context, event storage.Event)
	Drain(ctx context.Context)
}

// Triggers turns recorded events into trigger runs and runs them.
type Automations interface {
	// MayHaveAutomations says whether events are worth resolving at once.
	MayHaveAutomations(ctx context.Context) bool
	// FanOut creates the event's trigger runs using pre-loaded entities and the
	// marshaled event envelope. It must be safe to repeat.
	FanOut(ctx context.Context, event storage.Event, entities storage.EventEntities, envelope []byte)
	// Drain sends and starts due runs and settles expired ones.
	Drain(ctx context.Context)
}

// Dispatcher fans recorded events out to triggers and Git mirrors.
type Dispatcher struct {
	stores Stores
	opts   Options
	nudge  chan struct{}
	inline chan struct{}
}

// NewDispatcher returns a dispatcher; call Run to fan out in the background.
func NewDispatcher(stores Stores, opts Options) *Dispatcher {
	opts.WebBaseURL = strings.TrimRight(opts.WebBaseURL, "/")
	return &Dispatcher{
		stores: stores,
		opts:   opts,
		nudge:  make(chan struct{}, 1),
		inline: make(chan struct{}, inlineDrains),
	}
}

// SetConsumers attaches the automation engine and Git mirror runner to the
// dispatcher during server wiring, before Run starts. This lets callers build
// event-wrapped stores (such as CheckStore) before constructing consumers that
// write through those wrapped stores, without circular proxy shims.
func (d *Dispatcher) SetConsumers(automations Automations, mirrors Mirrors) {
	d.opts.Automations = automations
	d.opts.Mirrors = mirrors
}

// Emit records an event and fans it out (see Flush).
func (d *Dispatcher) Emit(ctx context.Context, event storage.Event) {
	if _, err := d.stores.Events.AppendEvent(context.WithoutCancel(ctx), event); err != nil {
		slog.WarnContext(ctx, "event not recorded", "event", event.Kind, "changeset", event.ChangesetID, "error", err)
		return
	}
	d.Flush(ctx)
}

// Flush fans out recorded events now, waiting up to two seconds so
// request-driven hosts make progress before their CPU is throttled, when a
// trigger is likely to run. Otherwise it only nudges the background loop, so a
// request that records an event doesn't wait for mirrors.
func (d *Dispatcher) Flush(ctx context.Context) {
	if d.opts.Automations == nil && d.opts.Mirrors == nil {
		return
	}
	if d.opts.Automations == nil || !d.opts.Automations.MayHaveAutomations(ctx) {
		d.Nudge()
		return
	}
	select {
	case d.inline <- struct{}{}:
	default:
		d.Nudge()
		return
	}
	done := make(chan struct{})
	go func() {
		defer func() {
			<-d.inline
			close(done)
		}()
		drainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), drainWindow)
		defer cancel()
		d.drainLogged(drainCtx)
	}()
	timer := time.NewTimer(inlineWait)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	case <-ctx.Done():
	}
}

// Nudge asks the background loop to drain now.
func (d *Dispatcher) Nudge() {
	select {
	case d.nudge <- struct{}{}:
	default:
	}
}

// Run drains until ctx ends: on a nudge, every 30 seconds for trigger and
// mirror retries, and prunes completed events every hour.
func (d *Dispatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	prune := time.NewTicker(pruneInterval)
	defer prune.Stop()
	if !d.waitActiveOrNudge(ctx) {
		return
	}
	d.drainLogged(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-prune.C:
			if d.gate().Wait(ctx) != nil {
				return
			}
			if err := d.stores.Events.PruneEvents(ctx, time.Now().Add(-historyKeep)); err != nil && ctx.Err() == nil {
				slog.Warn("prune events", "error", err)
			}
			continue
		case <-ticker.C:
			// Retries wait for the server to be in use (internal/activity): an
			// idle server leaves the database alone.
			if d.gate().Wait(ctx) != nil {
				return
			}
		case <-d.nudge:
		}
		d.drainLogged(ctx)
	}
}

func (d *Dispatcher) gate() *activity.Gate {
	if d.opts.Gate != nil {
		return d.opts.Gate
	}
	return activity.Default
}

func (d *Dispatcher) waitActiveOrNudge(ctx context.Context) bool {
	waitCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	active := make(chan error, 1)
	go func() {
		active <- d.gate().Wait(waitCtx)
	}()
	select {
	case <-ctx.Done():
		cancel()
		<-active
		return false
	case <-d.nudge:
		cancel()
		<-active
		return true
	case err := <-active:
		return err == nil
	}
}

func (d *Dispatcher) drainLogged(ctx context.Context) {
	if err := d.DrainOnce(ctx); err != nil && ctx.Err() == nil {
		slog.Warn("event drain", "error", err)
	}
}

// DrainOnce fans out recorded events and drains their consumers.
func (d *Dispatcher) DrainOnce(ctx context.Context) error {
	for range maxBatches {
		events, err := d.stores.Events.ClaimEvents(ctx, batchSize, eventLease)
		if err != nil {
			return fmt.Errorf("claim events: %w", err)
		}
		if len(events) == 0 {
			break
		}
		for _, event := range events {
			if d.opts.Mirrors != nil {
				d.opts.Mirrors.FanOut(ctx, event)
			}
			if d.opts.Automations != nil {
				ec, err := d.loadContext(ctx, event)
				if err != nil {
					slog.Warn("event context", "event", event.ID, "kind", event.Kind, "error", err)
				} else if envelope, err := json.Marshal(d.payload(event, ec)); err != nil {
					slog.Warn("marshal event payload", "event", event.ID, "error", err)
				} else {
					d.opts.Automations.FanOut(ctx, event, ec.entities(), envelope)
				}
			}
			if err := d.stores.Events.CompleteEvent(ctx, event.ID); err != nil {
				slog.Warn("complete event", "event", event.ID, "error", err)
			}
		}
		if len(events) < batchSize {
			break
		}
	}
	if d.opts.Automations != nil {
		d.opts.Automations.Drain(ctx)
	}
	if d.opts.Mirrors != nil {
		d.opts.Mirrors.Drain(ctx)
	}
	return nil
}
