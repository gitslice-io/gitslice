package agentd

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestHandleUserMessageDeduplicatesPositiveSeq(t *testing.T) {
	tests := []struct {
		name     string
		seq      int64
		wantRuns int
	}{
		{name: "sequenced redelivery", seq: 17, wantRuns: 1},
		{name: "legacy unsequenced", seq: 0, wantRuns: 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			session := &fakeAgentSession{err: errors.New("stop after runtime invocation")}
			runtime := &fakeAgentRuntime{session: session, skipHistory: true}
			root := t.TempDir()
			conv := &Conversation{
				id:           "conv-1",
				workdir:      filepath.Join(root, "conversations", "conv-1"),
				sliceAccount: "acme",
				sliceName:    "app",
				role:         "contributor",
				ready:        make(chan struct{}),
				session:      session,
			}
			conv.setReady(nil)
			d := &Daemon{
				Auth:          &fakeAgentAuthClient{},
				Runtime:       runtime,
				BaseCtx:       context.Background(),
				WorkingDir:    root,
				conversations: map[string]*Conversation{conv.id: conv},
			}
			msg := &corev1.DeliverUserMessage{ConversationId: conv.id, Text: "hello", Seq: tc.seq}

			d.HandleUserMessage(context.Background(), msg)
			d.HandleUserMessage(context.Background(), msg)

			if session.runCount != tc.wantRuns {
				t.Fatalf("runtime turns = %d, want %d", session.runCount, tc.wantRuns)
			}
		})
	}
}

func TestSendSystemErrorBuffersKnownConversation(t *testing.T) {
	conv := &Conversation{id: "conv-1"}
	d := &Daemon{conversations: map[string]*Conversation{conv.id: conv}}

	d.sendSystemError(context.Background(), conv.id, errors.New("runtime disconnected"))

	conv.outMu.Lock()
	defer conv.outMu.Unlock()
	if len(conv.pending) != 1 {
		t.Fatalf("pending events = %d, want 1", len(conv.pending))
	}
	event := conv.pending[0]
	if event.GetConversationId() != conv.id || event.GetRole() != "system" || event.GetType() != "error" || event.GetText() != "runtime disconnected" || event.GetClientSeq() != 1 {
		t.Fatalf("buffered system error = %#v, want sequenced durable error", event)
	}
}

