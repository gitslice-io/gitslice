package clibase

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

type progressSinkKey struct{}

// ProgressSink configures where a ProgressReporter writes.
type ProgressSink struct {
	W   io.Writer
	TTY bool
}

// WithProgress attaches a progress output sink to ctx.
func WithProgress(ctx context.Context, w io.Writer) context.Context {
	return context.WithValue(ctx, progressSinkKey{}, ProgressSink{
		W:   w,
		TTY: IsTerminal(w) && os.Getenv("TERM") != "dumb",
	})
}

// ProgressReporter prints periodic progress for long-running CLI operations.
type ProgressReporter struct {
	sink  ProgressSink
	verb  string
	unit  string
	total int64
	done  atomic.Int64
	start time.Time

	delay, interval time.Duration

	mu      sync.Mutex
	shown   bool
	stop    chan struct{}
	stopped chan struct{}
	once    sync.Once
}

// StartProgress starts reporting "<verb> <done>/<total> <unit>" until Finish.
// Returns nil when no progress sink is present in ctx.
func StartProgress(ctx context.Context, verb, unit string, total int) *ProgressReporter {
	sink, ok := ctx.Value(progressSinkKey{}).(ProgressSink)
	if !ok || sink.W == nil {
		return nil
	}
	if sink.TTY {
		return StartProgressEvery(sink, verb, unit, total, time.Second, 200*time.Millisecond)
	}
	return StartProgressEvery(sink, verb, unit, total, 2*time.Second, 5*time.Second)
}

func StartProgressEvery(sink ProgressSink, verb, unit string, total int, delay, interval time.Duration) *ProgressReporter {
	p := &ProgressReporter{
		sink:     sink,
		verb:     verb,
		unit:     unit,
		total:    int64(total),
		start:    time.Now(),
		delay:    delay,
		interval: interval,
		stop:     make(chan struct{}),
		stopped:  make(chan struct{}),
	}
	go p.run()
	return p
}

func (p *ProgressReporter) Add(n int) {
	if p == nil {
		return
	}
	p.done.Add(int64(n))
}

func (p *ProgressReporter) Finish() {
	if p == nil {
		return
	}
	p.once.Do(func() {
		close(p.stop)
		<-p.stopped
		p.mu.Lock()
		defer p.mu.Unlock()
		if !p.shown {
			return
		}
		line := p.line() + fmt.Sprintf(", done in %s", time.Since(p.start).Round(100*time.Millisecond))
		if p.sink.TTY {
			fmt.Fprintf(p.sink.W, "\r\x1b[K%s\n", line)
			return
		}
		fmt.Fprintln(p.sink.W, line)
	})
}

func (p *ProgressReporter) run() {
	defer close(p.stopped)
	timer := time.NewTimer(p.delay)
	defer timer.Stop()
	select {
	case <-p.stop:
		return
	case <-timer.C:
	}
	p.report()
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-ticker.C:
			p.report()
		}
	}
}

func (p *ProgressReporter) report() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.shown = true
	if p.sink.TTY {
		fmt.Fprintf(p.sink.W, "\r\x1b[K%s", p.line())
		return
	}
	fmt.Fprintln(p.sink.W, p.line())
}

func (p *ProgressReporter) line() string {
	done := p.done.Load()
	if p.total <= 0 {
		return fmt.Sprintf("%s %d %s", p.verb, done, p.unit)
	}
	if done > p.total {
		done = p.total
	}
	return fmt.Sprintf("%s %d/%d %s (%d%%)", p.verb, done, p.total, p.unit, done*100/p.total)
}
