package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"

	"gitslice.io/gitslice/internal/rpclimits"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

// FileReader is the unary file-read interface used by the hosted MCP handler.
type FileReader interface {
	ReadFile(ctx context.Context, req *corev1.ReadFileRequest) (*corev1.ReadFileResponse, error)
}

// BlobUploader is the unary blob-upload interface used by the hosted MCP handler.
type BlobUploader interface {
	UploadBlob(ctx context.Context, req *corev1.UploadBlobRequest) (*corev1.UploadBlobResponse, error)
}

// HostedDeps supplies narrow proto-based dependencies for NewHostedHTTPHandler.
// All service fields are satisfied directly by *service.Handlers fields without
// importing service/ or storage packages into internal/mcpserver.
type HostedDeps struct {
	Slices     SliceClient
	Repo       RepositoryClient
	Files      FileReader // optional if Repo also implements FileReader
	Changesets ChangesetClient
	Checks     CheckClient // optional
	Blobs      BlobUploader
	WebBaseURL string
}

// NewHostedHTTPHandler exposes Gitslice's MCP tool suite over stateless
// Streamable HTTP JSON-RPC 2.0 at POST /mcp (design/40_agent_onboarding.md).
// Mounted behind the server auth middleware, every request inherits Bearer
// authentication, scoped API key enforcement, rate limiting, and activity
// tracking with zero background loops.
func NewHostedHTTPHandler(deps HostedDeps) http.Handler {
	files := deps.Files
	if isNilDep(files) && !isNilDep(deps.Repo) {
		if r, ok := deps.Repo.(FileReader); ok {
			files = r
		}
	}
	checks := deps.Checks
	if isNilDep(checks) {
		checks = nil
	}
	ready := !isNilDep(deps.Slices) && !isNilDep(deps.Repo) && !isNilDep(files) && !isNilDep(deps.Changesets) && !isNilDep(deps.Blobs)
	webBaseURL := deps.WebBaseURL

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusMethodNotAllowed)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{
					"code":    "method_not_allowed",
					"message": "POST a JSON-RPC 2.0 MCP message to /mcp",
				},
			})
			return
		}
		if !ready {
			http.Error(w, "MCP endpoint is unavailable", http.StatusServiceUnavailable)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, rpclimits.MaxUnaryMessageBytes+1))
		if err != nil {
			http.Error(w, "failed to read request body", http.StatusBadRequest)
			return
		}
		if int64(len(body)) > rpclimits.MaxUnaryMessageBytes {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      nil,
				"error":   map[string]any{"code": InvalidRequest, "message": "request body too large"},
			})
			return
		}

		defaultSlice := strings.TrimSpace(r.URL.Query().Get("slice"))
		if defaultSlice == "" {
			defaultSlice = strings.TrimSpace(r.Header.Get("X-Gitslice-Slice"))
		}
		backend := NewBackend(BackendDeps{
			Slices:          deps.Slices,
			Repo:            deps.Repo,
			Changesets:      deps.Changesets,
			Checks:          checks,
			DefaultSlice:    defaultSlice,
			ResolveSliceRef: resolveHostedMCPSliceRef,
			FetchSingle: func(ctx context.Context, slice *corev1.SliceRef, commitID, path string) ([]byte, bool, error) {
				resp, err := files.ReadFile(ctx, &corev1.ReadFileRequest{
					CommitId: commitID,
					Path:     path,
					Slice:    slice,
				})
				if grpcstatus.Code(err) == codes.NotFound {
					return nil, false, nil
				}
				if err != nil {
					return nil, false, err
				}
				return resp.GetData(), true, nil
			},
			PrefetchToCache: func(ctx context.Context, slice *corev1.SliceRef, commitID string, targets []ReadTarget) (map[string][]byte, error) {
				out := make(map[string][]byte, len(targets))
				for _, t := range targets {
					resp, err := files.ReadFile(ctx, &corev1.ReadFileRequest{
						CommitId: commitID,
						Path:     t.Path,
						Slice:    slice,
					})
					if grpcstatus.Code(err) == codes.NotFound {
						continue
					}
					if err != nil {
						return nil, err
					}
					out[t.Path] = resp.GetData()
				}
				return out, nil
			},
			UploadEdits: func(ctx context.Context, slice *corev1.SliceRef, edits []*corev1.FileEdit, byHash map[string][]byte) error {
				uploaded := make(map[string]string, len(byHash))
				for hash, data := range byHash {
					resp, err := deps.Blobs.UploadBlob(ctx, &corev1.UploadBlobRequest{
						ContentHash: hash,
						Data:        data,
						Slice:       slice,
					})
					if err != nil {
						return err
					}
					uploaded[hash] = resp.GetBlobId()
				}
				for _, edit := range edits {
					if blobID, ok := uploaded[edit.GetContentHash()]; ok {
						edit.BlobId = blobID
					}
				}
				return nil
			},
			LatestCheckRuns: LatestPatchsetCheckRuns,
			WebResourceURL: func(path string) string {
				base := strings.TrimRight(strings.TrimSpace(webBaseURL), "/")
				if base == "" {
					base = "https://gitslice.io"
				}
				return base + "/" + strings.TrimLeft(path, "/")
			},
		})

		srv := &Server{
			Tools:       backend.Tools(),
			Version:     "hosted",
			FormatError: formatHostedMCPError,
		}
		statusCode, respBody := srv.HandleJSONRPC(r.Context(), body)
		if len(respBody) == 0 {
			w.WriteHeader(statusCode)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(statusCode)
		_, _ = w.Write(respBody)
	})
}

