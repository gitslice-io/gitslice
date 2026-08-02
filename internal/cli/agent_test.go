package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
)

func TestWorkspaceGSUserErrorOmitsCaptureDiagnostics(t *testing.T) {
	got := workspaceGSUserError("capture diagnostics: result=error total=2s\npermission denied\n")
	if got != "permission denied" {
		t.Fatalf("workspace error = %q, want permission denied", got)
	}
}

func TestAgentWorkspaceInstructionsIncludesEditableScope(t *testing.T) {
	got := agentWorkspaceInstructions([]string{"/nic/File"})
	for _, want := range []string{"/nic/File/", "only edit files", "canonical account-rooted", "gsfile:nic/File/Lol.txt", "not\n  `gsfile:Lol.txt`"} {
		if !strings.Contains(got, want) {
			t.Fatalf("instructions missing %q:\n%s", want, got)
		}
	}

	// No included paths falls back to generic scope guidance, not an empty list.
	fallback := agentWorkspaceInstructions(nil)
	if !strings.Contains(fallback, "included paths") {
		t.Fatalf("fallback instructions missing scope guidance:\n%s", fallback)
	}
}

func TestAgentWorkspaceInstructionsExplainGitsliceSideEffects(t *testing.T) {
	got := agentWorkspaceInstructions([]string{"/slices"})
	for _, want := range []string{
		"complete workspace result",
		"only after your turn completes",
		"`gs sync` is a\n  changeset-mutating rebase",
		"`gs import` is a server-side native operation",
		"local `git`\n  executable or `PATH`",
		"Existing or imported `AGENTS.md`",
		"`paths` matches changed repository paths only",
		"`workflow_dispatch`",
		"logical\n  repository-root namespace",
		"keep the source CI definition",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("instructions missing %q:\n%s", want, got)
		}
	}
	for _, notWant := range []string{
		"gs sync              pull the latest slice state",
		"Do NOT create AGENTS.md",
	} {
		if strings.Contains(got, notWant) {
			t.Fatalf("instructions unexpectedly contain %q:\n%s", notWant, got)
		}
	}
}

