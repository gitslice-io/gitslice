package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/rpclimits"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

type fakeHostedSlices struct {
	resolve func(ctx context.Context, req *corev1.ResolveSliceRequest) (*corev1.Slice, error)
}

func (f *fakeHostedSlices) ResolveSlice(ctx context.Context, req *corev1.ResolveSliceRequest) (*corev1.Slice, error) {
	if f.resolve != nil {
		return f.resolve(ctx, req)
	}
	return &corev1.Slice{
		Ref: req.GetRef(),
		Definition: &corev1.SliceDefinition{
			IncludedPaths: []string{"/"},
		},
	}, nil
}

type fakeHostedRepo struct {
	files     map[string][]byte
	readErr   error
	headID    string
	lastSlice *corev1.SliceRef
}

func (f *fakeHostedRepo) GetRef(_ context.Context, _ *corev1.GetRefRequest) (*corev1.Ref, error) {
	commitID := f.headID
	if commitID == "" {
		commitID = "commit-1"
	}
	return &corev1.Ref{CommitId: commitID}, nil
}

func (f *fakeHostedRepo) ListDirectory(_ context.Context, _ *corev1.ListDirectoryRequest) (*corev1.ListDirectoryResponse, error) {
	entries := make([]*corev1.TreeEntry, 0, len(f.files))
	for p, data := range f.files {
		entries = append(entries, &corev1.TreeEntry{
			Path: p,
			Kind: corev1.EntryKind_ENTRY_KIND_FILE,
			Size: int64(len(data)),
		})
	}
	return &corev1.ListDirectoryResponse{Entries: entries}, nil
}

func (f *fakeHostedRepo) ListCommits(_ context.Context, _ *corev1.ListCommitsRequest) (*corev1.ListCommitsResponse, error) {
	return &corev1.ListCommitsResponse{}, nil
}

func (f *fakeHostedRepo) BlameFile(_ context.Context, _ *corev1.BlameFileRequest) (*corev1.BlameFileResponse, error) {
	return &corev1.BlameFileResponse{}, nil
}

func (f *fakeHostedRepo) ReadFile(_ context.Context, req *corev1.ReadFileRequest) (*corev1.ReadFileResponse, error) {
	f.lastSlice = req.GetSlice()
	if f.readErr != nil {
		return nil, f.readErr
	}
	data, ok := f.files[req.GetPath()]
	if !ok {
		return nil, grpcstatus.Error(codes.NotFound, "file not found")
	}
	return &corev1.ReadFileResponse{Data: data}, nil
}

type fakeHostedChangesets struct {
	lastCreate *corev1.CreateChangesetRequest
	lastUpdate *corev1.UpdateChangesetRequest
}

func (f *fakeHostedChangesets) CreateChangeset(_ context.Context, req *corev1.CreateChangesetRequest) (*corev1.Changeset, error) {
	f.lastCreate = req
	return &corev1.Changeset{
		Id:                "cs-42",
		Title:             req.GetTitle(),
		Description:       req.GetDescription(),
		CurrentPatchsetId: "ps-1",
		Status:            "draft",
	}, nil
}

func (f *fakeHostedChangesets) UpdateChangeset(_ context.Context, req *corev1.UpdateChangesetRequest) (*corev1.Patchset, error) {
	f.lastUpdate = req
	return &corev1.Patchset{Id: "ps-1", Number: 1}, nil
}

func (f *fakeHostedChangesets) AbandonChangeset(_ context.Context, _ *corev1.AbandonChangesetRequest) (*corev1.Empty, error) {
	return &corev1.Empty{}, nil
}

func (f *fakeHostedChangesets) GetChangeset(_ context.Context, _ *corev1.GetChangesetRequest) (*corev1.Changeset, error) {
	return &corev1.Changeset{Id: "cs-42", Status: "draft", CurrentPatchsetId: "ps-1"}, nil
}

func (f *fakeHostedChangesets) SubmitChangeset(_ context.Context, _ *corev1.SubmitChangesetRequest) (*corev1.SubmitChangesetResponse, error) {
	return &corev1.SubmitChangesetResponse{
		Status:   "submitted",
		CommitId: "commit-2",
	}, nil
}

type fakeHostedBlobs struct {
	uploaded map[string][]byte
}

func (f *fakeHostedBlobs) UploadBlob(_ context.Context, req *corev1.UploadBlobRequest) (*corev1.UploadBlobResponse, error) {
	if f.uploaded == nil {
		f.uploaded = make(map[string][]byte)
	}
	f.uploaded[req.GetContentHash()] = append([]byte(nil), req.GetData()...)
	return &corev1.UploadBlobResponse{BlobId: "blob-" + req.GetContentHash()}, nil
}

