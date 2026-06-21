package cli

import (
	"context"
	"errors"
	"testing"

	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
)

type fakeAgentRuntime struct {
	emits []fakeAgentRuntimeEmit
	err   error
}

type fakeAgentRuntimeEmit struct {
	text  string
	final bool
}

func (r fakeAgentRuntime) Run(ctx context.Context, workdir, prompt string, emit func(text string, final bool)) error {
	for _, e := range r.emits {
		emit(e.text, e.final)
	}
	return r.err
}

func TestForwardAgentRuntimeMapsChunksToAgentEvents(t *testing.T) {
	runtime := fakeAgentRuntime{emits: []fakeAgentRuntimeEmit{
		{text: "first chunk"},
		{text: "second chunk"},
		{text: "done", final: true},
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
	assertAgentEvent(t, events[0], "conv-1", "agent", "delta", "first chunk", false)
	assertAgentEvent(t, events[1], "conv-1", "agent", "delta", "second chunk", false)
	assertAgentEvent(t, events[2], "conv-1", "agent", "message", "done", true)
}

func TestForwardAgentRuntimeSuppressesFinalOnRuntimeError(t *testing.T) {
	wantErr := errors.New("boom")
	runtime := fakeAgentRuntime{
		emits: []fakeAgentRuntimeEmit{
			{text: "partial"},
			{text: "would be final", final: true},
		},
		err: wantErr,
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
	assertAgentEvent(t, events[0], "conv-1", "agent", "delta", "partial", false)
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
