package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPHandlerCORSAllowsConnectTimeoutMs(t *testing.T) {
	handler := NewHTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("preflight should not reach wrapped handler")
	}), nil, "https://gitslice.io", Config{RateLimitDisabled: true})

	req := httptest.NewRequest(http.MethodOptions, "/gitslice.core.v1.ChangesetService/SubmitChangeset", nil)
	req.Header.Set("Origin", "https://gitslice.io")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	req.Header.Set("Access-Control-Request-Headers", "authorization,connect-protocol-version,connect-timeout-ms,content-type,x-user-agent")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	allowHeaders := rec.Header().Get("Access-Control-Allow-Headers")
	for _, header := range []string{
		"authorization",
		"connect-protocol-version",
		"connect-timeout-ms",
		"content-type",
		"x-user-agent",
	} {
		if !headerListContains(allowHeaders, header) {
			t.Fatalf("Access-Control-Allow-Headers = %q, missing %q", allowHeaders, header)
		}
	}
}

func headerListContains(values, want string) bool {
	want = strings.ToLower(want)
	for _, value := range strings.Split(values, ",") {
		if strings.ToLower(strings.TrimSpace(value)) == want {
			return true
		}
	}
	return false
}
