package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// agentRuntimeEvent is one categorized chunk of agent runtime output. Role/Type
// mirror the AgentEvent/ConversationEvent contract (proto/core/v1/agent.proto):
// role is "agent" | "system" | "tool"; type is "message" | "reasoning" |
// "tool_call" | "tool_output" | "status" | "error" | "delta". Data carries the
// raw structured payload (JSON) when one is available.
type agentRuntimeEvent struct {
	Role  string
	Type  string
	Text  string
	Data  string
	Final bool
}

type agentRuntime interface {
	// Run executes one turn in workdir for the given prompt, calling emit for
	// each categorized event. It must stop when ctx is cancelled.
	Run(ctx context.Context, workdir, prompt string, emit func(agentRuntimeEvent)) error
}

type codexRuntime struct {
	Binary string
}

func (r codexRuntime) Run(ctx context.Context, workdir, prompt string, emit func(agentRuntimeEvent)) error {
	binary := r.Binary
	if binary == "" {
		binary = "codex"
	}

	// --json makes codex emit structured JSONL thread events on stdout so we can
	// categorize them (agent messages vs. reasoning vs. tool activity) instead
	// of forwarding raw transcript text. Human-readable diagnostics still go to
	// stderr, which we keep separate so they never pollute the JSON stream.
	//
	// The reasoning-summary config is required for codex to emit `reasoning`
	// thread items at all: without it the model still reasons (reported only as
	// usage token counts) but the JSON stream carries no reasoning items, so the
	// UI would show tool calls with no accompanying thinking.
	cmd := exec.CommandContext(ctx, binary, "exec", "--json",
		"-c", "model_reasoning_summary=detailed",
		"-c", "model_reasoning_summary_format=experimental",
		"--dangerously-bypass-approvals-and-sandbox", "--cd", workdir, prompt)
	cmd.Dir = workdir

	stdout, writer := io.Pipe()
	cmd.Stdout = writer

	stderrReader, stderrWriter := io.Pipe()
	cmd.Stderr = stderrWriter

	if err := cmd.Start(); err != nil {
		_ = writer.Close()
		_ = stdout.Close()
		_ = stderrWriter.Close()
		_ = stderrReader.Close()
		return err
	}

	// Drain stderr concurrently, keeping the tail to surface on a non-zero exit.
	stderrCh := make(chan string, 1)
	go func() {
		stderrCh <- drainStderr(stderrReader)
	}()

	waitCh := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		_ = writer.Close()
		_ = stderrWriter.Close()
		waitCh <- err
	}()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		for _, event := range parseCodexLine(line) {
			emit(event)
		}
	}
	scanErr := scanner.Err()
	waitErr := <-waitCh
	stderrTail := <-stderrCh
	_ = stdout.Close()
	_ = stderrReader.Close()

	if scanErr != nil {
		return scanErr
	}
	if waitErr != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if stderrTail != "" {
			return fmt.Errorf("%w: %s", waitErr, stderrTail)
		}
		return waitErr
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

// drainStderr reads stderr to completion and returns a trimmed tail of the last
// few lines, used to enrich the error returned on a non-zero exit.
func drainStderr(r io.Reader) string {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 16*1024), 1024*1024)
	var lines []string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			lines = append(lines, line)
		}
	}
	const maxTail = 5
	if len(lines) > maxTail {
		lines = lines[len(lines)-maxTail:]
	}
	return strings.Join(lines, "\n")
}

// codexEnvelope is the outer JSONL frame emitted by `codex exec --json`.
type codexEnvelope struct {
	Type    string          `json:"type"`
	Item    json.RawMessage `json:"item"`
	Message string          `json:"message"`
	Error   string          `json:"error"`
}

// codexItem covers the union of fields across codex thread item types. Only the
// fields relevant to a given item.type are populated; the rest stay zero.
type codexItem struct {
	ID               string `json:"id"`
	Type             string `json:"type"`
	Text             string `json:"text"`
	Command          string `json:"command"`
	AggregatedOutput string `json:"aggregated_output"`
	ExitCode         *int   `json:"exit_code"`
	Status           string `json:"status"`
	Server           string `json:"server"`
	Tool             string `json:"tool"`
	Query            string `json:"query"`
	Changes          []struct {
		Path string `json:"path"`
		Kind string `json:"kind"`
	} `json:"changes"`
}

// parseCodexLine converts one JSONL event line into zero or more categorized
// runtime events. Non-JSON lines are forwarded as opaque deltas so nothing is
// silently dropped if codex changes its output.
func parseCodexLine(line string) []agentRuntimeEvent {
	var env codexEnvelope
	if err := json.Unmarshal([]byte(line), &env); err != nil || env.Type == "" {
		return []agentRuntimeEvent{{Role: "agent", Type: "delta", Text: line}}
	}

	switch env.Type {
	case "item.completed":
		if event, ok := codexItemEvent(env.Item); ok {
			return []agentRuntimeEvent{event}
		}
		return nil
	case "error", "turn.failed":
		text := strings.TrimSpace(env.Message)
		if text == "" {
			text = strings.TrimSpace(env.Error)
		}
		if text == "" {
			text = "agent runtime reported an error"
		}
		return []agentRuntimeEvent{{Role: "system", Type: "error", Text: text, Data: line}}
	default:
		// thread.started, turn.started, item.started, item.updated,
		// turn.completed, and any future envelope types carry no user-facing
		// content of their own.
		return nil
	}
}

// codexItemEvent maps a completed thread item to a categorized runtime event.
func codexItemEvent(raw json.RawMessage) (agentRuntimeEvent, bool) {
	if len(raw) == 0 {
		return agentRuntimeEvent{}, false
	}
	var item codexItem
	if err := json.Unmarshal(raw, &item); err != nil {
		return agentRuntimeEvent{Role: "agent", Type: "delta", Data: string(raw)}, true
	}
	data := string(raw)

	switch item.Type {
	case "agent_message":
		text := strings.TrimSpace(item.Text)
		if text == "" {
			return agentRuntimeEvent{}, false
		}
		return agentRuntimeEvent{Role: "agent", Type: "message", Text: text}, true
	case "reasoning":
		text := strings.TrimSpace(item.Text)
		if text == "" {
			return agentRuntimeEvent{}, false
		}
		return agentRuntimeEvent{Role: "agent", Type: "reasoning", Text: text, Data: data}, true
	case "command_execution":
		return agentRuntimeEvent{Role: "tool", Type: "tool_call", Text: codexItemLabel(item), Data: data}, true
	case "file_change", "patch_apply", "mcp_tool_call", "web_search", "todo_list":
		return agentRuntimeEvent{Role: "tool", Type: "tool_call", Text: codexItemLabel(item), Data: data}, true
	default:
		return agentRuntimeEvent{Role: "agent", Type: "delta", Text: codexItemLabel(item), Data: data}, true
	}
}

// codexItemLabel builds a short, human-readable label for a tool/trace item.
func codexItemLabel(item codexItem) string {
	switch item.Type {
	case "command_execution":
		return strings.TrimSpace(item.Command)
	case "file_change", "patch_apply":
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
	case "mcp_tool_call":
		name := strings.TrimSpace(item.Tool)
		if item.Server != "" {
			name = strings.TrimSpace(item.Server) + "." + name
		}
		if name != "" {
			return name
		}
		return "tool call"
	case "web_search":
		if q := strings.TrimSpace(item.Query); q != "" {
			return "search: " + q
		}
		return "web search"
	case "todo_list":
		return "todo list"
	default:
		if text := strings.TrimSpace(item.Text); text != "" {
			return text
		}
		return strings.TrimSpace(item.Type)
	}
}