func newTestHostedHandler(t *testing.T, webBaseURL string) (http.Handler, *fakeHostedRepo, *fakeHostedChangesets, *fakeHostedBlobs) {
	t.Helper()
	repo := &fakeHostedRepo{
		files: map[string][]byte{
			"/hello.txt": []byte("hello hosted mcp\n"),
		},
	}
	changesets := &fakeHostedChangesets{}
	blobs := &fakeHostedBlobs{}
	h := NewHostedHTTPHandler(HostedDeps{
		Slices:     &fakeHostedSlices{},
		Repo:       repo,
		Files:      repo,
		Changesets: changesets,
		Blobs:      blobs,
		WebBaseURL: webBaseURL,
	})
	return h, repo, changesets, blobs
}

func TestNewHostedHTTPHandler_MethodAndAvailabilityAndBodyCap(t *testing.T) {
	h, _, _, _ := newTestHostedHandler(t, "")

	// Non-POST returns 405 with Allow: POST and JSON error.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mcp", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", rec.Code)
	}
	if rec.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("Allow = %q, want POST", rec.Header().Get("Allow"))
	}
	if rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(rec.Body.String(), `"method_not_allowed"`) {
		t.Fatalf("unexpected 405 body: %s", rec.Body.String())
	}

	// Missing required deps (including typed nil pointers) return 503.
	var nilBlobs *fakeHostedBlobs
	unavailable := NewHostedHTTPHandler(HostedDeps{
		Slices:     &fakeHostedSlices{},
		Repo:       &fakeHostedRepo{},
		Changesets: &fakeHostedChangesets{},
		Blobs:      nilBlobs,
	})
	unavailRec := httptest.NewRecorder()
	unavailable.ServeHTTP(unavailRec, httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{}`)))
	if unavailRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable status = %d, want 503", unavailRec.Code)
	}

	// Body over rpclimits.MaxUnaryMessageBytes returns 413 JSON-RPC error.
	oversized := bytes.Repeat([]byte("a"), rpclimits.MaxUnaryMessageBytes+1)
	overRec := httptest.NewRecorder()
	h.ServeHTTP(overRec, httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(oversized)))
	if overRec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized status = %d, want 413", overRec.Code)
	}
	if !strings.Contains(overRec.Body.String(), `"request body too large"`) {
		t.Fatalf("unexpected 413 body: %s", overRec.Body.String())
	}
}

func TestNewHostedHTTPHandler_InitializeNotificationAndTools(t *testing.T) {
	h, repo, changesets, blobs := newTestHostedHandler(t, "https://app.example.com/")

	post := func(path, headerSlice, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if headerSlice != "" {
			req.Header.Set("X-Gitslice-Slice", headerSlice)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	// 1. initialize returns 200 with Cache-Control: no-store and hosted version.
	initRec := post("/mcp", "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	if initRec.Code != http.StatusOK {
		t.Fatalf("initialize status = %d, body = %s", initRec.Code, initRec.Body.String())
	}
	if initRec.Header().Get("Content-Type") != "application/json" || initRec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("headers = %+v", initRec.Header())
	}
	if !strings.Contains(initRec.Body.String(), `"version":"hosted"`) {
		t.Fatalf("unexpected initialize body: %s", initRec.Body.String())
	}

	// 2. Notification returns 202 Accepted with empty body.
	notifRec := post("/mcp", "", `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if notifRec.Code != http.StatusAccepted || notifRec.Body.Len() != 0 {
		t.Fatalf("notification status = %d len = %d, want 202 empty", notifRec.Code, notifRec.Body.Len())
	}

	// 3. read_file uses ?slice= query parameter.
	readBody := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"hello.txt"}}}`
	readRec := post("/mcp?slice=alice/main", "", readBody)
	if readRec.Code != http.StatusOK || !strings.Contains(readRec.Body.String(), "hello hosted mcp") {
		t.Fatalf("read_file via ?slice failed: status=%d body=%s", readRec.Code, readRec.Body.String())
	}
	if repo.lastSlice.GetAccount() != "alice" || repo.lastSlice.GetSlice() != "main" {
		t.Fatalf("resolved slice = %+v, want alice/main", repo.lastSlice)
	}

	// 4. read_file falls back to X-Gitslice-Slice header (also supporting account:slice syntax).
	headerRec := post("/mcp", "bob:dev", readBody)
	if headerRec.Code != http.StatusOK || !strings.Contains(headerRec.Body.String(), "hello hosted mcp") {
		t.Fatalf("read_file via header failed: status=%d body=%s", headerRec.Code, headerRec.Body.String())
	}
	if repo.lastSlice.GetAccount() != "bob" || repo.lastSlice.GetSlice() != "dev" {
		t.Fatalf("resolved header slice = %+v, want bob/dev", repo.lastSlice)
	}

	// 5. create_changeset uploads blobs and honors WebBaseURL.
	createPayload, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "create_changeset",
			"arguments": map[string]any{
				"title": "Add file",
				"edits": []map[string]any{
					{"op": "write", "path": "new.txt", "content": "abc\n"},
				},
			},
		},
	})
	createRec := post("/mcp?slice=alice/main", "", string(createPayload))
	if createRec.Code != http.StatusOK {
		t.Fatalf("create_changeset status = %d body = %s", createRec.Code, createRec.Body.String())
	}
	if len(blobs.uploaded) != 1 {
		t.Fatalf("uploaded blobs = %d, want 1", len(blobs.uploaded))
	}
	if changesets.lastUpdate == nil || len(changesets.lastUpdate.GetFileEdits()) != 1 || changesets.lastUpdate.GetFileEdits()[0].GetBlobId() == "" {
		t.Fatalf("expected blob_id populated on edit: %+v", changesets.lastUpdate)
	}
	if !strings.Contains(createRec.Body.String(), "https://app.example.com/") {
		t.Fatalf("expected custom WebBaseURL in response, got: %s", createRec.Body.String())
	}

	// 6. search exercises PrefetchToCache via FileReader.
	searchRec := post("/mcp?slice=alice/main", "", `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"search","arguments":{"pattern":"hosted mcp"}}}`)
	if searchRec.Code != http.StatusOK || !strings.Contains(searchRec.Body.String(), "hello.txt") {
		t.Fatalf("search failed: status=%d body=%s", searchRec.Code, searchRec.Body.String())
	}

	// 7. Default WebBaseURL falls back to https://gitslice.io when empty.
	defaultH, _, _, _ := newTestHostedHandler(t, "")
	defReq := httptest.NewRequest(http.MethodPost, "/mcp?slice=alice/main", bytes.NewReader(createPayload))
	defRec := httptest.NewRecorder()
	defaultH.ServeHTTP(defRec, defReq)
	if !strings.Contains(defRec.Body.String(), "https://gitslice.io/") {
		t.Fatalf("expected default WebBaseURL in response, got: %s", defRec.Body.String())
	}
}

func TestResolveHostedMCPSliceRefAndFormatError(t *testing.T) {
	ref, err := resolveHostedMCPSliceRef(context.Background(), " acme / app ")
	if err != nil || ref.GetAccount() != "acme" || ref.GetSlice() != "app" {
		t.Fatalf("slash ref = %+v, err = %v", ref, err)
	}
	ref, err = resolveHostedMCPSliceRef(context.Background(), "acme:app")
	if err != nil || ref.GetAccount() != "acme" || ref.GetSlice() != "app" {
		t.Fatalf("colon ref = %+v, err = %v", ref, err)
	}
	for _, bad := range []string{"", "onlyone", "a/b/c", "a:b/c", "/slice", "acct/"} {
		if _, err := resolveHostedMCPSliceRef(context.Background(), bad); err == nil {
			t.Errorf("resolveHostedMCPSliceRef(%q) succeeded, want error", bad)
		}
	}

	unauth := formatHostedMCPError(grpcstatus.Error(codes.Unauthenticated, "missing key"))
	if unauth["code"] != "unauthenticated" || unauth["remediation_command"] != "gs onboard --email <owner-email>" {
		t.Fatalf("unauth error = %+v", unauth)
	}
	denied := formatHostedMCPError(grpcstatus.Error(codes.PermissionDenied, "no write access"))
	if denied["code"] != "permissiondenied" || denied["remediation_command"] != "gs slice share <account>/<slice> <agent-username> --role writer" {
		t.Fatalf("denied error = %+v", denied)
	}
	userErr := formatHostedMCPError(&UserError{Code: "invalid_slice", Message: "bad", Hint: "use a/b"})
	if userErr["code"] != "invalid_slice" || userErr["hint"] != "use a/b" {
		t.Fatalf("userErr = %+v", userErr)
	}
	plain := formatHostedMCPError(errors.New("boom"))
	if plain["code"] != "error" || plain["message"] != "boom" {
		t.Fatalf("plain = %+v", plain)
	}
}
