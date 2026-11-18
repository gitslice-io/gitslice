package agentd

import (
	"encoding/json"
	"testing"
)

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
			var got []RuntimeEvent
			turn := &CodexTurn{Emit: func(e RuntimeEvent) { got = append(got, e) }, Accum: map[string]*DeltaAccum{}, Done: make(chan error, 1)}
			turn.HandleNotification("item/completed", json.RawMessage(tc.params))
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
	var got []RuntimeEvent
	turn := &CodexTurn{Emit: func(e RuntimeEvent) { got = append(got, e) }, Accum: map[string]*DeltaAccum{}, Done: make(chan error, 1)}
	turn.HandleNotification("item/agentMessage/delta", json.RawMessage(`{"itemId":"msg_0","delta":"Hel"}`))
	turn.HandleNotification("item/agentMessage/delta", json.RawMessage(`{"itemId":"msg_0","delta":"lo"}`))
	if len(got) < 1 {
		t.Fatalf("expected at least one ephemeral delta, got none")
	}
	first := got[0]
	if !first.Ephemeral || first.Type != "message_delta" || first.ItemID != "msg_0" || first.Text != "Hel" {
		t.Fatalf("first delta = %+v, want ephemeral message_delta msg_0 text=Hel", first)
	}
	turn.HandleNotification("item/completed", json.RawMessage(`{"item":{"id":"msg_0","type":"agentMessage","text":"Hello"}}`))
	last := got[len(got)-1]
	if last.Ephemeral || last.Type != "message" || last.Text != "Hello" {
		t.Fatalf("final = %+v, want persisted message text=Hello", last)
	}
	if _, ok := turn.Accum["msg_0"]; ok {
		t.Fatalf("accumulation for msg_0 should be cleared after completion")
	}
}

func TestCodexTurnCompletionSignalsDone(t *testing.T) {
	turn := &CodexTurn{Emit: func(RuntimeEvent) {}, Accum: map[string]*DeltaAccum{}, Done: make(chan error, 1)}
	turn.HandleNotification("turn/completed", nil)
	select {
	case err := <-turn.Done:
		if err != nil {
			t.Fatalf("done err = %v, want nil", err)
		}
	default:
		t.Fatalf("turn/completed did not signal done")
	}
}
