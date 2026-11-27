package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"gitslice.io/gitslice/internal/storage/memory"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"gitslice.io/gitslice/service"
)

func TestConnectMountsSocialImageRoute(t *testing.T) {
	mem := memory.New()
	mem.AddAccount("user_alice", "sunny")
	mem.PutSlice(&corev1.SliceRef{Account: "sunny", Slice: "neo"}, []string{"/sunny/neo"}, "public")
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
	httpHandler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/og/slices/sunny/neo.png", nil))
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" || rec.Body.Len() == 0 {
		t.Fatalf("GET /og/slices/sunny/neo.png = (%d, %q, %d bytes)", rec.Code, rec.Header().Get("Content-Type"), rec.Body.Len())
	}
}
