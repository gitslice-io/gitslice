package rpc_test

import (
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/postgres"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

func TestRPCSliceServiceCustomSliceDefinitions(t *testing.T) {
	ts := startRPCServer(t)
	token := ts.loginViaGRPC(t, "alice")
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
	token := ts.loginViaGRPC(t, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	slices := corev1.NewSliceServiceClient(conn)
	workspace := corev1.NewWorkspaceServiceClient(conn)
	repository := corev1.NewRepositoryServiceClient(conn)

	payment := resolveTestSlice(t, ctx, slices, "payment")
	backend := resolveTestSlice(t, ctx, slices, "backend")
	home := resolveTestSlice(t, ctx, slices, "home")
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
	assertPathCoverage(t, validation.Coverage, "/acme/backend/rpc_backend.go", home.Id, backend.Id)
	assertPathCoverage(t, validation.Coverage, "/acme/payment/shared/rpc_shared.go", home.Id, payment.Id, backend.Id)
	assertPathCoverage(t, validation.Coverage, "/acme/payment/shared/deep/rpc_shared_deep.go", home.Id, payment.Id, backend.Id)
	wantHash := storage.SubmitRequirementsHash(backend.Definition.IncludedPaths, backend.Definition.RequiredApprovals, backend.Definition.RequiredChecks)
	if validation.SubmitRequirements == nil || validation.SubmitRequirements.SourceSliceDefinitionHash != wantHash {
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
	token := ts.loginViaGRPC(t, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	clients := newTestCoreClients(conn)
	slices := corev1.NewSliceServiceClient(conn)

	payment := resolveTestSlice(t, ctx, slices, "payment")
	backend := resolveTestSlice(t, ctx, slices, "backend")
	home := resolveTestSlice(t, ctx, slices, "home")
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
	assertPathCoverage(t, patchset.Coverage, filePath, home.Id, payment.Id, backend.Id)
	wantHash := storage.SubmitRequirementsHash(backend.Definition.IncludedPaths, backend.Definition.RequiredApprovals, backend.Definition.RequiredChecks)
	if patchset.SubmitRequirements == nil || patchset.SubmitRequirements.SourceSliceDefinitionHash != wantHash {
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
	token := ts.loginViaGRPC(t, "alice")
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
	token := ts.loginViaGRPC(t, "alice")
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

	token, _, _ := ts.provisionAccount(t, "history-home", "history-home")
	ctx := grpcAuthContext(token)
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
	token := ts.loginViaGRPC(t, "alice")
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

func TestRPCImportGitRepositoryCustomSliceMount(t *testing.T) {
	ts := startRPCServer(t)
	token := ts.loginViaGRPC(t, "alice")
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
