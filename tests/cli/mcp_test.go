package cli_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
)

// mcpCalls runs one gs mcp session with tools/call requests, after the
// handshake, and returns each call's structuredContent and isError, by id.
func mcpCalls(t *testing.T, home, workspace string, calls ...string) map[int]map[string]any {
	t.Helper()
	lines := []string{
		`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
	}
	for i, call := range calls {
		lines = append(lines, `{"jsonrpc":"2.0","id":`+itoa(i+1)+`,"method":"tools/call","params":`+call+`}`)
	}
	stdout, stderr := runCLIStreamsWithInput(t, home, workspace, strings.Join(lines, "\n")+"\n", "mcp", "--slice", "acme/payment")
	results := map[int]map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		var resp struct {
			ID     int `json:"id"`
			Result struct {
				StructuredContent map[string]any `json:"structuredContent"`
				IsError           bool           `json:"isError"`
			} `json:"result"`
			Error any `json:"error"`
		}
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			t.Fatalf("not a JSON-RPC line: %q (stderr %s)", line, stderr)
		}
		if resp.Error != nil {
			t.Fatalf("protocol error for %d: %v", resp.ID, resp.Error)
		}
		out := resp.Result.StructuredContent
		if out == nil {
			out = map[string]any{}
		}
		out["isError"] = resp.Result.IsError
		results[resp.ID] = out
	}
	return results
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestMCPServerReadsSearchesAndChangesASlice(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)

	created := mcpCalls(t, home, workspace,
		`{"name":"create_changeset","arguments":{"title":"mcp: add greeting","submit":true,"edits":[{"op":"write","path":"mcp/hello.go","content":"package mcp\n\n// Greeting is what we say.\nconst Greeting = \"hello\"\n"}]}}`,
	)[1]
	if created["isError"] == true || created["changeset"] == "" {
		t.Fatalf("create_changeset = %v", created)
	}
	handle := created["changeset"].(string)
	if status := created["status"]; status != "submitted" && status != "pending_publish" {
		t.Fatalf("create_changeset status = %v (blocked: %v)", status, created["blocked_reason"])
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		got := mcpCalls(t, home, workspace, `{"name":"get_changeset","arguments":{"changeset":"`+handle+`"}}`)[1]
		if got["status"] == "submitted" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("changeset never landed: %v", got)
		}
		time.Sleep(200 * time.Millisecond)
	}

	results := mcpCalls(t, home, workspace,
		`{"name":"read_file","arguments":{"path":"mcp/hello.go"}}`,
		`{"name":"read_file","arguments":{"path":"/acme/payment/mcp/hello.go","start_line":3,"end_line":3}}`,
		`{"name":"list_directory","arguments":{"path":"mcp"}}`,
		`{"name":"search","arguments":{"pattern":"greeting","ignore_case":true,"glob":"**/*.go"}}`,
		`{"name":"history","arguments":{"path":"mcp/hello.go"}}`,
		`{"name":"blame","arguments":{"path":"mcp/hello.go"}}`,
		`{"name":"read_file","arguments":{"path":"mcp/missing.go"}}`,
		`{"name":"search","arguments":{"pattern":"("}}`,
	)
	if results[1]["content"] != "package mcp\n\n// Greeting is what we say.\nconst Greeting = \"hello\"\n" {
		t.Fatalf("read_file = %v", results[1])
	}
	if results[2]["content"] != "// Greeting is what we say.\n" {
		t.Fatalf("read_file lines 3-3 = %q", results[2]["content"])
	}
	entries := results[3]["entries"].([]any)
	if len(entries) != 1 || entries[0].(map[string]any)["path"] != "/acme/payment/mcp/hello.go" {
		t.Fatalf("list_directory = %v", results[3])
	}
	matches := results[4]["matches"].([]any)
	if len(matches) != 2 || matches[0].(map[string]any)["line"].(float64) != 3 {
		t.Fatalf("search = %v", results[4])
	}
	commits := results[5]["commits"].([]any)
	if len(commits) == 0 || !strings.HasPrefix(commits[0].(map[string]any)["message"].(string), "mcp: add greeting") || commits[0].(map[string]any)["changeset"] != handle {
		t.Fatalf("history = %v", results[5])
	}
	ranges := results[6]["ranges"].([]any)
	if len(ranges) != 1 || ranges[0].(map[string]any)["line_count"].(float64) != 4 {
		t.Fatalf("blame = %v", results[6])
	}
	missing := results[7]
	if missing["isError"] != true || missing["error"].(map[string]any)["code"] != "notfound" {
		t.Fatalf("read of a missing file = %v", missing)
	}
	if bad := results[8]; bad["isError"] != true || bad["error"].(map[string]any)["code"] != "invalid_pattern" {
		t.Fatalf("bad pattern = %v", bad)
	}
}
