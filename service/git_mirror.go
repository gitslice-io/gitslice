package service

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

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"gitslice.io/gitslice/internal/gitcompat"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/webhooks"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// Git mirrors (design/35_git_mirrors.md): the server pushes a slice's
// projection to a repository elsewhere after every landing and tag.

const (
	gitMirrorLease       = 30 * time.Minute
	gitMirrorPushTimeout = 25 * time.Minute
	gitMirrorClaimBatch  = 5
	maxGitMirrorError    = 2000
)

// gitMirrorBackoff is the wait after the nth failure in a row; the last
// value repeats.
var gitMirrorBackoff = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour, 12 * time.Hour}

var gitMirrorBranch = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,200}$`)

// GitMirrors queues and runs slice Git mirror pushes. The webhook
// dispatcher calls FanOut for each recorded event and Drain on each drain.
type GitMirrors struct {
	Mirrors storage.GitMirrorStore
	Slices  storage.SliceStore
	// Push pushes a slice to a target (gitcompat.Projector.PushMirror).
	Push func(ctx context.Context, account, slice string, target gitcompat.MirrorTarget) (*gitcompat.MirrorResult, error)
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

	// mu guards Push and Request, which the server sets after the
	// dispatcher has started (SetPusher).
	mu sync.Mutex
}

// SetPusher sets Push and Request while drains may be running.
func (m *GitMirrors) SetPusher(push func(ctx context.Context, account, slice string, target gitcompat.MirrorTarget) (*gitcompat.MirrorResult, error), request func(account, slice string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Push, m.Request = push, request
}

func (m *GitMirrors) pusher() (func(ctx context.Context, account, slice string, target gitcompat.MirrorTarget) (*gitcompat.MirrorResult, error), func(account, slice string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.Push, m.Request
}

func (m *GitMirrors) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now().UTC()
}

func (m *GitMirrors) nudge() {
	if m != nil && m.Nudge != nil {
		m.Nudge()
	}
}

// FanOut marks the mirrors an event gives something to push: a landing on
// the default target that touches a mirrored slice's paths, or a new tag of
// a mirrored slice.
func (m *GitMirrors) FanOut(ctx context.Context, event storage.WebhookEvent) {
	if m == nil || m.Mirrors == nil {
		return
	}
	var marked []string
	switch event.Kind {
	case storage.WebhookEventChangesetSubmitted:
		if event.TargetRef != "" && event.TargetRef != storage.DefaultTargetRef {
			return
		}
		mirrors, err := m.Mirrors.ListGitMirrors(ctx)
		if err != nil || len(mirrors) == 0 {
			return
		}
		for _, mirror := range mirrors {
			slice, err := m.Slices.Get(ctx, mirror.SliceID)
			if err != nil {
				continue
			}
			if touchesSlice(slice.GetDefinition().GetIncludedPaths(), event.ChangedPaths) {
				marked = append(marked, mirror.SliceID)
			}
		}
	case storage.WebhookEventTagCreated:
		if event.SliceID == "" {
			return
		}
		if _, err := m.Mirrors.GetGitMirror(ctx, event.SliceID); err != nil {
			return
		}
		marked = append(marked, event.SliceID)
	default:
		return
	}
	if len(marked) == 0 {
		return
	}
	if err := m.Mirrors.MarkGitMirrorsPending(ctx, marked, false); err != nil {
		slog.WarnContext(ctx, "mark git mirrors pending", "event", event.ID, "error", err)
	}
}

// touchesSlice says whether any changed path is under one of the slice's
// included paths. Without changed paths it assumes so.
func touchesSlice(included, changed []string) bool {
	if len(changed) == 0 {
		return true
	}
	for _, prefix := range included {
		prefix = "/" + strings.Trim(prefix, "/")
		for _, p := range changed {
			p = "/" + strings.Trim(p, "/")
			if prefix == "/" || p == prefix || strings.HasPrefix(p, prefix+"/") {
				return true
			}
		}
	}
	return false
}

// Drain claims the mirrors that are due and pushes them (or asks for it).
func (m *GitMirrors) Drain(ctx context.Context) {
	if m == nil || m.Mirrors == nil {
		return
	}
	push, request := m.pusher()
	if push == nil {
		return // the server is still starting
	}
	due, err := m.Mirrors.ClaimDueGitMirrors(ctx, gitMirrorClaimBatch, gitMirrorLease)
	if err != nil {
		slog.WarnContext(ctx, "claim git mirrors", "error", err)
		return
	}
	for _, mirror := range due {
		if request != nil {
			request(mirror.Account, mirror.Slice)
			continue
		}
		_ = m.Run(ctx, mirror.Account, mirror.Slice)
	}
}

// Run pushes a slice's mirror and records how it went. A slice without a
// mirror is not an error (it was removed meanwhile).
func (m *GitMirrors) Run(ctx context.Context, account, sliceSlug string) error {
	ctx = context.WithoutCancel(ctx)
	slice, err := m.Slices.Resolve(ctx, &corev1.SliceRef{Account: account, Slice: sliceSlug})
	if err != nil {
		return err
	}
	mirror, err := m.Mirrors.GetGitMirror(ctx, slice.GetId())
	if errors.Is(err, storage.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	seq := mirror.PendingSeq
	res, pushErr := m.push(ctx, account, sliceSlug, slice, mirror)
	now := m.now()
	if pushErr != nil {
		msg := pushErr.Error()
		if len(msg) > maxGitMirrorError {
			msg = msg[:maxGitMirrorError] + "…"
		}
		wait := gitMirrorBackoff[min(mirror.Failures, len(gitMirrorBackoff)-1)]
		slog.WarnContext(ctx, "git mirror push failed", "slice", account+"/"+sliceSlug, "failures", mirror.Failures+1, "retry_in", wait.String(), "error", msg)
		if err := m.Mirrors.FinishGitMirror(ctx, slice.GetId(), storage.GitMirrorPush{Seq: seq, Error: msg, NextAttemptAt: now.Add(wait), At: now}); err != nil {
			return err
		}
		return pushErr
	}
	if err := m.Mirrors.FinishGitMirror(ctx, slice.GetId(), storage.GitMirrorPush{Seq: seq, OK: true, Commit: res.Head, Native: res.NativeCommitID, At: now}); err != nil {
		return err
	}
	if res.Exported > 0 || len(res.NewTags) > 0 {
		slog.InfoContext(ctx, "git mirror pushed", "slice", account+"/"+sliceSlug, "commits", res.Exported, "tags", strings.Join(res.NewTags, ","), "head", res.Head)
	}
	if after, err := m.Mirrors.GetGitMirror(ctx, slice.GetId()); err == nil && after.Pending() {
		m.nudge() // something landed during the push
	}
	return nil
}

func (m *GitMirrors) push(ctx context.Context, account, sliceSlug string, slice *corev1.Slice, mirror *storage.GitMirror) (*gitcompat.MirrorResult, error) {
	target := gitcompat.MirrorTarget{URL: mirror.URL, Branch: mirror.Branch, Username: mirror.Username, AllowLocal: m.AllowPrivateTargets}
	if mirror.TokenSecret != "" {
		token, err := m.secretAt(ctx, slice, mirror.TokenSecret)
		if err != nil {
			return nil, err
		}
		target.Token = token
	}
	if u, err := url.Parse(mirror.URL); err == nil && (u.Scheme == "https" || u.Scheme == "http") {
		if !m.AllowPrivateTargets && u.Scheme != "https" {
			return nil, fmt.Errorf("the mirror URL must start with https://")
		}
		ip, err := webhooks.ResolvePublic(ctx, u.Hostname(), m.AllowPrivateTargets)
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
	} else if !m.AllowPrivateTargets {
		return nil, fmt.Errorf("the mirror URL must start with https://")
	}
	push, _ := m.pusher()
	if push == nil {
		return nil, errors.New("the server is still starting")
	}
	ctx, cancel := context.WithTimeout(ctx, gitMirrorPushTimeout)
	defer cancel()
	return push(ctx, account, sliceSlug, target)
}

// secretAt returns the value of the named secret where the slice's folders
// see it.
func (m *GitMirrors) secretAt(ctx context.Context, slice *corev1.Slice, name string) (string, error) {
	for _, folder := range slice.GetDefinition().GetIncludedPaths() {
		values, err := m.Slices.SecretValuesAt(ctx, folder)
		if err != nil {
			return "", err
		}
		if value, ok := values[name]; ok {
			return value, nil
		}
	}
	return "", fmt.Errorf("secret %s is not set for this slice's folders", name)
}

// validateGitMirrorURL checks a mirror URL as webhook URLs are checked; with
// private targets allowed, a local path is accepted too.
func (m *GitMirrors) validateGitMirrorURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if m.AllowPrivateTargets && filepath.IsAbs(raw) {
		return raw, nil
	}
	return webhooks.ValidateURL(raw, m.AllowPrivateTargets)
}

func gitMirrorProto(mirror *storage.GitMirror, slice *corev1.Slice) *corev1.SliceGitMirror {
	out := &corev1.SliceGitMirror{
		Slice:                  &corev1.SliceRef{Account: slice.GetRef().GetAccount(), Slice: slice.GetRef().GetSlice()},
		Url:                    mirror.URL,
		Branch:                 mirror.Branch,
		Username:               mirror.Username,
		TokenSecret:            mirror.TokenSecret,
		Pending:                mirror.Pending(),
		LastAttemptAt:          formatTime(mirror.LastAttemptAt),
		LastPushedAt:           formatTime(mirror.LastPushedAt),
		LastPushedCommit:       mirror.LastPushedCommit,
		LastPushedNativeCommit: mirror.LastPushedNative,
		LastError:              mirror.LastError,
		Failures:               int32(mirror.Failures),
		CreatedBy:              mirror.CreatedBy,
		UpdatedAt:              formatTime(mirror.UpdatedAt),
	}
	if mirror.Failures > 0 && mirror.Pending() {
		out.NextAttemptAt = formatTime(mirror.NextAttemptAt)
	}
	if root, err := gitcompat.MirrorSubdir(slice); err == nil && root != "" {
		out.Root = "/" + root
	}
	return out
}

// gitMirrorSlice resolves the slice of a mirror request and checks that the
// caller owns or administers its account.
func (s *SliceService) gitMirrorSlice(ctx context.Context, ref *corev1.SliceRef) (string, *corev1.Slice, error) {
	if s.GitMirrors == nil || s.GitMirrors.Mirrors == nil {
		return "", nil, status.Error(codes.Unimplemented, "this server has no Git mirrors")
	}
	subjectID, err := requireSubject(ctx)
	if err != nil {
		return "", nil, err
	}
	normalized, err := normalizeServiceSliceRef(ref)
	if err != nil {
		return "", nil, err
	}
	slice, err := s.Slices.Resolve(ctx, normalized)
	if err != nil {
		return "", nil, grpcError(err)
	}
	if err := authorizeSliceOwner(ctx, s.Auth, subjectID, slice); err != nil {
		return "", nil, err
	}
	return subjectID, slice, nil
}

// GetSliceGitMirror returns the slice's Git mirror.
func (s *SliceService) GetSliceGitMirror(ctx context.Context, req *corev1.GetSliceGitMirrorRequest) (*corev1.SliceGitMirror, error) {
	_, slice, err := s.gitMirrorSlice(ctx, req.GetSlice())
	if err != nil {
		return nil, err
	}
	mirror, err := s.GitMirrors.Mirrors.GetGitMirror(ctx, slice.GetId())
	if errors.Is(err, storage.ErrNotFound) {
		return nil, status.Error(codes.NotFound, "this slice has no Git mirror")
	}
	if err != nil {
		return nil, grpcError(err)
	}
	return gitMirrorProto(mirror, slice), nil
}

// SetSliceGitMirror creates or replaces the slice's Git mirror.
func (s *SliceService) SetSliceGitMirror(ctx context.Context, req *corev1.SetSliceGitMirrorRequest) (*corev1.SliceGitMirror, error) {
	subjectID, slice, err := s.gitMirrorSlice(ctx, req.GetSlice())
	if err != nil {
		return nil, err
	}
	target, err := s.GitMirrors.validateGitMirrorURL(req.GetUrl())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "url: %v", err)
	}
	branch := strings.TrimSpace(req.GetBranch())
	if branch == "" {
		branch = "main"
	}
	if !gitMirrorBranch.MatchString(branch) || strings.Contains(branch, "..") || strings.HasPrefix(branch, "-") ||
		strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") || strings.HasSuffix(branch, ".lock") {
		return nil, status.Errorf(codes.InvalidArgument, "branch %q is not a valid Git branch name", branch)
	}
	username := strings.TrimSpace(req.GetUsername())
	if len(username) > 100 || strings.ContainsAny(username, ":@/ \t\r\n") {
		return nil, status.Error(codes.InvalidArgument, "username may not hold ':', '@', '/' or spaces")
	}
	secret := strings.TrimSpace(req.GetTokenSecret())
	if secret != "" {
		if !storage.ValidSliceSecretName(secret) {
			return nil, status.Errorf(codes.InvalidArgument, "token_secret %q must match ^[A-Z_][A-Z0-9_]*$", secret)
		}
		if _, err := s.GitMirrors.secretAt(ctx, slice, secret); err != nil {
			return nil, status.Error(codes.FailedPrecondition, err.Error())
		}
	}
	mirror, err := s.GitMirrors.Mirrors.PutGitMirror(ctx, storage.GitMirror{
		SliceID: slice.GetId(), URL: target, Branch: branch, Username: username, TokenSecret: secret, CreatedBy: subjectID,
	})
	if err != nil {
		return nil, grpcError(err)
	}
	s.GitMirrors.nudge()
	return gitMirrorProto(mirror, slice), nil
}

// DeleteSliceGitMirror stops mirroring the slice.
func (s *SliceService) DeleteSliceGitMirror(ctx context.Context, req *corev1.DeleteSliceGitMirrorRequest) (*corev1.Empty, error) {
	_, slice, err := s.gitMirrorSlice(ctx, req.GetSlice())
	if err != nil {
		return nil, err
	}
	if err := s.GitMirrors.Mirrors.DeleteGitMirror(ctx, slice.GetId()); err != nil {
		return nil, grpcError(err)
	}
	return &corev1.Empty{}, nil
}

// PushSliceGitMirror pushes the slice's mirror now.
func (s *SliceService) PushSliceGitMirror(ctx context.Context, req *corev1.PushSliceGitMirrorRequest) (*corev1.SliceGitMirror, error) {
	_, slice, err := s.gitMirrorSlice(ctx, req.GetSlice())
	if err != nil {
		return nil, err
	}
	if _, err := s.GitMirrors.Mirrors.GetGitMirror(ctx, slice.GetId()); errors.Is(err, storage.ErrNotFound) {
		return nil, status.Error(codes.NotFound, "this slice has no Git mirror")
	} else if err != nil {
		return nil, grpcError(err)
	}
	if err := s.GitMirrors.Mirrors.MarkGitMirrorsPending(ctx, []string{slice.GetId()}, true); err != nil {
		return nil, grpcError(err)
	}
	s.GitMirrors.nudge()
	mirror, err := s.GitMirrors.Mirrors.GetGitMirror(ctx, slice.GetId())
	if err != nil {
		return nil, grpcError(err)
	}
	return gitMirrorProto(mirror, slice), nil
}
