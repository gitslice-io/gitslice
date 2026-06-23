package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if os.Getenv("GS_FAKE_CODEX") == "1" {
		fakeCodexAppServer(os.Stdin, os.Stdout)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestCodexRuntimeOpenSessionStartsThread(t *testing.T) {
	session := openFakeCodexSession(t, "")

	if got := session.ThreadID(); got != "thread-fake-1" {
		t.Fatalf("ThreadID() = %q, want thread-fake-1", got)
	}
	if !session.Alive() {
		t.Fatalf("Alive() = false, want true")
	}

	session.Close()
}

func TestCodexRuntimeRunTurnStreamsAndCompletes(t *testing.T) {
	t.Setenv("GS_FAKE_CODEX_SCRIPT", "message")
	session := openFakeCodexSession(t, "")

	ctx, done := context.WithTimeout(context.Background(), 10*time.Second)
	defer done()

	var events []agentRuntimeEvent
	if err := session.RunTurn(ctx, "write the status update", func(event agentRuntimeEvent) {
		events = append(events, event)
	}); err != nil {
		t.Fatalf("RunTurn returned error: %v", err)
	}

	delta := findRuntimeEvent(events, "agent", "message_delta")
	if delta == nil {
		t.Fatalf("missing message_delta event in %+v", events)
	}
	if !delta.Ephemeral || delta.ItemID != "msg_fake_1" || delta.Text != "Hello" {
		t.Fatalf("message_delta = %+v, want ephemeral msg_fake_1 text=Hello", *delta)
	}

	final := findRuntimeEvent(events, "agent", "message")
	if final == nil {
		t.Fatalf("missing final message event in %+v", events)
	}
	if final.Ephemeral || final.ItemID != "msg_fake_1" || final.Text != "Hello from fake codex." {
		t.Fatalf("final message = %+v, want non-ephemeral msg_fake_1 final text", *final)
	}
}

func TestCodexRuntimeRunTurnPropagatesError(t *testing.T) {
	t.Setenv("GS_FAKE_CODEX_SCRIPT", "error")
	session := openFakeCodexSession(t, "")

	ctx, done := context.WithTimeout(context.Background(), 10*time.Second)
	defer done()

	var events []agentRuntimeEvent
	err := session.RunTurn(ctx, "trigger an error", func(event agentRuntimeEvent) {
		events = append(events, event)
	})
	if err == nil {
		t.Fatalf("RunTurn returned nil error, want non-nil")
	}

	systemErr := findRuntimeEvent(events, "system", "error")
	if systemErr == nil {
		t.Fatalf("missing system error event in %+v", events)
	}
	if systemErr.Text != "fake codex turn failed" {
		t.Fatalf("system error text = %q, want fake codex turn failed", systemErr.Text)
	}
}

func TestCodexRuntimeResumesThread(t *testing.T) {
	session := openFakeCodexSession(t, "thread-existing-7")

	if got := session.ThreadID(); got != "thread-existing-7" {
		t.Fatalf("ThreadID() = %q, want thread-existing-7", got)
	}
}

func TestCodexRuntimeRunTurnEmitsToolCall(t *testing.T) {
	t.Setenv("GS_FAKE_CODEX_SCRIPT", "tool")
	session := openFakeCodexSession(t, "")

	ctx, done := context.WithTimeout(context.Background(), 10*time.Second)
	defer done()

	var events []agentRuntimeEvent
	if err := session.RunTurn(ctx, "run a command", func(event agentRuntimeEvent) {
		events = append(events, event)
	}); err != nil {
		t.Fatalf("RunTurn returned error: %v", err)
	}

	tool := findRuntimeEvent(events, "tool", "tool_call")
	if tool == nil {
		t.Fatalf("missing tool_call event in %+v", events)
	}
	if tool.ItemID != "cmd_fake_1" || tool.Text != "echo hi" {
		t.Fatalf("tool_call = %+v, want cmd_fake_1 text=echo hi", *tool)
	}
}

func openFakeCodexSession(t *testing.T, resumeThreadID string) agentSession {
	t.Helper()
	t.Setenv("GS_FAKE_CODEX", "1")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	session, err := (codexRuntime{Binary: os.Args[0]}).OpenSession(ctx, t.TempDir(), resumeThreadID)
	if err != nil {
		cancel()
		t.Fatalf("OpenSession returned error: %v", err)
	}
	t.Cleanup(cancel)
	t.Cleanup(session.Close)
	return session
}

func findRuntimeEvent(events []agentRuntimeEvent, role, typ string) *agentRuntimeEvent {
	for i := range events {
		if events[i].Role == role && events[i].Type == typ {
			return &events[i]
		}
	}
	return nil
}

func fakeCodexAppServer(in io.Reader, out io.Writer) {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	writer := bufio.NewWriter(out)

	for scanner.Scan() {
		var msg jsonrpcMessage
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			fmt.Fprintf(os.Stderr, "fake codex: decode request: %v\n", err)
			return
		}

		switch msg.Method {
		case "initialize":
			if !fakeCodexRespond(writer, msg.ID, map[string]any{}) {
				return
			}
		case "initialized":
			continue
		case "thread/start":
			if !fakeCodexRespond(writer, msg.ID, map[string]any{
				"thread": map[string]string{"id": "thread-fake-1"},
			}) {
				return
			}
		case "thread/resume":
			threadID := fakeCodexThreadID(msg.Params)
			if !fakeCodexRespond(writer, msg.ID, map[string]any{
				"thread": map[string]string{"id": threadID},
			}) {
				return
			}
		case "turn/start":
			if !fakeCodexRespond(writer, msg.ID, map[string]any{}) {
				return
			}
			if !fakeCodexRunScript(writer, os.Getenv("GS_FAKE_CODEX_SCRIPT")) {
				return
			}
		default:
			if len(msg.ID) > 0 && !fakeCodexRespondError(writer, msg.ID, -32601, "method not found") {
				return
			}
		}
	}
}

