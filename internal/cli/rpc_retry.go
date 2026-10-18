package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

// A dropped connection (Unavailable: "error reading from server: EOF", a
// reset, a proxy restart) says nothing about whether the server got the
// request. Calls that give the same result when repeated are retried; for the
// others gs says plainly that the change may have been applied, instead of
// showing a bare EOF.

const (
	rpcRetryAttempts = 3
	rpcRetryBackoff  = 250 * time.Millisecond
)

// retrySafeMethodPrefixes are the names, after "Service/", of methods whose
// repetition is harmless: they only read, validate, or store content under its
// hash.
var retrySafeMethodPrefixes = []string{"Get", "List", "Read", "Resolve", "Validate", "Check"}

// retrySafeMethods are idempotent writes: storing a blob under its content
// hash twice stores it once.
var retrySafeMethods = map[string]bool{
	"BlobService/UploadBlob":  true,
	"BlobService/UploadBlobs": true,
}

// retryUnsafeMethods are methods a retry-safe prefix matches but that change
// state.
var retryUnsafeMethods = map[string]bool{
	"FeedbackService/ResolveFeedback": true,
}

// rpcRetrySafe reports whether a gRPC method (as "/pkg.Service/Method") may
// be sent again after the connection dropped.
func rpcRetrySafe(fullMethod string) bool {
	method := rpcMethodName(fullMethod)
	if retrySafeMethods[method] {
		return true
	}
	if retryUnsafeMethods[method] {
		return false
	}
	name := method[strings.LastIndex(method, "/")+1:]
	for _, prefix := range retrySafeMethodPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// rpcMethodName shortens "/gitslice.core.v1.ChangesetService/CreateChangeset"
// to "ChangesetService/CreateChangeset".
func rpcMethodName(fullMethod string) string {
	service, name, ok := strings.Cut(strings.TrimPrefix(fullMethod, "/"), "/")
	if !ok {
		return fullMethod
	}
	return service[strings.LastIndex(service, ".")+1:] + "/" + name
}

// rpcUnavailableError is an Unavailable error that remembers the call that
// failed. It reads as the error it wraps (message and gRPC status), so code
// that inspects status codes is unaffected; enhanceCommandError explains it.
type rpcUnavailableError struct {
	method   string
	attempts int
	safe     bool
	err      error
}

func (e *rpcUnavailableError) Error() string { return e.err.Error() }
func (e *rpcUnavailableError) Unwrap() error { return e.err }
func (e *rpcUnavailableError) GRPCStatus() *grpcstatus.Status {
	return grpcstatus.Convert(e.err)
}

// retryUnaryInterceptor retries retry-safe unary calls that fail with
// Unavailable, and tags the Unavailable error of every other call.
func retryUnaryInterceptor(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	safe := rpcRetrySafe(method)
	attempts := 1
	if safe {
		attempts = rpcRetryAttempts
	}
	var err error
	for attempt := 1; attempt <= attempts; attempt++ {
		err = invoker(ctx, method, req, reply, cc, opts...)
		if grpcstatus.Code(err) != codes.Unavailable {
			return err
		}
		if attempt == attempts {
			return &rpcUnavailableError{method: rpcMethodName(method), attempts: attempt, safe: safe, err: err}
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(rpcRetryBackoff * time.Duration(attempt)):
		}
	}
	return err
}

// explainUnavailable turns an Unavailable error from a call into a message
// that says what happened and what to do, or returns nil for any other error.
func explainUnavailable(err error) error {
	var failed *rpcUnavailableError
	if !errors.As(err, &failed) {
		return nil
	}
	if failed.safe {
		return commandError{
			Code:      "server_unavailable",
			Message:   fmt.Sprintf("could not reach the server: the connection dropped on %s, %d times in a row (%s)", failed.method, failed.attempts, grpcstatus.Convert(err).Message()),
			Hint:      "This is a network or server hiccup and nothing was changed. Run the command again; gs doctor checks the connection and the server.",
			Retriable: true,
			Cause:     err,
		}
	}
	return commandError{
		Code:    "connection_dropped",
		Message: fmt.Sprintf("the connection dropped while %s was in flight (%s), so gs cannot tell whether the server applied it", failed.method, grpcstatus.Convert(err).Message()),
		Hint:    "gs did not retry, so it cannot create the change twice. Check what exists first (gs status, gs cs list), then run the command again if the change is not there. gs doctor checks the connection and the server.",
		Cause:   err,
	}
}
