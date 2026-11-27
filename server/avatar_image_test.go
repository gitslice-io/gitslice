package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"gitslice.io/gitslice/internal/storage/memory"
	"gitslice.io/gitslice/service"
)

func TestConnectMountsAvatarImageRoute(t *testing.T) {
	mem := memory.New()
	mem.AddAccount("user_alice", "anna")
	handlers := service.New(service.Stores{
		Auth:       mem.Auth,
		Blobs:      mem.Blobs,
		Changesets: mem.Changesets,
		Repository: mem.Repository,
		Slices:     mem.Slices,
		Agents:     mem.Agents,
		Checks:     mem.Checks,
	}, mem.Objects, nil)

	httpHandler := NewPrincipalConnectHandler(mem.Auth.AuthenticateToken, handlers)
	rec := httptest.NewRecorder()
	httpHandler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/avatars/anna.png", nil))
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" || rec.Body.Len() == 0 {
		t.Fatalf("GET /avatars/anna.png = (%d, %q, %d bytes)", rec.Code, rec.Header().Get("Content-Type"), rec.Body.Len())
	}
}
