// Package bgloop implements a nudged exponential-backoff background polling
// runner that obeys the server's activity gate ("idle when unused") and
// recovers panics in worker callbacks.
package bgloop

import (
	"context"
	"errors"
	"log/slog"
	"runtime/debug"
	"sync/atomic"
	"time"

	"gitslice.io/gitslice/internal/activity"
)

const (
	// DefaultBaseInterval is the default base poll interval when BaseInterval <= 0.
	DefaultBaseInterval = 25 * time.Millisecond
	// DefaultMaxInterval is the default maximum backoff cap when MaxInterval <= 0.
	DefaultMaxInterval = 60 * time.Second
	// DefaultErrorRetryInterval is the default retry delay after a step error.
	DefaultErrorRetryInterval = time.Second
	// DefaultDepthSampleInterval is the default cadence for queue depth sampling.
	DefaultDepthSampleInterval = 30 * time.Second
)

var errStepPanic = errors.New("bgloop step panicked")

// Outcome classifies the result of a polling step for backoff calculation.
type Outcome uint8

const (
	OutcomeEmpty Outcome = iota
	OutcomeProcessed
	OutcomeError
)

// Config configures a Runner.
type Config struct {
	// Name identifies the loop in panic logs.
	Name string
	// BaseInterval is the initial poll interval and the delay after work resets backoff.
	BaseInterval time.Duration
	// MaxInterval caps exponential backoff on empty polls. Values smaller than
	// BaseInterval disable backoff (clamping to BaseInterval).
	MaxInterval time.Duration
	// ErrorRetryInterval is the fixed retry delay after Step returns a non-nil error.
	ErrorRetryInterval time.Duration
	// DepthSampleInterval controls how often SampleDepth runs during idle polling.
	DepthSampleInterval time.Duration
	// Gate pauses timer-driven polls while the server is idle. Defaults to activity.Default.
	Gate *activity.Gate
	// Now overrides time.Now for depth-sampling cadence (defaults to time.Now).
	Now func() time.Time
	// Step performs one batch of work and returns how many items were processed.
	Step func(ctx context.Context) (processed int, err error)
	// SampleDepth optionally samples queue depth before Step on the first pass,
	// periodically at DepthSampleInterval, and on the pass following processed work.
	SampleDepth func(ctx context.Context)
	// OnProcessed is optionally invoked after Step returns processed > 0.
	OnProcessed func()
}

// Runner drives a nudged exponential-backoff polling loop.
type Runner struct {
	name                string
	baseInterval        time.Duration
	maxInterval         time.Duration
	errorRetryInterval  time.Duration
	depthSampleInterval time.Duration
	now                 func() time.Time
	step                func(ctx context.Context) (int, error)
	sampleDepth         func(ctx context.Context)
	onProcessed         func()

	gatePtr atomic.Pointer[activity.Gate]
	nudges  chan struct{}

	// Loop-private state owned by Run.
	delay             time.Duration
	lastDepthSample   time.Time
	sampleDepthOnNext bool
}

// New creates a Runner from cfg, applying defaults for zero intervals and a nil Gate.
func New(cfg Config) *Runner {
	base := cfg.BaseInterval
	if base <= 0 {
		base = DefaultBaseInterval
	}
	maxInterval := normalizeMaxInterval(base, cfg.MaxInterval)
	errorRetry := cfg.ErrorRetryInterval
	if errorRetry <= 0 {
		errorRetry = DefaultErrorRetryInterval
	}
	depthInterval := cfg.DepthSampleInterval
	if depthInterval <= 0 {
		depthInterval = DefaultDepthSampleInterval
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	r := &Runner{
		name:                cfg.Name,
		baseInterval:        base,
		maxInterval:         maxInterval,
		errorRetryInterval:  errorRetry,
		depthSampleInterval: depthInterval,
		now:                 now,
		step:                cfg.Step,
		sampleDepth:         cfg.SampleDepth,
		onProcessed:         cfg.OnProcessed,
		nudges:              make(chan struct{}, 1),
		delay:               base,
	}
	if cfg.Gate != nil {
		r.gatePtr.Store(cfg.Gate)
	}
	return r
}

// SetGate overrides the activity gate used by timer-driven polls. Passing nil
// reverts to activity.Default.
func (r *Runner) SetGate(gate *activity.Gate) {
	r.gatePtr.Store(gate)
}

func (r *Runner) gate() *activity.Gate {
	if g := r.gatePtr.Load(); g != nil {
		return g
	}
	return activity.Default
}

// Nudge wakes the loop promptly and resets backoff to BaseInterval. It never blocks.
func (r *Runner) Nudge() {
	select {
	case r.nudges <- struct{}{}:
	default:
	}
}

// Run polls until ctx is cancelled.
func (r *Runner) Run(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.nudges:
			if ctx.Err() != nil {
				return
			}
			r.delay = r.baseInterval
			r.runOnce(ctx, timer)
		case <-timer.C:
			if ctx.Err() != nil {
				return
			}
			// An idle server leaves the database alone (internal/activity).
			nudged, ok := r.waitGateOrNudge(ctx)
			if !ok {
				return
			}
			if nudged {
				r.delay = r.baseInterval
			}
			r.runOnce(ctx, timer)
		}
	}
}

