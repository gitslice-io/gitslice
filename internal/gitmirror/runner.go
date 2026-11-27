package gitmirror

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"gitslice.io/gitslice/internal/outbound"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

const (
	mirrorLease       = 30 * time.Minute
	mirrorPushTimeout = 25 * time.Minute
	mirrorClaimBatch  = 5
	maxMirrorError    = 2000
)

// mirrorBackoff is the wait after the nth failure in a row; the last value
// repeats.
var mirrorBackoff = []time.Duration{
	time.Minute,
	5 * time.Minute,
	30 * time.Minute,
	2 * time.Hour,
	6 * time.Hour,
	12 * time.Hour,
}

var mirrorBranchPattern = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,200}$`)

// Target describes where, and as whom, a slice mirror is pushed.
type Target struct {
	URL        string
	Branch     string
	Username   string
	Token      string
	Resolve    string
	AllowLocal bool
}

// PushResult reports a mirror push.
type PushResult struct {
	Head           string
	NativeCommitID string
	Exported       int
	NewTags        []string
	Subdir         string
}

// PushFunc pushes a slice's projected history to target.
type PushFunc func(ctx context.Context, account, slice string, target Target) (*PushResult, error)

// Runner queues and runs slice Git mirror pushes. The event dispatcher calls
// FanOut for each recorded event and Drain on each drain.
type Runner struct {
	Mirrors storage.GitMirrorStore
	Slices  storage.SliceStore
	// Push pushes a slice to a target.
	Push PushFunc
	// Request asks for a push where it gets CPU (a request the server sends
	// itself); nil pushes inside Drain.
	Request func(account, slice string)
	// AllowPrivateTargets permits http URLs, local paths and non-public
	// addresses (tests and local servers).
	AllowPrivateTargets bool
	// Nudge asks the dispatcher to drain soon.
	Nudge func()
	// Now replaces the clock (tests).
	Now func() time.Time

	// mu guards Push and Request, which the server may set via SetPusher.
	mu sync.Mutex
}

// SetPusher sets Push and Request while drains may be running.
func (r *Runner) SetPusher(push PushFunc, request func(account, slice string)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Push, r.Request = push, request
}

func (r *Runner) pusher() (PushFunc, func(account, slice string)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Push, r.Request
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now().UTC()
}

// Notify asks the event dispatcher to drain queued mirror pushes soon.
func (r *Runner) Notify() {
	if r != nil && r.Nudge != nil {
		r.Nudge()
	}
}

// FanOut marks the mirrors an event gives something to push: a landing on
// the default target that touches a mirrored slice's paths, or a new tag of
// a mirrored slice.
func (r *Runner) FanOut(ctx context.Context, event storage.Event) {
	if r == nil || r.Mirrors == nil {
		return
	}
	var marked []string
	switch event.Kind {
	case storage.EventChangesetSubmitted:
		if event.TargetRef != "" && event.TargetRef != storage.DefaultTargetRef {
			return
		}
		mirrors, err := r.Mirrors.ListGitMirrors(ctx)
		if err != nil || len(mirrors) == 0 {
			return
		}
		if len(event.ChangedPaths) == 0 {
			for _, mirror := range mirrors {
				marked = append(marked, mirror.SliceID)
			}
			break
		}
		if r.Slices == nil {
			return
		}
		covering, err := r.Slices.CoveringIDsByPath(ctx, event.ChangedPaths)
		if err != nil {
			slog.WarnContext(ctx, "resolve covering slices for git mirrors", "event", event.ID, "error", err)
			return
		}
		covered := map[string]bool{}
		for _, ids := range covering {
			for _, id := range ids {
				covered[id] = true
			}
		}
		for _, mirror := range mirrors {
			if covered[mirror.SliceID] {
				marked = append(marked, mirror.SliceID)
			}
		}
	case storage.EventTagCreated:
		if event.SliceID == "" {
			return
		}
		if _, err := r.Mirrors.GetGitMirror(ctx, event.SliceID); err != nil {
			return
		}
		marked = append(marked, event.SliceID)
	default:
		return
	}
	if len(marked) == 0 {
		return
	}
	if err := r.Mirrors.MarkGitMirrorsPending(ctx, marked, false); err != nil {
		slog.WarnContext(ctx, "mark git mirrors pending", "event", event.ID, "error", err)
	}
}

// Drain claims the mirrors that are due and pushes them (or asks for it).
func (r *Runner) Drain(ctx context.Context) {
	if r == nil || r.Mirrors == nil {
		return
	}
	push, request := r.pusher()
	if push == nil {
		return // the server is still starting
	}
	due, err := r.Mirrors.ClaimDueGitMirrors(ctx, mirrorClaimBatch, mirrorLease)
	if err != nil {
		slog.WarnContext(ctx, "claim git mirrors", "error", err)
		return
	}
	for _, mirror := range due {
		if request != nil {
			request(mirror.Account, mirror.Slice)
			continue
		}
		_ = r.Run(ctx, mirror.Account, mirror.Slice)
	}
}

// Run pushes a slice's mirror and records how it went. A slice without a
// mirror is not an error (it was removed meanwhile).
func (r *Runner) Run(ctx context.Context, account, sliceSlug string) error {
	ctx = context.WithoutCancel(ctx)
	slice, err := r.Slices.Resolve(ctx, &corev1.SliceRef{Account: account, Slice: sliceSlug})
	if err != nil {
		return err
	}
	mirror, err := r.Mirrors.GetGitMirror(ctx, slice.GetId())
	if errors.Is(err, storage.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	seq := mirror.PendingSeq
	res, pushErr := r.push(ctx, account, sliceSlug, slice, mirror)
	now := r.now()
	if pushErr != nil {
		msg := pushErr.Error()
		if len(msg) > maxMirrorError {
			msg = msg[:maxMirrorError] + "…"
		}
		wait := mirrorBackoff[min(mirror.Failures, len(mirrorBackoff)-1)]
		slog.WarnContext(ctx, "git mirror push failed", "slice", account+"/"+sliceSlug, "failures", mirror.Failures+1, "retry_in", wait.String(), "error", msg)
		if err := r.Mirrors.FinishGitMirror(ctx, slice.GetId(), storage.GitMirrorPush{Seq: seq, Error: msg, NextAttemptAt: now.Add(wait), At: now}); err != nil {
			return err
		}
		return pushErr
	}
	if err := r.Mirrors.FinishGitMirror(ctx, slice.GetId(), storage.GitMirrorPush{Seq: seq, OK: true, Commit: res.Head, Native: res.NativeCommitID, At: now}); err != nil {
		return err
	}
	if res.Exported > 0 || len(res.NewTags) > 0 {
		slog.InfoContext(ctx, "git mirror pushed", "slice", account+"/"+sliceSlug, "commits", res.Exported, "tags", strings.Join(res.NewTags, ","), "head", res.Head)
	}
	if after, err := r.Mirrors.GetGitMirror(ctx, slice.GetId()); err == nil && after.Pending() {
		r.Notify() // something landed during the push
	}
	return nil
}

func (r *Runner) push(ctx context.Context, account, sliceSlug string, slice *corev1.Slice, mirror *storage.GitMirror) (*PushResult, error) {
	target := Target{URL: mirror.URL, Branch: mirror.Branch, Username: mirror.Username, AllowLocal: r.AllowPrivateTargets}
	if mirror.TokenSecret != "" {
		token, err := r.SecretAt(ctx, slice, mirror.TokenSecret)
		if err != nil {
			return nil, err
		}
		target.Token = token
	}
	if u, err := url.Parse(mirror.URL); err == nil && (u.Scheme == "https" || u.Scheme == "http") {
		if !r.AllowPrivateTargets && u.Scheme != "https" {
			return nil, fmt.Errorf("the mirror URL must start with https://")
		}
		ip, err := outbound.ResolvePublic(ctx, u.Hostname(), r.AllowPrivateTargets)
		if err != nil {
			return nil, fmt.Errorf("mirror host %s: %w", u.Hostname(), err)
		}
		port := u.Port()
		if port == "" {
			port = map[string]string{"https": "443", "http": "80"}[u.Scheme]
		}
		addr := ip.String()
		if ip.Is6() {
			addr = "[" + addr + "]"
		}
		target.Resolve = u.Hostname() + ":" + port + ":" + addr
	} else if !r.AllowPrivateTargets {
		return nil, fmt.Errorf("the mirror URL must start with https://")
	}
	push, _ := r.pusher()
	if push == nil {
		return nil, errors.New("the server is still starting")
	}
	ctx, cancel := context.WithTimeout(ctx, mirrorPushTimeout)
	defer cancel()
	return push(ctx, account, sliceSlug, target)
}

// SecretAt returns the value of the named secret where the slice's folders
// see it.
func (r *Runner) SecretAt(ctx context.Context, slice *corev1.Slice, name string) (string, error) {
	for _, folder := range slice.GetDefinition().GetIncludedPaths() {
		values, err := r.Slices.SecretValuesAt(ctx, folder)
		if err != nil {
			return "", err
		}
		if value, ok := values[name]; ok {
			return value, nil
		}
	}
	return "", fmt.Errorf("secret %s is not set for this slice's folders", name)
}

// ValidateURL checks a mirror URL as outbound URLs are checked; with private
// targets allowed, an absolute local path is accepted too.
func (r *Runner) ValidateURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if r.AllowPrivateTargets && filepath.IsAbs(raw) {
		return raw, nil
	}
	return outbound.ValidateURL(raw, r.AllowPrivateTargets)
}

// ValidateBranch normalizes and validates a mirror Git branch name.
func ValidateBranch(raw string) (string, error) {
	branch := strings.TrimSpace(raw)
	if branch == "" {
		branch = "main"
	}
	if !mirrorBranchPattern.MatchString(branch) || strings.Contains(branch, "..") || strings.HasPrefix(branch, "-") ||
		strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") || strings.HasSuffix(branch, ".lock") {
		return "", fmt.Errorf("branch %q is not a valid Git branch name", branch)
	}
	return branch, nil
}

// ValidateUsername normalizes and validates a mirror HTTPS username.
func ValidateUsername(raw string) (string, error) {
	username := strings.TrimSpace(raw)
	if len(username) > 100 || strings.ContainsAny(username, ":@/ \t\r\n") {
		return "", errors.New("username may not hold ':', '@', '/' or spaces")
	}
	return username, nil
}
