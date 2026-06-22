package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// agentRuntimeEvent is one categorized chunk of agent runtime output. Role/Type
// mirror the AgentEvent/ConversationEvent contract (proto/core/v1/agent.proto):
// role is "agent" | "system" | "tool"; type is "message" | "reasoning" |
// "tool_call" | "tool_output" | "status" | "error" | "message_delta" |
// "reasoning_delta". Data carries a raw structured payload (JSON) when one is
// available. ItemID correlates streamed deltas with their finalized event, and
// Ephemeral marks a runtime token delta that clients should coalesce.
type agentRuntimeEvent struct {
	Role      string
	Type      string
	Text      string
	Data      string
	ItemID    string
	Ephemeral bool
	Final     bool
}

// agentRuntime opens long-lived runtime sessions, one per conversation.
type agentRuntime interface {
	// OpenSession starts a long-lived runtime session for the given workspace,
	// resuming resumeThreadID when non-empty. The session's underlying process
	// is bound to ctx (the daemon's lifetime) and lives until Close is called;
	// it is not tied to any single turn's context.
	OpenSession(ctx context.Context, workdir, resumeThreadID string) (agentSession, error)
}

// agentSession is a warm runtime session bound to one conversation workspace.
// Turns reuse the same underlying process. It is not safe for concurrent turns;
// the daemon serializes turns per conversation (conv.runMu).
type agentSession interface {
	// RunTurn runs a single turn, calling emit for each categorized event. It
	// returns when the turn completes, the process exits, or ctx is cancelled.
	// Cancelling ctx makes RunTurn return ctx.Err(); it does not itself tear the
	// session down (the caller decides whether to Close).
	RunTurn(ctx context.Context, prompt string, emit func(agentRuntimeEvent)) error
	// ThreadID returns the runtime thread id (for persistence / future resume).
	ThreadID() string
	// Alive reports whether the session's process is still running, so a session
	// that died between turns can be discarded and reopened.
	Alive() bool
	// Close tears down the session's process. Safe to call more than once.
	Close()
}

type codexRuntime struct {
	Binary string
}

// deltaThrottle bounds how often accumulated token deltas are flushed upstream,
// so a fast token stream doesn't overwhelm the relay.
const deltaThrottle = 60 * time.Millisecond

type codexSession struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	client    *codexClient
	stderr    *safeBuffer
	waitCh    chan error
	threadID  string
	closeOnce sync.Once
}

func (s *codexSession) ThreadID() string { return s.threadID }

func (s *codexSession) Alive() bool {
	select {
	case <-s.client.closed:
		return false
	default:
		return true
	}
}

func (s *codexSession) stderrErr(base error) error {
	if tail := strings.TrimSpace(s.stderr.String()); tail != "" {
		return fmt.Errorf("%w: %s", base, lastLines(tail, 5))
	}
	return base
}

func (s *codexSession) Close() {
	s.closeOnce.Do(func() {
		_ = s.stdin.Close()
		if s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
		<-s.waitCh
	})
}

func (s *codexSession) RunTurn(ctx context.Context, prompt string, emit func(agentRuntimeEvent)) error {
	select {
	case <-s.client.closed:
		return s.stderrErr(fmt.Errorf("codex app-server is not running"))
	default:
	}

	turn := &codexTurn{emit: emit, accum: map[string]*deltaAccum{}, done: make(chan error, 1)}
	s.client.setTurn(turn)
	defer s.client.setTurn(nil)

	if _, err := s.client.call(ctx, "turn/start", map[string]any{
		"threadId": s.threadID,
		"summary":  "detailed",
		"input":    []map[string]any{{"type": "text", "text": prompt}},
	}); err != nil {
		return s.stderrErr(fmt.Errorf("codex turn/start failed: %w", err))
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-turn.done:
		return err
	case <-s.client.closed:
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return s.stderrErr(fmt.Errorf("codex app-server exited before the turn completed"))
	}
}

