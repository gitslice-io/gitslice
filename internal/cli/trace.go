package cli

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	grpcstatus "google.golang.org/grpc/status"
)

// GS_TRACE names a file that gets one JSON line per server call (and per MCP
// tool call), for seeing what an agent's gs did: the method, how long it took,
// and how it ended. Requests and answers are not logged; they can hold file
// contents and tokens.

var traceMu sync.Mutex

func tracePath() string { return strings.TrimSpace(os.Getenv("GS_TRACE")) }

// traceEvent appends event to the GS_TRACE file, with the time; it does
// nothing when GS_TRACE is unset, and a file it cannot write is ignored.
func traceEvent(event map[string]any) {
	path := tracePath()
	if path == "" {
		return
	}
	event["time"] = time.Now().UTC().Format(time.RFC3339Nano)
	line, err := json.Marshal(event)
	if err != nil {
		return
	}
	traceMu.Lock()
	defer traceMu.Unlock()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = file.Write(append(line, '\n'))
}

func traceCall(method string, start time.Time, err error, stream bool) {
	event := map[string]any{"rpc": method, "duration_ms": time.Since(start).Milliseconds(), "code": grpcstatus.Code(err).String()}
	if stream {
		event["stream"] = true
	}
	if err != nil {
		event["error"] = grpcstatus.Convert(err).Message()
	}
	traceEvent(event)
}

func traceUnaryInterceptor(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	if tracePath() == "" {
		return invoker(ctx, method, req, reply, cc, opts...)
	}
	start := time.Now()
	err := invoker(ctx, method, req, reply, cc, opts...)
	traceCall(method, start, err, false)
	return err
}

// traceStreamInterceptor logs a stream when it is opened: how long it took to
// open and whether it opened. A stream's later messages are not logged.
func traceStreamInterceptor(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	if tracePath() == "" {
		return streamer(ctx, desc, cc, method, opts...)
	}
	start := time.Now()
	stream, err := streamer(ctx, desc, cc, method, opts...)
	traceCall(method, start, err, true)
	return stream, err
}
