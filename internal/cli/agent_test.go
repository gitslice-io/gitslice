package cli

import (
	"context"
	"encoding/json"
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

func TestForwardAgentRuntimePropagatesItemIDAndEphemeral(t *testing.T) {
	runtime := fakeAgentRuntime{emits: []agentRuntimeEvent{
		{Role: "agent", Type: "message_delta", Text: "hel", ItemID: "msg_1", Ephemeral: true},
		{Role: "agent", Type: "message", Text: "hello", ItemID: "msg_1"},
	}}
	var events []*corev1.AgentEvent
	err := forwardAgentRuntime(context.Background(), runtime, "/tmp/work", "conv-1", "prompt", func(event *corev1.AgentEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatalf("forwardAgentRuntime returned error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("event count = %d, want 2", len(events))
	}
	if !events[0].GetEphemeral() || events[0].GetType() != "message_delta" || events[0].GetItemId() != "msg_1" {
		t.Fatalf("delta event = %+v, want ephemeral message_delta item msg_1", events[0])
	}
	if events[1].GetEphemeral() || events[1].GetType() != "message" || events[1].GetItemId() != "msg_1" {
		t.Fatalf("final event = %+v, want persisted message item msg_1", events[1])
	}
}

func TestCodexTurnCategorizesCompletedItems(t *testing.T) {
	cases := []struct {
		name     string
		params   string
		wantRole string
		wantType string
		wantText string
		wantSkip bool
	}{
		{
			name:     "agent message",
			params:   `{"item":{"id":"msg_0","type":"agentMessage","text":"hello"}}`,
			wantRole: "agent", wantType: "message", wantText: "hello",
		},
		{
			name:     "command execution",
			params:   `{"item":{"id":"call_1","type":"commandExecution","command":"echo hi","exitCode":0,"status":"completed","aggregatedOutput":"hi\n"}}`,
			wantRole: "tool", wantType: "tool_call", wantText: "echo hi",
		},
		{
			name:     "reasoning",
			params:   `{"item":{"id":"r_1","type":"reasoning","text":"thinking"}}`,
			wantRole: "agent", wantType: "reasoning", wantText: "thinking",
		},
		{
			name:     "user message skipped",
			params:   `{"item":{"id":"u_1","type":"userMessage","text":"hi"}}`,
			wantSkip: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []agentRuntimeEvent
			turn := &codexTurn{emit: func(e agentRuntimeEvent) { got = append(got, e) }, accum: map[string]*deltaAccum{}, done: make(chan error, 1)}
			turn.handleNotification("item/completed", json.RawMessage(tc.params))
			if tc.wantSkip {
				if len(got) != 0 {
					t.Fatalf("expected no events, got %+v", got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("event count = %d, want 1 (%+v)", len(got), got)
			}
			e := got[0]
			if e.Role != tc.wantRole || e.Type != tc.wantType || e.Text != tc.wantText || e.Ephemeral {
				t.Fatalf("got %+v, want role=%q type=%q text=%q non-ephemeral", e, tc.wantRole, tc.wantType, tc.wantText)
			}
		})
	}
}

func TestCodexTurnStreamsThrottledDeltas(t *testing.T) {
	var got []agentRuntimeEvent
	turn := &codexTurn{emit: func(e agentRuntimeEvent) { got = append(got, e) }, accum: map[string]*deltaAccum{}, done: make(chan error, 1)}
	// First delta flushes immediately (zero lastFlush); subsequent within the
	// throttle window are buffered, so accumulated text grows when it does flush.
	turn.handleNotification("item/agentMessage/delta", json.RawMessage(`{"itemId":"msg_0","delta":"Hel"}`))
	turn.handleNotification("item/agentMessage/delta", json.RawMessage(`{"itemId":"msg_0","delta":"lo"}`))
	if len(got) < 1 {
		t.Fatalf("expected at least one ephemeral delta, got none")
	}
	first := got[0]
	if !first.Ephemeral || first.Type != "message_delta" || first.ItemID != "msg_0" || first.Text != "Hel" {
		t.Fatalf("first delta = %+v, want ephemeral message_delta msg_0 text=Hel", first)
	}
	// Completion emits the persisted final and clears accumulation.
	turn.handleNotification("item/completed", json.RawMessage(`{"item":{"id":"msg_0","type":"agentMessage","text":"Hello"}}`))
	last := got[len(got)-1]
	if last.Ephemeral || last.Type != "message" || last.Text != "Hello" {
		t.Fatalf("final = %+v, want persisted message text=Hello", last)
	}
	if _, ok := turn.accum["msg_0"]; ok {
		t.Fatalf("accumulation for msg_0 should be cleared after completion")
	}
}

func TestCodexTurnCompletionSignalsDone(t *testing.T) {
	turn := &codexTurn{emit: func(agentRuntimeEvent) {}, accum: map[string]*deltaAccum{}, done: make(chan error, 1)}
	turn.handleNotification("turn/completed", nil)
	select {
	case err := <-turn.done:
		if err != nil {
			t.Fatalf("done err = %v, want nil", err)
		}
	default:
		t.Fatalf("turn/completed did not signal done")
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