func TestConversationIncludedPaths(t *testing.T) {
	workdir := t.TempDir()
	gsDir := filepath.Join(workdir, ".gs")
	if err := os.MkdirAll(gsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := WorkspaceConfig{IncludedPaths: []string{"src/", "lib/"}}
	if err := writeJSONFile(filepath.Join(gsDir, "slice.json"), cfg, 0o644); err != nil {
		t.Fatal(err)
	}

	got := conversationIncludedPaths(workdir)
	if len(got) != 2 || got[0] != "src/" || got[1] != "lib/" {
		t.Fatalf("conversationIncludedPaths = %v, want [src/ lib/]", got)
	}

	// Missing config is best-effort: nil, no panic.
	if got := conversationIncludedPaths(t.TempDir()); got != nil {
		t.Fatalf("conversationIncludedPaths(empty) = %v, want nil", got)
	}
}

func TestInboundStale(t *testing.T) {
	base := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	timeout := 50 * time.Second
	tests := []struct {
		name    string
		last    time.Time
		now     time.Time
		timeout time.Duration
		want    bool
	}{
		{
			name:    "fresh",
			last:    base,
			now:     base.Add(49 * time.Second),
			timeout: timeout,
			want:    false,
		},
		{
			name:    "exact timeout",
			last:    base,
			now:     base.Add(timeout),
			timeout: timeout,
			want:    false,
		},
		{
			name:    "over timeout",
			last:    base,
			now:     base.Add(timeout + time.Nanosecond),
			timeout: timeout,
			want:    true,
		},
		{
			name:    "zero last seen",
			last:    time.Time{},
			now:     base.Add(timeout + time.Second),
			timeout: timeout,
			want:    false,
		},
		{
			name:    "clock moved backwards",
			last:    base,
			now:     base.Add(-time.Second),
			timeout: timeout,
			want:    false,
		},
		{
			name:    "nonpositive timeout",
			last:    base,
			now:     base.Add(time.Second),
			timeout: 0,
			want:    false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := inboundStale(tc.last, tc.now, tc.timeout)
			if got != tc.want {
				t.Fatalf("inboundStale() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestAgentInboundStateMarksReceivedMessage(t *testing.T) {
	base := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	inbound := newAgentInboundState(base)
	receivedAt := base.Add(30 * time.Second)

	inbound.mark(receivedAt)

	if got := inbound.lastSeen(); !got.Equal(receivedAt) {
		t.Fatalf("last inbound = %s, want %s", got, receivedAt)
	}
	if inboundStale(inbound.lastSeen(), receivedAt.Add(49*time.Second), agentInboundStaleTimeout) {
		t.Fatalf("recently marked inbound message should keep connection fresh")
	}
}

func TestAgentDaemonIsIdle(t *testing.T) {
	base := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	timeout := time.Hour
	tests := []struct {
		name         string
		lastActivity time.Time
		activeWork   int
		now          time.Time
		timeout      time.Duration
		want         bool
	}{
		{
			name:         "active work blocks idle",
			lastActivity: base,
			activeWork:   1,
			now:          base.Add(2 * timeout),
			timeout:      timeout,
			want:         false,
		},
		{
			name:         "disabled timeout",
			lastActivity: base,
			now:          base.Add(2 * timeout),
			timeout:      0,
			want:         false,
		},
		{
			name:    "zero activity time",
			now:     base.Add(2 * timeout),
			timeout: timeout,
			want:    false,
		},
		{
			name:         "exact timeout",
			lastActivity: base,
			now:          base.Add(timeout),
			timeout:      timeout,
			want:         false,
		},
		{
			name:         "inactive past timeout",
			lastActivity: base,
			now:          base.Add(timeout + time.Nanosecond),
			timeout:      timeout,
			want:         true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := &agentDaemon{
				lastActivity: tc.lastActivity,
				activeWork:   tc.activeWork,
			}
			if got := d.isIdle(tc.now, tc.timeout); got != tc.want {
				t.Fatalf("isIdle() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestAgentDaemonWorkActivityBalance(t *testing.T) {
	timeout := time.Hour
	d := &agentDaemon{}

	d.beginWork()
	if d.isIdle(time.Now().Add(10*timeout), timeout) {
		t.Fatalf("outstanding work should prevent idle shutdown")
	}

	d.endWork()
	if !d.isIdle(time.Now().Add(timeout+time.Second), timeout) {
		t.Fatalf("completed work should become idle after the timeout")
	}

	d.beginWork()
	if d.isIdle(time.Now().Add(10*timeout), timeout) {
		t.Fatalf("new outstanding work should prevent idle shutdown")
	}
	d.endWork()
	d.endWork()
	d.idleMu.Lock()
	activeWork := d.activeWork
	d.idleMu.Unlock()
	if activeWork != 0 {
		t.Fatalf("active work = %d after defensive extra end, want 0", activeWork)
	}
}

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
			conv := &agentConversation{
				id:      "conv-1",
				workdir: t.TempDir(),
				ready:   make(chan struct{}),
				session: session,
			}
			conv.setReady(nil)
			d := &agentDaemon{
				runtime:       runtime,
				baseCtx:       context.Background(),
				conversations: map[string]*agentConversation{conv.id: conv},
			}
			msg := &corev1.DeliverUserMessage{ConversationId: conv.id, Text: "hello", Seq: tc.seq}

			d.handleUserMessage(context.Background(), msg)
			d.handleUserMessage(context.Background(), msg)

			if session.runCount != tc.wantRuns {
				t.Fatalf("runtime turns = %d, want %d", session.runCount, tc.wantRuns)
			}
		})
	}
}

func TestSendSystemErrorBuffersKnownConversation(t *testing.T) {
	conv := &agentConversation{id: "conv-1"}
	d := &agentDaemon{conversations: map[string]*agentConversation{conv.id: conv}}

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

func (s *fakeAgentSession) ThreadID() string { return s.threadID }

func (s *fakeAgentSession) Alive() bool { return !s.dead }

func (s *fakeAgentSession) Close() { s.closed++ }

func TestForwardAgentTurnForwardsCategorizedEvents(t *testing.T) {
	session := &fakeAgentSession{emits: []agentRuntimeEvent{
		{Role: "tool", Type: "tool_call", Text: "echo hi", Data: `{"command":"echo hi"}`},
		{Role: "agent", Type: "reasoning", Text: "thinking"},
		{Role: "agent", Type: "message", Text: "done"},
	}}
	var events []*corev1.AgentEvent
	err := forwardAgentTurn(context.Background(), session, "prompt", "conv-1", func(event *corev1.AgentEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatalf("forwardAgentTurn returned error: %v", err)
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
	session := &fakeAgentSession{emits: []agentRuntimeEvent{{Text: "bare"}}}
	var events []*corev1.AgentEvent
	err := forwardAgentTurn(context.Background(), session, "prompt", "conv-1", func(event *corev1.AgentEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatalf("forwardAgentTurn returned error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("event count = %d, want 1", len(events))
	}
	assertAgentEvent(t, events[0], "conv-1", "agent", "delta", "bare", false)
}

func TestForwardAgentTurnPropagatesError(t *testing.T) {
	wantErr := errors.New("boom")
	session := &fakeAgentSession{
		emits: []agentRuntimeEvent{{Role: "agent", Type: "message", Text: "partial"}},
		err:   wantErr,
	}
	var events []*corev1.AgentEvent
	err := forwardAgentTurn(context.Background(), session, "prompt", "conv-1", func(event *corev1.AgentEvent) {
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
	session := &fakeAgentSession{emits: []agentRuntimeEvent{
		{Role: "agent", Type: "message_delta", Text: "hel", ItemID: "msg_1", Ephemeral: true},
		{Role: "agent", Type: "message", Text: "hello", ItemID: "msg_1"},
	}}
	var events []*corev1.AgentEvent
	err := forwardAgentTurn(context.Background(), session, "prompt", "conv-1", func(event *corev1.AgentEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatalf("forwardAgentTurn returned error: %v", err)
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

	normalizeAgentWorkspaceEventLinks(event, workdir)

	want := "Open [README.md](gsfile:nic/realtime/README.md#L3) and [outside](" + outside + ")."
	if event.GetText() != want {
		t.Fatalf("normalized text = %q, want %q", event.GetText(), want)
	}

	reasoning := &corev1.AgentEvent{Role: "agent", Type: "reasoning_delta", Text: "Open [README.md](" + readme + ")."}
	normalizeAgentWorkspaceEventLinks(reasoning, workdir)
	if reasoning.GetText() != "Open [README.md]("+readme+")." {
		t.Fatalf("reasoning text was normalized: %q", reasoning.GetText())
	}
}

func TestEnsureSessionResumesThreadID(t *testing.T) {
	session := &fakeAgentSession{threadID: "thread-new"}
	runtime := &fakeAgentRuntime{session: session}
	conv := &agentConversation{
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
	conv := &agentConversation{workdir: "/tmp/work"}

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
	conv := &agentConversation{workdir: "/tmp/work"}

	got, err := conv.ensureSession(context.Background(), runtime, nil)
	if err != nil {
		t.Fatalf("first ensureSession returned error: %v", err)
	}
	if got != first {
		t.Fatalf("first ensureSession returned unexpected session")
	}

	// The warm session dies between turns; the next turn must drop and reopen it.
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
	conv := &agentConversation{}
	if conv.getThreadID() != "" {
		t.Fatalf("new conversation thread id = %q, want empty", conv.getThreadID())
	}
	conv.setThreadID("thread-xyz")
	if conv.getThreadID() != "thread-xyz" {
		t.Fatalf("thread id = %q, want thread-xyz", conv.getThreadID())
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

func TestRequireAgentWorkspaceDir(t *testing.T) {
	mkdirAll := func(t *testing.T, path string) {
		t.Helper()
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
	}
	writeFile := func(t *testing.T, path string) {
		t.Helper()
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	t.Run("empty dir is allowed", func(t *testing.T) {
		dir := t.TempDir()
		if err := (Runner{Dir: dir}).requireAgentWorkspaceDir(); err != nil {
			t.Fatalf("empty dir: unexpected error: %v", err)
		}
	})

	t.Run("prior conversations dir is allowed", func(t *testing.T) {
		dir := t.TempDir()
		mkdirAll(t, filepath.Join(dir, "conversations", "conv_1"))
		writeFile(t, filepath.Join(dir, ".DS_Store"))
		if err := (Runner{Dir: dir}).requireAgentWorkspaceDir(); err != nil {
			t.Fatalf("conversations dir: unexpected error: %v", err)
		}
	})

	t.Run("unrelated file is rejected", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "notes.txt"))
		err := (Runner{Dir: dir}).requireAgentWorkspaceDir()
		if !isUserErrorCode(err, "agent_workspace_not_empty") {
			t.Fatalf("expected agent_workspace_not_empty, got %v", err)
		}
	})

	t.Run("inside an existing workspace is rejected", func(t *testing.T) {
		dir := t.TempDir()
		mkdirAll(t, filepath.Join(dir, ".gs"))
		writeFile(t, filepath.Join(dir, ".gs", "slice.json"))
		err := (Runner{Dir: dir}).requireAgentWorkspaceDir()
		if !isUserErrorCode(err, "already_in_workspace") {
			t.Fatalf("expected already_in_workspace, got %v", err)
		}
	})
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
	got := renderTranscript(events)
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
	if got := renderTranscript(nil); got != "" {
		t.Fatalf("renderTranscript(nil) = %q, want empty", got)
	}
}

func TestEnsureSessionSeedsColdThreadHistory(t *testing.T) {
	session := &fakeAgentSession{threadID: "thread-new"}
	runtime := &fakeAgentRuntime{session: session}
	conv := &agentConversation{workdir: t.TempDir()} // no codexThreadID => cold

	if _, err := conv.ensureSession(context.Background(), runtime, func(context.Context) (string, error) {
		return "PRIOR-TRANSCRIPT", nil
	}); err != nil {
		t.Fatalf("ensureSession returned error: %v", err)
	}
	if runtime.gotHistory != "PRIOR-TRANSCRIPT" {
		t.Fatalf("runtime gotHistory = %q, want PRIOR-TRANSCRIPT", runtime.gotHistory)
	}
}

func TestHandleReconcileWorkspacesReapsInactiveOnly(t *testing.T) {
	root := t.TempDir()
	d := &agentDaemon{workingDir: root, conversations: map[string]*agentConversation{}}

	makeConv := func(id string, ready bool) *agentConversation {
		subdir, workdir, err := agentConversationPaths(root, id)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(workdir, 0o755); err != nil {
			t.Fatal(err)
		}
		conv := &agentConversation{id: id, workdir: workdir, workspaceSubdir: subdir, ready: make(chan struct{})}
		if ready {
			conv.setReady(nil)
		}
		d.conversations[id] = conv
		return conv
	}

	keep := makeConv("conv_keep", true)        // active + ready -> kept
	reap := makeConv("conv_reap", true)        // inactive + ready -> reaped
	pending := makeConv("conv_pending", false) // not in set but mid-hydration -> left alone

	d.handleReconcileWorkspaces(&corev1.ReconcileWorkspaces{ActiveConversationIds: []string{"conv_keep"}})

	if !workspaceDirExists(keep.workdir) {
		t.Fatalf("active workspace should be kept")
	}
	if workspaceDirExists(reap.workdir) {
		t.Fatalf("inactive workspace should be removed")
	}
	if !workspaceDirExists(pending.workdir) {
		t.Fatalf("mid-hydration workspace should be left alone")
	}

	d.mu.Lock()
	_, keepMapped := d.conversations["conv_keep"]
	_, reapMapped := d.conversations["conv_reap"]
	_, pendingMapped := d.conversations["conv_pending"]
	d.mu.Unlock()
	if !keepMapped || pendingMapped == false {
		t.Fatalf("kept/pending convs should remain mapped (keep=%v pending=%v)", keepMapped, pendingMapped)
	}
	if reapMapped {
		t.Fatalf("reaped conversation should be dropped from the map")
	}
}
