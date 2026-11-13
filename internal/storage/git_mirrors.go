package storage

import (
	"context"
	"time"
)

// GitMirror is a slice's Git mirror setting and push state
// (design/35_git_mirrors.md).
type GitMirror struct {
	SliceID string
	// Account and Slice name the slice (filled on reads).
	Account string
	Slice   string

	URL         string
	Branch      string
	Username    string
	TokenSecret string
	CreatedBy   string
	CreatedAt   time.Time
	UpdatedAt   time.Time

	// PendingSeq grows whenever something needs pushing; PushedSeq is the
	// PendingSeq the last good push started from. The mirror is pending while
	// PendingSeq > PushedSeq.
	PendingSeq int64
	PushedSeq  int64
	// NextAttemptAt is when a pending mirror may be pushed (retry backoff),
	// and LeaseUntil when a claimed push lapses.
	NextAttemptAt time.Time
	LeaseUntil    time.Time
	Failures      int

	LastAttemptAt    time.Time
	LastPushedAt     time.Time
	LastPushedCommit string
	LastPushedNative string
	LastError        string
}

// Pending says whether the mirror has something to push.
func (m GitMirror) Pending() bool { return m.PendingSeq > m.PushedSeq }

// GitMirrorPush is the outcome of one push.
type GitMirrorPush struct {
	// Seq is the PendingSeq read before the push started.
	Seq int64
	OK  bool
	// On success: the mirror head ("" when there was nothing to push yet) and
	// the native commit it reflects.
	Commit string
	Native string
	// On failure: the error and when to try again.
	Error         string
	NextAttemptAt time.Time
	At            time.Time
}

// GitMirrorStore keeps slice Git mirrors.
type GitMirrorStore interface {
	// GetGitMirror returns a slice's mirror, or ErrNotFound.
	GetGitMirror(ctx context.Context, sliceID string) (*GitMirror, error)
	// PutGitMirror creates or replaces a slice's mirror setting (URL, branch,
	// user name, token secret, creator) and marks it pending with no backoff.
	PutGitMirror(ctx context.Context, mirror GitMirror) (*GitMirror, error)
	// DeleteGitMirror removes a slice's mirror; removing none is not an error.
	DeleteGitMirror(ctx context.Context, sliceID string) error
	// ListGitMirrors lists every mirror (there are few).
	ListGitMirrors(ctx context.Context) ([]GitMirror, error)
	// MarkGitMirrorsPending records that the slices have something to push.
	// With now, it also clears their backoff (a person asked).
	MarkGitMirrorsPending(ctx context.Context, sliceIDs []string, now bool) error
	// ClaimDueGitMirrors leases up to limit pending mirrors whose backoff
	// has passed and whose lease is free.
	ClaimDueGitMirrors(ctx context.Context, limit int, lease time.Duration) ([]GitMirror, error)
	// FinishGitMirror records a push and frees the lease.
	FinishGitMirror(ctx context.Context, sliceID string, push GitMirrorPush) error
}
