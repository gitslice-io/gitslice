package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeAvatars map[string]string // account -> version of its upload

func (f fakeAvatars) AccountAvatar(_ context.Context, account string) ([]byte, string) {
	return []byte("png of " + account), f[account]
}

func TestAvatarImageHandler(t *testing.T) {
	handler := newAvatarImageHandler(fakeAvatars{"anna": "abc123"})
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
		{"/avatars/Anna.png", "", "", http.StatusNotFound},
		{"/avatars/anna", "", "", http.StatusNotFound},
		{"/avatars/a/b.png", "", "", http.StatusNotFound},
		{"/avatars/.png", "", "", http.StatusNotFound},
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rec.Code != tc.wantStatus {
			t.Errorf("%s: status %d, want %d", tc.path, rec.Code, tc.wantStatus)
			continue
		}
		if tc.wantStatus != http.StatusOK {
			continue
		}
		if rec.Body.String() != tc.wantBody || rec.Header().Get("Cache-Control") != tc.wantCache || rec.Header().Get("Content-Type") != "image/png" {
			t.Errorf("%s: body %q, cache %q, type %q", tc.path, rec.Body.String(), rec.Header().Get("Cache-Control"), rec.Header().Get("Content-Type"))
		}
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/avatars/anna.png", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: status %d", rec.Code)
	}
}
