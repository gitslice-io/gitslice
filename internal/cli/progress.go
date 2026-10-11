package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// Long operations (an upload of thousands of files, a sync of hundreds of
// paths, a hydrate) report how far they have got on stderr, so a long run is
// not silent. On a terminal one line rewrites itself; anywhere else (an
// agent's shell, a CI log) a plain line is printed every few seconds. Nothing
// is printed for runs that finish within the first moment, and --quiet and
// --json turn it off.

type progressSinkKey struct{}

type progressSink struct {
	w   io.Writer
	tty bool
}

// withProgress lets the operations a command runs report progress to w.
func withProgress(ctx context.Context, w io.Writer) context.Context {
	return context.WithValue(ctx, progressSinkKey{}, progressSink{
		w:   w,
		tty: isTerminal(w) && os.Getenv("TERM") != "dumb",
	})
}

type progressReporter struct {
	sink  progressSink
	verb  string
	unit  string
	total int64
	done  atomic.Int64
	start time.Time

	// delay is how long the operation runs before the first report; interval
	// is the time between reports after it.
	delay, interval time.Duration

	mu      sync.Mutex
	shown   bool
	stop    chan struct{}
	stopped chan struct{}
	once    sync.Once
}

// startProgress reports "<verb> <done>/<total> <unit>" until Finish. total 0
// means the total is unknown. It returns nil (whose methods do nothing) when
// the command does not report progress.
func startProgress(ctx context.Context, verb, unit string, total int) *progressReporter {
	sink, ok := ctx.Value(progressSinkKey{}).(progressSink)
	if !ok || sink.w == nil {
		return nil
	}
	if sink.tty {
		return startProgressEvery(sink, verb, unit, total, time.Second, 200*time.Millisecond)
	}
	return startProgressEvery(sink, verb, unit, total, 2*time.Second, 5*time.Second)
}

func startProgressEvery(sink progressSink, verb, unit string, total int, delay, interval time.Duration) *progressReporter {
	p := &progressReporter{
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

// Add records n more finished items.
func (p *progressReporter) Add(n int) {
	if p == nil {
		return
	}
	p.done.Add(int64(n))
}

// Finish stops reporting. If anything was reported, it prints a last line
// with the final count and the time taken.
func (p *progressReporter) Finish() {
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
		if p.sink.tty {
			fmt.Fprintf(p.sink.w, "\r\x1b[K%s\n", line)
			return
		}
		fmt.Fprintln(p.sink.w, line)
	})
}

func (p *progressReporter) run() {
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

func (p *progressReporter) report() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.shown = true
	if p.sink.tty {
		fmt.Fprintf(p.sink.w, "\r\x1b[K%s", p.line())
		return
	}
	fmt.Fprintln(p.sink.w, p.line())
}

func (p *progressReporter) line() string {
	done := p.done.Load()
	if p.total <= 0 {
		return fmt.Sprintf("%s %d %s", p.verb, done, p.unit)
	}
	if done > p.total {
		done = p.total
	}
	return fmt.Sprintf("%s %d/%d %s (%d%%)", p.verb, done, p.total, p.unit, done*100/p.total)
}