func TestForwardAgentTurnForwardsCategorizedEvents(t *testing.T) {
	session := &fakeAgentSession{emits: []RuntimeEvent{
		{Role: "tool", Type: "tool_call", Text: "echo hi", Data: `{"command":"echo hi"}`},
		{Role: "agent", Type: "reasoning", Text: "thinking"},
		{Role: "agent", Type: "message", Text: "done"},
	}}
	var events []*corev1.AgentEvent
	err := ForwardTurn(context.Background(), session, "prompt", "conv-1", func(event *corev1.AgentEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatalf("ForwardTurn returned error: %v", err)
	}
	if session.gotPrompt != "prompt" {
		t.Fatalf("session prompt = %q, want prompt", session.gotPrompt)
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

func TestForwardAgentTurnDefaultsRoleAndType(t *testing.T) {
	session := &fakeAgentSession{emits: []RuntimeEvent{{Text: "bare"}}}
	var events []*corev1.AgentEvent
	err := ForwardTurn(context.Background(), session, "prompt", "conv-1", func(event *corev1.AgentEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatalf("ForwardTurn returned error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("event count = %d, want 1", len(events))
	}
	assertAgentEvent(t, events[0], "conv-1", "agent", "delta", "bare", false)
}

func TestForwardAgentTurnPropagatesError(t *testing.T) {
	wantErr := errors.New("boom")
	session := &fakeAgentSession{
		emits: []RuntimeEvent{{Role: "agent", Type: "message", Text: "partial"}},
		err:   wantErr,
	}
	var events []*corev1.AgentEvent
	err := ForwardTurn(context.Background(), session, "prompt", "conv-1", func(event *corev1.AgentEvent) {
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

func TestForwardAgentTurnPropagatesItemIDAndEphemeral(t *testing.T) {
	session := &fakeAgentSession{emits: []RuntimeEvent{
		{Role: "agent", Type: "message_delta", Text: "hel", ItemID: "msg_1", Ephemeral: true},
		{Role: "agent", Type: "message", Text: "hello", ItemID: "msg_1"},
	}}
	var events []*corev1.AgentEvent
	err := ForwardTurn(context.Background(), session, "prompt", "conv-1", func(event *corev1.AgentEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatalf("ForwardTurn returned error: %v", err)
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

func TestNormalizeAgentWorkspaceEventLinks(t *testing.T) {
	workdir := t.TempDir()
	readme := filepath.ToSlash(filepath.Join(workdir, "nic", "realtime", "README.md"))
	outside := filepath.ToSlash(filepath.Join(t.TempDir(), "README.md"))
	event := &corev1.AgentEvent{
		Role: "agent",
		Type: "message_delta",
		Text: "Open [README.md](" + readme + "#L3) and [outside](" + outside + ").",
	}

	NormalizeWorkspaceEventLinks(event, workdir)

	want := "Open [README.md](gsfile:nic/realtime/README.md#L3) and [outside](" + outside + ")."
	if event.GetText() != want {
		t.Fatalf("normalized text = %q, want %q", event.GetText(), want)
	}

	reasoning := &corev1.AgentEvent{Role: "agent", Type: "reasoning_delta", Text: "Open [README.md](" + readme + ")."}
	NormalizeWorkspaceEventLinks(reasoning, workdir)
	if reasoning.GetText() != "Open [README.md]("+readme+")." {
		t.Fatalf("reasoning text was normalized: %q", reasoning.GetText())
	}
}

func TestEnsureSessionResumesThreadID(t *testing.T) {
	session := &fakeAgentSession{threadID: "thread-new"}
	runtime := &fakeAgentRuntime{session: session}
	conv := &Conversation{
		workdir:       "/tmp/work",
		codexThreadID: "thread-prev",
	}

	got, err := conv.ensureSession(context.Background(), runtime, nil)
	if err != nil {
		t.Fatalf("ensureSession returned error: %v", err)
	}
	if got != session {
		t.Fatalf("ensureSession returned %p, want %p", got, session)
	}
	if runtime.gotWorkdir != "/tmp/work" {
		t.Fatalf("runtime workdir = %q, want /tmp/work", runtime.gotWorkdir)
	}
	if runtime.gotResume != "thread-prev" {
		t.Fatalf("runtime resume id = %q, want thread-prev", runtime.gotResume)
	}
	if got.ThreadID() != "thread-new" {
		t.Fatalf("session thread id = %q, want thread-new", got.ThreadID())
	}
}

func TestEnsureSessionCaches(t *testing.T) {
	session := &fakeAgentSession{threadID: "thread-1"}
	runtime := &fakeAgentRuntime{session: session}
	conv := &Conversation{workdir: "/tmp/work"}

	first, err := conv.ensureSession(context.Background(), runtime, nil)
	if err != nil {
		t.Fatalf("first ensureSession returned error: %v", err)
	}
	runtime.session = &fakeAgentSession{threadID: "thread-2"}
	second, err := conv.ensureSession(context.Background(), runtime, nil)
	if err != nil {
		t.Fatalf("second ensureSession returned error: %v", err)
	}
	if first != second {
		t.Fatalf("ensureSession returned different sessions: %p then %p", first, second)
	}
	if runtime.openCount != 1 {
		t.Fatalf("OpenSession calls = %d, want 1", runtime.openCount)
	}
}

func TestEnsureSessionReopensDeadSession(t *testing.T) {
	first := &fakeAgentSession{threadID: "thread-1"}
	runtime := &fakeAgentRuntime{session: first}
	conv := &Conversation{workdir: "/tmp/work"}

	got, err := conv.ensureSession(context.Background(), runtime, nil)
	if err != nil {
		t.Fatalf("first ensureSession returned error: %v", err)
	}
	if got != first {
		t.Fatalf("first ensureSession returned unexpected session")
	}

	first.dead = true
	second := &fakeAgentSession{threadID: "thread-2"}
	runtime.session = second

	got, err = conv.ensureSession(context.Background(), runtime, nil)
	if err != nil {
		t.Fatalf("second ensureSession returned error: %v", err)
	}
	if got != second {
		t.Fatalf("ensureSession did not reopen the dead session")
	}
	if runtime.openCount != 2 {
		t.Fatalf("OpenSession calls = %d, want 2", runtime.openCount)
	}
	if first.closed != 1 {
		t.Fatalf("stale session Close calls = %d, want 1", first.closed)
	}
}

func TestConversationThreadIDRoundTrip(t *testing.T) {
	conv := &Conversation{}
	if conv.getThreadID() != "" {
		t.Fatalf("new conversation thread id = %q, want empty", conv.getThreadID())
	}
	conv.setThreadID("thread-xyz")
	if conv.getThreadID() != "thread-xyz" {
		t.Fatalf("thread id = %q, want thread-xyz", conv.getThreadID())
	}
}

func TestRenderTranscriptKeepsMessagesAndToolCalls(t *testing.T) {
	events := []*corev1.ConversationEvent{
		{Role: "user", Type: "message", Text: "add a readme"},
		{Role: "agent", Type: "reasoning", Text: "SECRETREASONING"},
		{Role: "agent", Type: "message_delta", Text: "DELTACHUNK"},
		{Role: "tool", Type: "tool_call", Text: "echo hi"},
		{Role: "agent", Type: "message", Text: "done"},
		{Role: "system", Type: "turn_complete"},
	}
	got := RenderTranscript(events)
	for _, want := range []string{"[User] add a readme", "[Tool call] echo hi", "[Assistant] done", "transcript start"} {
		if !strings.Contains(got, want) {
			t.Fatalf("transcript missing %q:\n%s", want, got)
		}
	}
	for _, notWant := range []string{"SECRETREASONING", "DELTACHUNK"} {
		if strings.Contains(got, notWant) {
			t.Fatalf("transcript should not contain %q:\n%s", notWant, got)
		}
	}
	if got := RenderTranscript(nil); got != "" {
		t.Fatalf("RenderTranscript(nil) = %q, want empty", got)
	}
}

func TestEnsureSessionSeedsColdThreadHistory(t *testing.T) {
	session := &fakeAgentSession{threadID: "thread-new"}
	runtime := &fakeAgentRuntime{session: session}
	conv := &Conversation{workdir: t.TempDir()}

	if _, err := conv.ensureSession(context.Background(), runtime, func(context.Context) (string, error) {
		return "PRIOR-TRANSCRIPT", nil
	}); err != nil {
		t.Fatalf("ensureSession returned error: %v", err)
	}
	if runtime.gotHistory != "PRIOR-TRANSCRIPT" {
		t.Fatalf("runtime gotHistory = %q, want PRIOR-TRANSCRIPT", runtime.gotHistory)
	}
}
