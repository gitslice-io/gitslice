package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

func TestTraceLogsEachCallAsAJSONLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	t.Setenv("GS_TRACE", path)
	ok := func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error { return nil }
	denied := func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
		return grpcstatus.Error(codes.PermissionDenied, "not a member")
	}
	if err := traceUnaryInterceptor(context.Background(), "/gitslice.core.v1.RepositoryService/ReadFile", nil, nil, nil, ok); err != nil {
		t.Fatal(err)
	}
	if err := traceUnaryInterceptor(context.Background(), "/gitslice.core.v1.SliceService/ResolveSlice", nil, nil, nil, denied); grpcstatus.Code(err) != codes.PermissionDenied {
		t.Fatalf("the interceptor changed the error: %v", err)
	}
	traceEvent(map[string]any{"tool": "search", "ok": true})

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 3 {
		t.Fatalf("trace has %d lines, want 3:\n%s", len(lines), data)
	}
	var events []map[string]any
	for _, line := range lines {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("not JSON: %q", line)
		}
		if event["time"] == nil {
			t.Fatalf("no time: %v", event)
		}
		events = append(events, event)
	}
	if events[0]["rpc"] != "/gitslice.core.v1.RepositoryService/ReadFile" || events[0]["code"] != "OK" {
		t.Fatalf("first event = %v", events[0])
	}
	if events[1]["code"] != "PermissionDenied" || events[1]["error"] != "not a member" {
		t.Fatalf("second event = %v", events[1])
	}
	if events[2]["tool"] != "search" {
		t.Fatalf("third event = %v", events[2])
	}
}

func TestTraceIsOffWithoutGSTrace(t *testing.T) {
	t.Setenv("GS_TRACE", "")
	dir := t.TempDir()
	traceEvent(map[string]any{"tool": "x"})
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatal("trace wrote a file with GS_TRACE unset")
	}
}