func (r codexRuntime) OpenSession(ctx context.Context, workdir, resumeThreadID string) (agentSession, error) {
	binary := r.Binary
	if binary == "" {
		binary = "codex"
	}

	// `codex app-server` is a long-lived JSON-RPC server over stdio. Driving it
	// gives token-level streaming: agent message and reasoning text arrive as
	// deltas we relay live, with finalized items persisted at turn completion.
	cmd := exec.CommandContext(ctx, binary, "app-server")
	cmd.Dir = workdir

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr := &safeBuffer{}
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	client := newCodexClient(stdin)
	session := &codexSession{
		cmd:    cmd,
		stdin:  stdin,
		client: client,
		stderr: stderr,
		waitCh: waitCh,
	}
	go client.readLoop(stdout)

	if _, err := client.call(ctx, "initialize", initializeParams()); err != nil {
		session.Close()
		return nil, session.stderrErr(fmt.Errorf("codex initialize failed: %w", err))
	}
	if err := client.notify("initialized", struct{}{}); err != nil {
		session.Close()
		return nil, session.stderrErr(fmt.Errorf("codex initialized notify failed: %w", err))
	}

	threadID, err := client.openThread(ctx, workdir, resumeThreadID)
	if err != nil {
		session.Close()
		return nil, session.stderrErr(err)
	}

	session.threadID = threadID
	return session, nil
}

// openThread resumes an existing codex thread when resumeID is set, falling back
// to starting a fresh thread if no id is given or the resume fails (e.g. the
// session has aged out). It returns the live thread id either way.
func (c *codexClient) openThread(ctx context.Context, workdir, resumeID string) (string, error) {
	base := map[string]any{
		"cwd":            workdir,
		"sandbox":        "danger-full-access",
		"approvalPolicy": "never",
	}
	if resumeID != "" {
		params := map[string]any{"threadId": resumeID}
		for k, v := range base {
			params[k] = v
		}
		if raw, err := c.call(ctx, "thread/resume", params); err == nil {
			if id := parseThreadID(raw); id != "" {
				return id, nil
			}
		} else if ctx.Err() != nil {
			return "", ctx.Err()
		}
		// Resume failed for a non-cancellation reason: start fresh below.
	}

	raw, err := c.call(ctx, "thread/start", base)
	if err != nil {
		return "", fmt.Errorf("codex thread/start failed: %w", err)
	}
	id := parseThreadID(raw)
	if id == "" {
		return "", fmt.Errorf("codex thread/start returned no thread id")
	}
	return id, nil
}

func parseThreadID(raw json.RawMessage) string {
	var resp struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return ""
	}
	return resp.Thread.ID
}

func initializeParams() map[string]any {
	version := cliVersionInfo().Version
	if strings.TrimSpace(version) == "" {
		version = "dev"
	}
	return map[string]any{
		"clientInfo": map[string]any{
			"name":    "gitslice",
			"title":   "gitslice agent",
			"version": version,
		},
	}
}

// ---- JSON-RPC plumbing ----

type jsonrpcMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *jsonrpcError   `json:"error,omitempty"`
}

type jsonrpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *jsonrpcError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("jsonrpc error %d: %s", e.Code, e.Message)
}

type codexClient struct {
	mu     sync.Mutex // serializes writes to stdin
	stdin  io.Writer
	nextID int64

	tmu     sync.Mutex
	curTurn *codexTurn

	pmu     sync.Mutex
	pending map[int64]chan jsonrpcMessage
	closed  chan struct{}
	once    sync.Once
}

func newCodexClient(stdin io.Writer) *codexClient {
	return &codexClient{
		stdin:   stdin,
		pending: map[int64]chan jsonrpcMessage{},
		closed:  make(chan struct{}),
	}
}

func (c *codexClient) write(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.stdin.Write(append(data, '\n')); err != nil {
		return err
	}
	return nil
}

func (c *codexClient) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.pmu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan jsonrpcMessage, 1)
	c.pending[id] = ch
	c.pmu.Unlock()
	defer func() {
		c.pmu.Lock()
		delete(c.pending, id)
		c.pmu.Unlock()
	}()

	if err := c.write(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.closed:
		return nil, io.ErrUnexpectedEOF
	case msg := <-ch:
		if msg.Error != nil {
			return nil, msg.Error
		}
		return msg.Result, nil
	}
}

