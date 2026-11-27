package avatar

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/usernames"
)

type fakeSource map[string]string // account -> version of its upload; "ERR" simulates transient error

func (f fakeSource) AccountAvatar(_ context.Context, account string) ([]byte, string) {
	if f[account] == "ERR" {
		return nil, ""
	}
	return []byte("png of " + account), f[account]
}

func TestNewHandler(t *testing.T) {
	maxName := strings.Repeat("a", usernames.MaxLength)
	tooLongName := strings.Repeat("a", usernames.MaxLength+1)

	handler := NewHandler(fakeSource{
		"anna":  "abc123",
		"flaky": "ERR",
	})
	for _, tc := range []struct {
		path, wantBody, wantCache string
		wantStatus                int
	}{
		{"/avatars/anna.png", "png of anna", avatarMaxAge, http.StatusOK},
		// The version it shows now: cached for good.
		{"/avatars/anna.png?v=abc123", "png of anna", avatarImmutableMaxAge, http.StatusOK},
		// An old version still gets the current picture, briefly cached.
		{"/avatars/anna.png?v=old", "png of anna", avatarMaxAge, http.StatusOK},
		{"/avatars/anna.png?v=none", "png of anna", avatarMaxAge, http.StatusOK},
		// A generated picture never changes for its name.
		{"/avatars/bob-2.png?v=none", "png of bob-2", avatarImmutableMaxAge, http.StatusOK},
		// Maximum valid username length succeeds.
		{"/avatars/" + maxName + ".png", "png of " + maxName, avatarMaxAge, http.StatusOK},
		// Transient backend errors must return 503 no-store rather than caching a wrong fallback.
		{"/avatars/flaky.png?v=none", "", "no-store", http.StatusServiceUnavailable},
		{"/avatars/flaky.png?v=abc123", "", "no-store", http.StatusServiceUnavailable},
		{"/avatars/Anna.png", "", "", http.StatusNotFound},
		{"/avatars/anna", "", "", http.StatusNotFound},
		{"/avatars/a/b.png", "", "", http.StatusNotFound},
		{"/avatars/.png", "", "", http.StatusNotFound},
		{"/avatars/" + tooLongName + ".png", "", "", http.StatusNotFound},
		{"/other/anna.png", "", "", http.StatusNotFound},
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rec.Code != tc.wantStatus {
			t.Errorf("%s: status %d, want %d", tc.path, rec.Code, tc.wantStatus)
			continue
		}
		if tc.wantStatus == http.StatusServiceUnavailable {
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("%s: 503 Cache-Control = %q, want no-store", tc.path, got)
			}
			if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("%s: 503 X-Content-Type-Options = %q, want nosniff", tc.path, got)
			}
			continue
		}
		if tc.wantStatus != http.StatusOK {
			continue
		}
		if rec.Body.String() != tc.wantBody || rec.Header().Get("Cache-Control") != tc.wantCache || rec.Header().Get("Content-Type") != "image/png" {
			t.Errorf("%s: body %q, cache %q, type %q", tc.path, rec.Body.String(), rec.Header().Get("Cache-Control"), rec.Header().Get("Content-Type"))
		}
		if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s: X-Content-Type-Options = %q, want nosniff", tc.path, got)
		}
		if got := rec.Header().Get("Content-Security-Policy"); got != "default-src 'none'" {
			t.Errorf("%s: Content-Security-Policy = %q, want default-src 'none'", tc.path, got)
		}
	}

	// HEAD returns headers with an empty body.
	headRec := httptest.NewRecorder()
	handler.ServeHTTP(headRec, httptest.NewRequest(http.MethodHead, "/avatars/anna.png?v=abc123", nil))
	if headRec.Code != http.StatusOK || headRec.Body.Len() != 0 {
		t.Errorf("HEAD: status %d, body len %d", headRec.Code, headRec.Body.Len())
	}
	if headRec.Header().Get("Content-Type") != "image/png" ||
		headRec.Header().Get("Cache-Control") != avatarImmutableMaxAge ||
		headRec.Header().Get("X-Content-Type-Options") != "nosniff" ||
		headRec.Header().Get("Content-Security-Policy") != "default-src 'none'" {
		t.Errorf("HEAD headers: %+v", headRec.Header())
	}

	// Non-GET/HEAD methods are rejected.
	postRec := httptest.NewRecorder()
	handler.ServeHTTP(postRec, httptest.NewRequest(http.MethodPost, "/avatars/anna.png", nil))
	if postRec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: status %d, want %d", postRec.Code, http.StatusMethodNotAllowed)
	}

	// Nil source degrades gracefully to 503 no-store.
	nilRec := httptest.NewRecorder()
	NewHandler(nil).ServeHTTP(nilRec, httptest.NewRequest(http.MethodGet, "/avatars/anna.png", nil))
	if nilRec.Code != http.StatusServiceUnavailable || nilRec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("nil source: status %d cache %q", nilRec.Code, nilRec.Header().Get("Cache-Control"))
	}
}
