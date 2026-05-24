package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestSignupPageAndApproveRedirect(t *testing.T) {
	store := &fakeSignupStore{token: "devtok_test", subjectID: "user_signup"}
	server := httptest.NewServer(NewHandler(store))
	defer server.Close()

	callback := "http://127.0.0.1:45678/callback"
	pageURL := server.URL + "/signup?username=signup-user&callback_url=" + url.QueryEscape(callback) + "&state=state-1"
	resp, err := http.Get(pageURL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("signup page status = %d, want 200", resp.StatusCode)
	}

	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	form := url.Values{
		"username":     {"signup-user"},
		"callback_url": {callback},
		"state":        {"state-1"},
	}
	resp, err = client.PostForm(server.URL+"/signup/approve", form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("approve status = %d, want 302", resp.StatusCode)
	}
	location := resp.Header.Get("Location")
	if !strings.HasPrefix(location, callback) {
		t.Fatalf("redirect location = %q, want callback prefix %q", location, callback)
	}
	redirect, err := url.Parse(location)
	if err != nil {
		t.Fatal(err)
	}
	query := redirect.Query()
	if query.Get("token") != "devtok_test" || query.Get("subject_id") != "user_signup" || query.Get("state") != "state-1" {
		t.Fatalf("redirect query = %#v", query)
	}
	if store.username != "signup-user" {
		t.Fatalf("store username = %q, want signup-user", store.username)
	}
}

func TestSignupRejectsRemoteCallback(t *testing.T) {
	server := httptest.NewServer(NewHandler(&fakeSignupStore{}))
	defer server.Close()
	resp, err := http.Get(server.URL + "/signup?username=a&callback_url=https://example.com/callback&state=s")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("remote callback status = %d, want 400", resp.StatusCode)
	}
}

type fakeSignupStore struct {
	username  string
	token     string
	subjectID string
}

func (f *fakeSignupStore) SignupUser(ctx context.Context, username string) (string, string, error) {
	f.username = username
	return f.token, f.subjectID, nil
}
