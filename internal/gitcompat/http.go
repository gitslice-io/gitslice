package gitcompat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/proto/core/v1"
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
	// Clones, pushes and the first build of a slice's history can outlast the
	// API gateway's body deadlines, which share this handler on some
	// deployments. Lift them for Git requests. Headers have already been read,
	// and idle keep-alive connections stay bounded by the server.
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Time{})
	_ = controller.SetWriteDeadline(time.Time{})
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
	// Reads may be anonymous: the projector's slice authorization lets anyone
	// read a public slice. Pushes always need credentials, and credentials that
	// are present but invalid are rejected rather than downgraded to anonymous.
	subjectID := ""
	if token := requestToken(r); token != "" {
		subjectID, err = h.resolve(r.Context(), token)
		if err != nil {
			writeAuthChallenge(w)
			return
		}
	} else if isReceivePack(r) {
		writeAuthChallenge(w)
		return
	}
	if isReceivePack(r) {
		h.handleReceivePack(w, r, subjectID, account, slice)
		return
	}
	if _, _, err := h.projector.EnsureProjectedRepo(r.Context(), subjectID, account, slice); err != nil {
		if subjectID == "" && isAccessError(err) {
			// Answer a missing slice and a private one alike, so anonymous
			// callers cannot probe which private slices exist. Git then asks
			// for credentials.
			writeAuthChallenge(w)
			return
		}
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

// requestToken returns the bearer token or HTTP basic-auth password, or "" when
// the request carries no credentials.
func requestToken(r *http.Request) string {
	if token := bearerToken(r.Header.Get("Authorization")); token != "" {
		return token
	}
	return basicPassword(r.Header.Get("Authorization"))
}

func isAccessError(err error) bool {
	return errors.Is(err, storage.ErrUnauthenticated) ||
		errors.Is(err, storage.ErrUnauthorized) ||
		errors.Is(err, storage.ErrNotFound)
}

func writeAuthChallenge(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="gitslice"`)
	http.Error(w, "authentication required", http.StatusUnauthorized)
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
		// Partial clones (--filter=blob:none, --filter=tree:0): a client on a
		// large slice can start with history and fetch file contents as it
		// reads them. Set here, not in each repository's config, so caches
		// built before this change get it too.
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=uploadpack.allowFilter",
		"GIT_CONFIG_VALUE_0=true",
	)
	if protocol := r.Header.Get("Git-Protocol"); protocol != "" {
		cmd.Env = append(cmd.Env, "HTTP_GIT_PROTOCOL="+protocol)
	}
	// Git gzips large fetch negotiations (many "have" lines). http-backend
	// inflates the body itself, but only when it is told the encoding.
	if encoding := r.Header.Get("Content-Encoding"); encoding != "" {
		cmd.Env = append(cmd.Env, "HTTP_CONTENT_ENCODING="+encoding)
	}
	// Only stdout is the CGI response. Git writes pack progress to stderr
	// when a client negotiates neither side-band nor no-progress (minimal
	// clients such as the importer behind Cloudflare Artifacts); mixed into
	// the body, it corrupts the packfile those clients read.
	//
	// The response is streamed: a clone of a large slice is gigabytes, and
	// holding it in memory first would exhaust the instance.
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr limitedBuffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	reader := bufio.NewReaderSize(stdout, 64<<10)
	statusCode, header, err := readCGIHeader(reader)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("git http-backend: %w\n%s", err, stderr.String())
	}
	for name, values := range header {
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	w.WriteHeader(statusCode)
	_, copyErr := io.Copy(w, reader)
	waitErr := cmd.Wait()
	switch {
	case copyErr != nil:
		// The client went away; git stops on SIGPIPE or context cancel.
		return nil
	case waitErr != nil:
		// The status line is already sent, so the only way to tell the client
		// the pack is incomplete is to cut the connection (it fails its
		// checksum either way); record why.
		slog.Warn("git http-backend failed after streaming started", "error", waitErr, "stderr", stderr.String())
	}
	return nil
}

// limitedBuffer keeps the first bytes written to it: enough of git's stderr to
// explain a failure.
type limitedBuffer struct{ buf bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := 64<<10 - b.buf.Len(); room > 0 {
		if len(p) > room {
			b.buf.Write(p[:room])
		} else {
			b.buf.Write(p)
		}
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string { return b.buf.String() }

// readCGIHeader reads the header block of a CGI response and returns the
// status it asks for and the headers to pass on.
func readCGIHeader(r *bufio.Reader) (int, http.Header, error) {
	statusCode := http.StatusOK
	header := http.Header{}
	for lines := 0; ; lines++ {
		line, err := r.ReadString('\n')
		if err != nil {
			return 0, nil, errors.New("malformed CGI response")
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if lines == 0 {
				return 0, nil, errors.New("malformed CGI response")
			}
			return statusCode, header, nil
		}
		name, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		if strings.EqualFold(name, "Status") {
			if fields := strings.Fields(value); len(fields) > 0 {
				if code, err := strconv.Atoi(fields[0]); err == nil {
					statusCode = code
				}
			}
			continue
		}
		header.Add(name, value)
	}
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

func writeGitError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storage.ErrUnauthenticated):
		writeAuthChallenge(w)
	case errors.Is(err, storage.ErrUnauthorized):
		http.Error(w, "permission denied", http.StatusForbidden)
	case errors.Is(err, storage.ErrNotFound):
		http.NotFound(w, nil)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
