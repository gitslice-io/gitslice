package gitcompat

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
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

// maxGitRequestBytes caps the in-memory buffering of Git receive-pack packfiles
// so a single push cannot exhaust server memory. It matches the unary gRPC limit.
const maxGitRequestBytes = 128 * 1024 * 1024

// maxGitUploadPackBodyBytes caps raw and gzip-decoded upload-pack negotiation
// bodies so compressed request bodies cannot expand into unbounded memory.
const maxGitUploadPackBodyBytes = 32 * 1024 * 1024

var (
	errInvalidContentEncoding = errors.New("invalid git request content encoding")
	errRequestBodyTooLarge    = errors.New("git request exceeds maximum size")
	errUnsupportedGitEndpoint = errors.New("unsupported git HTTP endpoint")
)

// SubjectResolver maps a verified bearer/basic-auth token to an internal subject
// ID. It is supplied by the caller so the Git layer authenticates through the same
// provider as the gRPC server (dev sessions or Clerk).
type SubjectResolver func(ctx context.Context, token string) (string, error)

// PrincipalResolver maps a verified bearer/basic-auth token to an
// authenticated Principal (carrying API key grants when used).
type PrincipalResolver func(ctx context.Context, token string) (*authctx.Principal, error)

type Handler struct {
	resolve    PrincipalResolver
	projector  *Projector
	blobs      BlobAPI
	changesets ChangesetAPI
	// internalKey signs the server's requests to itself (compact_http.go).
	internalKey []byte
	// mirrorRunner pushes a slice's Git mirror (mirror_http.go).
	mirrorRunner MirrorRunner
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
	var principalResolve PrincipalResolver
	if resolve != nil {
		principalResolve = func(ctx context.Context, token string) (*authctx.Principal, error) {
			subjectID, err := resolve(ctx, token)
			if err != nil {
				return nil, err
			}
			return &authctx.Principal{SubjectID: subjectID}, nil
		}
	}
	return NewHandlerWithPrincipal(principalResolve, projector, blobs, changesets)
}

func NewHandlerWithPrincipal(resolve PrincipalResolver, projector *Projector, blobs BlobAPI, changesets ChangesetAPI) *Handler {
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
	if hasEncodedPathTraversal(r.URL.RawPath) {
		http.NotFound(w, r)
		return
	}
	if compactURLPath(r.URL.Path) {
		h.serveCompact(w, r)
		return
	}
	if r.URL.Path == mirrorPath {
		h.serveMirror(w, r)
		return
	}
	account, slice, suffix, err := parseGitPath(r.URL.Path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	receivePack := isReceivePackEndpoint(suffix, r)
	// Reads may be anonymous: the projector's slice authorization lets anyone
	// read a public slice. Pushes always need credentials, and credentials that
	// are present but invalid are rejected rather than downgraded to anonymous.
	subjectID := ""
	if token := requestToken(r); token != "" {
		if h.resolve == nil {
			writeAuthChallenge(w)
			return
		}
		principal, err := h.resolve(r.Context(), token)
		if err != nil || principal == nil {
			writeAuthChallenge(w)
			return
		}
		subjectID = principal.SubjectID
		r = r.WithContext(authctx.WithPrincipal(r.Context(), *principal))
	} else if receivePack {
		writeAuthChallenge(w)
		return
	}
	if receivePack {
		h.handleReceivePack(w, r, subjectID, account, slice, suffix)
		return
	}
	if err := validateReadGitRequest(r, suffix); err != nil {
		if errors.Is(err, errUnsupportedGitEndpoint) {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		http.Error(w, err.Error(), http.StatusMethodNotAllowed)
		return
	}
	repoPath, _, err := h.projector.EnsureProjectedRepo(r.Context(), subjectID, account, slice)
	if err != nil {
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
	if r.Method == http.MethodPost && suffix == "/git-upload-pack" {
		// Once the response is over, ask for a cover if the next clone needs
		// one: also when this one failed, as a cold clone of a large slice cut
		// off at the CDN's time limit does, or no clone would ever be cheap.
		defer h.projector.MaybeRequestCover(account, slice)
	}
	if err := h.serveBackend(w, r, repoPath, suffix); err != nil {
		writeServeBackendError(w, err)
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
	header := r.Header.Get("Authorization")
	if token, ok := authctx.BearerToken(header); ok {
		return token
	}
	if password, ok := authctx.BasicPassword(header); ok {
		return password
	}
	return ""
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

func validateReadGitRequest(r *http.Request, suffix string) error {
	switch suffix {
	case "/info/refs":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			return errors.New("git info/refs requires GET or HEAD")
		}
		if service := r.URL.Query().Get("service"); service != "" && service != "git-upload-pack" {
			return errUnsupportedGitEndpoint
		}
		return nil
	case "/HEAD":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			return errors.New("git HEAD requires GET or HEAD")
		}
		return nil
	case "/git-upload-pack":
		if r.Method != http.MethodPost {
			return errors.New("git upload-pack requires POST")
		}
		return nil
	default:
		return errUnsupportedGitEndpoint
	}
}

func writeServeBackendError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge) || errors.Is(err, errRequestBodyTooLarge):
		http.Error(w, "git request exceeds maximum size", http.StatusRequestEntityTooLarge)
	case errors.Is(err, errInvalidUploadRequest) || errors.Is(err, errInvalidContentEncoding):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, errUnsupportedGitEndpoint):
		http.Error(w, err.Error(), http.StatusForbidden)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func sanitizeHeaderEnv(value string) string {
	return strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == 0 {
			return -1
		}
		return r
	}, value)
}

