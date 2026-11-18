package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"

	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func runMCPSession(t *testing.T, tools []mcpTool, lines ...string) []map[string]any {
	t.Helper()
	var out bytes.Buffer
	var events []map[string]any
	server := &mcpServer{tools: tools, version: "test", out: &out, trace: func(e map[string]any) { events = append(events, e) }}
	if err := server.serve(context.Background(), strings.NewReader(strings.Join(lines, "\n")+"\n")); err != nil {
		t.Fatal(err)
	}
	var responses []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var resp map[string]any
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			t.Fatalf("response %q is not JSON: %v", line, err)
		}
		responses = append(responses, resp)
	}
	return responses
}

func echoTools() []mcpTool {
	return []mcpTool{
		{
			Name:        "echo",
			Description: "echo",
			InputSchema: schema([]string{"text"}, map[string]any{"text": str("text")}),
			call: func(_ context.Context, raw json.RawMessage) (any, error) {
				var args struct {
					Text string `json:"text"`
				}
				if err := json.Unmarshal(raw, &args); err != nil {
					return nil, badArgs(err)
				}
				return map[string]any{"text": args.Text}, nil
			},
		},
		{
			Name:        "fail",
			Description: "fail",
			InputSchema: schema(nil, map[string]any{}),
			call: func(context.Context, json.RawMessage) (any, error) {
				return nil, userError("slice_required", "no slice given", "Pass slice.")
			},
		},
	}
}

func TestMCPHandshakeListAndCall(t *testing.T) {
	responses := runMCPSession(t, echoTools(),
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":"three","method":"tools/call","params":{"name":"echo","arguments":{"text":"hi"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"ping"}`,
	)
	if len(responses) != 4 {
		t.Fatalf("got %d responses, want 4 (a notification gets none): %v", len(responses), responses)
	}
	init := responses[0]["result"].(map[string]any)
	if init["protocolVersion"] != "2025-03-26" {
		t.Fatalf("protocol version = %v, want the client's", init["protocolVersion"])
	}
	if _, ok := init["capabilities"].(map[string]any)["tools"]; !ok {
		t.Fatalf("capabilities %v have no tools", init["capabilities"])
	}
	tools := responses[1]["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 2 || tools[0].(map[string]any)["inputSchema"] == nil {
		t.Fatalf("tools/list = %v", tools)
	}
	call := responses[2]
	if call["id"] != "three" {
		t.Fatalf("response id = %v, want the request's", call["id"])
	}
	result := call["result"].(map[string]any)
	if result["isError"] != false || result["structuredContent"].(map[string]any)["text"] != "hi" {
		t.Fatalf("tools/call result = %v", result)
	}
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, `"text": "hi"`) {
		t.Fatalf("text content = %q", text)
	}
}

func TestMCPToolErrorsAreResultsAndProtocolErrorsAreErrors(t *testing.T) {
	responses := runMCPSession(t, echoTools(),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"fail","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"nope"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"resources/list"}`,
		`not json`,
	)
	if len(responses) != 4 {
		t.Fatalf("got %d responses: %v", len(responses), responses)
	}
	failed := responses[0]["result"].(map[string]any)
	if failed["isError"] != true {
		t.Fatalf("a failing tool is not isError: %v", failed)
	}
	structured := failed["structuredContent"].(map[string]any)["error"].(map[string]any)
	if structured["code"] != "slice_required" || structured["hint"] != "Pass slice." {
		t.Fatalf("tool error = %v, want the CLI's code and hint", structured)
	}
	if responses[1]["error"].(map[string]any)["code"].(float64) != mcpInvalidParams {
		t.Fatalf("unknown tool = %v", responses[1])
	}
	if responses[2]["error"].(map[string]any)["code"].(float64) != mcpMethodNotFound {
		t.Fatalf("unknown method = %v", responses[2])
	}
	if responses[3]["error"].(map[string]any)["code"].(float64) != mcpParseError {
		t.Fatalf("bad JSON = %v", responses[3])
	}
}

