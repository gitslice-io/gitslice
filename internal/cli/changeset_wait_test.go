package cli

import (
	"bytes"
	"context"
	"errors"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakePublishPollServer struct {
	corev1.UnimplementedChangesetServiceServer
	corev1.UnimplementedRepositoryServiceServer
	corev1.UnimplementedCheckServiceServer

	mu            sync.Mutex
	getStates     []*corev1.Changeset
	getCalls      int
	getRefCalls   int
	submitPending int
	submitFails   bool
	submitCalls   int
}

func (f *fakePublishPollServer) GetChangeset(_ context.Context, req *corev1.GetChangesetRequest) (*corev1.Changeset, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	idx := f.getCalls
	f.getCalls++
	if len(f.getStates) == 0 {
		return &corev1.Changeset{Id: req.GetChangesetId(), Status: "submitted", CommitId: "sha256:landed"}, nil
	}
	if idx >= len(f.getStates) {
		idx = len(f.getStates) - 1
	}
	cs := f.getStates[idx]
	return &corev1.Changeset{
		Id:                firstNonEmpty(cs.GetId(), req.GetChangesetId()),
		Status:            cs.GetStatus(),
		CommitId:          cs.GetCommitId(),
		CurrentPatchsetId: firstNonEmpty(cs.GetCurrentPatchsetId(), "ps_1"),
		Title:             cs.GetTitle(),
	}, nil
}

func (f *fakePublishPollServer) GetRef(_ context.Context, req *corev1.GetRefRequest) (*corev1.Ref, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getRefCalls++
	return &corev1.Ref{Name: req.GetRefName(), CommitId: "sha256:refhead"}, nil
}

func (f *fakePublishPollServer) SubmitChangeset(_ context.Context, _ *corev1.SubmitChangesetRequest) (*corev1.SubmitChangesetResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.submitCalls++
	if f.submitFails {
		return nil, status.Error(codes.FailedPrecondition, `conflict: required check "unit" is failing for current patchset`)
	}
	if f.submitCalls <= f.submitPending {
		return nil, status.Error(codes.FailedPrecondition, `conflict: required check "unit" has no result for current patchset`)
	}
	return &corev1.SubmitChangesetResponse{
		Status:         "submitted",
		CommitId:       "sha256:landed",
		NewRefCommitId: "sha256:refhead",
		TargetRef:      "refs/global/main",
	}, nil
}

func (f *fakePublishPollServer) ListCheckRuns(_ context.Context, _ *corev1.ListCheckRunsRequest) (*corev1.ListCheckRunsResponse, error) {
	return &corev1.ListCheckRunsResponse{}, nil
}

func startFakePublishPollServer(t *testing.T, srv *fakePublishPollServer) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	grpcServer := grpc.NewServer()
	corev1.RegisterChangesetServiceServer(grpcServer, srv)
	corev1.RegisterRepositoryServiceServer(grpcServer, srv)
	corev1.RegisterCheckServiceServer(grpcServer, srv)
	errCh := make(chan error, 1)
	go func() { errCh <- grpcServer.Serve(lis) }()
	t.Cleanup(func() {
		grpcServer.Stop()
		if err := <-errCh; err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			t.Errorf("fake server: %v", err)
		}
	})
	return lis.Addr().String()
}

func TestPublishPollDelaySchedule(t *testing.T) {
	cfg := Runner{}.resolveChangesetWaitConfig()

	var pendingGot []time.Duration
	for step := 0; step < 8; step++ {
		pendingGot = append(pendingGot, cfg.publishPollDelay(step, "pending_publish"))
	}
	pendingWant := []time.Duration{
		50 * time.Millisecond,
		100 * time.Millisecond,
		250 * time.Millisecond,
		500 * time.Millisecond,
		time.Second,
		2 * time.Second,
		2 * time.Second,
		2 * time.Second,
	}
	if !reflect.DeepEqual(pendingGot, pendingWant) {
		t.Fatalf("pending_publish schedule = %v, want %v", pendingGot, pendingWant)
	}

	var draftGot []time.Duration
	for step := 0; step < 9; step++ {
		draftGot = append(draftGot, cfg.publishPollDelay(step, "draft"))
	}
	draftWant := []time.Duration{
		50 * time.Millisecond,
		100 * time.Millisecond,
		250 * time.Millisecond,
		500 * time.Millisecond,
		time.Second,
		2 * time.Second,
		5 * time.Second,
		5 * time.Second,
		5 * time.Second,
	}
	if !reflect.DeepEqual(draftGot, draftWant) {
		t.Fatalf("draft schedule = %v, want %v", draftGot, draftWant)
	}
}

