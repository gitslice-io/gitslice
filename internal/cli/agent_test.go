package cli

import (
	"context"
	"errors"
	"testing"

	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
)

type fakeAgentRuntime struct {
	emits []agentRuntimeEvent
	err   error
}

func (r fakeAgentRuntime) Run(ctx context.Context, workdir, prompt string, emit func(agentRuntimeEvent)) error {
	for _, e := range r.emits {
		emit(e)
	}
	return r.err
}

func TestForwardAgentRuntimeForwardsCategorizedEvents(t *testing.T) {
	runtime := fakeAgentRuntime{emits: []agentRuntimeEvent{
		{Role: "tool", Type: "tool_call", Text: "echo hi", Data: `{"command":"echo hi"}`},
		{Role: "agent", Type: "reasoning", Text: "thinking"},
		{Role: "agent", Type: "message", Text: "done"},
	}}
	var events []*corev1.AgentEvent
	err := forwardAgentRuntime(context.Background(), runtime, "/tmp/work", "conv-1", "prompt", func(event *corev1.AgentEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatalf("forwardAgentRuntime returned error: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("event count = %d, want 3", len(events))
	}
	assertAgentEvent(t, events[0], "conv-1", "tool", "tool_call", "echo hi", false)
	if events[0].GetDataJson() != `{"command":"echo hi"}` {
		t.Fatalf("data_json = %q, want command payload", events[0].GetDataJson())
	}
	assertAgentEvent(t, events[1], "conv-1", "agent", "reasoning", "thinking", false)
	assertAgentEvent(t, events[2], "conv-1", "agent", "message", "done", false)
}

func TestForwardAgentRuntimeDefaultsRoleAndType(t *testing.T) {
	runtime := fakeAgentRuntime{emits: []agentRuntimeEvent{{Text: "bare"}}}
	var events []*corev1.AgentEvent
	err := forwardAgentRuntime(context.Background(), runtime, "/tmp/work", "conv-1", "prompt", func(event *corev1.AgentEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatalf("forwardAgentRuntime returned error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("event count = %d, want 1", len(events))
	}
	assertAgentEvent(t, events[0], "conv-1", "agent", "delta", "bare", false)
}

func TestForwardAgentRuntimePropagatesError(t *testing.T) {
	wantErr := errors.New("boom")
	runtime := fakeAgentRuntime{
		emits: []agentRuntimeEvent{{Role: "agent", Type: "message", Text: "partial"}},
		err:   wantErr,
	}
	var events []*corev1.AgentEvent
	err := forwardAgentRuntime(context.Background(), runtime, "/tmp/work", "conv-1", "prompt", func(event *corev1.AgentEvent) {
		events = append(events, event)
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if len(events) != 1 {
		t.Fatalf("event count = %d, want 1", len(events))
	}
	assertAgentEvent(t, events[0], "conv-1", "agent", "message", "partial", false)
}

func TestParseCodexLineCategorizesEvents(t *testing.T) {
	cases := []struct {
		name     string
		line     string
		wantRole string
		wantType string
		wantText string
	}{
		{
			name:     "agent message",
			line:     `{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"hello"}}`,
			wantRole: "agent",
			wantType: "message",
			wantText: "hello",
		},
		{
			name:     "command execution",
			line:     `{"type":"item.completed","item":{"id":"item_1","type":"command_execution","command":"echo hi","exit_code":0,"status":"completed"}}`,
			wantRole: "tool",
			wantType: "tool_call",
			wantText: "echo hi",
		},
		{
			name:     "reasoning",
			line:     `{"type":"item.completed","item":{"id":"item_2","type":"reasoning","text":"thinking"}}`,
			wantRole: "agent",
			wantType: "reasoning",
			wantText: "thinking",
		},
		{
			name:     "error envelope",
			line:     `{"type":"error","message":"model unavailable"}`,
			wantRole: "system",
			wantType: "error",
			wantText: "model unavailable",
		},
		{
			name:     "non-json line",
			line:     `Reading additional input from stdin...`,
			wantRole: "agent",
			wantType: "delta",
			wantText: "Reading additional input from stdin...",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			events := parseCodexLine(tc.line)
			if len(events) != 1 {
				t.Fatalf("event count = %d, want 1", len(events))
			}
			got := events[0]
			if got.Role != tc.wantRole || got.Type != tc.wantType || got.Text != tc.wantText {
				t.Fatalf("got %+v, want role=%q type=%q text=%q", got, tc.wantRole, tc.wantType, tc.wantText)
			}
		})
	}
}

func TestParseCodexLineSkipsEnvelopeNoise(t *testing.T) {
	for _, line := range []string{
		`{"type":"thread.started","thread_id":"abc"}`,
		`{"type":"turn.started"}`,
		`{"type":"turn.completed","usage":{"input_tokens":1}}`,
		`{"type":"item.started","item":{"id":"item_1","type":"command_execution","status":"in_progress"}}`,
	} {
		if events := parseCodexLine(line); len(events) != 0 {
			t.Fatalf("line %q produced %d events, want 0", line, len(events))
		}
	}
}

func assertAgentEvent(t *testing.T, event *corev1.AgentEvent, conversationID, role, eventType, text string, final bool) {
	t.Helper()
	if event.GetConversationId() != conversationID {
		t.Fatalf("conversation_id = %q, want %q", event.GetConversationId(), conversationID)
	}
	if event.GetRole() != role {
		t.Fatalf("role = %q, want %q", event.GetRole(), role)
	}
	if event.GetType() != eventType {
		t.Fatalf("type = %q, want %q", event.GetType(), eventType)
	}
	if event.GetText() != text {
		t.Fatalf("text = %q, want %q", event.GetText(), text)
	}
	if event.GetFinal() != final {
		t.Fatalf("final = %t, want %t", event.GetFinal(), final)
	}
}
