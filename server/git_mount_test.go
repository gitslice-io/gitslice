package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPHandlerRoutesGitPrefix(t *testing.T) {
	apiCalls := 0
	api := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		apiCalls++
		w.WriteHeader(http.StatusNoContent)
	})
	gitCalls := 0
	gitHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		gitCalls++
		w.WriteHeader(http.StatusAccepted)
	})
	handler := NewHTTPHandler(api, gitHandler, "")

	gitRequest := httptest.NewRequest(http.MethodGet, "/git/foo/bar.git/info/refs", nil)
	gitResponse := httptest.NewRecorder()
	handler.ServeHTTP(gitResponse, gitRequest)
	if gitResponse.Code != http.StatusAccepted || gitCalls != 1 || apiCalls != 0 {
		t.Fatalf("git route: status=%d git calls=%d api calls=%d", gitResponse.Code, gitCalls, apiCalls)
	}

	apiRequest := httptest.NewRequest(http.MethodGet, "/", nil)
	apiResponse := httptest.NewRecorder()
	handler.ServeHTTP(apiResponse, apiRequest)
	if apiResponse.Code != http.StatusNoContent || gitCalls != 1 || apiCalls != 1 {
		t.Fatalf("api route: status=%d git calls=%d api calls=%d", apiResponse.Code, gitCalls, apiCalls)
	}
}