func (c *codexClient) notify(method string, params any) error {
	return c.write(map[string]any{"method": method, "params": params})
}

func (c *codexClient) reply(id json.RawMessage, result any) error {
	return c.write(map[string]any{"id": id, "result": result})
}

func (c *codexClient) setTurn(turn *codexTurn) {
	c.tmu.Lock()
	defer c.tmu.Unlock()
	c.curTurn = turn
}

func (c *codexClient) currentTurn() *codexTurn {
	c.tmu.Lock()
	defer c.tmu.Unlock()
	return c.curTurn
}

// readLoop consumes the app-server's stdout, routing responses to waiters and
// notifications/requests to the turn handler. It runs until stdout closes.
func (c *codexClient) readLoop(stdout io.Reader) {
	defer c.once.Do(func() { close(c.closed) })
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var msg jsonrpcMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		switch {
		case msg.Method != "" && len(msg.ID) > 0:
			// Server -> client request. We run with approvalPolicy "never", so
			// approvals shouldn't arrive; reply best-effort to avoid stalling.
			c.handleServerRequest(msg)
		case msg.Method != "":
			if t := c.currentTurn(); t != nil {
				t.handleNotification(msg.Method, msg.Params)
			}
		case len(msg.ID) > 0:
			c.deliver(msg)
		}
	}
}

func (c *codexClient) deliver(msg jsonrpcMessage) {
	var id int64
	if err := json.Unmarshal(msg.ID, &id); err != nil {
		return
	}
	c.pmu.Lock()
	ch := c.pending[id]
	c.pmu.Unlock()
	if ch != nil {
		ch <- msg
	}
}

func (c *codexClient) handleServerRequest(msg jsonrpcMessage) {
	switch msg.Method {
	case "item/commandExecution/requestApproval",
		"item/fileChange/requestApproval",
		"item/permissions/requestApproval",
		"applyPatchApproval",
		"execCommandApproval":
		_ = c.reply(msg.ID, map[string]any{"decision": "approved"})
	default:
		_ = c.reply(msg.ID, map[string]any{})
	}
}

// ---- Turn streaming ----

type codexTurn struct {
	emit  func(agentRuntimeEvent)
	accum map[string]*deltaAccum // itemId -> accumulated delta text (reader goroutine only)
	done  chan error
	once  sync.Once
}

type deltaAccum struct {
	typ       string // "message_delta" | "reasoning_delta"
	buf       strings.Builder
	lastFlush time.Time
}

func (t *codexTurn) finish(err error) {
	t.once.Do(func() { t.done <- err })
}

func (t *codexTurn) handleNotification(method string, params json.RawMessage) {
	switch method {
	case "item/agentMessage/delta":
		t.accumulate(params, "message_delta")
	case "item/reasoning/summaryTextDelta", "item/reasoning/textDelta":
		t.accumulate(params, "reasoning_delta")
	case "item/completed":
		t.handleItemCompleted(params)
	case "turn/completed":
		t.finish(nil)
	case "error":
		t.handleError(params)
	}
}

