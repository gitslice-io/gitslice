package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
)

// gs mcp serves Gitslice to an agent over the Model Context Protocol, on
// stdin and stdout: reading, searching and changing slices without a
// workspace (design/04_cli_design.md, "gs mcp"). Messages are JSON-RPC 2.0,
// one per line. Anything that is not the protocol goes to stderr.

// mcpProtocolVersions are the protocol versions the server speaks, newest
// first; a client asking for another gets the newest.
var mcpProtocolVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

// mcpMaxLine bounds one message: an edit carries whole files.
const mcpMaxLine = 64 << 20

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *mcpError       `json:"error,omitempty"`
}

type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

const (
	mcpParseError     = -32700
	mcpInvalidRequest = -32600
	mcpMethodNotFound = -32601
	mcpInvalidParams  = -32602
)

// mcpTool is one tool: its schema for the client and what it does.
type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	call        func(ctx context.Context, args json.RawMessage) (any, error)
}

// mcpServer answers one client.
type mcpServer struct {
	tools   []mcpTool
	version string
	trace   func(event map[string]any)
	out     io.Writer
	outMu   sync.Mutex
}

func (r Runner) mcpCommand(opts *commandOptions) *cobra.Command {
	slice := ""
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve Gitslice to an agent over MCP (stdio): read, search and change slices without a workspace",
		Long: "gs mcp is a Model Context Protocol server on stdin and stdout, for agents that speak MCP. " +
			"Its tools read files, list folders, search, show history and blame, and make changesets " +
			"(write, delete, move, copy) and submit them, as the signed-in user. Errors come back structured: " +
			"a refused submit names its reason (a pending check, a conflict and its paths). " +
			"--slice sets the slice tools use when a call names none; inside a workspace it defaults to the workspace's. " +
			"Set GS_TRACE to a file path to log every server call and tool call there as JSON lines.\n\n" +
			"Add it to an MCP client as the command: gs mcp [--slice account/slice]",
		Args: maxArgs(0, "gs mcp [--slice account/slice]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runMCP(cmd.Context(), *opts, slice)
		},
	}
	cmd.Flags().StringVar(&slice, "slice", "", "the slice tools use when a call names none (default: the workspace's)")
	return cmd
}

func (r Runner) runMCP(ctx context.Context, opts commandOptions, defaultSlice string) error {
	tools, closeTools, err := r.mcpTools(ctx, defaultSlice)
	if err != nil {
		return err
	}
	defer closeTools()
	server := &mcpServer{tools: tools, version: cliVersionInfo().Version, trace: traceEvent, out: r.stdout()}
	return server.serve(ctx, r.Stdin)
}

// serve reads messages until the input ends.
func (s *mcpServer) serve(ctx context.Context, in io.Reader) error {
	if in == nil {
		return errors.New("gs mcp needs stdin")
	}
	reader := bufio.NewReaderSize(in, 1<<20)
	for {
		line, err := readMCPLine(reader)
		if len(strings.TrimSpace(string(line))) > 0 {
			s.handleLine(ctx, line)
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func readMCPLine(reader *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, err := reader.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > mcpMaxLine {
			// Drop the rest of the message.
			for errors.Is(err, bufio.ErrBufferFull) {
				_, err = reader.ReadSlice('\n')
			}
			return []byte(`{"jsonrpc":"2.0","method":"gs/too-large"}`), err
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return line, err
		}
	}
}

func (s *mcpServer) handleLine(ctx context.Context, line []byte) {
	var req mcpRequest
	if err := json.Unmarshal(line, &req); err != nil {
		s.reply(mcpResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &mcpError{Code: mcpParseError, Message: "not JSON: " + err.Error()}})
		return
	}
	notification := len(req.ID) == 0
	if req.Method == "gs/too-large" {
		s.reply(mcpResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &mcpError{Code: mcpInvalidRequest, Message: fmt.Sprintf("message over %d bytes", mcpMaxLine)}})
		return
	}
	result, rpcErr := s.dispatch(ctx, req)
	if notification {
		return // notifications get no answer, even on error
	}
	s.reply(mcpResponse{JSONRPC: "2.0", ID: req.ID, Result: result, Error: rpcErr})
}

func (s *mcpServer) dispatch(ctx context.Context, req mcpRequest) (any, *mcpError) {
	switch req.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &params)
		version := mcpProtocolVersions[0]
		for _, known := range mcpProtocolVersions {
			if params.ProtocolVersion == known {
				version = known
			}
		}
		return map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "gitslice", "version": s.version},
			"instructions": "Gitslice tools work on slices of one repository without a checkout. Paths are repository paths " +
				"such as /acme/app/main.go; a path without a leading slash is taken inside the slice's included folder. " +
				"Changes are changesets: create_changeset makes one (and submits it with submit: true); a submit can be " +
				"refused for a reason it names (a required check without a result, a conflict), and get_changeset says " +
				"where it stands.",
		}, nil
	case "notifications/initialized", "notifications/cancelled", "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": s.tools}, nil
	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, &mcpError{Code: mcpInvalidParams, Message: "tools/call params: " + err.Error()}
		}
		for _, tool := range s.tools {
			if tool.Name == params.Name {
				return s.callTool(ctx, tool, params.Arguments), nil
			}
		}
		return nil, &mcpError{Code: mcpInvalidParams, Message: "no tool named " + params.Name}
	}
	return nil, &mcpError{Code: mcpMethodNotFound, Message: "method not found: " + req.Method}
}

// callTool runs a tool. What it returns is the structured result, also given
// as JSON text for clients that read only text; an error is a tool error
// (isError), structured the same way, not a protocol error.
func (s *mcpServer) callTool(ctx context.Context, tool mcpTool, args json.RawMessage) map[string]any {
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage("{}")
	}
	start := time.Now()
	value, err := tool.call(ctx, args)
	event := map[string]any{"tool": tool.Name, "duration_ms": time.Since(start).Milliseconds(), "ok": err == nil}
	if err != nil {
		value = map[string]any{"error": mcpToolError(err)}
		event["error"] = err.Error()
	}
	if s.trace != nil {
		s.trace(event)
	}
	text, marshalErr := json.MarshalIndent(value, "", "  ")
	if marshalErr != nil {
		text = []byte(fmt.Sprintf(`{"error":{"message":%q}}`, marshalErr.Error()))
	}
	result := map[string]any{
		"content": []map[string]any{{"type": "text", "text": string(text)}},
		"isError": err != nil,
	}
	if _, isObject := value.(map[string]any); isObject || err != nil {
		result["structuredContent"] = value
	} else if structured, ok := asObject(value); ok {
		result["structuredContent"] = structured
	}
	return result
}

// asObject turns a struct result into the JSON object MCP's
// structuredContent wants.
func asObject(value any) (map[string]any, bool) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, false
	}
	var out map[string]any
	if json.Unmarshal(data, &out) != nil || out == nil {
		return nil, false
	}
	return out, true
}

func (s *mcpServer) reply(resp mcpResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		data = []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":null,"error":{"code":-32603,"message":%q}}`, err.Error()))
	}
	s.outMu.Lock()
	defer s.outMu.Unlock()
	_, _ = s.out.Write(append(data, '\n'))
}
