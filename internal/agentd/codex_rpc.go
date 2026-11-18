package agentd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

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
	mu     sync.Mutex
	stdin  io.Writer
	nextID int64

	tmu     sync.Mutex
	curTurn *CodexTurn

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

func (c *codexClient) openThread(ctx context.Context, workdir, resumeID, instructions string, freshThreadHistory func(context.Context) (string, error)) (string, error) {
	base := map[string]any{
		"cwd":            workdir,
		"sandbox":        CodexSandboxMode(),
		"approvalPolicy": "never",
	}
	devInstructions := strings.TrimSpace(instructions)
	if resumeID != "" {
		params := map[string]any{"threadId": resumeID}
		for k, v := range base {
			params[k] = v
		}
		if devInstructions != "" {
			params["developerInstructions"] = devInstructions
		}
		if raw, err := c.call(ctx, "thread/resume", params); err == nil {
			if id := parseThreadID(raw); id != "" {
				return id, nil
			}
		} else if ctx.Err() != nil {
			return "", ctx.Err()
		}
	}

	if freshThreadHistory != nil {
		if history, err := freshThreadHistory(ctx); err == nil {
			if history = strings.TrimSpace(history); history != "" {
				if devInstructions != "" {
					devInstructions += "\n\n"
				}
				devInstructions += history
			}
		}
	}
	if devInstructions != "" {
		base["developerInstructions"] = devInstructions
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

func (c *codexClient) setTurn(turn *CodexTurn) {
	c.tmu.Lock()
	defer c.tmu.Unlock()
	c.curTurn = turn
}

func (c *codexClient) currentTurn() *CodexTurn {
	c.tmu.Lock()
	defer c.tmu.Unlock()
	return c.curTurn
}

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
			c.handleServerRequest(msg)
		case msg.Method != "":
			if t := c.currentTurn(); t != nil {
				t.HandleNotification(msg.Method, msg.Params)
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

type CodexTurn struct {
	Emit  func(RuntimeEvent)
	Accum map[string]*DeltaAccum
	Done  chan error
	once  sync.Once
}

type DeltaAccum struct {
	Typ       string
	Buf       strings.Builder
	LastFlush time.Time
}

func NewCodexTurn(emit func(RuntimeEvent)) *CodexTurn {
	return &CodexTurn{
		Emit:  emit,
		Accum: map[string]*DeltaAccum{},
		Done:  make(chan error, 1),
	}
}

func (t *CodexTurn) finish(err error) {
	t.once.Do(func() { t.Done <- err })
}

func (t *CodexTurn) HandleNotification(method string, params json.RawMessage) {
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

func (t *CodexTurn) accumulate(params json.RawMessage, typ string) {
	var p struct {
		Delta  string `json:"delta"`
		ItemID string `json:"itemId"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.ItemID == "" {
		return
	}
	if t.Accum == nil {
		t.Accum = map[string]*DeltaAccum{}
	}
	st := t.Accum[p.ItemID]
	if st == nil {
		st = &DeltaAccum{Typ: typ}
		t.Accum[p.ItemID] = st
	}
	st.Buf.WriteString(p.Delta)

	now := time.Now()
	if now.Sub(st.LastFlush) < DeltaThrottle {
		return
	}
	st.LastFlush = now
	t.Emit(RuntimeEvent{
		Role:      "agent",
		Type:      typ,
		Text:      st.Buf.String(),
		ItemID:    p.ItemID,
		Ephemeral: true,
	})
}

func (t *CodexTurn) handleItemCompleted(params json.RawMessage) {
	var p struct {
		Item json.RawMessage `json:"item"`
	}
	if err := json.Unmarshal(params, &p); err != nil || len(p.Item) == 0 {
		return
	}
	var item CodexItem
	if err := json.Unmarshal(p.Item, &item); err != nil {
		return
	}
	delete(t.Accum, item.ID)
	data := string(p.Item)

	switch item.Type {
	case "userMessage":
		return
	case "agentMessage":
		text := strings.TrimSpace(item.Text)
		if text == "" {
			return
		}
		t.Emit(RuntimeEvent{Role: "agent", Type: "message", Text: text, ItemID: item.ID})
	case "reasoning":
		text := strings.TrimSpace(item.Text)
		if text == "" {
			return
		}
		t.Emit(RuntimeEvent{Role: "agent", Type: "reasoning", Text: text, Data: data, ItemID: item.ID})
	case "commandExecution", "fileChange", "patchApply", "mcpToolCall", "webSearch", "todoList":
		t.Emit(RuntimeEvent{Role: "tool", Type: "tool_call", Text: CodexItemLabel(item), Data: data, ItemID: item.ID})
	default:
		t.Emit(RuntimeEvent{Role: "agent", Type: "delta", Text: CodexItemLabel(item), Data: data, ItemID: item.ID})
	}
}

func (t *CodexTurn) handleError(params json.RawMessage) {
	var p struct {
		Error json.RawMessage `json:"error"`
	}
	_ = json.Unmarshal(params, &p)
	msg := errorMessage(p.Error)
	t.Emit(RuntimeEvent{Role: "system", Type: "error", Text: msg, Data: string(p.Error)})
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

type CodexItem struct {
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

func CodexItemLabel(item CodexItem) string {
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
