package memory

import (
	"context"
	"testing"

	"github.com/gitslice-io/gitslice/internal/storage"
)

func TestAgentStoreUnansweredUserEvents(t *testing.T) {
	ctx := context.Background()
	stores := New()
	daemon, err := stores.Agents.RegisterDaemon(ctx, storage.AgentDaemonInput{
		SubjectID: "user_alice",
		Account:   "acme",
		Name:      "test-daemon",
		Runtime:   "test",
	})
	if err != nil {
		t.Fatalf("RegisterDaemon: %v", err)
	}
	conv, err := stores.Agents.CreateConversation(ctx, storage.ConversationInput{
		DaemonID:  daemon.Id,
		SubjectID: "user_alice",
		SliceID:   "slice_acme_home",
		Account:   "acme",
		SliceName: "home",
	})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	assertEvents := func(wantTexts ...string) {
		t.Helper()
		events, err := stores.Agents.UnansweredUserEvents(ctx, conv.Id)
		if err != nil {
			t.Fatalf("UnansweredUserEvents: %v", err)
		}
		if len(events) != len(wantTexts) {
			t.Fatalf("UnansweredUserEvents returned %d events, want %d: %#v", len(events), len(wantTexts), events)
		}
		for i, want := range wantTexts {
			if events[i].Text != want {
				t.Fatalf("event %d text = %q, want %q", i, events[i].Text, want)
			}
			if i > 0 && events[i-1].Seq >= events[i].Seq {
				t.Fatalf("events are not ordered by seq: %#v", events)
			}
		}
	}
	appendEvent := func(role, eventType, text string) {
		t.Helper()
		if _, _, err := stores.Agents.AppendEvent(ctx, conv.Id, role, eventType, text, "", "", 0); err != nil {
			t.Fatalf("AppendEvent(%s, %s): %v", role, eventType, err)
		}
	}

	assertEvents()
	appendEvent("user", "message", "first question")
	assertEvents("first question")
	appendEvent("agent", "message", "first answer")
	assertEvents()
	appendEvent("agent", "status", "ready for more")
	appendEvent("user", "message", "second question")
	appendEvent("user", "message", "third question")
	assertEvents("second question", "third question")
}