func (r *Runner) waitGateOrNudge(ctx context.Context) (nudged bool, ok bool) {
	g := r.gate()
	if g.Awake() {
		return false, ctx.Err() == nil
	}
	waitCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- g.Wait(waitCtx)
	}()
	select {
	case <-ctx.Done():
		cancel()
		<-done
		return false, false
	case <-r.nudges:
		cancel()
		<-done
		return true, ctx.Err() == nil
	case err := <-done:
		return false, err == nil
	}
}

func (r *Runner) runOnce(ctx context.Context, timer *time.Timer) {
	now := r.now()
	if r.sampleDepth != nil && (r.sampleDepthOnNext || r.lastDepthSample.IsZero() || now.Sub(r.lastDepthSample) >= r.depthSampleInterval) {
		r.lastDepthSample = now
		r.sampleDepthOnNext = false
		r.callSampleDepth(ctx)
	}

	processed, err := r.callStep(ctx)
	if processed > 0 {
		r.sampleDepthOnNext = true
		if r.onProcessed != nil {
			r.callOnProcessed()
		}
	}

	outcome := OutcomeEmpty
	if err != nil {
		outcome = OutcomeError
	} else if processed > 0 {
		outcome = OutcomeProcessed
	}
	r.delay = NextDelay(r.delay, r.baseInterval, r.maxInterval, r.errorRetryInterval, outcome)
	if outcome == OutcomeProcessed {
		resetTimer(timer, 0)
		return
	}
	resetTimer(timer, r.delay)
}

func (r *Runner) callSampleDepth(ctx context.Context) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("background loop panic recovered",
				"loop", r.name,
				"phase", "sample_depth",
				"panic", rec,
				"stack", string(debug.Stack()),
			)
		}
	}()
	r.sampleDepth(ctx)
}

func (r *Runner) callStep(ctx context.Context) (processed int, err error) {
	if r.step == nil {
		return 0, nil
	}
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("background loop panic recovered",
				"loop", r.name,
				"phase", "step",
				"panic", rec,
				"stack", string(debug.Stack()),
			)
			processed = 0
			err = errStepPanic
		}
	}()
	return r.step(ctx)
}

func (r *Runner) callOnProcessed() {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("background loop panic recovered",
				"loop", r.name,
				"phase", "on_processed",
				"panic", rec,
				"stack", string(debug.Stack()),
			)
		}
	}()
	r.onProcessed()
}

// NextDelay computes the next poll delay from current delay, bounds, error
// retry interval, and step outcome.
func NextDelay(current, base, maxInterval, errorRetry time.Duration, outcome Outcome) time.Duration {
	if base <= 0 {
		base = DefaultBaseInterval
	}
	if errorRetry <= 0 {
		errorRetry = DefaultErrorRetryInterval
	}
	switch outcome {
	case OutcomeProcessed:
		return base
	case OutcomeError:
		return errorRetry
	}

	maxInterval = normalizeMaxInterval(base, maxInterval)
	if current < base {
		current = base
	}
	if current >= maxInterval || current > maxInterval-current {
		return maxInterval
	}
	return current * 2
}

func normalizeMaxInterval(base, maxInterval time.Duration) time.Duration {
	if maxInterval <= 0 {
		maxInterval = DefaultMaxInterval
	}
	if maxInterval < base {
		return base
	}
	return maxInterval
}

func resetTimer(timer *time.Timer, d time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(d)
}
