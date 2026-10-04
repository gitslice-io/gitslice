package gitcompat

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// A projection is expensive to build from nothing: it replays a slice's whole
// native history, and the cache lives on one server instance's disk. Every new
// instance, including each deploy, would pay that on the first Git request for
// every slice. The PackMirror (packmirror.go) keeps a copy in the object store;
// this file holds the publisher that uploads it in the background.

const (
	// A burst of landings becomes one upload per interval.
	mirrorPushInterval = 5 * time.Second
	mirrorPushTimeout  = 10 * time.Minute
	mirrorPushRetries  = 3
	// How long a request that changed the head waits for the mirror to catch
	// up, while the instance still has CPU.
	mirrorSettleWait = 20 * time.Second
)

// publisher runs one background publish per slice at a time. While one is
// running, later snapshots replace each other, so a burst of landings becomes
// at most one more run. A failed run is tried again, unless a newer snapshot
// has replaced it.
//
// On Cloud Run a container only gets CPU while it serves a request, so a
// publish left to itself stalls once the request that started it ends. A caller
// that can spare the time calls wait while it still holds a request open.
type publisher[T any] struct {
	mu       sync.Mutex
	jobs     map[string]*publishJob[T]
	interval time.Duration
	retries  int
	run      func(ctx context.Context, key string, snapshot T) error
	label    string
}

type publishJob[T any] struct {
	pending *T
	running bool
	// submitted counts snapshots handed in; finished is the highest one a run
	// has covered (snapshots that were replaced count as covered by the run
	// that took the newer one). changed is closed, and replaced, on every run.
	submitted, finished int
	lastErr             error
	changed             chan struct{}
}

func newPublisher[T any](label string, run func(ctx context.Context, key string, snapshot T) error) *publisher[T] {
	return &publisher[T]{jobs: map[string]*publishJob[T]{}, interval: mirrorPushInterval, retries: mirrorPushRetries, run: run, label: label}
}

func (p *publisher[T]) submit(key string, snapshot T) {
	p.mu.Lock()
	job := p.jobs[key]
	if job == nil {
		job = &publishJob[T]{changed: make(chan struct{})}
		p.jobs[key] = job
	}
	job.pending = &snapshot
	job.submitted++
	start := !job.running
	job.running = true
	p.mu.Unlock()
	if start {
		go p.loop(key, job)
	}
}

// wait blocks until a run has covered everything submitted for key so far, the
// context ends, or the timeout passes. It returns that run's error. A timeout
// is not an error: the publish carries on in the background.
func (p *publisher[T]) wait(ctx context.Context, key string, timeout time.Duration) error {
	p.mu.Lock()
	job := p.jobs[key]
	if job == nil {
		p.mu.Unlock()
		return nil
	}
	target := job.submitted
	p.mu.Unlock()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		p.mu.Lock()
		if job.finished >= target {
			err := job.lastErr
			p.mu.Unlock()
			return err
		}
		changed := job.changed
		p.mu.Unlock()
		select {
		case <-changed:
		case <-timer.C:
			return nil
		case <-ctx.Done():
			return nil
		}
	}
}

func (p *publisher[T]) loop(key string, job *publishJob[T]) {
	failures := 0
	for {
		p.mu.Lock()
		snapshot := job.pending
		covers := job.submitted
		job.pending = nil
		if snapshot == nil {
			job.running = false
			p.mu.Unlock()
			return
		}
		p.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), mirrorPushTimeout)
		started := time.Now()
		err := p.run(ctx, key, *snapshot)
		cancel()
		if err != nil {
			slog.Warn(p.label+" failed", "repo", key+".git", "error", err)
			recordGitMirror("publish", "error")
		} else {
			failures = 0
			slog.Info(p.label, "repo", key+".git", "total_ms", time.Since(started).Milliseconds())
			recordGitMirror("publish", "ok")
		}
		p.mu.Lock()
		retry := err != nil && job.pending == nil && failures < p.retries
		if retry {
			failures++
			job.pending = snapshot
		}
		if !retry {
			job.finished = covers
			job.lastErr = err
			close(job.changed)
			job.changed = make(chan struct{})
		}
		more := job.pending != nil
		p.mu.Unlock()
		if more {
			// Debounces a burst of landings; after a failure it is also the backoff.
			time.Sleep(p.interval * time.Duration(1<<failures))
		}
	}
}
