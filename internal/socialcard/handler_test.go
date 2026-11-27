package socialcard

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

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

func TestHandlerDrawsPublicSlicesOnly(t *testing.T) {
	archived := testSlice("sunny", "old", "public", "Archived slice")
	archived.Archived = true

	handler := NewHandler(fakeSlices{
		"sunny/neo":   testSlice("sunny", "neo", "public", "An emulator"),
		"sunny/old":   archived,
		"acme/secret": testSlice("acme", "secret", "private", "Unreleased plans"),
	})
	generic, err := Render(Card{})
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
	if public.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("public X-Content-Type-Options = %q", public.Header().Get("X-Content-Type-Options"))
	}
	if bytes.Equal(public.Body.Bytes(), generic) {
		t.Fatal("a public slice got the generic card")
	}

	for _, path := range []string{
		"/og/slices/acme/secret.png",
		"/og/slices/sunny/old.png",
		"/og/slices/nobody/missing.png",
		"/og/default.png",
	} {
		rec := get(http.MethodGet, path)
		if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), generic) {
			t.Fatalf("%s: code %d, generic card %v", path, rec.Code, bytes.Equal(rec.Body.Bytes(), generic))
		}
		if rec.Header().Get("Cache-Control") != socialGenericMaxAge {
			t.Fatalf("%s: cache control = %q", path, rec.Header().Get("Cache-Control"))
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("%s: X-Content-Type-Options = %q", path, rec.Header().Get("X-Content-Type-Options"))
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

func TestHandlerSkipsRenderOnHeadAndCachesCards(t *testing.T) {
	slices := fakeSlices{
		"sunny/neo": testSlice("sunny", "neo", "public", "v1 description"),
	}
	var defaultRenders atomic.Int32
	var sliceRenders atomic.Int32

	handler := NewHandlerWithRenderer(
		slices,
		func() ([]byte, error) {
			defaultRenders.Add(1)
			return []byte("default-png"), nil
		},
		func(c Card) ([]byte, error) {
			sliceRenders.Add(1)
			return []byte("slice-png:" + c.Description), nil
		},
	)

	// HEAD must not invoke default or slice renderer.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/og/slices/sunny/neo.png", nil))
	if rec.Code != http.StatusOK || sliceRenders.Load() != 0 || defaultRenders.Load() != 0 {
		t.Fatalf("HEAD rendered unexpectedly: code=%d sliceRenders=%d defaultRenders=%d", rec.Code, sliceRenders.Load(), defaultRenders.Load())
	}

	// Repeated GETs for the same slice & description hit the LRU cache.
	for range 5 {
		rec = httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/og/slices/sunny/neo.png", nil))
		if rec.Code != http.StatusOK || rec.Body.String() != "slice-png:v1 description" {
			t.Fatalf("GET slice = %d %q", rec.Code, rec.Body.String())
		}
	}
	if got := sliceRenders.Load(); got != 1 {
		t.Fatalf("sliceRenders = %d, want 1", got)
	}

	// Updating description re-renders cleanly.
	slices["sunny/neo"] = testSlice("sunny", "neo", "public", "v2 description")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/og/slices/sunny/neo.png", nil))
	if rec.Body.String() != "slice-png:v2 description" || sliceRenders.Load() != 2 {
		t.Fatalf("after description change: body=%q sliceRenders=%d", rec.Body.String(), sliceRenders.Load())
	}
}

func TestHandlerCoalescesConcurrentRendersAndDoesNotCacheErrors(t *testing.T) {
	slices := fakeSlices{
		"sunny/neo": testSlice("sunny", "neo", "public", "concurrent"),
	}
	var calls atomic.Int32
	started := make(chan struct{}, 1)
	release := make(chan struct{})

	handler := NewHandlerWithRenderer(
		slices,
		func() ([]byte, error) { return []byte("default"), nil },
		func(c Card) ([]byte, error) {
			n := calls.Add(1)
			if n == 1 {
				return nil, errors.New("transient render failure")
			}
			select {
			case started <- struct{}{}:
			default:
			}
			<-release
			return []byte("ok:" + c.Description), nil
		},
	)

	// First request fails and must NOT be cached.
	errRec := httptest.NewRecorder()
	handler.ServeHTTP(errRec, httptest.NewRequest(http.MethodGet, "/og/slices/sunny/neo.png", nil))
	if errRec.Code != http.StatusInternalServerError {
		t.Fatalf("first error status = %d, want 500", errRec.Code)
	}

	// Concurrent requests for the same card coalesce into a single render call.
	const workers = 4
	var wg sync.WaitGroup
	results := make([]*httptest.ResponseRecorder, workers)
	for i := range workers {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/og/slices/sunny/neo.png", nil))
			results[idx] = rec
		}(i)
	}
	<-started
	close(release)
	wg.Wait()

	for i, rec := range results {
		if rec.Code != http.StatusOK || rec.Body.String() != "ok:concurrent" {
			t.Fatalf("worker %d: status=%d body=%q", i, rec.Code, rec.Body.String())
		}
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("total render calls = %d, want 2 (1 error + 1 coalesced success)", got)
	}
}