func isNilDep(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Slice, reflect.Map, reflect.Func, reflect.Chan:
		return rv.IsNil()
	default:
		return false
	}
}

func resolveHostedMCPSliceRef(_ context.Context, value string) (*corev1.SliceRef, error) {
	trimmed := strings.TrimSpace(value)
	var account, slice string
	var ok bool
	if strings.Contains(trimmed, "/") {
		account, slice, ok = strings.Cut(trimmed, "/")
	} else if strings.Contains(trimmed, ":") {
		account, slice, ok = strings.Cut(trimmed, ":")
	}
	account = strings.TrimSpace(account)
	slice = strings.TrimSpace(slice)
	if !ok || account == "" || slice == "" || strings.ContainsAny(account+slice, "/:") {
		return nil, &UserError{
			Code:    "invalid_slice",
			Message: "invalid slice reference " + trimmed,
			Hint:    "Pass slice as account/slice, e.g. gitslice/gitslice.",
		}
	}
	return &corev1.SliceRef{Account: account, Slice: slice}, nil
}

func formatHostedMCPError(err error) map[string]any {
	if err == nil {
		return map[string]any{"code": "error"}
	}
	var uerr *UserError
	if errors.As(err, &uerr) {
		out := map[string]any{"code": uerr.Code, "message": uerr.Message}
		if uerr.Hint != "" {
			out["hint"] = uerr.Hint
		}
		return out
	}
	if st, ok := grpcstatus.FromError(err); ok && st.Code() != codes.OK {
		out := map[string]any{
			"code":    strings.ToLower(st.Code().String()),
			"message": st.Message(),
		}
		switch st.Code() {
		case codes.Unauthenticated:
			out["hint"] = "Send an Authorization: Bearer <gsk_...> header with a valid Gitslice API key."
			out["remediation_command"] = "gs onboard --email <owner-email>"
		case codes.PermissionDenied:
			out["hint"] = "Ensure your agent has access to this slice (or use a key scoped to this slice with sufficient role)."
			out["remediation_command"] = "gs slice share <account>/<slice> <agent-username> --role writer"
		}
		return out
	}
	return map[string]any{"code": "error", "message": err.Error()}
}