func (h *Handler) serveBackend(w http.ResponseWriter, r *http.Request, repoPath, suffix string) error {
	if err := validateReadGitRequest(r, suffix); err != nil {
		return err
	}
	cleanRepoPath, err := h.projector.verifyRepoPath(repoPath)
	if err != nil {
		return err
	}
	rawBody, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxGitUploadPackBodyBytes))
	if err != nil {
		return err
	}
	body, err := decodeRequestBody(r, rawBody, maxGitUploadPackBodyBytes)
	if err != nil {
		return err
	}
	if r.Method == http.MethodPost && suffix == "/git-upload-pack" {
		// On a lazily built projection the repository may lack the files this
		// fetch would send; put them there first.
		if err := h.projector.HydrateFor(r.Context(), cleanRepoPath, body); err != nil {
			return fmt.Errorf("hydrating file contents: %w", err)
		}
	}
	// A compaction does not replace the packs while git reads them.
	unlockServe := h.projector.rLockServe(cleanRepoPath)
	defer unlockServe()
	cmd := exec.CommandContext(r.Context(), "git", "http-backend")
	cmd.Stdin = bytes.NewReader(body)
	cmd.Env = append(os.Environ(),
		"GIT_PROJECT_ROOT="+cleanRepoPath,
		"GIT_HTTP_EXPORT_ALL=1",
		"PATH_INFO="+suffix,
		"REQUEST_METHOD="+r.Method,
		"QUERY_STRING="+r.URL.RawQuery,
		"CONTENT_TYPE="+sanitizeHeaderEnv(r.Header.Get("Content-Type")),
		"CONTENT_LENGTH="+strconv.Itoa(len(body)),
		"REMOTE_USER=gitslice",
		// Partial clones (--filter=blob:none, --filter=tree:0): a client on a
		// large slice can start with history and fetch file contents as it
		// reads them. Set here, not in each repository's config, so caches
		// built before this change get it too.
		// Only blobs reachable from the slice's projected refs may be fetched
		// by SHA-1, and receive-pack is never served through http-backend.
		"GIT_CONFIG_COUNT=3",
		"GIT_CONFIG_KEY_0=uploadpack.allowFilter",
		"GIT_CONFIG_VALUE_0=true",
		"GIT_CONFIG_KEY_1=uploadpack.allowReachableSHA1InWant",
		"GIT_CONFIG_VALUE_1=true",
		"GIT_CONFIG_KEY_2=http.receivepack",
		"GIT_CONFIG_VALUE_2=false",
	)
	if protocol := sanitizeHeaderEnv(r.Header.Get("Git-Protocol")); protocol != "" {
		cmd.Env = append(cmd.Env, "HTTP_GIT_PROTOCOL="+protocol)
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

// decodeRequestBody inflates Content-Encoding: gzip once with a hard byte
// ceiling so neither HydrateFor nor git http-backend re-inflates or buffers an
// unbounded gzip bomb.
func decodeRequestBody(r *http.Request, body []byte, maxBytes int64) ([]byte, error) {
	encoding := strings.TrimSpace(r.Header.Get("Content-Encoding"))
	if encoding == "" || strings.EqualFold(encoding, "identity") {
		return body, nil
	}
	if !strings.EqualFold(encoding, "gzip") {
		return nil, fmt.Errorf("%w: unsupported Content-Encoding %q", errInvalidContentEncoding, encoding)
	}
	zr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errInvalidContentEncoding, err)
	}
	out, err := io.ReadAll(io.LimitReader(zr, maxBytes+1))
	closeErr := zr.Close()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errInvalidContentEncoding, err)
	}
	if int64(len(out)) > maxBytes {
		return nil, errRequestBodyTooLarge
	}
	if closeErr != nil {
		return nil, fmt.Errorf("%w: %v", errInvalidContentEncoding, closeErr)
	}
	return out, nil
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

