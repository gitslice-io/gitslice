package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

func TestRPCRetrySafe(t *testing.T) {
	for method, want := range map[string]bool{
		"/gitslice.core.v1.ChangesetService/GetChangeset":          true,
		"/gitslice.core.v1.RepositoryService/ListDirectory":        true,
		"/gitslice.core.v1.RepositoryService/ReadFiles":            true,
		"/gitslice.core.v1.SliceService/ResolveSlice":              true,
		"/gitslice.core.v1.WorkspaceService/ValidateWorkspaceDiff": true,
		"/gitslice.core.v1.BlobService/GetBlobStatus":              true,
		"/gitslice.core.v1.BlobService/UploadBlobs":                true,
		"/gitslice.core.v1.ChangesetService/CreateChangeset":       false,
		"/gitslice.core.v1.ChangesetStackService/AddStackEntry":    false,
		"/gitslice.core.v1.ChangesetService/SubmitChangeset":       false,
		"/gitslice.core.v1.ChangesetService/UpdateChangeset":       false,
		"/gitslice.core.v1.SliceService/RenameSlice":               false,
		"/gitslice.core.v1.FeedbackService/ResolveFeedback":        false,
		"/gitslice.core.v1.FeedbackService/SubmitFeedback":         false,
		"/gitslice.core.v1.CheckService/RerunCheck":                false,
	} {
		if got := rpcRetrySafe(method); got != want {
			t.Errorf("rpcRetrySafe(%s) = %v, want %v", method, got, want)
		}
	}
	if got := rpcMethodName("/gitslice.core.v1.ChangesetService/CreateChangeset"); got != "ChangesetService/CreateChangeset" {
		t.Errorf("rpcMethodName = %q", got)
	}
}

func unavailable() error {
	return grpcstatus.Error(codes.Unavailable, "error reading from server: EOF")
}

func TestRetryInterceptorRetriesSafeCallsOnly(t *testing.T) {
	run := func(method string, results ...error) (int, error) {
		calls := 0
		invoker := func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, opts ...grpc.CallOption) error {
			calls++
			if calls > len(results) {
				t.Fatalf("%s: called %d times", method, calls)
			}
			return results[calls-1]
		}
		err := retryUnaryInterceptor(context.Background(), method, nil, nil, nil, invoker)
		return calls, err
	}

	// A read recovers after one dropped connection.
	if calls, err := run("/p.S/GetThing", unavailable(), nil); calls != 2 || err != nil {
		t.Fatalf("read: calls = %d, err = %v; want 2 calls and success", calls, err)
	}
	// A read that keeps failing gives up after three tries and says so.
	calls, err := run("/p.S/ListThings", unavailable(), unavailable(), unavailable())
	if calls != 3 || grpcstatus.Code(err) != codes.Unavailable {
		t.Fatalf("read: calls = %d, err = %v; want 3 calls and Unavailable", calls, err)
	}
	// A write is sent once.
	calls, err = run("/p.S/CreateThing", unavailable())
	if calls != 1 || grpcstatus.Code(err) != codes.Unavailable {
		t.Fatalf("write: calls = %d, err = %v; want 1 call and Unavailable", calls, err)
	}
	// Other errors are never retried.
	notFound := grpcstatus.Error(codes.NotFound, "not found")
	if calls, err := run("/p.S/GetThing", notFound); calls != 1 || grpcstatus.Code(err) != codes.NotFound {
		t.Fatalf("NotFound: calls = %d, err = %v", calls, err)
	}
}

func TestUnavailableErrorsSayWhetherTheChangeMayHaveHappened(t *testing.T) {
	r := Runner{}
	invoker := func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, opts ...grpc.CallOption) error {
		return unavailable()
	}

	write := r.enhanceCommandError(retryUnaryInterceptor(context.Background(), "/gitslice.core.v1.ChangesetStackService/AddStackEntry", nil, nil, nil, invoker))
	var cmdErr commandError
	if !errors.As(write, &cmdErr) || cmdErr.Code != "connection_dropped" || cmdErr.Retriable {
		t.Fatalf("write error = %#v", write)
	}
	for _, want := range []string{"ChangesetStackService/AddStackEntry", "cannot tell whether the server applied it", "gs status"} {
		if !strings.Contains(write.Error(), want) {
			t.Errorf("write error %q does not mention %q", write.Error(), want)
		}
	}
	if body := classifyError(write).Error; body.Retriable || body.Code != "connection_dropped" {
		t.Errorf("JSON error = %+v; a dropped write must not be marked retriable", body)
	}

	read := r.enhanceCommandError(retryUnaryInterceptor(context.Background(), "/gitslice.core.v1.RepositoryService/GetRef", nil, nil, nil, invoker))
	if !errors.As(read, &cmdErr) || cmdErr.Code != "server_unavailable" || !cmdErr.Retriable {
		t.Fatalf("read error = %#v", read)
	}
	if !strings.Contains(read.Error(), "nothing was changed") || !strings.Contains(read.Error(), "3 times") {
		t.Errorf("read error %q", read.Error())
	}

	// The wrapped error still reads as the gRPC error it is.
	wrapped := retryUnaryInterceptor(context.Background(), "/p.S/CreateThing", nil, nil, nil, invoker)
	if grpcstatus.Code(wrapped) != codes.Unavailable || !strings.Contains(wrapped.Error(), "error reading from server: EOF") {
		t.Errorf("wrapped error = %v, code %v", wrapped, grpcstatus.Code(wrapped))
	}
}
