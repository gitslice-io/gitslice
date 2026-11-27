package bgloop

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/activity"
)

func TestNextDelay(t *testing.T) {
	const (
		base       = 25 * time.Millisecond
		max        = 200 * time.Millisecond
		errorRetry = time.Second
	)

	tests := []struct {
		name        string
		current     time.Duration
		maxInterval time.Duration
		errorRetry  time.Duration
		outcome     Outcome
		want        time.Duration
	}{
		{name: "empty doubles", current: base, maxInterval: max, errorRetry: errorRetry, outcome: OutcomeEmpty, want: 50 * time.Millisecond},
		{name: "empty doubles again", current: 50 * time.Millisecond, maxInterval: max, errorRetry: errorRetry, outcome: OutcomeEmpty, want: 100 * time.Millisecond},
		{name: "empty clamps to cap", current: 150 * time.Millisecond, maxInterval: max, errorRetry: errorRetry, outcome: OutcomeEmpty, want: max},
		{name: "empty stays at cap", current: max, maxInterval: max, errorRetry: errorRetry, outcome: OutcomeEmpty, want: max},
		{name: "work resets to base", current: max, maxInterval: max, errorRetry: errorRetry, outcome: OutcomeProcessed, want: base},
		{name: "error uses configured retry", current: base, maxInterval: max, errorRetry: 500 * time.Millisecond, outcome: OutcomeError, want: 500 * time.Millisecond},
		{name: "error uses default retry when non-positive", current: base, maxInterval: max, errorRetry: 0, outcome: OutcomeError, want: DefaultErrorRetryInterval},
		{name: "cap below base disables backoff", current: base, maxInterval: time.Millisecond, errorRetry: errorRetry, outcome: OutcomeEmpty, want: base},
		{name: "non-positive cap uses default", current: 40 * time.Second, maxInterval: 0, errorRetry: errorRetry, outcome: OutcomeEmpty, want: DefaultMaxInterval},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NextDelay(tt.current, base, tt.maxInterval, tt.errorRetry, tt.outcome); got != tt.want {
				t.Fatalf("NextDelay(%s, %s, %s, %s, %d) = %s, want %s", tt.current, base, tt.maxInterval, tt.errorRetry, tt.outcome, got, tt.want)
			}
		})
	}
}

