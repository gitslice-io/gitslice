package rpc_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gitslice-io/gitslice/internal/objectid"
	"github.com/gitslice-io/gitslice/internal/postgres"
	"github.com/gitslice-io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

func TestRPCSliceServiceCustomSliceDefinitions(t *testing.T) {
	ts := startRPCServer(t)
	token := loginViaGRPC(t, ts.addr, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	slices := corev1.NewSliceServiceClient(conn)

	backend, err := slices.ResolveSlice(ctx, &corev1.ResolveSliceRequest{
		Ref: &corev1.SliceRef{Account: "acme", Slice: "backend"},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertSliceRef(t, backend, "acme", "backend")
	assertStringSet(t, backend.Definition.IncludedPaths, "/acme/backend", "/acme/payment/shared")

	byID, err := slices.GetSlice(ctx, &corev1.GetSliceRequest{SliceId: backend.Id})
	if err != nil {
		t.Fatal(err)
	}
	assertSliceRef(t, byID, "acme", "backend")
	assertStringSet(t, byID.Definition.IncludedPaths, "/acme/backend", "/acme/payment/shared")

	listed, err := slices.ListSlices(ctx, &corev1.ListSlicesRequest{Account: "acme", PageSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*corev1.Slice{}
	for _, slice := range listed.Slices {
		if slice != nil && slice.Ref != nil {
			got[slice.Ref.Slice] = slice
		}
	}
	if got["payment"] == nil || got["backend"] == nil {
		t.Fatalf("expected payment and backend slices in list response: %#v", listed.Slices)
	}
	assertStringSet(t, got["backend"].Definition.IncludedPaths, "/acme/backend", "/acme/payment/shared")
}

func TestRPCWorkspaceValidationUsesAllCustomSliceIncludedPaths(t *testing.T) {
	ts := startRPCServer(t)
	token := loginViaGRPC(t, ts.addr, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	slices := corev1.NewSliceServiceClient(conn)
	workspace := corev1.NewWorkspaceServiceClient(conn)
	repository := corev1.NewRepositoryServiceClient(conn)

	payment := resolveTestSlice(t, ctx, slices, "payment")
	backend := resolveTestSlice(t, ctx, slices, "backend")
	ref, err := repository.GetRef(ctx, &corev1.GetRefRequest{RefName: postgres.DefaultTargetRef})
	if err != nil {
		t.Fatal(err)
	}

	state, err := workspace.GetWorkspaceState(ctx, &corev1.GetWorkspaceStateRequest{
		Workspace: &corev1.WorkspaceRef{Id: "acme/backend"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.BaseCommitId == "" || state.Slice == nil || state.Slice.SliceId != backend.Id {
		t.Fatalf("unexpected backend workspace state: %#v", state)
	}
	assertStringSet(t, state.HydratedPaths, "/acme/backend", "/acme/payment/shared")

	validation, err := workspace.ValidateWorkspaceDiff(ctx, &corev1.ValidateWorkspaceDiffRequest{
		Workspace:    &corev1.WorkspaceRef{Id: "acme/backend"},
		BaseCommitId: ref.CommitId,
		FileEdits: []*corev1.FileEdit{
			{Op: "upsert", Path: "/acme/backend/rpc_backend.go"},
			{Op: "upsert", Path: "/acme/payment/shared/rpc_shared.go"},
			{Op: "upsert", Path: "/acme/payment/shared/deep/rpc_shared_deep.go"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertStringSet(t, validation.AffectedPaths, "/acme/backend/rpc_backend.go", "/acme/payment/shared/rpc_shared.go", "/acme/payment/shared/deep/rpc_shared_deep.go")
	assertPathCoverage(t, validation.Coverage, "/acme/backend/rpc_backend.go", backend.Id)
	assertPathCoverage(t, validation.Coverage, "/acme/payment/shared/rpc_shared.go", payment.Id, backend.Id)
	assertPathCoverage(t, validation.Coverage, "/acme/payment/shared/deep/rpc_shared_deep.go", payment.Id, backend.Id)
	if validation.SubmitRequirements == nil || validation.SubmitRequirements.SourceSliceDefinitionHash != backend.DefinitionHash {
		t.Fatalf("unexpected submit requirements: %#v", validation.SubmitRequirements)
	}

	_, err = workspace.ValidateWorkspaceDiff(ctx, &corev1.ValidateWorkspaceDiffRequest{
		Workspace:    &corev1.WorkspaceRef{Id: "acme/backend"},
		BaseCommitId: ref.CommitId,
		FileEdits:    []*corev1.FileEdit{{Op: "upsert", Path: "/acme/payment/private.go"}},
	})
	if grpcstatus.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected private payment path to fail with FailedPrecondition, got %v", err)
	}
}

func TestRPCChangesetCanWriteCustomSliceSecondIncludedPath(t *testing.T) {
	ts := startRPCServer(t)
	token := loginViaGRPC(t, ts.addr, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	clients := newTestCoreClients(conn)
	slices := corev1.NewSliceServiceClient(conn)

	payment := resolveTestSlice(t, ctx, slices, "payment")
	backend := resolveTestSlice(t, ctx, slices, "backend")
	ref, err := clients.repository.GetRef(ctx, &corev1.GetRefRequest{RefName: postgres.DefaultTargetRef})
	if err != nil {
		t.Fatal(err)
	}

	const filePath = "/acme/payment/shared/from_backend_rpc.go"
	const content = "package shared\nconst FromBackendRPC = true\n"
	upload, err := clients.blob.UploadBlob(ctx, &corev1.UploadBlobRequest{Data: []byte(content), Slice: testBackendSliceRef()})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := clients.changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "backend"},
		TargetRef:      postgres.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "write custom slice shared path",
	})
	if err != nil {
		t.Fatal(err)
	}
	patchset, err := clients.changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        filePath,
			BlobId:      upload.BlobId,
			ContentHash: upload.ContentHash,
			Mode:        0o100644,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertStringSet(t, patchset.ChangedPaths, filePath)
	assertPathCoverage(t, patchset.Coverage, filePath, payment.Id, backend.Id)
	if patchset.SubmitRequirements == nil || patchset.SubmitRequirements.SourceSliceDefinitionHash != backend.DefinitionHash {
		t.Fatalf("unexpected patchset submit requirements: %#v", patchset.SubmitRequirements)
	}

	if _, err := clients.changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{
		ChangesetId:               cs.Id,
		ExpectedCurrentPatchsetId: patchset.Id,
	}); err != nil {
		t.Fatal(err)
	}
	submitted := waitForSubmittedChangeset(t, ctx, clients.changeset, cs.Id)
	read, err := clients.repository.ReadFile(ctx, &corev1.ReadFileRequest{CommitId: submitted.CommitId, Path: filePath})
	if err != nil {
		t.Fatal(err)
	}
	if string(read.Data) != content || read.ContentHash != upload.ContentHash {
		t.Fatalf("unexpected read after backend submit: data=%q hash=%q", string(read.Data), read.ContentHash)
	}
}

func TestRPCListDirectoryCanUseCustomSliceProjection(t *testing.T) {
	ts := startRPCServer(t)
	token := loginViaGRPC(t, ts.addr, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	clients := newTestCoreClients(conn)

	submitRPCFile(t, ctx, clients, "payment", "/acme/payment/private/listing_private.go", "package private\n")
	submitRPCFile(t, ctx, clients, "backend", "/acme/backend/listing_backend.go", "package backend\n")
	submitRPCFile(t, ctx, clients, "backend", "/acme/payment/shared/listing_shared.go", "package shared\n")

	ref, err := clients.repository.GetRef(ctx, &corev1.GetRefRequest{RefName: postgres.DefaultTargetRef})
	if err != nil {
		t.Fatal(err)
	}
	globalPayment, err := clients.repository.ListDirectory(ctx, &corev1.ListDirectoryRequest{
		CommitId: ref.CommitId,
		Path:     "/acme/payment",
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEntryNameSet(t, globalPayment.Entries, "private", "shared")

	backendPayment, err := clients.repository.ListDirectory(ctx, &corev1.ListDirectoryRequest{
		CommitId: ref.CommitId,
		Path:     "/acme/payment",
		Slice:    &corev1.SliceRef{Account: "acme", Slice: "backend"},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEntryNameSet(t, backendPayment.Entries, "shared")

	backendAccount, err := clients.repository.ListDirectory(ctx, &corev1.ListDirectoryRequest{
		CommitId: ref.CommitId,
		Path:     "/acme",
		Slice:    &corev1.SliceRef{Account: "acme", Slice: "backend"},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEntryNameSet(t, backendAccount.Entries, "backend", "payment")
}

func TestRPCListCommitsSupportsPathAndCustomSlice(t *testing.T) {
	ts := startRPCServer(t)
	token := loginViaGRPC(t, ts.addr, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	clients := newTestCoreClients(conn)
	slices := corev1.NewSliceServiceClient(conn)

	privateCommit := submitRPCFile(t, ctx, clients, "payment", "/acme/payment/history/private.go", "package history\nconst Private = true\n")
	sharedCommit := submitRPCFile(t, ctx, clients, "payment", "/acme/payment/history/shared/shared.go", "package shared\nconst Shared = true\n")
	backendCommit := submitRPCFile(t, ctx, clients, "backend", "/acme/backend/history/backend.go", "package backend\nconst Backend = true\n")
	ts.waitForOutboxDrain(t)

	if _, err := slices.CreateSlice(ctx, &corev1.CreateSliceRequest{
		Ref:           &corev1.SliceRef{Account: "acme", Slice: "history"},
		IncludedPaths: []string{"/acme/payment/history/shared", "/acme/backend/history"},
		Visibility:    "private",
	}); err != nil {
		t.Fatal(err)
	}

	pathHistory, err := clients.repository.ListCommits(ctx, &corev1.ListCommitsRequest{
		RefName: postgres.DefaultTargetRef,
		Path:    "/acme/payment/history/shared",
		Limit:   20,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCommitSetIncludes(t, pathHistory.Commits, sharedCommit)
	assertCommitSetExcludes(t, pathHistory.Commits, privateCommit, backendCommit)

	sliceHistory, err := clients.repository.ListCommits(ctx, &corev1.ListCommitsRequest{
		RefName: postgres.DefaultTargetRef,
		Slice:   &corev1.SliceRef{Account: "acme", Slice: "history"},
		Limit:   20,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCommitSetIncludes(t, sliceHistory.Commits, sharedCommit, backendCommit)
	assertCommitSetExcludes(t, sliceHistory.Commits, privateCommit)

	allFirstPage, err := clients.repository.ListCommits(ctx, &corev1.ListCommitsRequest{
		RefName: postgres.DefaultTargetRef,
		Limit:   1,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCommitIDs(t, allFirstPage.Commits, backendCommit)
	if allFirstPage.NextPageToken == "" {
		t.Fatalf("unfiltered first page missing next token: %#v", allFirstPage)
	}
	allSecondPage, err := clients.repository.ListCommits(ctx, &corev1.ListCommitsRequest{
		RefName:   postgres.DefaultTargetRef,
		Limit:     1,
		PageToken: allFirstPage.NextPageToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCommitIDs(t, allSecondPage.Commits, sharedCommit)
	if allSecondPage.NextPageToken == "" {
		t.Fatalf("unfiltered second page missing next token: %#v", allSecondPage)
	}

	sliceFirstPage, err := clients.repository.ListCommits(ctx, &corev1.ListCommitsRequest{
		RefName: postgres.DefaultTargetRef,
		Slice:   &corev1.SliceRef{Account: "acme", Slice: "history"},
		Limit:   1,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCommitIDs(t, sliceFirstPage.Commits, backendCommit)
	if sliceFirstPage.NextPageToken == "" {
		t.Fatalf("slice first page missing next token: %#v", sliceFirstPage)
	}
	sliceSecondPage, err := clients.repository.ListCommits(ctx, &corev1.ListCommitsRequest{
		RefName:   postgres.DefaultTargetRef,
		Slice:     &corev1.SliceRef{Account: "acme", Slice: "history"},
		Limit:     1,
		PageToken: sliceFirstPage.NextPageToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCommitIDs(t, sliceSecondPage.Commits, sharedCommit)
	if sliceSecondPage.NextPageToken != "" {
		t.Fatalf("slice second page next token = %q, want empty", sliceSecondPage.NextPageToken)
	}

	intersected, err := clients.repository.ListCommits(ctx, &corev1.ListCommitsRequest{
		RefName: postgres.DefaultTargetRef,
		Path:    "/acme/payment/history/private.go",
		Slice:   &corev1.SliceRef{Account: "acme", Slice: "history"},
		Limit:   20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(intersected.Commits) != 0 {
		t.Fatalf("path outside slice returned commits: %#v", commitIDs(intersected.Commits))
	}

	shortShared := strings.TrimPrefix(sharedCommit, "sha256:")
	if len(shortShared) < 12 {
		t.Fatalf("shared commit id too short for prefix test: %s", sharedCommit)
	}
	resolved, err := clients.repository.ResolveCommit(ctx, &corev1.ResolveCommitRequest{
		CommitId: shortShared[:12],
		RefName:  postgres.DefaultTargetRef,
		Path:     "/acme/payment/history/shared",
		Slice:    &corev1.SliceRef{Account: "acme", Slice: "history"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Commit.GetId() != sharedCommit || resolved.MatchedPrefix != "sha256:"+shortShared[:12] {
		t.Fatalf("resolved commit = (%s, %s), want (%s, %s)", resolved.Commit.GetId(), resolved.MatchedPrefix, sharedCommit, "sha256:"+shortShared[:12])
	}
	_, err = clients.repository.ResolveCommit(ctx, &corev1.ResolveCommitRequest{
		CommitId: shortShared[:7],
		RefName:  postgres.DefaultTargetRef,
	})
	if grpcstatus.Code(err) != codes.InvalidArgument {
		t.Fatalf("short prefix error = %v, want InvalidArgument", err)
	}
}

func TestRPCCustomSlicePublishIsConsistentWhenHomeObserves(t *testing.T) {
	ts := startRPCServer(t)
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()

	signup, err := corev1.NewFakeAccountServiceClient(conn).ApproveSignup(context.Background(), &corev1.ApproveSignupRequest{
		Username:    "history-home",
		CallbackUrl: "http://127.0.0.1/callback",
		State:       "state",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := grpcAuthContext(signup.Token)
	clients := newTestCoreClients(conn)
	slices := corev1.NewSliceServiceClient(conn)

	submitRPCFileInSlice(t, ctx, clients, "history-home", "home", "/history-home/project/existing.txt", "seed\n")
	homeBefore, err := clients.repository.GetRef(ctx, &corev1.GetRefRequest{RefName: postgres.DefaultTargetRef})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := slices.CreateSlice(ctx, &corev1.CreateSliceRequest{
		Ref:           &corev1.SliceRef{Account: "history-home", Slice: "project"},
		IncludedPaths: []string{"/history-home/project"},
		Visibility:    "private",
	}); err != nil {
		t.Fatal(err)
	}

	customCommit := submitRPCFileInSlice(t, ctx, clients, "history-home", "project", "/history-home/project/value.txt", "custom v1\n")
	customList, err := clients.repository.ListDirectory(ctx, &corev1.ListDirectoryRequest{
		CommitId: customCommit,
		Path:     "/history-home/project",
		Slice:    &corev1.SliceRef{Account: "history-home", Slice: "project"},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEntryNameSet(t, customList.Entries, "existing.txt", "value.txt")

	staleHomeList, err := clients.repository.ListDirectory(ctx, &corev1.ListDirectoryRequest{
		CommitId: homeBefore.CommitId,
		Path:     "/history-home/project",
		Slice:    &corev1.SliceRef{Account: "history-home", Slice: "home"},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEntryNameSet(t, staleHomeList.Entries, "existing.txt")

	homeAfter, err := clients.repository.GetRef(ctx, &corev1.GetRefRequest{RefName: postgres.DefaultTargetRef})
	if err != nil {
		t.Fatal(err)
	}
	if homeAfter.CommitId != customCommit {
		t.Fatalf("home observed commit %s, want custom publish commit %s", homeAfter.CommitId, customCommit)
	}
	latestHomeList, err := clients.repository.ListDirectory(ctx, &corev1.ListDirectoryRequest{
		CommitId: homeAfter.CommitId,
		Path:     "/history-home/project",
		Slice:    &corev1.SliceRef{Account: "history-home", Slice: "home"},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEntryNameSet(t, latestHomeList.Entries, "existing.txt", "value.txt")
	read, err := clients.repository.ReadFile(ctx, &corev1.ReadFileRequest{
		CommitId: homeAfter.CommitId,
		Path:     "/history-home/project/value.txt",
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(read.Data) != "custom v1\n" {
		t.Fatalf("home read data = %q, want custom content", string(read.Data))
	}
}

func TestRPCChangesetRejectsCustomSliceOutsidePath(t *testing.T) {
	ts := startRPCServer(t)
	token := loginViaGRPC(t, ts.addr, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	clients := newTestCoreClients(conn)

	ref, err := clients.repository.GetRef(ctx, &corev1.GetRefRequest{RefName: postgres.DefaultTargetRef})
	if err != nil {
		t.Fatal(err)
	}
	upload, err := clients.blob.UploadBlob(ctx, &corev1.UploadBlobRequest{Data: []byte("package payment\n"), Slice: testBackendSliceRef()})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := clients.changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "backend"},
		TargetRef:      postgres.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "reject path outside backend slice",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = clients.changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        "/acme/payment/private_backend_rpc.go",
			BlobId:      upload.BlobId,
			ContentHash: upload.ContentHash,
			Mode:        0o100644,
		}},
	})
	if grpcstatus.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected outside custom slice path to fail with FailedPrecondition, got %v", err)
	}
}

func TestRPCBlobStatusUploadAndHashValidation(t *testing.T) {
	ts := startRPCServer(t)
	token := loginViaGRPC(t, ts.addr, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	blob := corev1.NewBlobServiceClient(conn)

	data := []byte("package blob\nconst RPCBlob = true\n")
	contentHash := objectid.RawContentHash(data)
	_, err := blob.GetBlobStatus(ctx, &corev1.GetBlobStatusRequest{ContentHashes: []string{contentHash}})
	assertGRPCCode(t, err, codes.InvalidArgument)
	_, err = blob.UploadBlob(ctx, &corev1.UploadBlobRequest{ContentHash: contentHash, Data: data})
	assertGRPCCode(t, err, codes.InvalidArgument)

	before, err := blob.GetBlobStatus(ctx, &corev1.GetBlobStatusRequest{ContentHashes: []string{contentHash}, Slice: testPaymentSliceRef()})
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Blobs) != 1 || before.Blobs[0].ContentHash != contentHash || before.Blobs[0].State != "missing" {
		t.Fatalf("unexpected pre-upload blob status: %#v", before.Blobs)
	}

	_, err = blob.UploadBlob(ctx, &corev1.UploadBlobRequest{ContentHash: "sha256:not-the-content", Data: data, Slice: testPaymentSliceRef()})
	if grpcstatus.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected mismatched content hash to fail with InvalidArgument, got %v", err)
	}

	uploaded, err := blob.UploadBlob(ctx, &corev1.UploadBlobRequest{ContentHash: contentHash, Data: data, Slice: testPaymentSliceRef()})
	if err != nil {
		t.Fatal(err)
	}
	if uploaded.ContentHash != contentHash || uploaded.BlobId != objectid.BlobID(data) || uploaded.Size != int64(len(data)) {
		t.Fatalf("unexpected upload response: %#v", uploaded)
	}
	after, err := blob.GetBlobStatus(ctx, &corev1.GetBlobStatusRequest{ContentHashes: []string{contentHash, "sha256:missing"}, Slice: testPaymentSliceRef()})
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Blobs) != 2 {
		t.Fatalf("expected two blob status records, got %#v", after.Blobs)
	}
	if after.Blobs[0].Id != uploaded.BlobId || after.Blobs[0].State != "available" || after.Blobs[0].Size != int64(len(data)) {
		t.Fatalf("unexpected uploaded blob status: %#v", after.Blobs[0])
	}
	if after.Blobs[1].ContentHash != "sha256:missing" || after.Blobs[1].State != "missing" {
		t.Fatalf("unexpected missing blob status: %#v", after.Blobs[1])
	}
}

func TestRPCStreamingBlobUploadReadAndHashMismatch(t *testing.T) {
	ts := startRPCServer(t)
	token := loginViaGRPC(t, ts.addr, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	blob := corev1.NewBlobServiceClient(conn)

	data := bytes.Repeat([]byte("streaming blob payload\n"), 300000)
	contentHash := objectid.RawContentHash(data)
	uploaded := uploadBlobStreamForTest(t, ctx, blob, testPaymentSliceRef(), contentHash, data)
	if uploaded.ContentHash != contentHash || uploaded.BlobId != objectid.BlobID(data) || uploaded.Size != int64(len(data)) {
		t.Fatalf("unexpected streaming upload response: %#v", uploaded)
	}
	read := readBlobStreamForTest(t, ctx, blob, testPaymentSliceRef(), contentHash)
	if !bytes.Equal(read, data) {
		t.Fatalf("streaming read length = %d, want %d", len(read), len(data))
	}

	badData := []byte("bad hash streaming upload")
	_, err := uploadBlobStreamForTestErr(ctx, blob, testPaymentSliceRef(), "sha256:not-the-content", badData)
	if grpcstatus.Code(err) != codes.InvalidArgument {
		t.Fatalf("streaming hash mismatch error = %v, want InvalidArgument", err)
	}
	actualBadHash := objectid.RawContentHash(badData)
	status, err := blob.GetBlobStatus(ctx, &corev1.GetBlobStatusRequest{ContentHashes: []string{actualBadHash}, Slice: testPaymentSliceRef()})
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Blobs) != 1 || status.Blobs[0].State != "missing" {
		t.Fatalf("mismatched streaming upload left available metadata: %#v", status.Blobs)
	}
}

func TestRPCImportGitRepositoryCustomSliceMount(t *testing.T) {
	ts := startRPCServer(t)
	token := loginViaGRPC(t, ts.addr, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	repository := corev1.NewRepositoryServiceClient(conn)
	sourceRepo := createImportGitRepo(t)

	_, err := repository.ImportGitRepository(ctx, &corev1.ImportGitRepositoryRequest{
		Source:         sourceRepo,
		MountPath:      "/acme/payment/private_import",
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "backend"},
		Mode:           "shallow",
		TargetRef:      postgres.DefaultTargetRef,
	})
	if grpcstatus.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected import outside backend custom slice to fail with FailedPrecondition, got %v", err)
	}

	imported, err := repository.ImportGitRepository(ctx, &corev1.ImportGitRepositoryRequest{
		Source:         sourceRepo,
		MountPath:      "/acme/payment/shared/imported_rpc",
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "backend"},
		Mode:           "shallow",
		TargetRef:      postgres.DefaultTargetRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	if imported.FinalCommitId == "" || len(imported.Commits) != 1 || imported.Commits[0].Message != "third commit" {
		t.Fatalf("unexpected import response: %#v", imported)
	}
	read, err := repository.ReadFile(ctx, &corev1.ReadFileRequest{
		CommitId: imported.FinalCommitId,
		Path:     "/acme/payment/shared/imported_rpc/README.md",
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(read.Data) != "hello v2\n" {
		t.Fatalf("unexpected imported README contents: %q", string(read.Data))
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

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

func resolveTestSlice(t *testing.T, ctx context.Context, slices corev1.SliceServiceClient, name string) *corev1.Slice {
	t.Helper()
	slice, err := slices.ResolveSlice(ctx, &corev1.ResolveSliceRequest{
		Ref: &corev1.SliceRef{Account: "acme", Slice: name},
	})
	if err != nil {
		t.Fatal(err)
	}
	return slice
}

func submitRPCFile(t *testing.T, ctx context.Context, clients testCoreClients, sliceName, path, content string) string {
	t.Helper()
	return submitRPCFileInSlice(t, ctx, clients, "acme", sliceName, path, content)
}

func submitRPCFileInSlice(t *testing.T, ctx context.Context, clients testCoreClients, account, sliceName, path, content string) string {
	t.Helper()
	ref, err := clients.repository.GetRef(ctx, &corev1.GetRefRequest{RefName: postgres.DefaultTargetRef})
	if err != nil {
		t.Fatal(err)
	}
	upload, err := clients.blob.UploadBlob(ctx, &corev1.UploadBlobRequest{Data: []byte(content), Slice: &corev1.SliceRef{Account: account, Slice: sliceName}})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := clients.changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: account, Slice: sliceName},
		TargetRef:      postgres.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "seed " + path,
	})
	if err != nil {
		t.Fatal(err)
	}
	patchset, err := clients.changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        path,
			BlobId:      upload.BlobId,
			ContentHash: upload.ContentHash,
			Mode:        0o100644,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := clients.changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{
		ChangesetId:               cs.Id,
		ExpectedCurrentPatchsetId: patchset.Id,
	}); err != nil {
		t.Fatal(err)
	}
	return waitForSubmittedChangeset(t, ctx, clients.changeset, cs.Id).CommitId
}

func assertSliceRef(t *testing.T, slice *corev1.Slice, account, name string) {
	t.Helper()
	if slice == nil || slice.Ref == nil || slice.Ref.Account != account || slice.Ref.Slice != name {
		t.Fatalf("unexpected slice ref: %#v", slice)
	}
	if slice.Id == "" || slice.DefinitionHash == "" || slice.Definition == nil {
		t.Fatalf("slice missing identity or definition: %#v", slice)
	}
}

func assertStringSet(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("strings = %#v, want %#v", got, want)
	}
	counts := map[string]int{}
	for _, value := range got {
		counts[value]++
	}
	for _, value := range want {
		counts[value]--
	}
	for value, count := range counts {
		if count != 0 {
			t.Fatalf("strings = %#v, want %#v; mismatch at %q count %d", got, want, value, count)
		}
	}
}

func assertEntryNameSet(t *testing.T, entries []*corev1.TreeEntry, want ...string) {
	t.Helper()
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry != nil {
			names = append(names, entry.Name)
		}
	}
	assertStringSet(t, names, want...)
}

func assertPathCoverage(t *testing.T, coverage []*corev1.PathCoverage, path string, wantIDs ...string) {
	t.Helper()
	for _, entry := range coverage {
		if entry != nil && entry.Path == path {
			assertStringSet(t, entry.CoveringSliceIds, wantIDs...)
			return
		}
	}
	t.Fatalf("coverage for %s not found in %#v", path, coverage)
}

func uploadBlobStreamForTest(t *testing.T, ctx context.Context, blob corev1.BlobServiceClient, slice *corev1.SliceRef, contentHash string, data []byte) *corev1.UploadBlobResponse {
	t.Helper()
	res, err := uploadBlobStreamForTestErr(ctx, blob, slice, contentHash, data)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func uploadBlobStreamForTestErr(ctx context.Context, blob corev1.BlobServiceClient, slice *corev1.SliceRef, contentHash string, data []byte) (*corev1.UploadBlobResponse, error) {
	stream, err := blob.UploadBlobStream(ctx)
	if err != nil {
		return nil, err
	}
	size := int64(len(data))
	if err := stream.Send(&corev1.UploadBlobChunk{Payload: &corev1.UploadBlobChunk_Init{Init: &corev1.UploadBlobInit{
		Slice:       slice,
		ContentHash: contentHash,
		Size:        &size,
	}}}); err != nil {
		return nil, err
	}
	const chunkSize = 1 << 20
	for offset := 0; offset < len(data); offset += chunkSize {
		end := offset + chunkSize
		if end > len(data) {
			end = len(data)
		}
		if err := stream.Send(&corev1.UploadBlobChunk{Payload: &corev1.UploadBlobChunk_Data{Data: data[offset:end]}}); err != nil {
			return nil, err
		}
	}
	return stream.CloseAndRecv()
}

func readBlobStreamForTest(t *testing.T, ctx context.Context, blob corev1.BlobServiceClient, slice *corev1.SliceRef, contentHash string) []byte {
	t.Helper()
	stream, err := blob.ReadBlobStream(ctx, &corev1.ReadBlobStreamRequest{Slice: slice, ContentHash: contentHash})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			return out.Bytes()
		}
		if err != nil {
			t.Fatal(err)
		}
		out.Write(chunk.Data)
	}
}

func assertCommitSetIncludes(t *testing.T, commits []*corev1.Commit, wantIDs ...string) {
	t.Helper()
	got := commitIDSet(commits)
	for _, want := range wantIDs {
		if !got[want] {
			t.Fatalf("commit history missing %s; got %#v", want, commitIDs(commits))
		}
	}
}

func assertCommitSetExcludes(t *testing.T, commits []*corev1.Commit, rejectedIDs ...string) {
	t.Helper()
	got := commitIDSet(commits)
	for _, rejected := range rejectedIDs {
		if got[rejected] {
			t.Fatalf("commit history included %s; got %#v", rejected, commitIDs(commits))
		}
	}
}

func assertCommitIDs(t *testing.T, commits []*corev1.Commit, wantIDs ...string) {
	t.Helper()
	got := commitIDs(commits)
	if len(got) != len(wantIDs) {
		t.Fatalf("commit ids = %#v, want %#v", got, wantIDs)
	}
	for i, want := range wantIDs {
		if got[i] != want {
			t.Fatalf("commit ids = %#v, want %#v", got, wantIDs)
		}
	}
}

func commitIDSet(commits []*corev1.Commit) map[string]bool {
	out := make(map[string]bool, len(commits))
	for _, commit := range commits {
		if commit != nil {
			out[commit.Id] = true
		}
	}
	return out
}

func commitIDs(commits []*corev1.Commit) []string {
	out := make([]string, 0, len(commits))
	for _, commit := range commits {
		if commit != nil {
			out = append(out, commit.Id)
		}
	}
	return out
}
