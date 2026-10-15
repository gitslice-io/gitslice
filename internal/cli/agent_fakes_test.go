package cli

import (
	"context"
	"testing"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

type fakeAgentRuntime struct {
	session         *fakeAgentSession
	openErr         error
	gotWorkdir      string
	gotResume       string
	gotInstructions string
	gotHistory      string
	openCount       int
	skipHistory     bool
}

func (r *fakeAgentRuntime) OpenSession(ctx context.Context, workdir, resumeThreadID, instructions string, freshThreadHistory func(context.Context) (string, error)) (agentSession, error) {
	r.openCount++
	r.gotWorkdir = workdir
	r.gotResume = resumeThreadID
	r.gotInstructions = instructions
	if freshThreadHistory != nil && !r.skipHistory {
		if history, err := freshThreadHistory(ctx); err == nil {
			r.gotHistory = history
		}
	}
	if r.openErr != nil {
		return nil, r.openErr
	}
	if r.session == nil {
		r.session = &fakeAgentSession{}
	}
	return r.session, nil
}

type fakeAgentSession struct {
	emits     []agentRuntimeEvent
	threadID  string
	err       error
	gotPrompt string
	closed    int
	dead      bool
	runCount  int
}

func (s *fakeAgentSession) RunTurn(ctx context.Context, prompt string, emit func(agentRuntimeEvent)) error {
	s.runCount++
	s.gotPrompt = prompt
	for _, e := range s.emits {
		emit(e)
	}
	return s.err
}

func (s *fakeAgentSession) ThreadID() string { return s.threadID }

func (s *fakeAgentSession) Alive() bool { return !s.dead }

func (s *fakeAgentSession) Close() { s.closed++ }

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
