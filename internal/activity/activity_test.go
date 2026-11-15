package activity

import (
	"context"
	"net/http"
	"net/http/httptest"
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
