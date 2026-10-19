package service

import (
	"context"
	"errors"
	"sync"
	"time"

	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	// importHeartbeatEvery is how often an import tells its client it is
	// still working. A step can take a minute (a commit that moves thousands
	// of files), and a client behind a flaky link needs to tell a server that
	// is busy from a connection that is dead.
	importHeartbeatEvery = 10 * time.Second
	// importCommitTimeout bounds one commit's work after the client that
	// asked for it went away.
	importCommitTimeout = 20 * time.Minute
)

// importProgress sends an import's progress to its client. It is safe for
// concurrent use, and a client that has gone away is not an error: the import
// goes on with the commit it is publishing and stops at the next one.
type importProgress struct {
	mu   sync.Mutex
	send func(*corev1.ImportGitRepositoryProgress) error
	gone bool
	last corev1.ImportGitRepositoryProgress
	// heartbeatEvery is importHeartbeatEvery; tests shorten it.
	heartbeatEvery time.Duration
}

func newImportProgress(send func(*corev1.ImportGitRepositoryProgress) error) *importProgress {
	return &importProgress{send: send, heartbeatEvery: importHeartbeatEvery}
}

func (p *importProgress) emit(event *corev1.ImportGitRepositoryProgress) error {
	if p == nil || p.send == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if event.Phase != "working" {
		p.last.Phase, p.last.Message = event.Phase, event.Message
		p.last.Current, p.last.Total = event.Current, event.Total
		p.last.GitCommitId = event.GitCommitId
	}
	if p.gone {
		return nil
	}
	if err := p.send(event); err != nil {
		p.gone = true
	}
	return nil
}

// heartbeat tells the client every importHeartbeatEvery that the import is
// still going, as "working" with the position of the last real event. It
// stops when the returned func is called.
func (p *importProgress) heartbeat() (stop func()) {
	if p == nil || p.send == nil {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(p.heartbeatEvery)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				p.mu.Lock()
				event := &corev1.ImportGitRepositoryProgress{
					Phase:       "working",
					Message:     p.last.Message,
					Current:     p.last.Current,
					Total:       p.last.Total,
					GitCommitId: p.last.GitCommitId,
				}
				if !p.gone {
					if err := p.send(event); err != nil {
						p.gone = true
					}
				}
				p.mu.Unlock()
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}

// importFlights makes imports of the same source into the same place run one
// at a time. A client that reconnects while the server is still finishing the
// commit its previous connection started waits for it, then resumes after it,
// instead of publishing the same commit a second time beside it.
type importFlights struct {
	mu    sync.Mutex
	locks map[string]chan struct{}
}

// acquire waits for the import named key, calling waiting every
// importHeartbeatEvery meanwhile, and returns the func that releases it.
func (f *importFlights) acquire(ctx context.Context, key string, waiting func()) (func(), error) {
	f.mu.Lock()
	if f.locks == nil {
		f.locks = map[string]chan struct{}{}
	}
	lock := f.locks[key]
	if lock == nil {
		lock = make(chan struct{}, 1)
		f.locks[key] = lock
	}
	f.mu.Unlock()
	ticker := time.NewTicker(importHeartbeatEvery)
	defer ticker.Stop()
	for {
		select {
		case lock <- struct{}{}:
			return func() { <-lock }, nil
		case <-ticker.C:
			if waiting != nil {
				waiting()
			}
		case <-ctx.Done():
			return nil, status.FromContextError(ctx.Err()).Err()
		}
	}
}

// importWorkContext is the context a commit's work runs on: it keeps the
// request's values but not its cancellation, so a client that drops while a
// commit is being published does not leave it half done, and it is bounded
// by importCommitTimeout.
func importWorkContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), importCommitTimeout)
}

// importReadError is the status for a git read that failed: the caller's
// mistake, unless the work was cut short.
func importReadError(err error, message string) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return status.Errorf(codes.Unavailable, "%s: %v", message, err)
	}
	return status.Errorf(codes.InvalidArgument, "%s: %v", message, err)
}

// knownBlobs remembers, for one import, the blobs it has already read and
// uploaded, by Git blob id. A commit that moves or copies files, or leaves
// them as they are, then neither reads nor uploads them again.
type knownBlobs map[string]importFile