func (t *codexTurn) accumulate(params json.RawMessage, typ string) {
	var p struct {
		Delta  string `json:"delta"`
		ItemID string `json:"itemId"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.ItemID == "" {
		return
	}
	st := t.accum[p.ItemID]
	if st == nil {
		st = &deltaAccum{typ: typ}
		t.accum[p.ItemID] = st
	}
	st.buf.WriteString(p.Delta)

	now := time.Now()
	if now.Sub(st.lastFlush) < deltaThrottle {
		return
	}
	st.lastFlush = now
	t.emit(agentRuntimeEvent{
		Role:      "agent",
		Type:      typ,
		Text:      st.buf.String(),
		ItemID:    p.ItemID,
		Ephemeral: true,
	})
}

func (t *codexTurn) handleItemCompleted(params json.RawMessage) {
	var p struct {
		Item json.RawMessage `json:"item"`
	}
	if err := json.Unmarshal(params, &p); err != nil || len(p.Item) == 0 {
		return
	}
	var item codexItem
	if err := json.Unmarshal(p.Item, &item); err != nil {
		return
	}
	delete(t.accum, item.ID)
	data := string(p.Item)

	switch item.Type {
	case "userMessage":
		// Echo of the user's own input; already persisted by the server.
		return
	case "agentMessage":
		text := strings.TrimSpace(item.Text)
		if text == "" {
			return
		}
		t.emit(agentRuntimeEvent{Role: "agent", Type: "message", Text: text, ItemID: item.ID})
	case "reasoning":
		text := strings.TrimSpace(item.Text)
		if text == "" {
			return
		}
		t.emit(agentRuntimeEvent{Role: "agent", Type: "reasoning", Text: text, Data: data, ItemID: item.ID})
	case "commandExecution", "fileChange", "patchApply", "mcpToolCall", "webSearch", "todoList":
		t.emit(agentRuntimeEvent{Role: "tool", Type: "tool_call", Text: codexItemLabel(item), Data: data, ItemID: item.ID})
	default:
		// Unknown item type: surface its payload so nothing is silently dropped.
		t.emit(agentRuntimeEvent{Role: "agent", Type: "delta", Text: codexItemLabel(item), Data: data, ItemID: item.ID})
	}
}

func (t *codexTurn) handleError(params json.RawMessage) {
	var p struct {
		Error json.RawMessage `json:"error"`
	}
	_ = json.Unmarshal(params, &p)
	msg := errorMessage(p.Error)
	t.emit(agentRuntimeEvent{Role: "system", Type: "error", Text: msg, Data: string(p.Error)})
	t.finish(fmt.Errorf("codex turn error: %s", msg))
}

func errorMessage(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "agent runtime reported an error"
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil && strings.TrimSpace(s) != "" {
		return s
	}
	var obj struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil && strings.TrimSpace(obj.Message) != "" {
		return obj.Message
	}
	return strings.TrimSpace(string(raw))
}

// codexItem covers the union of fields across app-server thread item types
// (camelCase). Only the fields relevant to a given item.type are populated.
type codexItem struct {
	ID               string `json:"id"`
	Type             string `json:"type"`
	Text             string `json:"text"`
	Command          string `json:"command"`
	AggregatedOutput string `json:"aggregatedOutput"`
	ExitCode         *int   `json:"exitCode"`
	Status           string `json:"status"`
	Server           string `json:"server"`
	Tool             string `json:"tool"`
	Query            string `json:"query"`
	Changes          []struct {
		Path string `json:"path"`
		Kind string `json:"kind"`
	} `json:"changes"`
}

// codexItemLabel builds a short, human-readable label for a tool/trace item.
func codexItemLabel(item codexItem) string {
	switch item.Type {
	case "commandExecution":
		return strings.TrimSpace(item.Command)
	case "fileChange", "patchApply":
		paths := make([]string, 0, len(item.Changes))
		for _, change := range item.Changes {
			if strings.TrimSpace(change.Path) != "" {
				paths = append(paths, change.Path)
			}
		}
		if len(paths) > 0 {
			return "edit " + strings.Join(paths, ", ")
		}
		return "file change"
	case "mcpToolCall":
		name := strings.TrimSpace(item.Tool)
		if item.Server != "" {
			name = strings.TrimSpace(item.Server) + "." + name
		}
		if name != "" {
			return name
		}
		return "tool call"
	case "webSearch":
		if q := strings.TrimSpace(item.Query); q != "" {
			return "search: " + q
		}
		return "web search"
	case "todoList":
		return "todo list"
	default:
		if text := strings.TrimSpace(item.Text); text != "" {
			return text
		}
		return strings.TrimSpace(item.Type)
	}
}

// safeBuffer is a goroutine-safe bytes.Buffer for collecting stderr.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
