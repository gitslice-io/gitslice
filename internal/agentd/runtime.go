package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

const (
	DefaultRuntimeName = "codex"
	CIRuntimeName      = "ci"
	DeltaThrottle      = 60 * time.Millisecond
)

// RuntimeEvent is one categorized chunk of agent runtime output.
type RuntimeEvent struct {
	Role      string
	Type      string
	Text      string
	Data      string
	ItemID    string
	Ephemeral bool
	Final     bool
}

// Runtime opens long-lived runtime sessions, one per conversation.
type Runtime interface {
	OpenSession(ctx context.Context, workdir, resumeThreadID, instructions string, freshThreadHistory func(context.Context) (string, error)) (Session, error)
}

// Session is a warm runtime session bound to one conversation workspace.
type Session interface {
	RunTurn(ctx context.Context, prompt string, emit func(RuntimeEvent)) error
	ThreadID() string
	Alive() bool
	Close()
}

type CodexRuntime struct {
	Binary  string
	Version string
}

func RuntimeForName(name, version string) (Runtime, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", DefaultRuntimeName:
		return CodexRuntime{Version: version}, nil
	default:
		return nil, NewError("invalid_agent_runtime", "unsupported agent runtime: "+name, "Only codex is supported in this version.")
	}
}

func ForwardTurn(ctx context.Context, session Session, prompt, conversationID string, emitEvent func(*corev1.AgentEvent)) error {
	return session.RunTurn(ctx, prompt, func(event RuntimeEvent) {
		role := event.Role
		if role == "" {
			role = "agent"
		}
		eventType := event.Type
		if eventType == "" {
			eventType = "delta"
		}
		emitEvent(&corev1.AgentEvent{
			ConversationId: conversationID,
			Role:           role,
			Type:           eventType,
			Text:           event.Text,
			DataJson:       event.Data,
			Final:          event.Final,
			ItemId:         event.ItemID,
			Ephemeral:      event.Ephemeral,
		})
	})
}

type codexSession struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	client    *codexClient
	stderr    *SafeBuffer
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

func (s *codexSession) RunTurn(ctx context.Context, prompt string, emit func(RuntimeEvent)) error {
	select {
	case <-s.client.closed:
		return s.stderrErr(fmt.Errorf("codex app-server is not running"))
	default:
	}

	turn := NewCodexTurn(emit)
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
	case err := <-turn.Done:
		return err
	case <-s.client.closed:
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return s.stderrErr(fmt.Errorf("codex app-server exited before the turn completed"))
	}
}

func (r CodexRuntime) OpenSession(ctx context.Context, workdir, resumeThreadID, instructions string, freshThreadHistory func(context.Context) (string, error)) (Session, error) {
	binary := r.Binary
	if binary == "" {
		binary = "codex"
	}

	cmd := exec.CommandContext(ctx, binary, "app-server")
	cmd.Dir = workdir
	cmd.Env = MergeEnv(SanitizedRuntimeEnv(os.Environ()), RuntimeEnvFrom(ctx))

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr := &SafeBuffer{}
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

	if _, err := client.call(ctx, "initialize", initializeParams(r.Version)); err != nil {
		session.Close()
		return nil, session.stderrErr(fmt.Errorf("codex initialize failed: %w", err))
	}
	if err := client.notify("initialized", struct{}{}); err != nil {
		session.Close()
		return nil, session.stderrErr(fmt.Errorf("codex initialized notify failed: %w", err))
	}

	threadID, err := client.openThread(ctx, workdir, resumeThreadID, instructions, freshThreadHistory)
	if err != nil {
		session.Close()
		return nil, session.stderrErr(err)
	}

	session.threadID = threadID
	return session, nil
}

func CodexSandboxMode() string {
	switch mode := strings.ToLower(strings.TrimSpace(os.Getenv("GITSLICE_CODEX_SANDBOX"))); mode {
	case "danger-full-access", "workspace-write", "read-only":
		return mode
	}
	v := strings.TrimSpace(os.Getenv("GS_CODEX_DANGER_FULL_ACCESS"))
	if v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes") || strings.EqualFold(v, "on") {
		return "danger-full-access"
	}
	return "workspace-write"
}

func SanitizedRuntimeEnv(environ []string) []string {
	allowGSToken := strings.EqualFold(strings.TrimSpace(os.Getenv("GS_AGENT_ALLOW_GS_TOKEN")), "1") ||
		strings.EqualFold(strings.TrimSpace(os.Getenv("GS_AGENT_ALLOW_GS_TOKEN")), "true")
	out := make([]string, 0, len(environ))
	for _, entry := range environ {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if IsSensitiveHostEnvKey(key, allowGSToken) {
			continue
		}
		out = append(out, entry)
	}
	return out
}

type runtimeEnvKey struct{}

func WithRuntimeEnv(ctx context.Context, env map[string]string) context.Context {
	if len(env) == 0 {
		return ctx
	}
	cp := make(map[string]string, len(env))
	for k, v := range env {
		cp[k] = v
	}
	return context.WithValue(ctx, runtimeEnvKey{}, cp)
}

func RuntimeEnvFrom(ctx context.Context) map[string]string {
	if ctx == nil {
		return nil
	}
	env, _ := ctx.Value(runtimeEnvKey{}).(map[string]string)
	if len(env) == 0 {
		return nil
	}
	cp := make(map[string]string, len(env))
	for k, v := range env {
		cp[k] = v
	}
	return cp
}

func MergeEnv(base []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return base
	}
	out := make([]string, 0, len(base)+len(overrides))
	for _, entry := range base {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, overridden := overrides[key]; overridden {
			continue
		}
		out = append(out, entry)
	}
	keys := make([]string, 0, len(overrides))
	for k := range overrides {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		out = append(out, k+"="+overrides[k])
	}
	return out
}

func IsSensitiveHostEnvKey(key string, allowGSToken bool) bool {
	upper := strings.ToUpper(strings.TrimSpace(key))
	if upper == "GS_TOKEN" {
		return !allowGSToken
	}
	switch upper {
	case "GS_SERVICE_PRIVATE_KEY",
		"GS_SERVICE_PRIVATE_KEY_FILE",
		"GITSLICE_TOKEN",
		"GITSLICE_API_TOKEN",
		"GITSLICE_RUNNER_TOKEN",
		"GITSLICE_SECRETS_KEY",
		"GITSLICE_DOORBELL_SECRET",
		"GITSLICE_TRIGGER_SECRET",
		"GITSLICE_WEBHOOK_SECRET",
		"GITSLICE_INTERNAL_TOKEN",
		"CLERK_SECRET_KEY",
		"DATABASE_URL":
		return true
	}
	return false
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

func initializeParams(version string) map[string]any {
	version = strings.TrimSpace(version)
	if version == "" {
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

// SafeBuffer is a goroutine-safe bytes.Buffer for collecting stderr.
type SafeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *SafeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *SafeBuffer) String() string {
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
