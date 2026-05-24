package web

import (
	"context"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"strings"
)

type SignupStore interface {
	SignupUser(context.Context, string) (string, string, error)
}

type Handler struct {
	Signup SignupStore
}

func NewHandler(signup SignupStore) http.Handler {
	return &Handler{Signup: signup}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/signup":
		h.signupPage(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/signup/approve":
		h.approveSignup(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *Handler) signupPage(w http.ResponseWriter, r *http.Request) {
	req, err := signupRequestFromValues(r.URL.Query())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = signupTemplate.Execute(w, req)
}

func (h *Handler) approveSignup(w http.ResponseWriter, r *http.Request) {
	if h.Signup == nil {
		http.Error(w, "signup is not configured", http.StatusInternalServerError)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	req, err := signupRequestFromValues(r.Form)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	token, subjectID, err := h.Signup.SignupUser(r.Context(), req.Username)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	callback := *req.CallbackURL
	q := callback.Query()
	q.Set("token", token)
	q.Set("subject_id", subjectID)
	q.Set("state", req.State)
	callback.RawQuery = q.Encode()
	http.Redirect(w, r, callback.String(), http.StatusFound)
}

type signupRequest struct {
	Username    string
	CallbackURL *url.URL
	State       string
}

func signupRequestFromValues(values url.Values) (signupRequest, error) {
	username := strings.TrimSpace(values.Get("username"))
	if username == "" {
		return signupRequest{}, fmt.Errorf("username is required")
	}
	state := strings.TrimSpace(values.Get("state"))
	if state == "" {
		return signupRequest{}, fmt.Errorf("state is required")
	}
	callback, err := parseLocalCallbackURL(values.Get("callback_url"))
	if err != nil {
		return signupRequest{}, err
	}
	return signupRequest{Username: username, CallbackURL: callback, State: state}, nil
}

func parseLocalCallbackURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("callback_url is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid callback_url: %w", err)
	}
	if parsed.Scheme != "http" {
		return nil, fmt.Errorf("callback_url must use http")
	}
	host := parsed.Hostname()
	if host == "" {
		return nil, fmt.Errorf("callback_url host is required")
	}
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return nil, fmt.Errorf("callback_url must point to localhost")
		}
	}
	return parsed, nil
}

var signupTemplate = template.Must(template.New("signup").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Gitslice Signup</title>
  <style>
    body { margin: 0; font-family: system-ui, sans-serif; background: #f7f7f8; color: #18181b; }
    main { max-width: 520px; margin: 12vh auto; padding: 32px; background: white; border-radius: 8px; }
    h1 { margin: 0 0 12px; font-size: 24px; }
    p { line-height: 1.5; color: #3f3f46; }
    code { background: #f4f4f5; padding: 2px 5px; border-radius: 4px; }
    button { margin-top: 20px; padding: 10px 14px; font: inherit; border: 0; border-radius: 6px; background: #1849a9; color: white; cursor: pointer; }
  </style>
</head>
<body>
  <main>
    <h1>Approve Gitslice Signup</h1>
    <p>Create or sign in to the development account for <code>{{.Username}}</code>.</p>
    <form method="post" action="/signup/approve">
      <input type="hidden" name="username" value="{{.Username}}">
      <input type="hidden" name="callback_url" value="{{.CallbackURL}}">
      <input type="hidden" name="state" value="{{.State}}">
      <button type="submit">Approve signup</button>
    </form>
  </main>
</body>
</html>`))