func fakeCodexThreadID(params json.RawMessage) string {
	var p struct {
		ThreadID string `json:"threadId"`
	}
	_ = json.Unmarshal(params, &p)
	return p.ThreadID
}

func fakeCodexRunScript(writer *bufio.Writer, script string) bool {
	switch script {
	case "error":
		return fakeCodexNotify(writer, "error", map[string]any{
			"error": map[string]string{"message": "fake codex turn failed"},
		})
	case "tool":
		return fakeCodexNotify(writer, "item/completed", map[string]any{
			"item": map[string]any{
				"id":               "cmd_fake_1",
				"type":             "commandExecution",
				"command":          "echo hi",
				"exitCode":         0,
				"status":           "completed",
				"aggregatedOutput": "hi\n",
			},
		}) && fakeCodexNotify(writer, "turn/completed", map[string]any{})
	default:
		return fakeCodexNotify(writer, "item/agentMessage/delta", map[string]any{
			"itemId": "msg_fake_1",
			"delta":  "Hello",
		}) && fakeCodexNotify(writer, "item/completed", map[string]any{
			"item": map[string]any{
				"id":   "msg_fake_1",
				"type": "agentMessage",
				"text": "Hello from fake codex.",
			},
		}) && fakeCodexNotify(writer, "turn/completed", map[string]any{})
	}
}

func fakeCodexRespond(writer *bufio.Writer, id json.RawMessage, result any) bool {
	return fakeCodexWrite(writer, map[string]any{"id": id, "result": result})
}

func fakeCodexRespondError(writer *bufio.Writer, id json.RawMessage, code int, message string) bool {
	return fakeCodexWrite(writer, map[string]any{
		"id": id,
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	})
}

func fakeCodexNotify(writer *bufio.Writer, method string, params any) bool {
	return fakeCodexWrite(writer, map[string]any{"method": method, "params": params})
}

func fakeCodexWrite(writer *bufio.Writer, msg any) bool {
	data, err := json.Marshal(msg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fake codex: encode response: %v\n", err)
		return false
	}
	if _, err := writer.Write(append(data, '\n')); err != nil {
		fmt.Fprintf(os.Stderr, "fake codex: write response: %v\n", err)
		return false
	}
	if err := writer.Flush(); err != nil {
		fmt.Fprintf(os.Stderr, "fake codex: flush response: %v\n", err)
		return false
	}
	return true
}
