package gitcompat

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/gitslice-io/gitslice/internal/postgres"
)

type Handler struct {
	store     *postgres.Store
	projector *Projector
}

func NewHandler(store *postgres.Store, projector *Projector) *Handler {
	return &Handler{store: store, projector: projector}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	account, slice, pathInfo, err := parseGitPath(r.URL.Path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if isReceivePack(r) {
		http.Error(w, "git push is not supported by the MVP Git layer; use native changesets", http.StatusForbidden)
		return
	}
	subjectID, err := h.authenticate(r.Context(), r)
	if err != nil {
		w.Header().Set("WWW-Authenticate", `Basic realm="gitslice"`)
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if _, _, err := h.projector.EnsureProjectedRepo(r.Context(), subjectID, account, slice); err != nil {
		writeGitError(w, err)
		return
	}
	if err := h.serveBackend(w, r, pathInfo); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *Handler) authenticate(ctx context.Context, r *http.Request) (string, error) {
	token := bearerToken(r.Header.Get("Authorization"))
	if token == "" {
		token = basicPassword(r.Header.Get("Authorization"))
	}
	if token == "" {
		return "", postgres.ErrUnauthenticated
	}
	subject, err := h.store.SubjectForToken(ctx, token)
	if err != nil {
		return "", err
	}
	return subject.ID, nil
}

func (h *Handler) serveBackend(w http.ResponseWriter, r *http.Request, pathInfo string) error {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(r.Context(), "git", "http-backend")
	cmd.Stdin = bytes.NewReader(body)
	cmd.Env = append(os.Environ(),
		"GIT_PROJECT_ROOT="+h.projector.CacheRoot(),
		"GIT_HTTP_EXPORT_ALL=1",
		"PATH_INFO="+pathInfo,
		"REQUEST_METHOD="+r.Method,
		"QUERY_STRING="+r.URL.RawQuery,
		"CONTENT_TYPE="+r.Header.Get("Content-Type"),
		"CONTENT_LENGTH="+strconv.Itoa(len(body)),
		"REMOTE_USER=gitslice",
	)
	if protocol := r.Header.Get("Git-Protocol"); protocol != "" {
		cmd.Env = append(cmd.Env, "HTTP_GIT_PROTOCOL="+protocol)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git http-backend failed: %w\n%s", err, string(out))
	}
	headers, payload, ok := splitCGIResponse(out)
	if !ok {
		return errors.New("git http-backend returned malformed CGI response")
	}
	statusCode := http.StatusOK
	for _, line := range strings.Split(headers, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		name, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		if strings.EqualFold(name, "Status") {
			fields := strings.Fields(value)
			if len(fields) > 0 {
				if code, err := strconv.Atoi(fields[0]); err == nil {
					statusCode = code
				}
			}
			continue
		}
		w.Header().Add(name, value)
	}
	w.WriteHeader(statusCode)
	_, err = w.Write(payload)
	return err
}

func parseGitPath(path string) (string, string, string, error) {
	trimmed := strings.TrimPrefix(path, "/git/")
	if trimmed == path || trimmed == "" {
		return "", "", "", errors.New("not a git path")
	}
	account, rest, ok := strings.Cut(trimmed, "/")
	if !ok || account == "" {
		return "", "", "", errors.New("git path missing account")
	}
	idx := strings.Index(rest, ".git")
	if idx <= 0 {
		return "", "", "", errors.New("git path missing repository suffix")
	}
	slice := rest[:idx]
	suffix := rest[idx+len(".git"):]
	if strings.Contains(slice, "/") || slice == "" {
		return "", "", "", errors.New("invalid slice")
	}
	return account, slice, "/" + account + "/" + slice + ".git" + suffix, nil
}

func isReceivePack(r *http.Request) bool {
	return strings.Contains(r.URL.Path, "git-receive-pack") || r.URL.Query().Get("service") == "git-receive-pack"
}

func bearerToken(header string) string {
	const prefix = "bearer "
	header = strings.TrimSpace(header)
	if strings.HasPrefix(strings.ToLower(header), prefix) {
		return strings.TrimSpace(header[len(prefix):])
	}
	return ""
}

func basicPassword(header string) string {
	const prefix = "basic "
	header = strings.TrimSpace(header)
	if !strings.HasPrefix(strings.ToLower(header), prefix) {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(header[len(prefix):]))
	if err != nil {
		return ""
	}
	_, password, ok := strings.Cut(string(raw), ":")
	if !ok {
		return ""
	}
	return password
}

func splitCGIResponse(out []byte) (string, []byte, bool) {
	if idx := bytes.Index(out, []byte("\r\n\r\n")); idx >= 0 {
		return string(out[:idx]), out[idx+4:], true
	}
	if idx := bytes.Index(out, []byte("\n\n")); idx >= 0 {
		return string(out[:idx]), out[idx+2:], true
	}
	return "", nil, false
}

func writeGitError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, postgres.ErrUnauthenticated):
		http.Error(w, "authentication required", http.StatusUnauthorized)
	case errors.Is(err, postgres.ErrUnauthorized):
		http.Error(w, "permission denied", http.StatusForbidden)
	case errors.Is(err, postgres.ErrNotFound):
		http.NotFound(w, nil)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