func TestWaitForChangesetPublishedBackoffAndDraftTransition(t *testing.T) {
	srv := &fakePublishPollServer{
		getStates: []*corev1.Changeset{
			{Status: "draft"},
			{Status: "draft"},
			{Status: "draft"},
			{Status: "draft"},
			{Status: "draft"},
			{Status: "draft"},
			{Status: "draft"},
			{Status: "draft"},
			{Status: "pending_publish"},
			{Status: "pending_publish"},
			{Status: "submitted", CommitId: "sha256:landed"},
		},
	}
	addr := startFakePublishPollServer(t, srv)
	conn, err := dial(context.Background(), addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	var sleeps []time.Duration
	var stderr bytes.Buffer
	r := Runner{
		Stdout: &bytes.Buffer{},
		Stderr: &stderr,
		changesetWait: &changesetWaitConfig{
			sleep: func(_ context.Context, d time.Duration) error {
				sleeps = append(sleeps, d)
				return nil
			},
		},
	}

	commitID, refCommitID, err := r.waitForChangesetPublished(context.Background(), conn, UserConfig{Token: fakeToken}, "cs_1", "cs_1", time.Minute, true)
	if err != nil {
		t.Fatalf("waitForChangesetPublished failed: %v", err)
	}
	if commitID != "sha256:landed" || refCommitID != "sha256:refhead" {
		t.Fatalf("got (%q, %q), want (sha256:landed, sha256:refhead)", commitID, refCommitID)
	}
	wantSleeps := []time.Duration{
		// 8 draft polls -> capped at 5s
		50 * time.Millisecond,
		100 * time.Millisecond,
		250 * time.Millisecond,
		500 * time.Millisecond,
		time.Second,
		2 * time.Second,
		5 * time.Second,
		5 * time.Second,
		// status transitions draft -> pending_publish, resetting backoff ramp
		50 * time.Millisecond,
		100 * time.Millisecond,
	}
	if !reflect.DeepEqual(sleeps, wantSleeps) {
		t.Fatalf("sleeps = %v, want %v", sleeps, wantSleeps)
	}
	wantProgress := "changeset cs_1 status: draft\nchangeset cs_1 status: pending_publish\nchangeset cs_1 status: submitted\n"
	if stderr.String() != wantProgress {
		t.Fatalf("stderr = %q, want %q", stderr.String(), wantProgress)
	}
}

func TestWaitForChangesetPublishedErrorsAndTimeout(t *testing.T) {
	t.Run("terminal non-submitted status fails", func(t *testing.T) {
		srv := &fakePublishPollServer{
			getStates: []*corev1.Changeset{{Status: "abandoned"}},
		}
		conn, err := dial(context.Background(), startFakePublishPollServer(t, srv))
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()

		r := Runner{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
		_, _, err = r.waitForChangesetPublished(context.Background(), conn, UserConfig{}, "cs_1", "cs_1", time.Second, false)
		if !isUserErrorCode(err, "publish_failed") {
			t.Fatalf("expected publish_failed error, got %v", err)
		}
	})

	t.Run("timeout returns publish_timeout", func(t *testing.T) {
		srv := &fakePublishPollServer{
			getStates: []*corev1.Changeset{{Status: "pending_publish"}},
		}
		conn, err := dial(context.Background(), startFakePublishPollServer(t, srv))
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()

		r := Runner{
			Stdout: &bytes.Buffer{},
			Stderr: &bytes.Buffer{},
			changesetWait: &changesetWaitConfig{
				sleep: func(_ context.Context, _ time.Duration) error {
					return context.DeadlineExceeded
				},
			},
		}
		_, _, err = r.waitForChangesetPublished(context.Background(), conn, UserConfig{}, "cs_1", "cs_1", 50*time.Millisecond, false)
		if !isUserErrorCode(err, "publish_timeout") {
			t.Fatalf("expected publish_timeout error, got %v", err)
		}
	})

	t.Run("parent cancel returns context.Canceled", func(t *testing.T) {
		srv := &fakePublishPollServer{
			getStates: []*corev1.Changeset{{Status: "pending_publish"}},
		}
		conn, err := dial(context.Background(), startFakePublishPollServer(t, srv))
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()

		ctx, cancel := context.WithCancel(context.Background())
		r := Runner{
			Stdout: &bytes.Buffer{},
			Stderr: &bytes.Buffer{},
			changesetWait: &changesetWaitConfig{
				sleep: func(_ context.Context, _ time.Duration) error {
					cancel()
					return context.Canceled
				},
			},
		}
		_, _, err = r.waitForChangesetPublished(ctx, conn, UserConfig{}, "cs_1", "cs_1", time.Minute, false)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	})
}

func TestRunChangesetStatusWatchDraftAvoidsDuplicateGetsAndCapsAt5s(t *testing.T) {
	srv := &fakePublishPollServer{
		getStates: []*corev1.Changeset{
			{Id: "cs_watch", Status: "draft", Title: "watch draft"},
			{Id: "cs_watch", Status: "draft", Title: "watch draft"},
			{Id: "cs_watch", Status: "draft", Title: "watch draft"},
			{Id: "cs_watch", Status: "draft", Title: "watch draft"},
			{Id: "cs_watch", Status: "draft", Title: "watch draft"},
			{Id: "cs_watch", Status: "draft", Title: "watch draft"},
			{Id: "cs_watch", Status: "draft", Title: "watch draft"},
			{Id: "cs_watch", Status: "submitted", CommitId: "sha256:landed", Title: "watch draft"},
		},
	}
	addr := startFakePublishPollServer(t, srv)
	var sleeps []time.Duration
	var stdout, stderr bytes.Buffer
	r := Runner{
		Home:   t.TempDir(),
		Dir:    t.TempDir(),
		Stdout: &stdout,
		Stderr: &stderr,
		changesetWait: &changesetWaitConfig{
			sleep: func(_ context.Context, d time.Duration) error {
				sleeps = append(sleeps, d)
				return nil
			},
		},
	}
	if err := r.writeUserConfig(UserConfig{ServerAddr: addr, Token: fakeToken, SubjectID: "user_alice"}); err != nil {
		t.Fatal(err)
	}

	if err := r.runChangesetStatus(context.Background(), commandOptions{Format: "text"}, "cs_watch", true, time.Minute); err != nil {
		t.Fatalf("runChangesetStatus --watch failed: %v", err)
	}
	if srv.getCalls != len(srv.getStates) {
		t.Fatalf("getCalls = %d, want %d (no duplicate initial or trailing GetChangeset)", srv.getCalls, len(srv.getStates))
	}
	if srv.getRefCalls != 0 {
		t.Fatalf("getRefCalls = %d, want 0 for cs status --watch", srv.getRefCalls)
	}
	if len(sleeps) != 7 || sleeps[len(sleeps)-1] != 5*time.Second {
		t.Fatalf("sleeps = %v, want 7 sleeps ending at 5s", sleeps)
	}
	if !strings.Contains(stdout.String(), "status: submitted") {
		t.Fatalf("stdout = %q, want submitted status", stdout.String())
	}
}

func TestRunChangesetSubmitUsesSharedPendingCheckRetry(t *testing.T) {
	srv := &fakePublishPollServer{
		getStates:     []*corev1.Changeset{{Id: "cs_direct", Status: "draft", CurrentPatchsetId: "ps_1"}},
		submitPending: 2,
	}
	addr := startFakePublishPollServer(t, srv)
	var sleeps []time.Duration
	var stdout, stderr bytes.Buffer
	r := Runner{
		Home:   t.TempDir(),
		Dir:    t.TempDir(),
		Stdout: &stdout,
		Stderr: &stderr,
		changesetWait: &changesetWaitConfig{
			checkRetryDelay: 250 * time.Millisecond,
			sleep: func(_ context.Context, d time.Duration) error {
				sleeps = append(sleeps, d)
				return nil
			},
		},
	}
	if err := r.writeUserConfig(UserConfig{ServerAddr: addr, Token: fakeToken, SubjectID: "user_alice"}); err != nil {
		t.Fatal(err)
	}

	if err := r.runChangesetSubmit(context.Background(), commandOptions{Format: "text"}, "cs_direct", true, time.Minute); err != nil {
		t.Fatalf("runChangesetSubmit failed: %v", err)
	}
	if srv.submitCalls != 3 {
		t.Fatalf("submitCalls = %d, want 3", srv.submitCalls)
	}
	if len(sleeps) != 2 || sleeps[0] != 250*time.Millisecond || sleeps[1] != 250*time.Millisecond {
		t.Fatalf("sleeps = %v, want two 250ms retries", sleeps)
	}
	if strings.Count(stderr.String(), `waiting for required check "unit" to pass...`) != 1 {
		t.Fatalf("stderr = %q", stderr.String())
	}

	// Non-pending failure returns immediately without sleeping.
	srv.submitCalls = 0
	srv.submitFails = true
	sleeps = nil
	err := r.runChangesetSubmit(context.Background(), commandOptions{Format: "text"}, "cs_direct", true, time.Minute)
	if err == nil || !strings.Contains(err.Error(), "is failing") {
		t.Fatalf("expected immediate failing-check error, got %v", err)
	}
	if srv.submitCalls != 1 || len(sleeps) != 0 {
		t.Fatalf("failing check retried: submitCalls=%d sleeps=%v", srv.submitCalls, sleeps)
	}
}
