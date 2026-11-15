package server

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"gitslice.io/gitslice/internal/socialcard"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeSlices map[string]*corev1.Slice

func (f fakeSlices) ResolveSlice(_ context.Context, req *corev1.ResolveSliceRequest) (*corev1.Slice, error) {
	if slice, ok := f[req.GetRef().GetAccount()+"/"+req.GetRef().GetSlice()]; ok {
		return slice, nil
	}
	return nil, status.Error(codes.NotFound, "not found")
}

func testSlice(account, name, visibility, description string) *corev1.Slice {
	return &corev1.Slice{
		Ref:         &corev1.SliceRef{Account: account, Slice: name},
		Description: description,
		Definition:  &corev1.SliceDefinition{Visibility: visibility},
	}
}

func TestSocialImageDrawsPublicSlicesOnly(t *testing.T) {
	handler := newSocialImageHandler(fakeSlices{
		"sunny/neo":   testSlice("sunny", "neo", "public", "An emulator"),
		"acme/secret": testSlice("acme", "secret", "private", "Unreleased plans"),
	})
	generic, err := socialcard.Render(socialcard.Card{})
	if err != nil {
		t.Fatal(err)
	}
	get := func(method, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		return rec
	}

	public := get(http.MethodGet, "/og/slices/sunny/neo.png")
	if public.Code != http.StatusOK || public.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("public card: %d %q", public.Code, public.Header().Get("Content-Type"))
	}
	if public.Header().Get("Cache-Control") != socialImageMaxAge {
		t.Fatalf("public cache control = %q", public.Header().Get("Cache-Control"))
	}
	if bytes.Equal(public.Body.Bytes(), generic) {
		t.Fatal("a public slice got the generic card")
	}

	for _, path := range []string{"/og/slices/acme/secret.png", "/og/slices/nobody/missing.png", "/og/default.png"} {
		rec := get(http.MethodGet, path)
		if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), generic) {
			t.Fatalf("%s: code %d, generic card %v", path, rec.Code, bytes.Equal(rec.Body.Bytes(), generic))
		}
		if rec.Header().Get("Cache-Control") != socialGenericMaxAge {
			t.Fatalf("%s: cache control = %q", path, rec.Header().Get("Cache-Control"))
		}
	}

	for _, path := range []string{"/og/slices/sunny/neo", "/og/slices/sunny.png", "/og/slices/a/b/c.png", "/og/other.png"} {
		if code := get(http.MethodGet, path).Code; code != http.StatusNotFound {
			t.Fatalf("%s: code %d, want 404", path, code)
		}
	}
	if rec := get(http.MethodHead, "/og/slices/sunny/neo.png"); rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("HEAD: code %d, body %d bytes", rec.Code, rec.Body.Len())
	}
	if code := get(http.MethodPost, "/og/default.png").Code; code != http.StatusMethodNotAllowed {
		t.Fatalf("POST: code %d", code)
	}
}
