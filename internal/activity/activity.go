// Package activity tells the server's background loops whether anyone is
// using it. Each loop (publisher, index worker, event (trigger and mirror)
// dispatcher, check sweeps, blame warmer) waits here before its next pass,
// so once no request has been seen for IdleAfter they stop querying the
// database, and Neon can scale to zero. Cloud Run only throttles a server's
// CPU between requests, it does not stop its goroutines, so without this the
// loops kept the database busy around the clock.
//
// Nothing is lost while they wait: their work is recorded in the database,
// and new work arrives with a request, which wakes them.
package activity

import (
	"context"
	"math"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// IdleAfter is how long after the last request ends the loops keep going.
const IdleAfter = 5 * time.Minute

const neverEnded = math.MinInt64

// Gate counts requests in flight and remembers when the last one ended.
type Gate struct {
	idleAfter time.Duration
	now       func() time.Time

	inFlight    atomic.Int64
	lastEndNano atomic.Int64
	hasWaiter   atomic.Bool

	mu   sync.Mutex
	wake chan struct{} // lazily created when a waiter parks while asleep
}

// New returns a gate that counts as busy until idleAfter from now.
func New(idleAfter time.Duration) *Gate {
	g := &Gate{idleAfter: idleAfter, now: time.Now}
	g.lastEndNano.Store(time.Now().UnixNano())
	return g
}

// NewIdle returns a gate that starts asleep until the first request begins.
func NewIdle(idleAfter time.Duration) *Gate {
	g := &Gate{idleAfter: idleAfter, now: time.Now}
	g.lastEndNano.Store(neverEnded)
	return g
}

// Default is the server's gate.
var Default = New(IdleAfter)

// ResetIdle marks a gate with no in-flight requests as idle immediately.
// Server startup calls this before launching background loops so a newly
// booted instance does not wake Neon before real user traffic arrives.
func (g *Gate) ResetIdle() {
	g.lastEndNano.Store(neverEnded)
}

func (g *Gate) nowTime() time.Time {
	if g.now != nil {
		return g.now()
	}
	return time.Now()
}

func (g *Gate) begin() {
	g.inFlight.Add(1)
	if g.hasWaiter.Load() {
		g.mu.Lock()
		if g.wake != nil {
			close(g.wake)
			g.wake = nil
		}
		g.hasWaiter.Store(false)
		g.mu.Unlock()
	}
}

func (g *Gate) end() {
	g.lastEndNano.Store(g.nowTime().UnixNano())
	g.inFlight.Add(-1)
}

// Begin records a request starting; call the returned func when it ends.
func (g *Gate) Begin() func() {
	g.begin()
	var done atomic.Bool
	return func() {
		if done.CompareAndSwap(false, true) {
			g.end()
		}
	}
}

// Awake reports whether a request is in flight or one ended within
// idleAfter.
func (g *Gate) Awake() bool {
	if g.inFlight.Load() > 0 {
		return true
	}
	last := g.lastEndNano.Load()
	if last == neverEnded {
		return false
	}
	return g.nowTime().Sub(time.Unix(0, last)) < g.idleAfter
}

// Wait returns at once while the server is awake, and otherwise when the
// next request begins or ctx ends (with ctx's error).
func (g *Gate) Wait(ctx context.Context) error {
	if g.Awake() {
		return nil
	}
	for {
		g.mu.Lock()
		if g.Awake() {
			g.mu.Unlock()
			return nil
		}
		if g.wake == nil {
			g.wake = make(chan struct{})
		}
		wake := g.wake
		g.hasWaiter.Store(true)
		if g.Awake() {
			g.mu.Unlock()
			return nil
		}
		g.mu.Unlock()

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wake:
		}
	}
}

func isExemptRequest(r *http.Request) bool {
	if r.Method == http.MethodOptions {
		return true
	}
	switch r.URL.Path {
	case "/metrics", "/healthz", "/readyz":
		return true
	}
	return strings.HasPrefix(r.URL.Path, "/grpc.health.v1.Health/")
}

// Middleware counts non-probe HTTP requests (gRPC, Connect, git) on the gate.
// Health checks, readiness probes, metrics scrapes, and CORS preflights do not
// wake background database loops.
func (g *Gate) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isExemptRequest(r) {
			next.ServeHTTP(w, r)
			return
		}
		g.begin()
		defer g.end()
		next.ServeHTTP(w, r)
	})
}
