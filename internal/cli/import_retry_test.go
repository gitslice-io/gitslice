package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

func fastImportRetries(t *testing.T) {
	t.Helper()
	base, max := importRetryBaseDelay, importRetryMaxDelay
	importRetryBaseDelay, importRetryMaxDelay = time.Millisecond, 4*time.Millisecond
	t.Cleanup(func() { importRetryBaseDelay, importRetryMaxDelay = base, max })
}

var errDropped = grpcstatus.Error(codes.Unavailable, "error reading from server: EOF")

// Each attempt that publishes more commits resets the count, so a long
// import over a flaky link keeps going as long as it moves.
func TestImportRetriesWhileTheImportMakesProgress(t *testing.T) {
	fastImportRetries(t)
	var out bytes.Buffer
	attempts := 0
	res, err := Runner{}.importWithRetries(context.Background(), importRetryPolicy{retries: 2, resume: true}, &out, func(progress *int64) (*corev1.ImportGitRepositoryResponse, error) {
		attempts++
		if attempts < 6 {
			*progress = int64(attempts * 100)
			return nil, errDropped
		}
		return &corev1.ImportGitRepositoryResponse{FinalCommitId: "done"}, nil
	})
	if err != nil || res.GetFinalCommitId() != "done" {
		t.Fatalf("import = %v, %v; want it to finish after 6 attempts", res, err)
	}
	if !strings.Contains(out.String(), "connection lost at commit 500 (error reading from server: EOF); reconnecting") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestImportGivesUpAfterRetriesWithoutProgress(t *testing.T) {
	fastImportRetries(t)
	attempts := 0
	_, err := Runner{}.importWithRetries(context.Background(), importRetryPolicy{retries: 3, resume: true}, nil, func(progress *int64) (*corev1.ImportGitRepositoryResponse, error) {
		attempts++
		return nil, errDropped
	})
	if !errors.Is(err, errDropped) || attempts != 4 {
		t.Fatalf("err %v after %d attempts, want the drop after 1+3", err, attempts)
	}
}

func TestImportDoesNotRetryItsOwnErrors(t *testing.T) {
	fastImportRetries(t)
	attempts := 0
	bad := grpcstatus.Error(codes.InvalidArgument, "clone git repository: remote: Repository not found.")
	_, err := Runner{}.importWithRetries(context.Background(), importRetryPolicy{retries: 5, resume: true}, nil, func(*int64) (*corev1.ImportGitRepositoryResponse, error) {
		attempts++
		return nil, bad
	})
	if err != bad || attempts != 1 {
		t.Fatalf("err %v after %d attempts, want no retry", err, attempts)
	}
}

// Without --resume a retry would publish the same commits again, so it
// stops once anything was published and says how to continue.
func TestImportWithoutResumeStopsOncePublished(t *testing.T) {
	fastImportRetries(t)
	attempts := 0
	_, err := Runner{}.importWithRetries(context.Background(), importRetryPolicy{retries: 5}, nil, func(progress *int64) (*corev1.ImportGitRepositoryResponse, error) {
		attempts++
		if attempts == 1 {
			return nil, errDropped // before publishing: retried
		}
		*progress = 3
		return nil, errDropped
	})
	if attempts != 2 || err == nil || !strings.Contains(err.Error(), "3 commit(s) were published") {
		t.Fatalf("err %v after %d attempts", err, attempts)
	}
}

func TestImportRetryableErrors(t *testing.T) {
	cases := map[error]bool{
		errDropped: true,
		grpcstatus.Error(codes.Internal, "stream terminated by RST_STREAM with error code: INTERNAL_ERROR"): true,
		grpcstatus.Error(codes.Canceled, "context canceled"):                                                true,
		grpcstatus.Error(codes.InvalidArgument, "import mode must be shallow or deep"):                      false,
		grpcstatus.Error(codes.PermissionDenied, "denied"):                                                  false,
		errors.New("plain"): false,
	}
	for err, want := range cases {
		if got := importRetryable(err); got != want {
			t.Errorf("importRetryable(%v) = %v, want %v", err, got, want)
		}
	}
}

// fakeImportClient serves one ImportGitRepositoryStream from a script.
type fakeImportClient struct {
	corev1.RepositoryServiceClient
	serve func(ctx context.Context, send func(*corev1.ImportGitRepositoryProgress)) error
}

func (f fakeImportClient) ImportGitRepositoryStream(ctx context.Context, _ *corev1.ImportGitRepositoryRequest, _ ...grpc.CallOption) (corev1.RepositoryService_ImportGitRepositoryStreamClient, error) {
	events := make(chan *corev1.ImportGitRepositoryProgress, 16)
	done := make(chan error, 1)
	go func() {
		done <- f.serve(ctx, func(e *corev1.ImportGitRepositoryProgress) { events <- e })
		close(events)
	}()
	return &fakeImportStream{ctx: ctx, events: events, done: done}, nil
}

type fakeImportStream struct {
	grpc.ClientStream
	ctx    context.Context
	events chan *corev1.ImportGitRepositoryProgress
	done   chan error
}

func (s *fakeImportStream) Recv() (*corev1.ImportGitRepositoryProgress, error) {
	select {
	case e, ok := <-s.events:
		if !ok {
			if err := <-s.done; err != nil {
				return nil, err
			}
			return nil, io.EOF // a stream that ends cleanly
		}
		return e, nil
	case <-s.ctx.Done():
		return nil, grpcstatus.FromContextError(s.ctx.Err()).Err()
	}
}

func fastStall(t *testing.T) {
	t.Helper()
	saved := importStallTimeout
	importStallTimeout = 80 * time.Millisecond
	t.Cleanup(func() { importStallTimeout = saved })
}

// After a heartbeat, a server that goes silent is given up on, and the
// attempt ends in a retryable error instead of waiting for ever.
func TestImportAttemptGivesUpOnAServerThatWentSilent(t *testing.T) {
	fastStall(t)
	client := fakeImportClient{serve: func(ctx context.Context, send func(*corev1.ImportGitRepositoryProgress)) error {
		send(&corev1.ImportGitRepositoryProgress{Phase: "published", Current: 7, Total: 9})
		send(&corev1.ImportGitRepositoryProgress{Phase: "working", Current: 7, Total: 9})
		<-ctx.Done() // then nothing: the connection is dead
		return ctx.Err()
	}}
	progress := int64(0)
	start := time.Now()
	_, err := importAttempt(context.Background(), client, &corev1.ImportGitRepositoryRequest{}, &importProgressReporter{quiet: true}, &progress)
	if !errors.Is(err, errImportStalled) || !importRetryable(err) {
		t.Fatalf("err = %v, want a retryable stall", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("took %s to notice", took)
	}
	if progress != 7 {
		t.Fatalf("progress = %d, want the 7 commits published before the stall", progress)
	}
}

// An older server sends no heartbeats; its long steps are silent, and a
// long silence from it is not a stall.
func TestImportAttemptWaitsForAServerWithoutHeartbeats(t *testing.T) {
	fastStall(t)
	client := fakeImportClient{serve: func(ctx context.Context, send func(*corev1.ImportGitRepositoryProgress)) error {
		send(&corev1.ImportGitRepositoryProgress{Phase: "uploading_blobs", Current: 1, Total: 1})
		time.Sleep(5 * importStallTimeout) // a long, silent step
		send(&corev1.ImportGitRepositoryProgress{Phase: "done", Result: &corev1.ImportGitRepositoryResponse{FinalCommitId: "c1"}})
		return nil
	}}
	progress := int64(0)
	res, err := importAttempt(context.Background(), client, &corev1.ImportGitRepositoryRequest{}, &importProgressReporter{quiet: true}, &progress)
	if err != nil || res.GetFinalCommitId() != "c1" {
		t.Fatalf("import = %v, %v; want it to finish", res, err)
	}
}

// Pressing Ctrl-C is the user's decision, not a stall to retry.
func TestImportAttemptStopsWhenTheUserCancels(t *testing.T) {
	fastStall(t)
	ctx, cancel := context.WithCancel(context.Background())
	client := fakeImportClient{serve: func(ctx context.Context, send func(*corev1.ImportGitRepositoryProgress)) error {
		send(&corev1.ImportGitRepositoryProgress{Phase: "working"})
		<-ctx.Done()
		return ctx.Err()
	}}
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	progress := int64(0)
	_, err := importAttempt(ctx, client, &corev1.ImportGitRepositoryRequest{}, &importProgressReporter{quiet: true}, &progress)
	if errors.Is(err, errImportStalled) || err == nil {
		t.Fatalf("err = %v, want the cancellation, not a stall", err)
	}
}

func TestImportReporterNamesWhatItWaitsFor(t *testing.T) {
	var out bytes.Buffer
	r := importProgressReporter{w: &out}
	r.report(&corev1.ImportGitRepositoryProgress{Phase: "waiting"})
	r.report(&corev1.ImportGitRepositoryProgress{Phase: "working", Current: 3, Total: 9}) // a heartbeat prints nothing
	if got := out.String(); got != "waiting for the previous connection of this import to finish its commit...\n" {
		t.Fatalf("output = %q", got)
	}
}
