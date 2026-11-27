package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/storage/memory"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"gitslice.io/gitslice/service"
)

func TestHostedMCPEndpointEndToEnd(t *testing.T) {
	ctx := context.Background()
	mem := memory.New()
	handlers := service.New(service.Stores{
		Auth:       mem.Auth,
		Blobs:      mem.Blobs,
		Changesets: mem.Changesets,
		Repository: mem.Repository,
		Slices:     mem.Slices,
		Agents:     mem.Agents,
		Checks:     mem.Checks,
	}, mem.Objects, nil)
	handlers.Auth.AgentSignupEnabled = true

	// Register an agent so we get a real gsk_ API key and private home slice.
	reg, err := handlers.Auth.RegisterAgent(ctx, &corev1.RegisterAgentRequest{
		Username:   "mcp-bot",
		OwnerEmail: "owner@example.com",
	})
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	resolve, err := newPrincipalResolver(mem.Auth, Config{})
	if err != nil {
		t.Fatalf("newPrincipalResolver: %v", err)
	}
	httpHandler := NewPrincipalConnectHandler(resolve, handlers, Config{
		Network: NetworkConfig{WebBaseURL: "https://web.gitslice.test"},
	})

	callMCP := func(body string, token string, query string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/mcp"+query, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		httpHandler.ServeHTTP(rec, req)
		return rec
	}

	// 1. GET returns 405 Method Not Allowed.
	getReq := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	getRec := httptest.NewRecorder()
	httpHandler.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /mcp status = %d, want 405", getRec.Code)
	}

	// 2. initialize + tools/list work without auth (capability discovery).
	initRec := callMCP(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`, "", "")
	if initRec.Code != http.StatusOK {
		t.Fatalf("initialize status = %d, body = %s", initRec.Code, initRec.Body.String())
	}
	if !strings.Contains(initRec.Body.String(), `"serverInfo"`) {
		t.Fatalf("unexpected initialize body: %s", initRec.Body.String())
	}

	// 3. Notifications return 202 Accepted with empty body.
	notifRec := callMCP(`{"jsonrpc":"2.0","method":"notifications/initialized"}`, "", "")
	if notifRec.Code != http.StatusAccepted || notifRec.Body.Len() != 0 {
		t.Fatalf("notification status = %d len = %d, want 202 empty", notifRec.Code, notifRec.Body.Len())
	}

	// 4. Authenticated agent can create_changeset + submit and read_file over /mcp.
	createPayload, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "create_changeset",
			"arguments": map[string]any{
				"title":  "Add hello via hosted MCP",
				"submit": true,
				"edits": []map[string]any{
					{"op": "write", "path": "hello.txt", "content": "hello hosted mcp\n"},
				},
			},
		},
	})
	createRec := callMCP(string(createPayload), reg.GetApiKey(), "?slice=mcp-bot/home")
	if createRec.Code != http.StatusOK {
		t.Fatalf("create_changeset status = %d, body = %s", createRec.Code, createRec.Body.String())
	}
	var createResp struct {
		Result struct {
			IsError           bool           `json:"isError"`
			StructuredContent map[string]any `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("unmarshal create_changeset: %v", err)
	}
	if createResp.Result.IsError {
		t.Fatalf("create_changeset returned tool error: %s", createRec.Body.String())
	}
	if !strings.Contains(createRec.Body.String(), "https://web.gitslice.test/") {
		t.Fatalf("create_changeset response did not use configured WebBaseURL: %s", createRec.Body.String())
	}

	// Publish pending changeset so read_file sees it at head.
	if _, err := mem.Changesets.PublishPending(ctx, 10); err != nil {
		t.Fatalf("PublishPending: %v", err)
	}

	readPayload, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "read_file",
			"arguments": map[string]any{"path": "hello.txt"},
		},
	})
	readRec := callMCP(string(readPayload), reg.GetApiKey(), "?slice=mcp-bot/home")
	if readRec.Code != http.StatusOK || !bytes.Contains(readRec.Body.Bytes(), []byte("hello hosted mcp")) {
		t.Fatalf("read_file failed: status=%d body=%s", readRec.Code, readRec.Body.String())
	}

	// 5. Unauthenticated access to private slice fails with structured remediation.
	unauthRec := callMCP(string(readPayload), "", "?slice=mcp-bot/home")
	if unauthRec.Code != http.StatusOK || !strings.Contains(unauthRec.Body.String(), `"isError":true`) {
		t.Fatalf("expected MCP tool error on unauthenticated private read, got status=%d body=%s", unauthRec.Code, unauthRec.Body.String())
	}
	_ = storage.APIKeyPrefix
}
