package activity

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestGateSleepsAfterIdleAndWakesOnARequest(t *testing.T) {
	g := New(time.Minute)
	now := time.Now()
	g.now = func() time.Time { return now }
	if !g.Awake() {
		t.Fatal("a new gate should be awake")
	}
	end := g.Begin()
	now = now.Add(time.Hour)
	if !g.Awake() {
		t.Fatal("awake while a request is in flight, however long")
	}
	end()
	end() // twice is harmless
	if !g.Awake() {
		t.Fatal("awake just after a request")
	}
	now = now.Add(2 * time.Minute)
	if g.Awake() {
		t.Fatal("asleep after idleAfter")
	}

	done := make(chan error, 1)
	go func() { done <- g.Wait(context.Background()) }()
	select {
	case <-done:
		t.Fatal("Wait returned while asleep")
	case <-time.After(20 * time.Millisecond):
	}
	h := g.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("a request did not wake Wait")
	}

	now = now.Add(2 * time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := g.Wait(ctx); err == nil {
		t.Fatal("Wait should end with its context")
	}
}

func TestNewIdleAndResetIdleStartAsleep(t *testing.T) {
	g := NewIdle(time.Minute)
	if g.Awake() {
		t.Fatal("NewIdle gate should start asleep")
	}

	awakeGate := New(time.Minute)
	if !awakeGate.Awake() {
		t.Fatal("New gate should start awake")
	}
	awakeGate.ResetIdle()
	if awakeGate.Awake() {
		t.Fatal("ResetIdle should put idle gate to sleep")
	}

	end := awakeGate.Begin()
	if !awakeGate.Awake() {
		t.Fatal("Begin should wake idle gate")
	}
	end()
	if !awakeGate.Awake() {
		t.Fatal("gate should stay awake within idleAfter")
	}
}

func TestMiddlewareExemptsProbesMetricsAndOptions(t *testing.T) {
	g := NewIdle(time.Minute)
	called := 0
	h := g.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called++
		w.WriteHeader(http.StatusOK)
	}))

	exempt := []*http.Request{
		httptest.NewRequest(http.MethodOptions, "/v1/slices", nil),
		httptest.NewRequest(http.MethodGet, "/metrics", nil),
		httptest.NewRequest(http.MethodGet, "/healthz", nil),
		httptest.NewRequest(http.MethodGet, "/readyz", nil),
		httptest.NewRequest(http.MethodPost, "/grpc.health.v1.Health/Check", nil),
		httptest.NewRequest(http.MethodPost, "/grpc.health.v1.Health/Watch", nil),
	}
	for _, req := range exempt {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s status = %d, want 200", req.Method, req.URL.Path, rec.Code)
		}
		if g.Awake() {
			t.Fatalf("exempt request %s %s woke the gate", req.Method, req.URL.Path)
		}
	}
	if called != len(exempt) {
		t.Fatalf("handler called %d times, want %d", called, len(exempt))
	}

	// A real API request wakes the gate.
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/gitslice.v1.SliceService/GetSlice", nil))
	if !g.Awake() {
		t.Fatal("real request did not wake the gate")
	}
}

func TestMiddlewareZeroAllocsWhenAwake(t *testing.T) {
	g := New(time.Minute)
	h := g.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/v1/slices", nil)
	var w http.ResponseWriter

	allocs := testing.AllocsPerRun(100, func() {
		h.ServeHTTP(w, req)
	})
	if allocs != 0 {
		t.Fatalf("Middleware allocated %.1f objects/op when awake, want 0", allocs)
	}
}

func TestGateConcurrentWaitersWakeTogether(t *testing.T) {
	g := NewIdle(time.Minute)
	const waiters = 8
	var ready sync.WaitGroup
	var done sync.WaitGroup
	ready.Add(waiters)
	done.Add(waiters)

	errs := make(chan error, waiters)
	for i := 0; i < waiters; i++ {
		go func() {
			defer done.Done()
			ready.Done()
			errs <- g.Wait(context.Background())
		}()
	}
	ready.Wait()
	time.Sleep(15 * time.Millisecond)

	end := g.Begin()
	defer end()

	finished := make(chan struct{})
	go func() {
		done.Wait()
		close(finished)
	}()

	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for concurrent waiters to wake")
	}
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Wait() error = %v", err)
		}
	}
}
