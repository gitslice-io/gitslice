package functional_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/gitslice-io/gitslice/internal/cli"
	"github.com/gitslice-io/gitslice/internal/postgres"
	"github.com/gitslice-io/gitslice/proto/core/v1"
	"github.com/gitslice-io/gitslice/server"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthv1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	grpcstatus "google.golang.org/grpc/status"
)

func TestMinimalCLIJourney(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	runCLI(t, home, workspace, "auth", "login", "--server", ts.addr, "--dev-user", "alice")
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	status := runCLI(t, home, workspace, "status")
	if !strings.Contains(status, "status: clean") {
		t.Fatalf("expected clean status, got:\n%s", status)
	}
	writeWorkspaceFile(t, workspace, "app.go", "package payment\n")
	status = runCLI(t, home, workspace, "status")
	if !strings.Contains(status, "/acme/payment/app.go") {
		t.Fatalf("expected app.go to be dirty, got:\n%s", status)
	}
	runCLI(t, home, workspace, "cs", "create")
	runCLI(t, home, workspace, "cs", "submit")
	csStatus := runCLI(t, home, workspace, "cs", "status")
	if !strings.Contains(csStatus, "status: submitted") {
		t.Fatalf("expected submitted changeset, got:\n%s", csStatus)
	}
	status = runCLI(t, home, workspace, "status")
	if !strings.Contains(status, "status: clean") {
		t.Fatalf("expected clean status after submit, got:\n%s", status)
	}
}

func TestHTTPGatewayLoginAndListSlices(t *testing.T) {
	ts := startTestServer(t)
	statusCode, _, body := httpGatewayPostRaw(t, ts.httpAddr, "/gitslice.core.v1.SliceService/ListSlices", "", map[string]any{
		"account": "acme",
	})
	if statusCode != http.StatusUnauthorized {
		t.Fatalf("expected unauthenticated ListSlices to return 401, got %d:\n%s", statusCode, string(body))
	}
	statusCode, _, body = httpGatewayPostRaw(t, ts.httpAddr, "/gitslice.core.v1.SliceService/ListSlices", "not-a-token", map[string]any{
		"account": "acme",
	})
	if statusCode != http.StatusUnauthorized {
		t.Fatalf("expected invalid-token ListSlices to return 401, got %d:\n%s", statusCode, string(body))
	}
	statusCode, headers := httpGatewayOptions(t, ts.httpAddr, "/gitslice.core.v1.SliceService/ListSlices", "http://web.test")
	if statusCode != http.StatusNoContent {
		t.Fatalf("expected CORS preflight to return 204, got %d", statusCode)
	}
	if got := headers.Get("Access-Control-Allow-Origin"); got != "http://web.test" {
		t.Fatalf("expected CORS allow-origin for web app, got %q", got)
	}

	login := httpGatewayPost(t, ts.httpAddr, "/gitslice.core.v1.FakeAccountService/Login", "", map[string]string{
		"devUser": "alice",
	})
	token, _ := login["token"].(string)
	if token == "" {
		t.Fatalf("expected login token in response: %#v", login)
	}
	if subjectID, _ := login["subjectId"].(string); subjectID == "" {
		t.Fatalf("expected subject id in response: %#v", login)
	}

	response := httpGatewayPost(t, ts.httpAddr, "/gitslice.core.v1.SliceService/ListSlices", token, map[string]any{
		"account": "acme",
	})
	slices, ok := response["slices"].([]any)
	if !ok || len(slices) == 0 {
		t.Fatalf("expected slices in response: %#v", response)
	}
	for _, raw := range slices {
		slice, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		ref, _ := slice["ref"].(map[string]any)
		if ref["account"] == "acme" && ref["slice"] == "payment" {
			return
		}
	}
	t.Fatalf("expected acme/payment slice in response: %#v", response)
}

func TestHTTPGatewayWriteChangesetFlow(t *testing.T) {
	ts := startTestServer(t)
	login := httpGatewayPost(t, ts.httpAddr, "/gitslice.core.v1.FakeAccountService/Login", "", map[string]string{
		"devUser": "alice",
	})
	token, _ := login["token"].(string)
	if token == "" {
		t.Fatalf("expected login token in response: %#v", login)
	}

	content := []byte("package payment\nconst GatewayWrite = true\n")
	upload := httpGatewayPost(t, ts.httpAddr, "/gitslice.core.v1.BlobService/UploadBlob", token, map[string]any{
		"data": base64.StdEncoding.EncodeToString(content),
	})
	blobID, _ := upload["blobId"].(string)
	contentHash, _ := upload["contentHash"].(string)
	if blobID == "" || contentHash == "" {
		t.Fatalf("expected uploaded blob id and hash: %#v", upload)
	}

	blobStatus := httpGatewayPost(t, ts.httpAddr, "/gitslice.core.v1.BlobService/GetBlobStatus", token, map[string]any{
		"contentHashes": []string{contentHash, "sha256:missing"},
	})
	records, ok := blobStatus["blobs"].([]any)
	if !ok || len(records) != 2 {
		t.Fatalf("expected two blob status records: %#v", blobStatus)
	}
	if first, _ := records[0].(map[string]any); first["state"] != "available" {
		t.Fatalf("expected uploaded blob to be available: %#v", blobStatus)
	}
	if second, _ := records[1].(map[string]any); second["state"] != "missing" {
		t.Fatalf("expected unknown blob to be missing: %#v", blobStatus)
	}

	cs := httpGatewayPost(t, ts.httpAddr, "/gitslice.core.v1.ChangesetService/CreateChangeset", token, map[string]any{
		"authoringSlice": map[string]string{"account": "acme", "slice": "payment"},
		"title":          "gateway write",
		"description":    "created through HTTP gateway",
	})
	changesetID, _ := cs["id"].(string)
	baseCommitID, _ := cs["baseCommitId"].(string)
	if changesetID == "" || baseCommitID == "" {
		t.Fatalf("expected changeset id and base commit: %#v", cs)
	}

	patchset := httpGatewayPost(t, ts.httpAddr, "/gitslice.core.v1.ChangesetService/UpdateChangeset", token, map[string]any{
		"changesetId":  changesetID,
		"baseCommitId": baseCommitID,
		"fileEdits": []map[string]any{{
			"op":          "add",
			"path":        "/acme/payment/gateway_write.go",
			"blobId":      blobID,
			"contentHash": contentHash,
			"mode":        420,
		}},
	})
	patchsetID, _ := patchset["id"].(string)
	if patchsetID == "" {
		t.Fatalf("expected patchset id: %#v", patchset)
	}

	submit := httpGatewayPost(t, ts.httpAddr, "/gitslice.core.v1.ChangesetService/SubmitChangeset", token, map[string]any{
		"changesetId":               changesetID,
		"expectedCurrentPatchsetId": patchsetID,
	})
	if status, _ := submit["status"].(string); status != "pending_publish" && status != "submitted" {
		t.Fatalf("unexpected submit response: %#v", submit)
	}
	published := waitForGatewayChangesetStatus(t, ts.httpAddr, token, changesetID, "submitted")
	if commitID, _ := published["commitId"].(string); commitID == "" {
		t.Fatalf("expected published commit id: %#v", published)
	}
}

