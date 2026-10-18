package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "gitslice.io/gitslice/proto/core/v1"
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
