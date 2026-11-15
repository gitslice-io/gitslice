// Package activity tells the server's background loops whether anyone is
// using it. Each loop (publisher, index worker, webhook and trigger
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
	"net/http"
	"sync"
	"time"
)

// IdleAfter is how long after the last request ends the loops keep going.
const IdleAfter = 5 * time.Minute

// Gate counts requests in flight and remembers when the last one ended.
type Gate struct {
	idleAfter time.Duration
	now       func() time.Time

	mu       sync.Mutex
	inFlight int
	lastEnd  time.Time
	wake     chan struct{} // closed when a request begins
}

// New returns a gate that counts as busy until idleAfter from now.
func New(idleAfter time.Duration) *Gate {
	return &Gate{idleAfter: idleAfter, now: time.Now, lastEnd: time.Now(), wake: make(chan struct{})}
}

// Default is the server's gate.
var Default = New(IdleAfter)

// Begin records a request starting; call the returned func when it ends.
func (g *Gate) Begin() func() {
	g.mu.Lock()
	g.inFlight++
	close(g.wake)
	g.wake = make(chan struct{})
	g.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			g.inFlight--
			g.lastEnd = g.now()
			g.mu.Unlock()
		})
	}
}

// Awake reports whether a request is in flight or one ended within
// idleAfter.
func (g *Gate) Awake() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.inFlight > 0 || g.now().Sub(g.lastEnd) < g.idleAfter
}

// Wait returns at once while the server is awake, and otherwise when the
// next request begins or ctx ends (with ctx's error).
func (g *Gate) Wait(ctx context.Context) error {
	for {
		g.mu.Lock()
		awake := g.inFlight > 0 || g.now().Sub(g.lastEnd) < g.idleAfter
		wake := g.wake
		g.mu.Unlock()
		if awake {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wake:
		}
	}
}

// Middleware counts every HTTP request (gRPC, Connect, git) on the gate.
func (g *Gate) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		end := g.Begin()
		defer end()
		next.ServeHTTP(w, r)
	})
}