func TestRunnerEmptyPollsBackOff(t *testing.T) {
	const (
		base   = 5 * time.Millisecond
		window = 230 * time.Millisecond
	)
	gate := activity.New(time.Minute)
	var stepCalls, depthCalls atomic.Int32

	r := New(Config{
		Name:                "backoff-test",
		BaseInterval:        base,
		MaxInterval:         80 * time.Millisecond,
		DepthSampleInterval: time.Minute,
		Gate:                gate,
		Step: func(context.Context) (int, error) {
			stepCalls.Add(1)
			return 0, nil
		},
		SampleDepth: func(context.Context) {
			depthCalls.Add(1)
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Run(ctx)
	}()

	time.Sleep(window)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runner did not stop after cancellation")
	}

	calls := int(stepCalls.Load())
	fixedCalls := int(window / base)
	if calls < 2 {
		t.Fatalf("Step called %d times, want at least 2", calls)
	}
	if calls >= fixedCalls/3 {
		t.Fatalf("Step called %d times over %s; fixed cadence would be ~%d", calls, window, fixedCalls)
	}
	if got := depthCalls.Load(); got != 1 {
		t.Fatalf("SampleDepth called %d times during idle polling, want 1", got)
	}
}

func TestRunnerNudgeWakesAndResetsCadence(t *testing.T) {
	gate := activity.New(time.Minute)
	calls := make(chan time.Time, 64)

	r := New(Config{
		Name:         "nudge-test",
		BaseInterval: 10 * time.Millisecond,
		MaxInterval:  200 * time.Millisecond,
		Gate:         gate,
		Step: func(context.Context) (int, error) {
			calls <- time.Now()
			return 0, nil
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Run(ctx)
	}()
	defer func() {
		cancel()
		<-done
	}()

	for range 3 {
		waitTime(t, calls, 500*time.Millisecond)
	}

	nudgedAt := time.Now()
	r.Nudge()
	nudgePoll := waitTime(t, calls, 100*time.Millisecond)
	if elapsed := nudgePoll.Sub(nudgedAt); elapsed > 100*time.Millisecond {
		t.Fatalf("nudged poll took %s, want <= 100ms", elapsed)
	}

	followUp := waitTime(t, calls, 100*time.Millisecond)
	if elapsed := followUp.Sub(nudgePoll); elapsed > 100*time.Millisecond {
		t.Fatalf("post-nudge poll took %s, want reset backoff cadence", elapsed)
	}
}

func TestRunnerProcessedWorkRepollsImmediatelyAndCallsOnProcessed(t *testing.T) {
	gate := activity.New(time.Minute)

	var mu sync.Mutex
	queue := []int{0, 2, 0}
	type stepEvent struct {
		at        time.Time
		processed int
	}
	events := make(chan stepEvent, 64)
	var onProcessedCalls, depthCalls atomic.Int32

	r := New(Config{
		Name:                "processed-test",
		BaseInterval:        10 * time.Millisecond,
		MaxInterval:         200 * time.Millisecond,
		DepthSampleInterval: time.Minute,
		Gate:                gate,
		Step: func(context.Context) (int, error) {
			mu.Lock()
			n := 0
			if len(queue) > 0 {
				n = queue[0]
				queue = queue[1:]
			}
			mu.Unlock()
			events <- stepEvent{at: time.Now(), processed: n}
			return n, nil
		},
		SampleDepth: func(context.Context) {
			depthCalls.Add(1)
		},
		OnProcessed: func() {
			onProcessedCalls.Add(1)
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Run(ctx)
	}()
	defer func() {
		cancel()
		<-done
	}()

	// Initial empty pass.
	ev1 := waitStepEvent(t, events, 200*time.Millisecond)
	if ev1.processed != 0 {
		t.Fatalf("first pass processed = %d, want 0", ev1.processed)
	}
	// Trigger the work pass.
	r.Nudge()
	ev2 := waitStepEvent(t, events, 200*time.Millisecond)
	if ev2.processed != 2 {
		t.Fatalf("second pass processed = %d, want 2", ev2.processed)
	}
	// Immediate follow-up re-poll after processed work.
	ev3 := waitStepEvent(t, events, 100*time.Millisecond)
	if elapsed := ev3.at.Sub(ev2.at); elapsed > 100*time.Millisecond {
		t.Fatalf("follow-up poll after work took %s, want immediate re-poll", elapsed)
	}

	if got := onProcessedCalls.Load(); got != 1 {
		t.Fatalf("OnProcessed called %d times, want 1", got)
	}
	if got := depthCalls.Load(); got < 2 {
		t.Fatalf("SampleDepth called %d times, want at least 2 (initial + after work)", got)
	}
}

func TestRunnerErrorRetryInterval(t *testing.T) {
	gate := activity.New(time.Minute)
	calls := make(chan time.Time, 16)
	var count atomic.Int32

	r := New(Config{
		Name:               "error-retry-test",
		BaseInterval:       5 * time.Millisecond,
		MaxInterval:        10 * time.Millisecond,
		ErrorRetryInterval: 60 * time.Millisecond,
		Gate:               gate,
		Step: func(context.Context) (int, error) {
			calls <- time.Now()
			if count.Add(1) == 1 {
				return 0, errors.New("transient db error")
			}
			return 0, nil
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Run(ctx)
	}()
	defer func() {
		cancel()
		<-done
	}()

	t1 := waitTime(t, calls, 200*time.Millisecond)
	t2 := waitTime(t, calls, 500*time.Millisecond)
	if gap := t2.Sub(t1); gap < 45*time.Millisecond {
		t.Fatalf("retry gap after error = %s, want ~60ms (ErrorRetryInterval)", gap)
	}
}

func TestRunnerDepthSamplingCadence(t *testing.T) {
	var mu sync.Mutex
	now := time.Unix(1_000_000, 0)
	var depthCalls int
	var nextProcessed int

	r := New(Config{
		Name:                "depth-cadence-test",
		BaseInterval:        10 * time.Millisecond,
		MaxInterval:         100 * time.Millisecond,
		DepthSampleInterval: 30 * time.Second,
		Gate:                activity.New(time.Minute),
		Now: func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			return now
		},
		Step: func(context.Context) (int, error) {
			return nextProcessed, nil
		},
		SampleDepth: func(context.Context) {
			depthCalls++
		},
	})

	timer := time.NewTimer(time.Hour)
	defer timer.Stop()

	// Pass 1: initial sample always runs.
	r.runOnce(context.Background(), timer)
	// Pass 2: within DepthSampleInterval, no new sample.
	r.runOnce(context.Background(), timer)
	if depthCalls != 1 {
		t.Fatalf("depthCalls = %d, want 1", depthCalls)
	}

	// Advance clock by DepthSampleInterval: should sample again.
	mu.Lock()
	now = now.Add(30 * time.Second)
	mu.Unlock()
	r.runOnce(context.Background(), timer)
	if depthCalls != 2 {
		t.Fatalf("depthCalls after interval = %d, want 2", depthCalls)
	}

	// Process work: schedules depth sample on the very next pass regardless of clock.
	nextProcessed = 1
	r.runOnce(context.Background(), timer)
	if depthCalls != 2 {
		t.Fatalf("depthCalls during work pass = %d, want 2", depthCalls)
	}
	nextProcessed = 0
	r.runOnce(context.Background(), timer)
	if depthCalls != 3 {
		t.Fatalf("depthCalls after work pass = %d, want 3", depthCalls)
	}
}

func TestRunnerIdleGateBlocksAndWakes(t *testing.T) {
	gate := activity.NewIdle(time.Minute)
	var calls atomic.Int32
	woke := make(chan struct{}, 4)

	r := New(Config{
		Name:         "idle-gate-test",
		BaseInterval: 5 * time.Millisecond,
		MaxInterval:  20 * time.Millisecond,
		Gate:         gate,
		Step: func(context.Context) (int, error) {
			calls.Add(1)
			select {
			case woke <- struct{}{}:
			default:
			}
			return 0, nil
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Run(ctx)
	}()
	defer func() {
		cancel()
		<-done
	}()

	time.Sleep(40 * time.Millisecond)
	if got := calls.Load(); got != 0 {
		t.Fatalf("Step called %d times while gate is idle, want 0", got)
	}

	end := gate.Begin()
	end()

	select {
	case <-woke:
	case <-time.After(time.Second):
		t.Fatal("runner did not wake after activity gate opened")
	}
}

func TestRunnerNudgeWakesWhileGateIdle(t *testing.T) {
	gate := activity.NewIdle(time.Minute)
	woke := make(chan struct{}, 4)

	r := New(Config{
		Name:         "nudge-while-idle-test",
		BaseInterval: 5 * time.Millisecond,
		MaxInterval:  20 * time.Millisecond,
		Gate:         gate,
		Step: func(context.Context) (int, error) {
			select {
			case woke <- struct{}{}:
			default:
			}
			return 0, nil
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Run(ctx)
	}()
	defer func() {
		cancel()
		<-done
	}()

	time.Sleep(30 * time.Millisecond)
	r.Nudge()

	select {
	case <-woke:
	case <-time.After(time.Second):
		t.Fatal("Nudge did not wake runner parked on idle gate")
	}
}

func TestRunnerRecoversPanicsInStepAndSampleDepth(t *testing.T) {
	gate := activity.New(time.Minute)
	var stepCalls atomic.Int32
	recovered := make(chan struct{}, 1)

	r := New(Config{
		Name:                "panic-test",
		BaseInterval:        5 * time.Millisecond,
		MaxInterval:         20 * time.Millisecond,
		ErrorRetryInterval:  15 * time.Millisecond,
		DepthSampleInterval: time.Minute,
		Gate:                gate,
		SampleDepth: func(context.Context) {
			panic("boom sample depth")
		},
		Step: func(context.Context) (int, error) {
			n := stepCalls.Add(1)
			if n == 1 {
				panic("boom step")
			}
			select {
			case recovered <- struct{}{}:
			default:
			}
			return 0, nil
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Run(ctx)
	}()
	defer func() {
		cancel()
		<-done
	}()

	select {
	case <-recovered:
	case <-time.After(time.Second):
		t.Fatal("runner did not continue after panics in SampleDepth and Step")
	}
}

func waitTime(t *testing.T, ch <-chan time.Time, timeout time.Duration) time.Time {
	t.Helper()
	select {
	case ts := <-ch:
		return ts
	case <-time.After(timeout):
		t.Fatalf("timed out after %s waiting for step", timeout)
		return time.Time{}
	}
}

func waitStepEvent[T any](t *testing.T, ch <-chan T, timeout time.Duration) T {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(timeout):
		t.Fatalf("timed out after %s waiting for step event", timeout)
		var zero T
		return zero
	}
}
