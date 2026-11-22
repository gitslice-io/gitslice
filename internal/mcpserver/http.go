package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// HandleJSONRPC processes a single JSON-RPC 2.0 MCP message over Streamable
// HTTP (stateless request/response). Notifications return 202 Accepted with no
// body; requests return 200 OK with the JSON-RPC response payload.
func (s *Server) HandleJSONRPC(ctx context.Context, body []byte) (int, []byte) {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return http.StatusBadRequest, marshalJSONRPC(response{
			JSONRPC: "2.0",
			ID:      json.RawMessage("null"),
			Error:   &rpcError{Code: InvalidRequest, Message: "empty JSON-RPC body"},
		})
	}
	var req request
	if err := json.Unmarshal(body, &req); err != nil {
		return http.StatusBadRequest, marshalJSONRPC(response{
			JSONRPC: "2.0",
			ID:      json.RawMessage("null"),
			Error:   &rpcError{Code: ParseError, Message: "not JSON: " + err.Error()},
		})
	}
	if req.Method == "" {
		return http.StatusBadRequest, marshalJSONRPC(response{
			JSONRPC: "2.0",
			ID:      idOrNull(req.ID),
			Error:   &rpcError{Code: InvalidRequest, Message: "missing method"},
		})
	}
	notification := len(req.ID) == 0
	result, rpcErr := s.dispatch(ctx, req)
	if notification {
		return http.StatusAccepted, nil
	}
	return http.StatusOK, marshalJSONRPC(response{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  result,
		Error:   rpcErr,
	})
}

func idOrNull(id json.RawMessage) json.RawMessage {
	if len(id) == 0 {
		return json.RawMessage("null")
	}
	return id
}

func marshalJSONRPC(resp response) []byte {
	data, err := json.Marshal(resp)
	if err != nil {
		return []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":null,"error":{"code":-32603,"message":%q}}`, err.Error()))
	}
	return data
}
