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

	"github.com/gitslice-io/gitslice/internal/authctx"
	"github.com/gitslice-io/gitslice/internal/storage"
	"github.com/gitslice-io/gitslice/proto/core/v1"
)

// maxGitRequestBytes caps the in-memory buffering of Git smart-HTTP request
// bodies (upload-pack negotiation and receive-pack packfiles) so a single
// request cannot exhaust server memory. It matches the unary gRPC message limit.
const maxGitRequestBytes = 128 * 1024 * 1024

// SubjectResolver maps a verified bearer/basic-auth token to an internal subject
// ID. It is supplied by the caller so the Git layer authenticates through the same
// provider as the gRPC server (dev sessions or Clerk).
type SubjectResolver func(ctx context.Context, token string) (string, error)

type Handler struct {
	resolve    SubjectResolver
	projector  *Projector
	blobs      BlobAPI
	changesets ChangesetAPI
}

type BlobAPI interface {
	UploadBlob(context.Context, *corev1.UploadBlobRequest) (*corev1.UploadBlobResponse, error)
}

type ChangesetAPI interface {
	CreateChangeset(context.Context, *corev1.CreateChangesetRequest) (*corev1.Changeset, error)
	GetChangeset(context.Context, *corev1.GetChangesetRequest) (*corev1.Changeset, error)
	UpdateChangeset(context.Context, *corev1.UpdateChangesetRequest) (*corev1.Patchset, error)
}

func NewHandler(resolve SubjectResolver, projector *Projector, blobs BlobAPI, changesets ChangesetAPI) *Handler {
	return &Handler{resolve: resolve, projector: projector, blobs: blobs, changesets: changesets}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	operation := gitHTTPOperation(r)
	recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	defer func() {
		recordGitHTTPRequest(operation, recorder.status)
	}()
	w = recorder
	account, slice, pathInfo, err := parseGitPath(r.URL.Path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	subjectID, err := h.authenticate(r.Context(), r)
	if err != nil {
		w.Header().Set("WWW-Authenticate", `Basic realm="gitslice"`)
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if isReceivePack(r) {
		h.handleReceivePack(w, r, subjectID, account, slice)
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

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusRecorder) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(data)
}

func (h *Handler) authenticate(ctx context.Context, r *http.Request) (string, error) {
	token := bearerToken(r.Header.Get("Authorization"))
	if token == "" {
		token = basicPassword(r.Header.Get("Authorization"))
	}
	if token == "" {
		return "", storage.ErrUnauthenticated
	}
	return h.resolve(ctx, token)
}

func (h *Handler) serveBackend(w http.ResponseWriter, r *http.Request, pathInfo string) error {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxGitRequestBytes))
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

func (h *Handler) handleReceivePack(w http.ResponseWriter, r *http.Request, subjectID, account, slice string) {
	if h.blobs == nil || h.changesets == nil {
		http.Error(w, "git push is not configured", http.StatusInternalServerError)
		return
	}
	if err := h.projector.AuthorizeSlice(r.Context(), subjectID, account, slice); err != nil {
		writeGitError(w, err)
		return
	}
	repoPath, projection, err := h.projector.EnsureProjectedRepo(r.Context(), subjectID, account, slice)
	if err != nil {
		writeGitError(w, err)
		return
	}
	if isReceivePackDiscovery(r) {
		writeReceivePackAdvertisement(w, projection)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "git receive-pack requires POST", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxGitRequestBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "git push exceeds maximum size", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	req, err := parseReceivePackRequest(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	result := h.applyReceivePack(r.Context(), authctx.WithSubjectID(r.Context(), subjectID), repoPath, projection, account, slice, req)
	writeReceivePackResult(w, req.capabilities, result)
}

func isReceivePackDiscovery(r *http.Request) bool {
	return r.Method == http.MethodGet && r.URL.Query().Get("service") == "git-receive-pack"
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

func gitHTTPOperation(r *http.Request) string {
	switch {
	case isReceivePack(r):
		return "receive-pack"
	case strings.Contains(r.URL.Path, "git-upload-pack") || r.URL.Query().Get("service") == "git-upload-pack":
		return "upload-pack"
	default:
		return "unknown"
	}
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
	case errors.Is(err, storage.ErrUnauthenticated):
		http.Error(w, "authentication required", http.StatusUnauthorized)
	case errors.Is(err, storage.ErrUnauthorized):
		http.Error(w, "permission denied", http.StatusForbidden)
	case errors.Is(err, storage.ErrNotFound):
		http.NotFound(w, nil)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
