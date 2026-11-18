// Package mcpserver implements Gitslice's stdio Model Context Protocol (MCP)
// JSON-RPC 2.0 server and slice/changeset tool handlers.
package mcpserver

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
)

// ProtocolVersions are the MCP protocol versions the server speaks, newest
// first; a client asking for another gets the newest.
var ProtocolVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

// MaxLine bounds one JSON-RPC message: an edit carries whole files.
const MaxLine = 64 << 20

const (
	ParseError     = -32700
	InvalidRequest = -32600
	MethodNotFound = -32601
	InvalidParams  = -32602
)

// UserError represents a structured tool error with code, message, and hint.
type UserError struct {
	Code    string
	Message string
	Hint    string
}

func (e *UserError) Error() string {
	if e.Hint == "" {
		return e.Message
	}
	return e.Message + "\nhint: " + e.Hint
}

func (e *UserError) UserError() (string, string, string) {
	return e.Code, e.Message, e.Hint
}

func newUserError(code, message, hint string) error {
	return &UserError{Code: code, Message: message, Hint: hint}
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Tool is one MCP tool: its JSON schema and handler.
type Tool struct {
	Name        string                                                       `json:"name"`
	Description string                                                       `json:"description"`
	InputSchema map[string]any                                               `json:"inputSchema"`
	Call        func(ctx context.Context, args json.RawMessage) (any, error) `json:"-"`
}

// Server answers one MCP client over newline-delimited JSON-RPC 2.0.
type Server struct {
	Tools       []Tool
	Version     string
	Trace       func(event map[string]any)
	FormatError func(err error) map[string]any
	Out         io.Writer
	outMu       sync.Mutex
}

// Serve reads messages from in until EOF.
func (s *Server) Serve(ctx context.Context, in io.Reader) error {
	if in == nil {
		return errors.New("gs mcp needs stdin")
	}
	reader := bufio.NewReaderSize(in, 1<<20)
	for {
		line, err := readLine(reader)
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

func readLine(reader *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, err := reader.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > MaxLine {
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

func (s *Server) handleLine(ctx context.Context, line []byte) {
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		s.reply(response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: ParseError, Message: "not JSON: " + err.Error()}})
		return
	}
	notification := len(req.ID) == 0
	if req.Method == "gs/too-large" {
		s.reply(response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: InvalidRequest, Message: fmt.Sprintf("message over %d bytes", MaxLine)}})
		return
	}
	result, rpcErr := s.dispatch(ctx, req)
	if notification {
		return
	}
	s.reply(response{JSONRPC: "2.0", ID: req.ID, Result: result, Error: rpcErr})
}

func (s *Server) dispatch(ctx context.Context, req request) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &params)
		version := ProtocolVersions[0]
		for _, known := range ProtocolVersions {
			if params.ProtocolVersion == known {
				version = known
			}
		}
		return map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "gitslice", "version": s.Version},
			"instructions": "Gitslice tools work on slices of one repository without a checkout. Paths are repository paths " +
				"such as /acme/app/main.go; a path without a leading slash is taken inside the slice's included folder. " +
				"Changes are changesets: create_changeset makes one (and submits it with submit: true); a submit can be " +
				"refused for a reason it names (a required check without a result, a conflict), and get_changeset says " +
				"where it stands.",
		}, nil
	case "notifications/initialized", "notifications/cancelled", "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": s.Tools}, nil
	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, &rpcError{Code: InvalidParams, Message: "tools/call params: " + err.Error()}
		}
		for _, tool := range s.Tools {
			if tool.Name == params.Name {
				return s.callTool(ctx, tool, params.Arguments), nil
			}
		}
		return nil, &rpcError{Code: InvalidParams, Message: "no tool named " + params.Name}
	}
	return nil, &rpcError{Code: MethodNotFound, Message: "method not found: " + req.Method}
}

func (s *Server) callTool(ctx context.Context, tool Tool, args json.RawMessage) map[string]any {
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage("{}")
	}
	start := time.Now()
	value, err := tool.Call(ctx, args)
	event := map[string]any{"tool": tool.Name, "duration_ms": time.Since(start).Milliseconds(), "ok": err == nil}
	if err != nil {
		formatErr := s.FormatError
		if formatErr == nil {
			formatErr = defaultFormatToolError
		}
		value = map[string]any{"error": formatErr(err)}
		event["error"] = err.Error()
	}
	if s.Trace != nil {
		s.Trace(event)
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

func defaultFormatToolError(err error) map[string]any {
	if err == nil {
		return map[string]any{"code": "error"}
	}
	var uerr *UserError
	if errors.As(err, &uerr) {
		out := map[string]any{"code": uerr.Code, "message": uerr.Message}
		if uerr.Hint != "" {
			out["hint"] = uerr.Hint
		}
		return out
	}
	return map[string]any{"code": "error", "message": err.Error()}
}

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

func (s *Server) reply(resp response) {
	data, err := json.Marshal(resp)
	if err != nil {
		data = []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":null,"error":{"code":-32603,"message":%q}}`, err.Error()))
	}
	s.outMu.Lock()
	defer s.outMu.Unlock()
	_, _ = s.Out.Write(append(data, '\n'))
}