func TestMCPTracesToolCalls(t *testing.T) {
	var out bytes.Buffer
	var events []map[string]any
	server := &mcpServer{tools: echoTools(), out: &out, trace: func(e map[string]any) { events = append(events, e) }}
	_ = server.serve(context.Background(), strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"fail"}}`+"\n"))
	if len(events) != 1 || events[0]["tool"] != "fail" || events[0]["ok"] != false {
		t.Fatalf("trace events = %v", events)
	}
}

func TestMCPToolErrorKeepsGRPCCodes(t *testing.T) {
	if got := mcpToolError(errors.New("boom")); got["code"] != "error" {
		t.Fatalf("plain error = %v", got)
	}
}

func TestMCPSliceRepoPath(t *testing.T) {
	one := mcpSlice{included: []string{"/acme/app"}}
	if p, err := one.repoPath("src/main.go"); err != nil || p != "/acme/app/src/main.go" {
		t.Fatalf("relative = %q, %v", p, err)
	}
	if p, err := one.repoPath("/acme/app/x/../y"); err != nil || p != "/acme/app/y" {
		t.Fatalf("absolute = %q, %v", p, err)
	}
	two := mcpSlice{included: []string{"/acme/a", "/acme/b"}}
	if _, err := two.repoPath("main.go"); err == nil {
		t.Fatal("a relative path in a slice with two folders was accepted")
	}
}

type stubMCPChangesetServer struct {
	corev1.UnimplementedChangesetServiceServer
	corev1.UnimplementedCheckServiceServer
}

func (s *stubMCPChangesetServer) GetChangeset(_ context.Context, req *corev1.GetChangesetRequest) (*corev1.Changeset, error) {
	return &corev1.Changeset{
		Id:                req.GetChangesetId(),
		Title:             "Fix flaky check",
		Status:            "draft",
		CurrentPatchsetId: "ps_2",
		Patchsets: []*corev1.Patchset{
			{Id: "ps_2", Number: 2, ChangedPaths: []string{"/acme/app/main.go"}},
		},
	}, nil
}

func (s *stubMCPChangesetServer) ListCheckRuns(context.Context, *corev1.ListCheckRunsRequest) (*corev1.ListCheckRunsResponse, error) {
	return &corev1.ListCheckRunsResponse{
		Runs: []*corev1.CheckRun{
			{Id: "run_1", CheckName: "gitslice/e2e", PatchsetId: "ps_2", Attempt: 1, Status: "failed", Summary: "flake", CreatedAt: "2026-01-01T00:01:00Z"},
			{Id: "run_2", CheckName: "gitslice/e2e", PatchsetId: "ps_2", Attempt: 2, Status: "passed", Summary: "green", CreatedAt: "2026-01-01T00:02:00Z"},
			{Id: "run_0", CheckName: "gitslice/old", PatchsetId: "ps_1", Attempt: 1, Status: "failed", CreatedAt: "2026-01-01T00:00:00Z"},
		},
	}, nil
}

func TestMCPGetChangesetDeduplicatesLatestCheckRuns(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	stub := &stubMCPChangesetServer{}
	corev1.RegisterChangesetServiceServer(srv, stub)
	corev1.RegisterCheckServiceServer(srv, stub)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	backend := &mcpBackend{conn: conn}
	got, err := backend.getChangeset(context.Background(), json.RawMessage(`{"changeset":"cs_123"}`))
	if err != nil {
		t.Fatalf("getChangeset = %v", err)
	}
	out := got.(map[string]any)
	checks, ok := out["checks"].([]map[string]any)
	if !ok || len(checks) != 1 {
		t.Fatalf("checks = %#v, want single deduplicated check run on current patchset", out["checks"])
	}
	if checks[0]["name"] != "gitslice/e2e" || checks[0]["status"] != "passed" || checks[0]["summary"] != "green" {
		t.Fatalf("checks[0] = %#v, want latest passed rerun", checks[0])
	}
}