func TestChangesetUpdateAndDelete(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	runCLI(t, home, workspace, "auth", "login", "--server", ts.addr, "--dev-user", "alice")
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspace, "draft.go", "package payment\nconst Version = 1\n")
	runCLI(t, home, workspace, "cs", "create")
	writeWorkspaceFile(t, workspace, "draft.go", "package payment\nconst Version = 2\n")
	runCLI(t, home, workspace, "cs", "update")
	runCLI(t, home, workspace, "cs", "submit")
	status := runCLI(t, home, workspace, "status")
	if !strings.Contains(status, "status: clean") {
		t.Fatalf("expected clean status after update submit, got:\n%s", status)
	}

	if err := os.Remove(filepath.Join(workspace, "draft.go")); err != nil {
		t.Fatal(err)
	}
	status = runCLI(t, home, workspace, "status")
	if !strings.Contains(status, "/acme/payment/draft.go") {
		t.Fatalf("expected delete to be dirty, got:\n%s", status)
	}
	runCLI(t, home, workspace, "cs", "create", "--title", "delete draft")
	runCLI(t, home, workspace, "cs", "submit")
	status = runCLI(t, home, workspace, "status")
	if !strings.Contains(status, "status: clean") {
		t.Fatalf("expected clean status after delete submit, got:\n%s", status)
	}
}

func TestOutsideSliceEditRejected(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	runCLI(t, home, workspace, "auth", "login", "--server", ts.addr, "--dev-user", "alice")
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspace, "acme/backend/app.go", "package backend\n")
	_, stderr := runCLIFails(t, home, workspace, "status")
	if !strings.Contains(stderr, "outside slice") {
		t.Fatalf("expected outside-slice rejection, got stderr:\n%s", stderr)
	}
}

func TestDisjointStaleChangesetsCanSubmit(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	runCLI(t, home, workspaceA, "auth", "login", "--server", ts.addr, "--dev-user", "alice")
	runCLI(t, home, workspaceA, "workspace", "init", "acme/payment")
	runCLI(t, home, workspaceB, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspaceA, "a.go", "package payment\nconst A = 1\n")
	writeWorkspaceFile(t, workspaceB, "b.go", "package payment\nconst B = 1\n")
	runCLI(t, home, workspaceA, "cs", "create", "--title", "add a")
	runCLI(t, home, workspaceB, "cs", "create", "--title", "add b")
	runCLI(t, home, workspaceA, "cs", "submit")
	runCLI(t, home, workspaceB, "cs", "submit")
}

func TestSamePathConflictRejected(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	runCLI(t, home, workspaceA, "auth", "login", "--server", ts.addr, "--dev-user", "alice")
	runCLI(t, home, workspaceA, "workspace", "init", "acme/payment")
	runCLI(t, home, workspaceB, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspaceA, "conflict.go", "package payment\nconst Conflict = 1\n")
	writeWorkspaceFile(t, workspaceB, "conflict.go", "package payment\nconst Conflict = 2\n")
	runCLI(t, home, workspaceA, "cs", "create", "--title", "conflict one")
	runCLI(t, home, workspaceB, "cs", "create", "--title", "conflict two")
	runCLI(t, home, workspaceA, "cs", "submit")
	_, stderr := runCLIFails(t, home, workspaceB, "cs", "submit")
	if !strings.Contains(stderr, "FailedPrecondition") && !strings.Contains(stderr, "conflict") {
		t.Fatalf("expected same-path conflict, got stderr:\n%s", stderr)
	}
}