func (h *Handler) handleReceivePack(w http.ResponseWriter, r *http.Request, subjectID, account, slice, suffix string) {
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
	if suffix == "/info/refs" && isReceivePackDiscovery(r) {
		writeReceivePackAdvertisement(w, projection)
		return
	}
	if suffix != "/git-receive-pack" || r.Method != http.MethodPost {
		http.Error(w, "git receive-pack requires POST", http.StatusMethodNotAllowed)
		return
	}
	rawBody, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxGitRequestBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "git push exceeds maximum size", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	body, err := decodeRequestBody(r, rawBody, maxGitRequestBytes)
	if err != nil {
		writeServeBackendError(w, err)
		return
	}
	req, err := parseReceivePackRequest(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	authCtx := r.Context()
	if _, ok := authctx.SubjectID(authCtx); !ok && subjectID != "" {
		authCtx = authctx.WithSubjectID(authCtx, subjectID)
	}
	result := h.applyReceivePack(r.Context(), authCtx, repoPath, projection, account, slice, req)
	writeReceivePackResult(w, req.capabilities, result)
}

func isReceivePackDiscovery(r *http.Request) bool {
	return r.Method == http.MethodGet && r.URL.Query().Get("service") == "git-receive-pack"
}

func hasEncodedPathTraversal(rawPath string) bool {
	if rawPath == "" {
		return false
	}
	lower := strings.ToLower(rawPath)
	return strings.Contains(lower, "%2f") || strings.Contains(lower, "%5c") || strings.Contains(lower, "%00")
}

func parseGitPath(path string) (string, string, string, error) {
	if strings.Contains(path, "\\") || strings.Contains(path, "\x00") || strings.Contains(path, "//") || strings.HasSuffix(path, "/") {
		return "", "", "", errors.New("invalid git path")
	}
	trimmed := strings.TrimPrefix(path, "/git/")
	if trimmed == path || trimmed == "" {
		return "", "", "", errors.New("not a git path")
	}
	for _, seg := range strings.Split(trimmed, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", "", "", errors.New("invalid git path segment")
		}
	}
	account, rest, ok := strings.Cut(trimmed, "/")
	if !ok || !validGitSlug(account) {
		return "", "", "", errors.New("invalid git account")
	}
	// The repository is the next path segment, with or without ".git": the go
	// command drops the suffix from a gitslice.io/git/<account>/<slice>.git
	// module path and asks for /git/<account>/<slice>/info/refs (as GitHub
	// allows).
	name, suffix, hasSuffix := strings.Cut(rest, "/")
	if !hasSuffix {
		return "", "", "", errors.New("missing git endpoint")
	}
	suffix = "/" + suffix
	slice := strings.TrimSuffix(name, ".git")
	if strings.HasSuffix(slice, ".git") || !validGitSlug(slice) {
		return "", "", "", errors.New("invalid slice")
	}
	switch suffix {
	case "/info/refs", "/git-upload-pack", "/git-receive-pack", "/HEAD":
	default:
		return "", "", "", errors.New("unsupported git endpoint")
	}
	return account, slice, suffix, nil
}

func isReceivePackEndpoint(suffix string, r *http.Request) bool {
	return suffix == "/git-receive-pack" || (suffix == "/info/refs" && r.URL.Query().Get("service") == "git-receive-pack")
}

func gitHTTPOperation(r *http.Request) string {
	account, slice, suffix, err := parseGitPath(r.URL.Path)
	if err != nil || account == "" || slice == "" {
		return "unknown"
	}
	switch {
	case isReceivePackEndpoint(suffix, r):
		return "receive-pack"
	case suffix == "/git-upload-pack" || (suffix == "/info/refs" && r.URL.Query().Get("service") == "git-upload-pack") || suffix == "/HEAD":
		return "upload-pack"
	default:
		return "unknown"
	}
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
