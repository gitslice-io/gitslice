package events

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/activity"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/storage/memory"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

type countingEvents struct {
	storage.EventStore
	claims atomic.Int32
}

func (c *countingEvents) ClaimEvents(ctx context.Context, limit int, lease time.Duration) ([]storage.Event, error) {
	c.claims.Add(1)
	return c.EventStore.ClaimEvents(ctx, limit, lease)
}

type fakeTriggers struct {
	likely    bool
	mu        sync.Mutex
	envelopes [][]byte
}

func (f *fakeTriggers) MayHaveTriggers(context.Context) bool { return f.likely }

func (f *fakeTriggers) FanOut(_ context.Context, _ storage.Event, envelope []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.envelopes = append(f.envelopes, envelope)
}

func (f *fakeTriggers) Drain(context.Context) {}

func (f *fakeTriggers) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.envelopes)
}

type fakeMirrors struct{ events atomic.Int32 }

func (f *fakeMirrors) FanOut(context.Context, storage.Event) { f.events.Add(1) }
func (f *fakeMirrors) Drain(context.Context)                 {}

func newTestDispatcher(t *testing.T, triggers *fakeTriggers, mirrors *fakeMirrors) (*Dispatcher, *countingEvents, *corev1.Slice) {
	t.Helper()
	mem := memory.New()
	mem.AddAccount("user_alice", "acme")
	slice := mem.PutSlice(&corev1.SliceRef{Account: "acme", Slice: "app"}, []string{"/acme/app"}, "private")
	store := &countingEvents{EventStore: mem.Events}
	d := NewDispatcher(Stores{
		Events:     store,
		Slices:     mem.Slices,
		Changesets: mem.Changesets,
		Checks:     mem.Checks,
		Auth:       mem.Auth,
	}, Options{WebBaseURL: "https://gitslice.test", Triggers: triggers, Mirrors: mirrors})
	return d, store, slice
}

// A tag event reaches triggers (with its payload) and mirrors, once.
func TestDrainFansEventsOutToTriggersAndMirrors(t *testing.T) {
	triggers := &fakeTriggers{likely: true}
	mirrors := &fakeMirrors{}
	d, _, slice := newTestDispatcher(t, triggers, mirrors)

	d.Emit(context.Background(), storage.Event{Kind: storage.EventTagCreated, SliceID: slice.Id, TagName: "v1.0.0"})
	if err := d.DrainOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if triggers.count() != 1 || mirrors.events.Load() != 1 {
		t.Fatalf("triggers = %d, mirrors = %d, want one each", triggers.count(), mirrors.events.Load())
	}
	var payload map[string]any
	if err := json.Unmarshal(triggers.envelopes[0], &payload); err != nil {
		t.Fatalf("trigger payload: %v", err)
	}
	if payload["event"] != storage.EventTagCreated {
		t.Fatalf("payload event = %v, want %s", payload["event"], storage.EventTagCreated)
	}
}

// When no trigger is likely, recording an event only nudges the background
// loop: the request that recorded it doesn't wait for a drain.
func TestFlushOnlyNudgesWhenNoTriggerIsLikely(t *testing.T) {
	d, store, slice := newTestDispatcher(t, &fakeTriggers{likely: false}, &fakeMirrors{})
	d.Emit(context.Background(), storage.Event{Kind: storage.EventTagCreated, SliceID: slice.Id, TagName: "v1.0.0"})
	if got := store.claims.Load(); got != 0 {
		t.Fatalf("ClaimEvents during Emit = %d, want 0 (drained inline)", got)
	}
	select {
	case <-d.nudge:
	default:
		t.Fatal("Emit did not nudge the background loop")
	}
}

// Run leaves the database alone while the server is idle, and drains once a
// request wakes it.
func TestRunWaitsForActivity(t *testing.T) {
	t.Cleanup(func() {
		done := activity.Default.Begin()
		done()
	})
	d, store, _ := newTestDispatcher(t, &fakeTriggers{}, &fakeMirrors{})
	activity.Default.ResetIdle()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.Run(ctx)
	}()
	time.Sleep(40 * time.Millisecond)
	if got := store.claims.Load(); got != 0 {
		cancel()
		<-done
		t.Fatalf("ClaimEvents while idle = %d, want 0", got)
	}
	end := activity.Default.Begin()
	end()
	deadline := time.Now().Add(2 * time.Second)
	for store.claims.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if store.claims.Load() == 0 {
		t.Fatal("Run did not drain after activity")
	}
}