func TestRepositoryReadAPIs(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	runCLI(t, home, workspace, "auth", "login", "--server", ts.addr, "--dev-user", "alice")
	token := readToken(t, home)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	const content = "package api\nconst ReadAPI = true\n"
	writeWorkspaceFile(t, workspace, "api/read_api.go", content)
	runCLI(t, home, workspace, "cs", "create", "--title", "repository api read")
	runCLI(t, home, workspace, "cs", "submit")

	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	repository := corev1.NewRepositoryServiceClient(conn)

	ref, err := repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if ref.Name != postgres.DefaultTargetRef || ref.CommitId == "" {
		t.Fatalf("unexpected default ref: %#v", ref)
	}
	commit, err := repository.GetCommit(ctx, &corev1.GetCommitRequest{CommitId: ref.CommitId})
	if err != nil {
		t.Fatal(err)
	}
	if !containsString(commit.ChangedPaths, "/acme/payment/api/read_api.go") {
		t.Fatalf("expected commit to include changed path, got %#v", commit.ChangedPaths)
	}
	root, err := repository.ListDirectory(ctx, &corev1.ListDirectoryRequest{CommitId: ref.CommitId, Path: "/"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasEntry(root.Entries, "acme", corev1.EntryKind_ENTRY_KIND_DIRECTORY) {
		t.Fatalf("expected root to contain acme directory: %#v", root.Entries)
	}
	payment, err := repository.ListDirectory(ctx, &corev1.ListDirectoryRequest{CommitId: ref.CommitId, Path: "/acme/payment"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasEntry(payment.Entries, "api", corev1.EntryKind_ENTRY_KIND_DIRECTORY) {
		t.Fatalf("expected payment directory to contain api directory: %#v", payment.Entries)
	}
	resolvedDir, err := repository.ResolvePath(ctx, &corev1.ResolvePathRequest{CommitId: ref.CommitId, Path: "/acme/payment/api"})
	if err != nil {
		t.Fatal(err)
	}
	if resolvedDir.Entry.Kind != corev1.EntryKind_ENTRY_KIND_DIRECTORY {
		t.Fatalf("expected api to resolve as directory: %#v", resolvedDir.Entry)
	}
	resolvedFile, err := repository.ResolvePath(ctx, &corev1.ResolvePathRequest{CommitId: ref.CommitId, Path: "/acme/payment/api/read_api.go"})
	if err != nil {
		t.Fatal(err)
	}
	if resolvedFile.Entry.Kind != corev1.EntryKind_ENTRY_KIND_FILE || resolvedFile.Entry.ContentHash == "" {
		t.Fatalf("expected read_api.go to resolve as file with content hash: %#v", resolvedFile.Entry)
	}
	read, err := repository.ReadFile(ctx, &corev1.ReadFileRequest{CommitId: ref.CommitId, Path: "/acme/payment/api/read_api.go"})
	if err != nil {
		t.Fatal(err)
	}
	if string(read.Data) != content || read.ContentHash != resolvedFile.Entry.ContentHash {
		t.Fatalf("unexpected read response: content=%q hash=%q want hash=%q", string(read.Data), read.ContentHash, resolvedFile.Entry.ContentHash)
	}
	partial, err := repository.ReadFile(ctx, &corev1.ReadFileRequest{CommitId: ref.CommitId, Path: "/acme/payment/api/read_api.go", Offset: 8, Length: 3})
	if err != nil {
		t.Fatal(err)
	}
	if string(partial.Data) != "api" || partial.Offset != 8 {
		t.Fatalf("unexpected partial read response: %#v data=%q", partial, string(partial.Data))
	}
}

func TestSliceDefinitionUpdateConflict(t *testing.T) {
	ts := startTestServer(t)
	token := loginViaGRPC(t, ts.addr, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	slices := corev1.NewSliceServiceClient(conn)

	slice, err := slices.ResolveSlice(ctx, &corev1.ResolveSliceRequest{Ref: &corev1.SliceRef{Account: "acme", Slice: "payment"}})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := slices.UpdateSliceDefinition(ctx, &corev1.UpdateSliceDefinitionRequest{
		SliceId:                slice.Id,
		ExpectedDefinitionHash: slice.DefinitionHash,
		Definition: &corev1.SliceDefinition{
			Version:       slice.Definition.Version,
			Visibility:    "public",
			IncludedPaths: append(append([]string{}, slice.Definition.IncludedPaths...), "/acme/payment/docs"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != slice.Definition.Version+1 {
		t.Fatalf("expected definition version %d, got %d", slice.Definition.Version+1, updated.Version)
	}
	if updated.Visibility != "public" || !containsString(updated.IncludedPaths, "/acme/payment/docs") {
		t.Fatalf("unexpected updated definition: %#v", updated)
	}
	_, err = slices.UpdateSliceDefinition(ctx, &corev1.UpdateSliceDefinitionRequest{
		SliceId:                slice.Id,
		ExpectedDefinitionHash: slice.DefinitionHash,
		Definition: &corev1.SliceDefinition{
			Version:       updated.Version,
			Visibility:    "account",
			IncludedPaths: []string{"/acme/payment"},
		},
	})
	if grpcstatus.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected stale definition update to fail with FailedPrecondition, got %v", err)
	}
}

func TestChangesetAbandonAndSubmitIdempotency(t *testing.T) {
	ts := startTestServer(t)
	token := loginViaGRPC(t, ts.addr, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	clients := newTestCoreClients(conn)

	submittedID, patchsetID := createDirectPatchset(t, ctx, clients, "/acme/payment/idempotent_submit.go", "package payment\nconst Idempotent = true\n", "idempotent submit")
	first, err := clients.changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{
		ChangesetId:               submittedID,
		ExpectedCurrentPatchsetId: patchsetID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "pending_publish" && first.Status != "submitted" {
		t.Fatalf("unexpected first submit response: %#v", first)
	}
	published := waitForSubmittedChangeset(t, ctx, clients.changeset, submittedID)
	second, err := clients.changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{
		ChangesetId:               submittedID,
		ExpectedCurrentPatchsetId: patchsetID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != "submitted" || second.CommitId != published.CommitId {
		t.Fatalf("expected idempotent submitted response with same commit, got %#v want commit %s", second, published.CommitId)
	}

	abandoned, err := clients.changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
		TargetRef:      postgres.DefaultTargetRef,
		Title:          "abandon draft",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := clients.changeset.AbandonChangeset(ctx, &corev1.AbandonChangesetRequest{ChangesetId: abandoned.Id, Reason: "test"}); err != nil {
		t.Fatal(err)
	}
	got, err := clients.changeset.GetChangeset(ctx, &corev1.GetChangesetRequest{ChangesetId: abandoned.Id})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "abandoned" {
		t.Fatalf("expected abandoned status, got %#v", got)
	}
	_, err = clients.changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{ChangesetId: abandoned.Id})
	if grpcstatus.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected abandoned submit to fail with FailedPrecondition, got %v", err)
	}
}

func TestWorkspaceServiceHelpers(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	runCLI(t, home, workspace, "auth", "login", "--server", ts.addr, "--dev-user", "alice")
	token := readToken(t, home)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	const content = "package payment\nconst Hydrate = true\n"
	writeWorkspaceFile(t, workspace, "hydrate.go", content)
	runCLI(t, home, workspace, "cs", "create", "--title", "hydrate helper")
	runCLI(t, home, workspace, "cs", "submit")

	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	workspaceClient := corev1.NewWorkspaceServiceClient(conn)
	ref := &corev1.WorkspaceRef{Id: "acme/payment"}

	state, err := workspaceClient.GetWorkspaceState(ctx, &corev1.GetWorkspaceStateRequest{Workspace: ref})
	if err != nil {
		t.Fatal(err)
	}
	if state.BaseCommitId == "" || state.Slice == nil || state.Slice.SliceId == "" || !containsString(state.HydratedPaths, "/acme/payment") {
		t.Fatalf("unexpected workspace state: %#v", state)
	}
	hydrated, err := workspaceClient.HydratePaths(ctx, &corev1.HydratePathsRequest{
		Workspace: ref,
		Paths:     []string{"/acme/payment/hydrate.go"},
		Mode:      "file_contents",
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(hydrated.Data) != content || hydrated.Entry == nil || hydrated.Entry.Kind != corev1.EntryKind_ENTRY_KIND_FILE {
		t.Fatalf("unexpected hydration response: %#v data=%q", hydrated, string(hydrated.Data))
	}
	recorded, err := workspaceClient.RecordWorkspaceOperation(ctx, &corev1.RecordWorkspaceOperationRequest{
		Operation: &corev1.WorkspaceOperation{
			Workspace:     ref,
			OperationType: "functional_test",
			Description:   "record workspace operation",
			AffectedPaths: []string{"/acme/payment/hydrate.go"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(recorded.OperationId, "op_") {
		t.Fatalf("expected generated operation id, got %#v", recorded)
	}
	_, err = workspaceClient.GetWorkspaceState(ctx, &corev1.GetWorkspaceStateRequest{Workspace: &corev1.WorkspaceRef{Id: "bad-workspace"}})
	if grpcstatus.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected invalid workspace id to fail with InvalidArgument, got %v", err)
	}
}

func TestStaleDisjointUpdatePreservesFinalState(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	seedWorkspace := t.TempDir()
	runCLI(t, home, seedWorkspace, "auth", "login", "--server", ts.addr, "--dev-user", "alice")
	token := readToken(t, home)
	runCLI(t, home, seedWorkspace, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, seedWorkspace, "shared.go", "package payment\nconst Shared = true\n")
	runCLI(t, home, seedWorkspace, "cs", "create", "--title", "seed shared")
	runCLI(t, home, seedWorkspace, "cs", "submit")

	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	runCLI(t, home, workspaceA, "workspace", "init", "acme/payment")
	runCLI(t, home, workspaceB, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspaceA, "shared.go", "package payment\nconst Shared = false\n")
	writeWorkspaceFile(t, workspaceB, "new_disjoint.go", "package payment\nconst NewDisjoint = true\n")
	runCLI(t, home, workspaceA, "cs", "create", "--title", "update shared")
	runCLI(t, home, workspaceB, "cs", "create", "--title", "add disjoint")
	runCLI(t, home, workspaceB, "cs", "submit")
	runCLI(t, home, workspaceA, "cs", "submit")

	cloneDir := cloneSlice(t, ts, token)
	assertProjectedFile(t, cloneDir, "acme/payment/shared.go", "package payment\nconst Shared = false\n")
	assertProjectedFile(t, cloneDir, "acme/payment/new_disjoint.go", "package payment\nconst NewDisjoint = true\n")
}

func TestDeleteUpdateConflicts(t *testing.T) {
	tests := []struct {
		name        string
		firstAction string
	}{
		{name: "delete_then_update", firstAction: "delete"},
		{name: "update_then_delete", firstAction: "update"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := startTestServer(t)
			home := t.TempDir()
			seedWorkspace := t.TempDir()
			runCLI(t, home, seedWorkspace, "auth", "login", "--server", ts.addr, "--dev-user", "alice")
			runCLI(t, home, seedWorkspace, "workspace", "init", "acme/payment")
			writeWorkspaceFile(t, seedWorkspace, "du.go", "package payment\nconst Value = 1\n")
			runCLI(t, home, seedWorkspace, "cs", "create", "--title", "seed du")
			runCLI(t, home, seedWorkspace, "cs", "submit")

			deleteWorkspace := t.TempDir()
			updateWorkspace := t.TempDir()
			runCLI(t, home, deleteWorkspace, "workspace", "init", "acme/payment")
			runCLI(t, home, updateWorkspace, "workspace", "init", "acme/payment")
			copyWorkspaceFile(t, seedWorkspace, deleteWorkspace, "du.go")
			copyWorkspaceFile(t, seedWorkspace, updateWorkspace, "du.go")
			copyWorkspaceFile(t, seedWorkspace, deleteWorkspace, ".gs/base_snapshot.json")
			copyWorkspaceFile(t, seedWorkspace, updateWorkspace, ".gs/base_snapshot.json")
			if err := os.Remove(filepath.Join(deleteWorkspace, "du.go")); err != nil {
				t.Fatal(err)
			}
			writeWorkspaceFile(t, updateWorkspace, "du.go", "package payment\nconst Value = 2\n")
			runCLI(t, home, deleteWorkspace, "cs", "create", "--title", "delete du")
			runCLI(t, home, updateWorkspace, "cs", "create", "--title", "update du")

			firstWorkspace := deleteWorkspace
			secondWorkspace := updateWorkspace
			if tt.firstAction == "update" {
				firstWorkspace = updateWorkspace
				secondWorkspace = deleteWorkspace
			}
			runCLI(t, home, firstWorkspace, "cs", "submit")
			_, stderr := runCLIFails(t, home, secondWorkspace, "cs", "submit")
			assertConflictError(t, stderr)
		})
	}
}

func TestSameNewPathConcurrentOnlyOneSubmitSucceeds(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	firstWorkspace := t.TempDir()
	runCLI(t, home, firstWorkspace, "auth", "login", "--server", ts.addr, "--dev-user", "alice")

	const workers = 6
	workspaces := make([]string, workers)
	for i := 0; i < workers; i++ {
		workspace := t.TempDir()
		workspaces[i] = workspace
		runCLI(t, home, workspace, "workspace", "init", "acme/payment")
		writeWorkspaceFile(t, workspace, "same_new.go", fmt.Sprintf("package payment\nconst Winner = %d\n", i))
		runCLI(t, home, workspace, "cs", "create", "--title", fmt.Sprintf("same new %d", i))
	}
	var wg sync.WaitGroup
	results := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := runCLIResult(home, workspaces[i], "cs", "submit")
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)

	successes := 0
	conflicts := 0
	for err := range results {
		if err == nil {
			successes++
			continue
		}
		if strings.Contains(err.Error(), "FailedPrecondition") || strings.Contains(err.Error(), "conflict") {
			conflicts++
			continue
		}
		t.Fatalf("unexpected submit error: %v", err)
	}
	if successes != 1 || conflicts != workers-1 {
		t.Fatalf("expected one success and %d conflicts, got successes=%d conflicts=%d", workers-1, successes, conflicts)
	}
}

func TestConcurrentDisjointSubmitFinalProjection(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	firstWorkspace := t.TempDir()
	runCLI(t, home, firstWorkspace, "auth", "login", "--server", ts.addr, "--dev-user", "alice")
	token := readToken(t, home)

	const workers = 10
	workspaces := make([]string, workers)
	for i := 0; i < workers; i++ {
		workspace := t.TempDir()
		workspaces[i] = workspace
		runCLI(t, home, workspace, "workspace", "init", "acme/payment")
		rel := fmt.Sprintf("concurrent/f_%02d.go", i)
		body := fmt.Sprintf("package concurrent\nconst File%d = %d\n", i, i)
		writeWorkspaceFile(t, workspace, rel, body)
		runCLI(t, home, workspace, "cs", "create", "--title", fmt.Sprintf("concurrent %02d", i))
	}
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := runCLIResult(home, workspaces[i], "cs", "submit"); err != nil {
				errs <- err
				return
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	cloneDir := cloneSlice(t, ts, token)
	for i := 0; i < workers; i++ {
		rel := fmt.Sprintf("acme/payment/concurrent/f_%02d.go", i)
		body := fmt.Sprintf("package concurrent\nconst File%d = %d\n", i, i)
		assertProjectedFile(t, cloneDir, rel, body)
	}
}

func TestRestartPreservesSubmittedState(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	runCLI(t, home, workspace, "auth", "login", "--server", ts.addr, "--dev-user", "alice")
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspace, "restart.go", "package payment\n")
	runCLI(t, home, workspace, "cs", "create")
	runCLI(t, home, workspace, "cs", "submit")
	ts.restart(t)
	status := runCLI(t, home, workspace, "cs", "status")
	if !strings.Contains(status, "status: submitted") {
		t.Fatalf("expected submitted state after restart, got:\n%s", status)
	}
}

func TestGitHTTPAuthAndUnsupportedOperationMatrix(t *testing.T) {
	ts := startTestServer(t)
	token := loginViaGRPC(t, ts.addr, "alice")

	uploadInfoRefs := "/git/acme/payment.git/info/refs?service=git-upload-pack"
	statusCode, headers, body := gitHTTPRaw(t, ts.gitAddr, uploadInfoRefs, "")
	if statusCode != http.StatusUnauthorized {
		t.Fatalf("expected unauthenticated upload-pack discovery to return 401, got %d:\n%s", statusCode, string(body))
	}
	if got := headers.Get("WWW-Authenticate"); !strings.Contains(got, `Basic realm="gitslice"`) {
		t.Fatalf("expected basic auth challenge, got %q", got)
	}

	statusCode, _, body = gitHTTPRaw(t, ts.gitAddr, uploadInfoRefs, "Bearer not-a-token")
	if statusCode != http.StatusUnauthorized {
		t.Fatalf("expected invalid token upload-pack discovery to return 401, got %d:\n%s", statusCode, string(body))
	}

	basicAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:"+token))
	statusCode, headers, body = gitHTTPRaw(t, ts.gitAddr, uploadInfoRefs, basicAuth)
	if statusCode != http.StatusOK {
		t.Fatalf("expected basic-auth upload-pack discovery to return 200, got %d:\n%s", statusCode, string(body))
	}
	if got := headers.Get("Content-Type"); !strings.Contains(got, "application/x-git-upload-pack-advertisement") {
		t.Fatalf("expected upload-pack advertisement content type, got %q", got)
	}

	statusCode, _, body = gitHTTPRaw(t, ts.gitAddr, "/git/acme/missing.git/info/refs?service=git-upload-pack", "Bearer "+token)
	if statusCode != http.StatusNotFound {
		t.Fatalf("expected missing slice to return 404, got %d:\n%s", statusCode, string(body))
	}

	receiveInfoRefs := "/git/acme/payment.git/info/refs?service=git-receive-pack"
	statusCode, headers, body = gitHTTPRaw(t, ts.gitAddr, receiveInfoRefs, "")
	if statusCode != http.StatusUnauthorized {
		t.Fatalf("expected unauthenticated receive-pack discovery to return 401, got %d:\n%s", statusCode, string(body))
	}
	if got := headers.Get("WWW-Authenticate"); !strings.Contains(got, `Basic realm="gitslice"`) {
		t.Fatalf("expected receive-pack auth challenge, got %q", got)
	}

	statusCode, _, body = gitHTTPRaw(t, ts.gitAddr, receiveInfoRefs, "Bearer "+token)
	if statusCode != http.StatusForbidden {
		t.Fatalf("expected authenticated receive-pack discovery to return 403, got %d:\n%s", statusCode, string(body))
	}
	if !strings.Contains(string(body), "git push is not supported") || !strings.Contains(string(body), "native changesets") {
		t.Fatalf("expected push rejection to direct users to native changesets, got:\n%s", string(body))
	}

	statusCode, _, body = gitHTTPRaw(t, ts.gitAddr, "/not-git/acme/payment.git/info/refs?service=git-upload-pack", "Bearer "+token)
	if statusCode != http.StatusNotFound {
		t.Fatalf("expected non-git route to return 404, got %d:\n%s", statusCode, string(body))
	}
}

func TestGitCloneProjection(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	runCLI(t, home, workspace, "auth", "login", "--server", ts.addr, "--dev-user", "alice")
	token := readToken(t, home)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspace, "git_layer.go", "package payment\nconst GitLayer = true\n")
	runCLI(t, home, workspace, "cs", "create", "--title", "git projection")
	runCLI(t, home, workspace, "cs", "submit")

	cloneDir := filepath.Join(t.TempDir(), "payment")
	gitURL := "http://" + ts.gitAddr + "/git/acme/payment.git"
	_, stderr, err := runGitResult("", "clone", gitURL, filepath.Join(t.TempDir(), "unauthenticated-payment"))
	if err == nil {
		t.Fatal("expected unauthenticated git clone to be rejected")
	}
	if !strings.Contains(stderr, "401") &&
		!strings.Contains(stderr, "Authentication failed") &&
		!strings.Contains(stderr, "authentication") &&
		!strings.Contains(stderr, "Username") {
		t.Fatalf("expected unauthenticated clone rejection, got stderr:\n%s", stderr)
	}
	runGit(t, "", "-c", "http.extraHeader=Authorization: Bearer "+token, "clone", gitURL, cloneDir)
	projected, err := os.ReadFile(filepath.Join(cloneDir, "acme", "payment", "git_layer.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(projected) != "package payment\nconst GitLayer = true\n" {
		t.Fatalf("unexpected projected file contents:\n%s", string(projected))
	}
	writeWorkspaceFile(t, workspace, "git_fetch.go", "package payment\nconst GitFetch = true\n")
	runCLI(t, home, workspace, "cs", "create", "--title", "git fetch projection")
	runCLI(t, home, workspace, "cs", "submit")
	runGit(t, "", "-C", cloneDir, "-c", "http.extraHeader=Authorization: Bearer "+token, "fetch", "origin", "main")
	runGit(t, "", "-C", cloneDir, "checkout", "-B", "main", "origin/main")
	projectedFetch, err := os.ReadFile(filepath.Join(cloneDir, "acme", "payment", "git_fetch.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(projectedFetch) != "package payment\nconst GitFetch = true\n" {
		t.Fatalf("unexpected fetched file contents:\n%s", string(projectedFetch))
	}

	_, stderr, err = runGitResult("", "-C", cloneDir, "-c", "http.extraHeader=Authorization: Bearer "+token, "push", "origin", "HEAD:refs/changes/new")
	if err == nil {
		t.Fatal("expected git push to be rejected")
	}
	if !strings.Contains(stderr, "403") && !strings.Contains(stderr, "not supported") {
		t.Fatalf("expected push rejection, got stderr:\n%s", stderr)
	}
}

func TestGitHubImportShallow(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	sourceRepo := createImportGitRepo(t)
	runCLI(t, home, workspace, "auth", "login", "--server", ts.addr, "--dev-user", "alice")

	raw := runCLI(t, home, workspace,
		"repo", "import", "github", sourceRepo,
		"--mount", "/acme/payment/imported/shallow",
		"--slice", "acme/payment",
		"--mode", "shallow",
		"--json",
	)
	var imported struct {
		FinalCommitID string `json:"final_commit_id"`
		Commits       []struct {
			NativeCommitID string `json:"native_commit_id"`
			Message        string `json:"message"`
		} `json:"commits"`
	}
	if err := json.Unmarshal([]byte(raw), &imported); err != nil {
		t.Fatal(err)
	}
	if len(imported.Commits) != 1 {
		t.Fatalf("shallow import commits = %d, want 1: %s", len(imported.Commits), raw)
	}
	if imported.Commits[0].Message != "third commit" {
		t.Fatalf("shallow import message = %q, want third commit", imported.Commits[0].Message)
	}
	inspect := runCLI(t, home, workspace, "commit", "inspect", imported.FinalCommitID)
	if !strings.Contains(inspect, "message: third commit") ||
		!strings.Contains(inspect, "/acme/payment/imported/shallow/README.md") {
		t.Fatalf("unexpected inspect output:\n%s", inspect)
	}
}

func TestGitHubImportProgressText(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	sourceRepo := createImportGitRepo(t)
	runCLI(t, home, workspace, "auth", "login", "--server", ts.addr, "--dev-user", "alice")

	stdout, stderr := runCLIStreams(t, home, workspace,
		"repo", "import", "github", sourceRepo,
		"--mount", "/acme/payment/imported/progress",
		"--slice", "acme/payment",
		"--mode", "shallow",
	)
	if !strings.Contains(stdout, "imported 1 commit(s)") ||
		!strings.Contains(stdout, "final commit:") {
		t.Fatalf("unexpected import stdout:\n%s", stdout)
	}
	for _, want := range []string{"cloning repository", "found 1 commit(s)", "import complete"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("import stderr missing %q:\n%s", want, stderr)
		}
	}
}

func TestGitHubImportDeepListAndInspectCommits(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	sourceRepo := createImportGitRepo(t)
	runCLI(t, home, workspace, "auth", "login", "--server", ts.addr, "--dev-user", "alice")
	token := readToken(t, home)

	raw := runCLI(t, home, workspace,
		"repo", "import", "github", sourceRepo,
		"--mount", "/acme/payment/imported/deep",
		"--slice", "acme/payment",
		"--mode", "deep",
		"--json",
	)
	var imported struct {
		FinalCommitID string `json:"final_commit_id"`
		Commits       []struct {
			NativeCommitID string `json:"native_commit_id"`
			Message        string `json:"message"`
		} `json:"commits"`
	}
	if err := json.Unmarshal([]byte(raw), &imported); err != nil {
		t.Fatal(err)
	}
	if len(imported.Commits) != 3 {
		t.Fatalf("deep import commits = %d, want 3: %s", len(imported.Commits), raw)
	}
	for i, want := range []string{"first commit", "second commit", "third commit"} {
		if imported.Commits[i].Message != want {
			t.Fatalf("commit %d message = %q, want %q", i, imported.Commits[i].Message, want)
		}
	}
	list := runCLI(t, home, workspace, "commit", "list", "--limit", "4")
	for _, want := range []string{"third commit", "second commit", "first commit"} {
		if !strings.Contains(list, want) {
			t.Fatalf("commit list missing %q:\n%s", want, list)
		}
	}
	inspect := runCLI(t, home, workspace, "commit", "inspect", imported.Commits[1].NativeCommitID)
	if !strings.Contains(inspect, "message: second commit") ||
		!strings.Contains(inspect, "/acme/payment/imported/deep/lib/code.go") {
		t.Fatalf("unexpected second commit inspect output:\n%s", inspect)
	}
	cloneDir := cloneSlice(t, ts, token)
	assertProjectedFile(t, cloneDir, "acme/payment/imported/deep/README.md", "hello v2\n")
	assertProjectedFile(t, cloneDir, "acme/payment/imported/deep/lib/code.go", "package lib\nconst Value = 1\n")
}

func TestGitHubImportDeepMaxCommitsAndResume(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	sourceRepo := createImportGitRepo(t)
	runCLI(t, home, workspace, "auth", "login", "--server", ts.addr, "--dev-user", "alice")

	raw := runCLI(t, home, workspace,
		"repo", "import", "github", sourceRepo,
		"--mount", "/acme/payment/imported/bounded",
		"--slice", "acme/payment",
		"--mode", "deep",
		"--max-commits", "2",
		"--json",
	)
	var imported struct {
		Commits []struct {
			Message string `json:"message"`
		} `json:"commits"`
	}
	if err := json.Unmarshal([]byte(raw), &imported); err != nil {
		t.Fatal(err)
	}
	if len(imported.Commits) != 2 {
		t.Fatalf("bounded deep import commits = %d, want 2: %s", len(imported.Commits), raw)
	}
	for i, want := range []string{"second commit", "third commit"} {
		if imported.Commits[i].Message != want {
			t.Fatalf("commit %d message = %q, want %q", i, imported.Commits[i].Message, want)
		}
	}

	stdout, stderr := runCLIStreams(t, home, workspace,
		"repo", "import", "github", sourceRepo,
		"--mount", "/acme/payment/imported/bounded",
		"--slice", "acme/payment",
		"--mode", "deep",
		"--max-commits", "2",
	)
	if !strings.Contains(stdout, "imported 2 commit(s)") {
		t.Fatalf("unexpected resume stdout:\n%s", stdout)
	}
	if !strings.Contains(stderr, "skipped") {
		t.Fatalf("resume stderr missing skipped progress:\n%s", stderr)
	}
}

type testServer struct {
	addr        string
	httpAddr    string
	gitAddr     string
	ctx         context.Context
	cancel      context.CancelFunc
	errCh       chan error
	databaseURL string
	schema      string
	objectRoot  string
}

func startTestServer(t *testing.T) *testServer {
	t.Helper()
	databaseURL := os.Getenv("GITSLICE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set GITSLICE_TEST_DATABASE_URL to run the real Postgres functional smoke test")
	}
	schema := "gitslice_test_" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "_")) + "_" + time.Now().Format("150405000000")
	createSchema(t, databaseURL, schema)
	ts := &testServer{
		addr:        freeAddr(t),
		httpAddr:    freeAddr(t),
		gitAddr:     freeAddr(t),
		databaseURL: databaseURL,
		schema:      schema,
		objectRoot:  t.TempDir(),
	}
	ts.start(t, true)
	t.Cleanup(func() {
		ts.stop(t)
		dropSchema(t, databaseURL, schema)
	})
	return ts
}

func (ts *testServer) start(t *testing.T, migrate bool) {
	t.Helper()
	ts.ctx, ts.cancel = context.WithCancel(context.Background())
	ts.errCh = make(chan error, 1)
	go func() {
		ts.errCh <- server.Run(ts.ctx, server.Config{
			GRPCAddr:          ts.addr,
			HTTPAddr:          ts.httpAddr,
			HTTPAllowedOrigin: "http://web.test",
			GitHTTPAddr:       ts.gitAddr,
			GitCacheRoot:      filepath.Join(ts.objectRoot, "git-cache"),
			DatabaseURL:       databaseURLWithSearchPath(t, ts.databaseURL, ts.schema),
			ObjectStoreRoot:   ts.objectRoot,
			RunMigrations:     migrate,
		})
	}()
	waitForHealth(t, ts.addr)
	waitForHTTPGateway(t, ts.httpAddr)
	waitForGitHTTP(t, ts.gitAddr)
}

func (ts *testServer) stop(t *testing.T) {
	t.Helper()
	if ts.cancel == nil {
		return
	}
	ts.cancel()
	err := <-ts.errCh
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("server exited with error: %v", err)
	}
	ts.cancel = nil
}

func (ts *testServer) restart(t *testing.T) {
	t.Helper()
	ts.stop(t)
	ts.start(t, false)
}

func runCLI(t *testing.T, home, workspace string, args ...string) string {
	t.Helper()
	stdout, _ := runCLIStreams(t, home, workspace, args...)
	return stdout
}

func runCLIStreams(t *testing.T, home, workspace string, args ...string) (string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	r := cli.Runner{Home: home, Dir: workspace, Stdout: &stdout, Stderr: &stderr}
	if err := r.Run(context.Background(), args); err != nil {
		t.Fatalf("gs %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String(), stderr.String()
}

func runCLIFails(t *testing.T, home, workspace string, args ...string) (string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	r := cli.Runner{Home: home, Dir: workspace, Stdout: &stdout, Stderr: &stderr}
	if err := r.Run(context.Background(), args); err == nil {
		t.Fatalf("gs %s unexpectedly succeeded\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), stdout.String(), stderr.String())
	} else {
		fmt.Fprintln(&stderr, err)
	}
	return stdout.String(), stderr.String()
}

func runCLIResult(home, workspace string, args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	r := cli.Runner{Home: home, Dir: workspace, Stdout: &stdout, Stderr: &stderr}
	if err := r.Run(context.Background(), args); err != nil {
		return stdout.String(), fmt.Errorf("gs %s failed: %w\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String(), nil
}

type testCoreClients struct {
	repository corev1.RepositoryServiceClient
	blob       corev1.BlobServiceClient
	changeset  corev1.ChangesetServiceClient
}

func newTestCoreClients(conn *grpc.ClientConn) testCoreClients {
	return testCoreClients{
		repository: corev1.NewRepositoryServiceClient(conn),
		blob:       corev1.NewBlobServiceClient(conn),
		changeset:  corev1.NewChangesetServiceClient(conn),
	}
}

func dialTestGRPC(t *testing.T, addr string) *grpc.ClientConn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := grpc.DialContext(ctx, addr, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func loginViaGRPC(t *testing.T, addr, devUser string) string {
	t.Helper()
	conn := dialTestGRPC(t, addr)
	defer conn.Close()
	login, err := corev1.NewFakeAccountServiceClient(conn).Login(context.Background(), &corev1.LoginRequest{DevUser: devUser})
	if err != nil {
		t.Fatal(err)
	}
	if login.Token == "" {
		t.Fatalf("empty token from login: %#v", login)
	}
	return login.Token
}

func grpcAuthContext(token string) context.Context {
	return metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+token)
}

func createDirectPatchset(t *testing.T, ctx context.Context, clients testCoreClients, path, content, title string) (string, string) {
	t.Helper()
	ref, err := clients.repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	upload, err := clients.blob.UploadBlob(ctx, &corev1.UploadBlobRequest{Data: []byte(content)})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := clients.changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
		TargetRef:      postgres.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          title,
	})
	if err != nil {
		t.Fatal(err)
	}
	patchset, err := clients.changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		FileEdits: []*corev1.FileEdit{{
			Op:          "add",
			Path:        path,
			BlobId:      upload.BlobId,
			ContentHash: upload.ContentHash,
			Mode:        0o644,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return cs.Id, patchset.Id
}

func waitForSubmittedChangeset(t *testing.T, ctx context.Context, client corev1.ChangesetServiceClient, changesetID string) *corev1.Changeset {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var last *corev1.Changeset
	for time.Now().Before(deadline) {
		cs, err := client.GetChangeset(ctx, &corev1.GetChangesetRequest{ChangesetId: changesetID})
		if err != nil {
			t.Fatal(err)
		}
		last = cs
		if cs.Status == "submitted" && cs.CommitId != "" {
			return cs
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("changeset %s did not reach submitted status, last=%#v", changesetID, last)
	return nil
}

func writeWorkspaceFile(t *testing.T, workspace, rel, content string) {
	t.Helper()
	path := filepath.Join(workspace, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func copyWorkspaceFile(t *testing.T, fromWorkspace, toWorkspace, rel string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fromWorkspace, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(toWorkspace, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func createImportGitRepo(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.name", "Import Tester")
	runGit(t, repo, "config", "user.email", "importer@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "first commit")
	if err := os.MkdirAll(filepath.Join(repo, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "lib", "code.go"), []byte("package lib\nconst Value = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "lib/code.go")
	runGit(t, repo, "commit", "-m", "second commit")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "third commit")
	return repo
}

func cloneSlice(t *testing.T, ts *testServer, token string) string {
	t.Helper()
	cloneDir := filepath.Join(t.TempDir(), "payment")
	gitURL := "http://" + ts.gitAddr + "/git/acme/payment.git"
	runGit(t, "", "-c", "http.extraHeader=Authorization: Bearer "+token, "clone", gitURL, cloneDir)
	return cloneDir
}

func assertProjectedFile(t *testing.T, cloneDir, rel, want string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(cloneDir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("unexpected contents for %s:\nwant:\n%s\ngot:\n%s", rel, want, string(got))
	}
}

func assertConflictError(t *testing.T, stderr string) {
	t.Helper()
	if !strings.Contains(stderr, "FailedPrecondition") && !strings.Contains(stderr, "conflict") {
		t.Fatalf("expected conflict error, got stderr:\n%s", stderr)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func hasEntry(entries []*corev1.TreeEntry, name string, kind corev1.EntryKind) bool {
	for _, entry := range entries {
		if entry.Name == name && entry.Kind == kind {
			return true
		}
	}
	return false
}

func readToken(t *testing.T, home string) string {
	t.Helper()
	var cfg struct {
		Token string `json:"token"`
	}
	if err := readJSONFile(filepath.Join(home, ".gitslice", "config.json"), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Token == "" {
		t.Fatal("empty token in CLI config")
	}
	return cfg.Token
}

func readJSONFile(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	stdout, stderr, err := runGitResult(dir, args...)
	if err != nil {
		t.Fatalf("git %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout, stderr)
	}
	return stdout
}

func runGitResult(dir string, args ...string) (string, string, error) {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func httpGatewayPost(t *testing.T, addr, path, token string, body any) map[string]any {
	t.Helper()
	statusCode, _, data := httpGatewayPostRaw(t, addr, path, token, body)
	if statusCode >= 300 {
		t.Fatalf("gateway %s returned %d:\n%s", path, statusCode, string(data))
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode gateway response: %v\n%s", err, string(data))
	}
	return out
}

func httpGatewayPostRaw(t *testing.T, addr, path, token string, body any) (int, http.Header, []byte) {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, "http://"+addr+path, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header.Clone(), data
}

func httpGatewayOptions(t *testing.T, addr, path, origin string) (int, http.Header) {
	t.Helper()
	req, err := http.NewRequest(http.MethodOptions, "http://"+addr+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", origin)
	req.Header.Set("Access-Control-Request-Method", "POST")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, resp.Header.Clone()
}

func gitHTTPRaw(t *testing.T, addr, path, authorization string) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://"+addr+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header.Clone(), data
}

func waitForGatewayChangesetStatus(t *testing.T, addr, token, changesetID, want string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var last map[string]any
	for time.Now().Before(deadline) {
		last = httpGatewayPost(t, addr, "/gitslice.core.v1.ChangesetService/GetChangeset", token, map[string]any{
			"changesetId": changesetID,
		})
		if status, _ := last["status"].(string); status == want {
			return last
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("changeset %s did not reach %s status, last=%#v", changesetID, want, last)
	return nil
}

func createSchema(t *testing.T, databaseURL, schema string) {
	t.Helper()
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`create schema ` + pqIdentifier(schema)); err != nil {
		t.Fatal(err)
	}
}

func dropSchema(t *testing.T, databaseURL, schema string) {
	t.Helper()
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Logf("open db for cleanup: %v", err)
		return
	}
	defer db.Close()
	if _, err := db.Exec(`drop schema if exists ` + pqIdentifier(schema) + ` cascade`); err != nil {
		t.Logf("drop schema %s: %v", schema, err)
	}
}

func pqIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func databaseURLWithSearchPath(t *testing.T, databaseURL, schema string) string {
	t.Helper()
	parsed, err := url.Parse(databaseURL)
	if err != nil || parsed.Scheme == "" {
		t.Fatalf("GITSLICE_TEST_DATABASE_URL must be a URL connection string for functional tests")
	}
	q := parsed.Query()
	q.Set("search_path", schema)
	parsed.RawQuery = q.Encode()
	return parsed.String()
}

func freeAddr(t *testing.T) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()
	return lis.Addr().String()
}

func waitForHealth(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		conn, err := grpc.DialContext(ctx, addr, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
		if err == nil {
			client := healthv1.NewHealthClient(conn)
			_, err = client.Check(ctx, &healthv1.HealthCheckRequest{})
			_ = conn.Close()
		}
		cancel()
		if err == nil {
			return
		}
		lastErr = err
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal(fmt.Errorf("server health check did not pass: %w", lastErr))
}

func waitForHTTPGateway(t *testing.T, addr string) {
	t.Helper()
	waitForHTTPServer(t, addr, "server HTTP gateway")
}

func waitForGitHTTP(t *testing.T, addr string) {
	t.Helper()
	waitForHTTPServer(t, addr, "server Git HTTP")
}

func waitForHTTPServer(t *testing.T, addr, name string) {
	t.Helper()
	client := http.Client{Timeout: time.Second}
	deadline := time.Now().Add(10 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := client.Get("http://" + addr + "/")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			return
		}
		lastErr = err
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal(fmt.Errorf("%s did not start: %w", name, lastErr))
}
