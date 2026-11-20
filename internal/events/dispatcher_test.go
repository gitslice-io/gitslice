package events

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/activity"
	"gitslice.io/gitslice/internal/automations"
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

type fakeAutomations struct {
	likely    bool
	mu        sync.Mutex
	envelopes [][]byte
}

func (f *fakeAutomations) MayHaveAutomations(context.Context) bool { return f.likely }

func (f *fakeAutomations) FanOut(_ context.Context, _ storage.Event, _ storage.EventEntities, envelope []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.envelopes = append(f.envelopes, envelope)
}

func (f *fakeAutomations) Drain(context.Context) {}

func (f *fakeAutomations) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.envelopes)
}

type fakeMirrors struct{ events atomic.Int32 }

func (f *fakeMirrors) FanOut(context.Context, storage.Event) { f.events.Add(1) }
func (f *fakeMirrors) Drain(context.Context)                 {}

func newTestDispatcher(t *testing.T, triggers *fakeAutomations, mirrors *fakeMirrors) (*Dispatcher, *countingEvents, *corev1.Slice) {
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
	}, Options{WebBaseURL: "https://gitslice.test", Automations: triggers, Mirrors: mirrors})
	return d, store, slice
}

// A tag event reaches triggers (with its payload) and mirrors, once.
func TestDrainFansEventsOutToTriggersAndMirrors(t *testing.T) {
	autos := &fakeAutomations{likely: true}
	mirrors := &fakeMirrors{}
	d, _, slice := newTestDispatcher(t, autos, mirrors)

	d.Emit(context.Background(), storage.Event{Kind: storage.EventTagCreated, SliceID: slice.Id, TagName: "v1.0.0"})
	if err := d.DrainOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if autos.count() != 1 || mirrors.events.Load() != 1 {
		t.Fatalf("automations = %d, mirrors = %d, want one each", autos.count(), mirrors.events.Load())
	}
	var payload map[string]any
	if err := json.Unmarshal(autos.envelopes[0], &payload); err != nil {
		t.Fatalf("automation payload: %v", err)
	}
	if payload["event"] != storage.EventTagCreated {
		t.Fatalf("payload event = %v, want %s", payload["event"], storage.EventTagCreated)
	}
}

// When no trigger is likely, recording an event only nudges the background
// loop: the request that recorded it doesn't wait for a drain.
func TestFlushOnlyNudgesWhenNoTriggerIsLikely(t *testing.T) {
	d, store, slice := newTestDispatcher(t, &fakeAutomations{likely: false}, &fakeMirrors{})
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
	d, store, _ := newTestDispatcher(t, &fakeAutomations{}, &fakeMirrors{})
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

type countingChangesets struct {
	storage.ChangesetStore
	gets atomic.Int32
}

func (c *countingChangesets) Get(ctx context.Context, id string) (*corev1.Changeset, error) {
	c.gets.Add(1)
	return c.ChangesetStore.Get(ctx, id)
}

type countingSlices struct {
	storage.SliceStore
	gets atomic.Int32
}

func (c *countingSlices) Get(ctx context.Context, id string) (*corev1.Slice, error) {
	c.gets.Add(1)
	return c.SliceStore.Get(ctx, id)
}

type countingChecks struct {
	storage.CheckStore
	gets atomic.Int32
}

func (c *countingChecks) GetCheckRun(ctx context.Context, id string) (*corev1.CheckRun, error) {
	c.gets.Add(1)
	return c.CheckStore.GetCheckRun(ctx, id)
}

type staticTreeReader struct{}

func (staticTreeReader) ReadFile(context.Context, string, string) ([]byte, error) {
	return nil, automations.ErrNotFound
}

func TestDrainOncePassesPreloadedEntitiesWithoutDuplicateReads(t *testing.T) {
	ctx := context.Background()
	mem := memory.New()
	mem.AddAccount("user_alice", "acme")
	slice := mem.PutSlice(&corev1.SliceRef{Account: "acme", Slice: "app"}, []string{"/acme/app"}, "private")
	ref, err := mem.Repository.GetRef(ctx, storage.DefaultTargetRef)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := mem.Changesets.Create(ctx, "user_alice", &corev1.CreateChangesetRequest{
		AuthoringSlice: slice.Ref,
		BaseCommitId:   ref.CommitId,
		Title:          "single read",
	})
	if err != nil {
		t.Fatal(err)
	}
	ps, err := mem.Changesets.AddPatchset(ctx, cs.Id, "", &corev1.Patchset{
		BaseCommitId: ref.CommitId,
		Author:       "user_alice",
		ChangedPaths: []string{"/acme/app/main.go"},
		FileEdits:    []*corev1.FileEdit{{Op: "upsert", Path: "/acme/app/main.go", ContentHash: "sha256:abc", Mode: 0o100644}},
	})
	if err != nil {
		t.Fatal(err)
	}
	checkRun, err := mem.Checks.CreateCheckRun(ctx, storage.CheckRunInput{
		ChangesetID: cs.Id,
		PatchsetID:  ps.Id,
		CheckName:   "unit",
		Status:      "passed",
	})
	if err != nil {
		t.Fatal(err)
	}

	csStore := &countingChangesets{ChangesetStore: mem.Changesets}
	sliceStore := &countingSlices{SliceStore: mem.Slices}
	checkStore := &countingChecks{CheckStore: mem.Checks}

	engine := automations.NewEngine(automations.Stores{
		Automations: mem.Automations,
		Changesets:  csStore,
		Slices:      sliceStore,
		Repository:  mem.Repository,
		Checks:      checkStore,
	}, automations.Options{
		Tree:                staticTreeReader{},
		AllowPrivateTargets: true,
	})
	dispatcher := NewDispatcher(Stores{
		Events:     mem.Events,
		Slices:     sliceStore,
		Changesets: csStore,
		Checks:     checkStore,
		Auth:       mem.Auth,
	}, Options{
		WebBaseURL:  "https://gitslice.test",
		Automations: engine,
	})

	if _, err := mem.Events.AppendEvent(ctx, storage.Event{
		Kind:         storage.EventCheckRunCompleted,
		ChangesetID:  cs.Id,
		PatchsetID:   ps.Id,
		CheckRunID:   checkRun.Id,
		ChangedPaths: []string{"/acme/app/main.go"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Events.AppendEvent(ctx, storage.Event{
		Kind:    storage.EventTagCreated,
		SliceID: slice.Id,
		TagName: "v1.2.3",
	}); err != nil {
		t.Fatal(err)
	}

	if err := dispatcher.DrainOnce(ctx); err != nil {
		t.Fatalf("DrainOnce: %v", err)
	}
	if got := csStore.gets.Load(); got != 1 {
		t.Fatalf("Changesets.Get calls = %d, want 1 (no duplicate read in Engine.FanOut)", got)
	}
	if got := sliceStore.gets.Load(); got != 1 {
		t.Fatalf("Slices.Get calls = %d, want 1 (no duplicate read in Engine.FanOut)", got)
	}
	if got := checkStore.gets.Load(); got != 1 {
		t.Fatalf("Checks.GetCheckRun calls = %d, want 1 (no duplicate read in Engine.FanOut)", got)
	}
}
