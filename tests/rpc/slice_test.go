package rpc_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/gitslice-io/gitslice/internal/auth/servicetoken"
	"github.com/gitslice-io/gitslice/internal/objectstore/filesystem"
	"github.com/gitslice-io/gitslice/internal/postgres"
	"github.com/gitslice-io/gitslice/internal/treestore"
	"github.com/gitslice-io/gitslice/proto/core/v1"
	"github.com/gitslice-io/gitslice/server"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthv1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	grpcstatus "google.golang.org/grpc/status"
)

func TestSliceServiceCustomSliceValidation(t *testing.T) {
	ts := startRPCServer(t)
	token := ts.loginViaGRPC(t, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	clients := newTestCoreClients(conn)
	slices := corev1.NewSliceServiceClient(conn)

	submitDirectFile(t, ctx, clients, "/acme/payment/rpc-docs/seed.txt", "rpc docs\n", "rpc docs seed")
	submitDirectFile(t, ctx, clients, "/acme/payment/rpc-docs/archive/seed.txt", "rpc archive\n", "rpc archive seed")
	submitDirectFile(t, ctx, clients, "/acme/payment/rpc-multi-a/seed.txt", "rpc multi a\n", "rpc multi a seed")
	submitDirectFile(t, ctx, clients, "/acme/payment/rpc-multi-b/seed.txt", "rpc multi b\n", "rpc multi b seed")

	_, err := slices.CreateSlice(ctx, &corev1.CreateSliceRequest{
		Ref:           &corev1.SliceRef{Account: "acme", Slice: "rpc-missing"},
		IncludedPaths: []string{"/acme/payment/rpc-missing"},
	})
	if grpcstatus.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "included path does not exist: /acme/payment/rpc-missing") {
		t.Fatalf("missing include CreateSlice err = %v, want FailedPrecondition existence error", err)
	}

	_, err = slices.CreateSlice(ctx, &corev1.CreateSliceRequest{
		Ref:           &corev1.SliceRef{Account: "acme", Slice: "rpc-comma"},
		IncludedPaths: []string{"/acme/payment/rpc-multi-a,/acme/payment/rpc-multi-b"},
	})
	if grpcstatus.Code(err) != codes.InvalidArgument || !strings.Contains(err.Error(), "must not contain commas") {
		t.Fatalf("comma include CreateSlice err = %v, want InvalidArgument comma error", err)
	}

	created, err := slices.CreateSlice(ctx, &corev1.CreateSliceRequest{
		Ref:           &corev1.SliceRef{Account: "acme", Slice: "rpc-docs"},
		IncludedPaths: []string{"/acme/payment/rpc-docs"},
		Visibility:    "private",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Ref.Account != "acme" || created.Ref.Slice != "rpc-docs" || created.Definition.Visibility != "private" {
		t.Fatalf("unexpected created slice: %#v", created)
	}
	if got := created.Definition.IncludedPaths; len(got) != 1 || got[0] != "/acme/payment/rpc-docs" {
		t.Fatalf("created included paths = %#v, want [/acme/payment/rpc-docs]", got)
	}

	multi, err := slices.CreateSlice(ctx, &corev1.CreateSliceRequest{
		Ref:           &corev1.SliceRef{Account: "acme", Slice: "rpc-multi"},
		IncludedPaths: []string{"/acme/payment/rpc-multi-a", "/acme/payment/rpc-multi-b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := multi.Definition.IncludedPaths; len(got) != 2 || got[0] != "/acme/payment/rpc-multi-a" || got[1] != "/acme/payment/rpc-multi-b" {
		t.Fatalf("multi included paths = %#v, want both RPC include paths", got)
	}

	_, err = slices.UpdateSliceDefinition(ctx, &corev1.UpdateSliceDefinitionRequest{
		SliceId:                created.Id,
		ExpectedDefinitionHash: created.DefinitionHash,
		Definition: &corev1.SliceDefinition{
			IncludedPaths: []string{"/acme/payment/rpc-missing-update"},
			Visibility:    "private",
		},
	})
	if grpcstatus.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "included path does not exist: /acme/payment/rpc-missing-update") {
		t.Fatalf("missing include UpdateSliceDefinition err = %v, want FailedPrecondition existence error", err)
	}

	updated, err := slices.UpdateSliceDefinition(ctx, &corev1.UpdateSliceDefinitionRequest{
		SliceId:                created.Id,
		ExpectedDefinitionHash: created.DefinitionHash,
		Definition: &corev1.SliceDefinition{
			IncludedPaths: []string{"/acme/payment/rpc-docs", "/acme/payment/rpc-docs/archive"},
			Visibility:    "public",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Visibility != "public" || updated.Version != created.Definition.Version+1 {
		t.Fatalf("unexpected updated definition: %#v", updated)
	}
	if got := updated.IncludedPaths; len(got) != 2 || got[0] != "/acme/payment/rpc-docs" || got[1] != "/acme/payment/rpc-docs/archive" {
		t.Fatalf("updated included paths = %#v, want docs and archive", got)
	}

	resolved, err := slices.ResolveSlice(ctx, &corev1.ResolveSliceRequest{Ref: &corev1.SliceRef{Account: "acme", Slice: "rpc-docs"}})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Definition.Visibility != "public" || !containsString(resolved.Definition.IncludedPaths, "/acme/payment/rpc-docs/archive") {
		t.Fatalf("resolved slice did not persist updated definition: %#v", resolved)
	}
}

func TestSliceDefinitionVersionHistory(t *testing.T) {
	ts := startRPCServer(t)
	token := ts.loginViaGRPC(t, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	clients := newTestCoreClients(conn)
	slices := corev1.NewSliceServiceClient(conn)

	submitDirectFile(t, ctx, clients, "/acme/payment/rpc-history/seed.txt", "history\n", "history seed")
	submitDirectFile(t, ctx, clients, "/acme/payment/rpc-history/archive/seed.txt", "archive\n", "history archive seed")

	created, err := slices.CreateSlice(ctx, &corev1.CreateSliceRequest{
		Ref:           &corev1.SliceRef{Account: "acme", Slice: "rpc-history"},
		IncludedPaths: []string{"/acme/payment/rpc-history"},
		Visibility:    "private",
	})
	if err != nil {
		t.Fatal(err)
	}

	firstUpdate, err := slices.UpdateSliceDefinition(ctx, &corev1.UpdateSliceDefinitionRequest{
		SliceId:                created.Id,
		ExpectedDefinitionHash: created.DefinitionHash,
		Definition: &corev1.SliceDefinition{
			IncludedPaths:     []string{"/acme/payment/rpc-history", "/acme/payment/rpc-history/archive"},
			Visibility:        "private",
			RequiredApprovals: 1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if firstUpdate.Version != 2 {
		t.Fatalf("first update version = %d, want 2", firstUpdate.Version)
	}
	second, err := slices.ResolveSlice(ctx, &corev1.ResolveSliceRequest{Ref: &corev1.SliceRef{Account: "acme", Slice: "rpc-history"}})
	if err != nil {
		t.Fatal(err)
	}

	secondUpdate, err := slices.UpdateSliceDefinition(ctx, &corev1.UpdateSliceDefinitionRequest{
		SliceId:                created.Id,
		ExpectedDefinitionHash: second.DefinitionHash,
		Definition: &corev1.SliceDefinition{
			IncludedPaths:     second.Definition.IncludedPaths,
			Visibility:        "private",
			RequiredApprovals: 1,
			RequiredChecks:    []string{"unit"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if secondUpdate.Version != 3 {
		t.Fatalf("second update version = %d, want 3", secondUpdate.Version)
	}
	third, err := slices.ResolveSlice(ctx, &corev1.ResolveSliceRequest{Ref: &corev1.SliceRef{Account: "acme", Slice: "rpc-history"}})
	if err != nil {
		t.Fatal(err)
	}

	history, err := slices.ListSliceDefinitionVersions(ctx, &corev1.ListSliceDefinitionVersionsRequest{
		SliceId:  created.Id,
		PageSize: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Versions) != 3 {
		t.Fatalf("history row count = %d, want 3: %#v", len(history.Versions), history.Versions)
	}
	assertSliceDefinitionVersion(t, history.Versions[0], created.Id, 3, third.DefinitionHash, "private", []string{"/acme/payment/rpc-history", "/acme/payment/rpc-history/archive"}, 1, []string{"unit"})
	assertSliceDefinitionVersion(t, history.Versions[1], created.Id, 2, second.DefinitionHash, "private", []string{"/acme/payment/rpc-history", "/acme/payment/rpc-history/archive"}, 1, nil)
	assertSliceDefinitionVersion(t, history.Versions[2], created.Id, 1, created.DefinitionHash, "private", []string{"/acme/payment/rpc-history"}, 0, nil)
	for _, version := range history.Versions {
		if version.CreatedBy != ts.defaultSubjectID {
			t.Fatalf("version %d created_by = %q, want %s", version.Version, version.CreatedBy, ts.defaultSubjectID)
		}
		if version.CreatedAt == "" {
			t.Fatalf("version %d has empty created_at", version.Version)
		}
	}

	outsiderToken, _, _ := ts.provisionAccount(t, "history-outsider", "history-outsider")
	_, err = slices.ListSliceDefinitionVersions(grpcAuthContext(outsiderToken), &corev1.ListSliceDefinitionVersionsRequest{
		SliceId:  created.Id,
		PageSize: 10,
	})
	assertGRPCCode(t, err, codes.PermissionDenied)
}

func TestChangesetServiceListAndDiff(t *testing.T) {
	ts := startRPCServer(t)
	token := ts.loginViaGRPC(t, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	clients := newTestCoreClients(conn)

	ref, err := clients.repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	first, err := clients.blob.UploadBlob(ctx, &corev1.UploadBlobRequest{Data: []byte("package payment\nconst Version = 1\n"), Slice: testPaymentSliceRef()})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := clients.changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
		TargetRef:      postgres.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "rpc diff workflow",
	})
	if err != nil {
		t.Fatal(err)
	}
	firstPatchset, err := clients.changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        "/acme/payment/rpc_diff.go",
			BlobId:      first.BlobId,
			ContentHash: first.ContentHash,
			Mode:        0o644,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := clients.blob.UploadBlob(ctx, &corev1.UploadBlobRequest{Data: []byte("package payment\nconst Version = 2\n"), Slice: testPaymentSliceRef()})
	if err != nil {
		t.Fatal(err)
	}
	secondPatchset, err := clients.changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:               cs.Id,
		ExpectedCurrentPatchsetId: firstPatchset.Id,
		BaseCommitId:              ref.CommitId,
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        "/acme/payment/rpc_diff.go",
			BlobId:      second.BlobId,
			ContentHash: second.ContentHash,
			Mode:        0o644,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	listed, err := clients.changeset.ListChangesets(ctx, &corev1.ListChangesetsRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
		Status:         "draft",
		Limit:          10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !changesetListContains(listed.Changesets, cs.Id) {
		t.Fatalf("ListChangesets did not include %s: %#v", cs.Id, listed.Changesets)
	}

	firstDiff, err := clients.changeset.DiffChangeset(ctx, &corev1.DiffChangesetRequest{
		ChangesetId: cs.Id,
		Patchset:    "1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if firstDiff.ToPatchsetId != firstPatchset.Id || !containsString(firstDiff.ChangedPaths, "/acme/payment/rpc_diff.go") || !strings.Contains(firstDiff.Diff, "+const Version = 1") {
		t.Fatalf("unexpected first patchset diff: %#v", firstDiff)
	}

	between, err := clients.changeset.DiffChangeset(ctx, &corev1.DiffChangesetRequest{
		ChangesetId:  cs.Id,
		FromPatchset: "1",
		ToPatchset:   "2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if between.FromPatchsetId != firstPatchset.Id || between.ToPatchsetId != secondPatchset.Id {
		t.Fatalf("unexpected patchset ids in diff: %#v", between)
	}
	if !strings.Contains(between.Diff, "-const Version = 1") || !strings.Contains(between.Diff, "+const Version = 2") {
		t.Fatalf("patchset-to-patchset diff missing expected content:\n%s", between.Diff)
	}
}

func TestRPCAuthenticationBoundary(t *testing.T) {
	ts := startRPCServer(t)
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()

	// StartCliLogin is a public (unauthenticated) method: the device-login code
	// is the only secret, so it must be reachable without a bearer token.
	start, err := corev1.NewAuthServiceClient(conn).StartCliLogin(context.Background(), &corev1.StartCliLoginRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if start.Code == "" {
		t.Fatalf("unexpected public StartCliLogin response: %#v", start)
	}

	health, err := healthv1.NewHealthClient(conn).Check(context.Background(), &healthv1.HealthCheckRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if health.Status != healthv1.HealthCheckResponse_SERVING {
		t.Fatalf("unexpected health status: %#v", health)
	}

	_, err = corev1.NewAuthServiceClient(conn).GetAuthStatus(context.Background(), &corev1.GetAuthStatusRequest{})
	assertGRPCCode(t, err, codes.Unauthenticated)
	_, err = corev1.NewSliceServiceClient(conn).ListSlices(context.Background(), &corev1.ListSlicesRequest{Account: "acme"})
	assertGRPCCode(t, err, codes.Unauthenticated)
}

func TestRPCAccountMembershipProtectsChangesetWritesAndSliceScopes(t *testing.T) {
	ts := startRPCServer(t)
	aliceToken := ts.loginViaGRPC(t, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()

	aliceCtx := grpcAuthContext(aliceToken)
	clients := newTestCoreClients(conn)
	changesetID, patchsetID := createDirectPatchset(t, aliceCtx, clients, "/acme/payment/authz_member.go", "package payment\nconst Authz = true\n", "membership authz")

	outsiderToken, _, _ := ts.provisionAccount(t, "outsider-authz", "outsider-authz")
	outsiderCtx := grpcAuthContext(outsiderToken)

	slices := corev1.NewSliceServiceClient(conn)
	var err error
	_, err = slices.ResolveSlice(outsiderCtx, &corev1.ResolveSliceRequest{
		Ref: &corev1.SliceRef{Account: "acme", Slice: "payment"},
	})
	assertGRPCCode(t, err, codes.PermissionDenied)
	_, err = slices.ListSlices(outsiderCtx, &corev1.ListSlicesRequest{Account: "acme"})
	assertGRPCCode(t, err, codes.PermissionDenied)

	workspace := corev1.NewWorkspaceServiceClient(conn)
	_, err = workspace.GetWorkspaceState(outsiderCtx, &corev1.GetWorkspaceStateRequest{
		Workspace: &corev1.WorkspaceRef{Id: "acme/payment"},
	})
	assertGRPCCode(t, err, codes.PermissionDenied)
	_, err = workspace.ValidateWorkspaceDiff(outsiderCtx, &corev1.ValidateWorkspaceDiffRequest{
		Workspace: &corev1.WorkspaceRef{Id: "acme/payment"},
		FileEdits: []*corev1.FileEdit{{
			Op:   "upsert",
			Path: "/acme/payment/unauthorized.go",
		}},
	})
	assertGRPCCode(t, err, codes.PermissionDenied)

	_, err = clients.repository.ListDirectory(outsiderCtx, &corev1.ListDirectoryRequest{
		Path:  "/acme/payment",
		Slice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
	})
	assertGRPCCode(t, err, codes.PermissionDenied)
	_, err = clients.repository.ListCommits(outsiderCtx, &corev1.ListCommitsRequest{
		RefName: postgres.DefaultTargetRef,
		Slice:   &corev1.SliceRef{Account: "acme", Slice: "payment"},
		Limit:   10,
	})
	assertGRPCCode(t, err, codes.PermissionDenied)
	_, err = clients.repository.ImportGitRepository(outsiderCtx, &corev1.ImportGitRepositoryRequest{
		Source:         "/unused/source",
		MountPath:      "/acme/payment/imported/unauthorized",
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
		Mode:           "shallow",
	})
	assertGRPCCode(t, err, codes.PermissionDenied)

	_, err = clients.changeset.CreateChangeset(outsiderCtx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
		Title:          "unauthorized create",
	})
	assertGRPCCode(t, err, codes.PermissionDenied)
	_, err = clients.changeset.GetChangeset(outsiderCtx, &corev1.GetChangesetRequest{ChangesetId: changesetID})
	assertGRPCCode(t, err, codes.PermissionDenied)
	_, err = clients.changeset.ListChangesets(outsiderCtx, &corev1.ListChangesetsRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
	})
	assertGRPCCode(t, err, codes.PermissionDenied)
	_, err = clients.changeset.DiffChangeset(outsiderCtx, &corev1.DiffChangesetRequest{ChangesetId: changesetID})
	assertGRPCCode(t, err, codes.PermissionDenied)
	_, err = clients.changeset.UpdateChangeset(outsiderCtx, &corev1.UpdateChangesetRequest{
		ChangesetId: changesetID,
		FileEdits: []*corev1.FileEdit{{
			Op:   "delete",
			Path: "/acme/payment/authz_member.go",
		}},
	})
	assertGRPCCode(t, err, codes.PermissionDenied)
	_, err = clients.changeset.SubmitChangeset(outsiderCtx, &corev1.SubmitChangesetRequest{
		ChangesetId:               changesetID,
		ExpectedCurrentPatchsetId: patchsetID,
	})
	assertGRPCCode(t, err, codes.PermissionDenied)
	_, err = clients.changeset.AbandonChangeset(outsiderCtx, &corev1.AbandonChangesetRequest{ChangesetId: changesetID})
	assertGRPCCode(t, err, codes.PermissionDenied)

	if _, err := clients.changeset.SubmitChangeset(aliceCtx, &corev1.SubmitChangesetRequest{
		ChangesetId:               changesetID,
		ExpectedCurrentPatchsetId: patchsetID,
	}); err != nil {
		t.Fatal(err)
	}
	submitted := waitForSubmittedChangeset(t, aliceCtx, clients.changeset, changesetID)
	if submitted.CommitId == "" {
		t.Fatalf("authorized submit did not publish commit: %#v", submitted)
	}
	ts.waitForOutboxDrain(t)
	if _, err := clients.repository.GetCommit(aliceCtx, &corev1.GetCommitRequest{
		CommitId: submitted.CommitId,
	}); err != nil {
		t.Fatal(err)
	}
	_, err = clients.repository.ListDirectory(outsiderCtx, &corev1.ListDirectoryRequest{
		CommitId: submitted.CommitId,
		Path:     "/acme/payment",
	})
	assertGRPCCode(t, err, codes.PermissionDenied)
	_, err = clients.repository.GetCommit(outsiderCtx, &corev1.GetCommitRequest{
		CommitId: submitted.CommitId,
	})
	assertGRPCCode(t, err, codes.PermissionDenied)
	_, err = clients.repository.ListCommits(outsiderCtx, &corev1.ListCommitsRequest{
		RefName: postgres.DefaultTargetRef,
		Path:    "/acme/payment",
		Limit:   10,
	})
	assertGRPCCode(t, err, codes.PermissionDenied)
	outsiderHistory, err := clients.repository.ListCommits(outsiderCtx, &corev1.ListCommitsRequest{
		RefName: postgres.DefaultTargetRef,
		Limit:   10,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCommitSetExcludes(t, outsiderHistory.Commits, submitted.CommitId)
	_, err = clients.repository.ResolvePath(outsiderCtx, &corev1.ResolvePathRequest{
		CommitId: submitted.CommitId,
		Path:     "/acme/payment/authz_member.go",
	})
	assertGRPCCode(t, err, codes.PermissionDenied)
	_, err = clients.repository.ReadFile(outsiderCtx, &corev1.ReadFileRequest{
		CommitId: submitted.CommitId,
		Path:     "/acme/payment/authz_member.go",
	})
	assertGRPCCode(t, err, codes.PermissionDenied)
	rootList, err := clients.repository.ListDirectory(outsiderCtx, &corev1.ListDirectoryRequest{
		CommitId: submitted.CommitId,
		Path:     "/",
	})
	if err != nil {
		t.Fatal(err)
	}
	if treeEntriesContainName(rootList.Entries, "acme") {
		t.Fatalf("outsider root listing leaked acme account: %#v", rootList.Entries)
	}
}

func TestRPCSliceVisibilityRolesAndBlobScopeAuthorization(t *testing.T) {
	ts := startRPCServer(t)
	aliceToken := ts.loginViaGRPC(t, "alice")
	bobToken := ts.loginViaGRPC(t, "bob")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()

	aliceCtx := grpcAuthContext(aliceToken)
	bobCtx := grpcAuthContext(bobToken)
	clients := newTestCoreClients(conn)
	slices := corev1.NewSliceServiceClient(conn)

	submitDirectFile(t, aliceCtx, clients, "/acme/payment/authz_visibility.go", "package payment\nconst Visibility = true\n", "visibility authz")
	payment, err := slices.ResolveSlice(aliceCtx, &corev1.ResolveSliceRequest{Ref: testPaymentSliceRef()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := slices.UpdateSliceDefinition(aliceCtx, &corev1.UpdateSliceDefinitionRequest{
		SliceId:                payment.Id,
		ExpectedDefinitionHash: payment.DefinitionHash,
		Definition: &corev1.SliceDefinition{
			IncludedPaths: payment.Definition.IncludedPaths,
			Visibility:    "private",
		},
	}); err != nil {
		t.Fatal(err)
	}
	privateSlice, err := slices.ResolveSlice(aliceCtx, &corev1.ResolveSliceRequest{Ref: testPaymentSliceRef()})
	if err != nil {
		t.Fatal(err)
	}

	outsiderToken, _, _ := ts.provisionAccount(t, "visibility-outsider", "visibility-outsider")
	outsiderCtx := grpcAuthContext(outsiderToken)

	_, err = slices.ResolveSlice(outsiderCtx, &corev1.ResolveSliceRequest{Ref: testPaymentSliceRef()})
	assertGRPCCode(t, err, codes.PermissionDenied)
	_, err = clients.repository.ListDirectory(outsiderCtx, &corev1.ListDirectoryRequest{
		Path:  "/acme/payment",
		Slice: testPaymentSliceRef(),
	})
	assertGRPCCode(t, err, codes.PermissionDenied)
	_, err = clients.blob.GetBlobStatus(outsiderCtx, &corev1.GetBlobStatusRequest{
		ContentHashes: []string{"sha256:cross-tenant-probe"},
		Slice:         testPaymentSliceRef(),
	})
	assertGRPCCode(t, err, codes.PermissionDenied)

	_, err = slices.UpdateSliceDefinition(bobCtx, &corev1.UpdateSliceDefinitionRequest{
		SliceId:                privateSlice.Id,
		ExpectedDefinitionHash: privateSlice.DefinitionHash,
		Definition: &corev1.SliceDefinition{
			IncludedPaths: privateSlice.Definition.IncludedPaths,
			Visibility:    "private",
		},
	})
	assertGRPCCode(t, err, codes.PermissionDenied)
	_, err = slices.DeleteSlice(bobCtx, &corev1.DeleteSliceRequest{SliceId: privateSlice.Id})
	assertGRPCCode(t, err, codes.PermissionDenied)

	publicDef, err := slices.UpdateSliceDefinition(aliceCtx, &corev1.UpdateSliceDefinitionRequest{
		SliceId:                privateSlice.Id,
		ExpectedDefinitionHash: privateSlice.DefinitionHash,
		Definition: &corev1.SliceDefinition{
			IncludedPaths: privateSlice.Definition.IncludedPaths,
			Visibility:    "public",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if publicDef.Visibility != "public" {
		t.Fatalf("visibility = %q, want public", publicDef.Visibility)
	}
	resolved, err := slices.ResolveSlice(outsiderCtx, &corev1.ResolveSliceRequest{Ref: testPaymentSliceRef()})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Definition.Visibility != "public" {
		t.Fatalf("outsider resolved visibility = %q, want public", resolved.Definition.Visibility)
	}
	ref, err := clients.repository.GetRef(outsiderCtx, &corev1.GetRefRequest{RefName: postgres.DefaultTargetRef})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := clients.repository.ListDirectory(outsiderCtx, &corev1.ListDirectoryRequest{
		CommitId: ref.CommitId,
		Path:     "/acme/payment",
		Slice:    testPaymentSliceRef(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !treeEntriesContainName(listed.Entries, "authz_visibility.go") {
		t.Fatalf("public slice listing missing authz_visibility.go: %#v", listed.Entries)
	}
}

type testRPCServer struct {
	addr        string
	ctx         context.Context
	cancel      context.CancelFunc
	errCh       chan error
	databaseURL string
	schema      string
	objectRoot  string
	servicePriv string
	servicePub  string

	mu               sync.Mutex
	defaultReady     bool
	defaultToken     string
	defaultSubjectID string
	memberTokens     map[string]string
}

func startRPCServer(t *testing.T) *testRPCServer {
	t.Helper()
	databaseURL := os.Getenv("GITSLICE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set GITSLICE_TEST_DATABASE_URL to run real Postgres RPC e2e tests")
	}
	schema := uniqueSchema("gitslice_rpc_", t)
	createSchema(t, databaseURL, schema)
	servicePriv, servicePub, err := servicetoken.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	ts := &testRPCServer{
		addr:         freeAddr(t),
		databaseURL:  databaseURL,
		schema:       schema,
		objectRoot:   t.TempDir(),
		servicePriv:  servicePriv,
		servicePub:   servicePub,
		memberTokens: map[string]string{},
	}
	ts.start(t)
	t.Cleanup(func() {
		ts.stop(t)
		dropSchema(t, databaseURL, schema)
	})
	return ts
}

func (ts *testRPCServer) start(t *testing.T) {
	t.Helper()
	ts.ctx, ts.cancel = context.WithCancel(context.Background())
	ts.errCh = make(chan error, 1)
	go func() {
		ts.errCh <- server.Run(ts.ctx, server.Config{
			GRPCAddr:        ts.addr,
			DatabaseURL:     databaseURLWithSearchPath(t, ts.databaseURL, ts.schema),
			ObjectStoreRoot: ts.objectRoot,
			ServiceToken: servicetoken.Config{
				PublicKeyPEM: ts.servicePub,
				Issuer:       servicetoken.DefaultIssuer,
			},
			RunMigrations: true,
		})
	}()
	waitForHealth(t, ts.addr)
}

func (ts *testRPCServer) stop(t *testing.T) {
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

func (ts *testRPCServer) waitForOutboxDrain(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := postgres.Open(ctx, databaseURLWithSearchPath(t, ts.databaseURL, ts.schema))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	objectStore, err := filesystem.New(ts.objectRoot)
	if err != nil {
		t.Fatal(err)
	}
	db.SetTreeStore(treestore.New(objectStore))
	if err := db.Changesets().WaitForOutboxDrain(ctx); err != nil {
		t.Fatal(err)
	}
}

type testCoreClients struct {
	repository corev1.RepositoryServiceClient
	blob       corev1.BlobServiceClient
	changeset  corev1.ChangesetServiceClient
	stack      corev1.ChangesetStackServiceClient
}

func newTestCoreClients(conn *grpc.ClientConn) testCoreClients {
	return testCoreClients{
		repository: corev1.NewRepositoryServiceClient(conn),
		blob:       corev1.NewBlobServiceClient(conn),
		changeset:  corev1.NewChangesetServiceClient(conn),
		stack:      corev1.NewChangesetStackServiceClient(conn),
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

func (ts *testRPCServer) loginViaGRPC(t *testing.T, user string) string {
	t.Helper()
	switch user {
	case "alice", "acme":
		token, _ := ts.defaultAcmeCredentials(t)
		return token
	case "bob":
		return ts.acmeMemberToken(t, "bob", "writer")
	default:
		token, _, _ := ts.provisionAccount(t, user, user)
		return token
	}
}

func (ts *testRPCServer) defaultAcmeCredentials(t *testing.T) (string, string) {
	t.Helper()
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.defaultReady {
		return ts.defaultToken, ts.defaultSubjectID
	}
	ts.clearSeedAcme(t)
	token, account, subjectID := ts.provisionAccount(t, "acme", "acme-owner")
	if account != "acme" {
		t.Fatalf("provisioned account = %q, want acme", account)
	}
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	ts.createDefaultAcmeSlices(t, ctx, conn)
	ts.defaultToken = token
	ts.defaultSubjectID = subjectID
	ts.defaultReady = true
	return token, subjectID
}

func (ts *testRPCServer) acmeMemberToken(t *testing.T, username, role string) string {
	t.Helper()
	ts.defaultAcmeCredentials(t)
	ts.mu.Lock()
	if token := ts.memberTokens[username]; token != "" {
		ts.mu.Unlock()
		return token
	}
	ts.mu.Unlock()

	token, _, subjectID := ts.provisionAccount(t, username, username)
	ts.grantAccountRole(t, subjectID, "acme", role)

	ts.mu.Lock()
	ts.memberTokens[username] = token
	ts.mu.Unlock()
	return token
}

func (ts *testRPCServer) mintToken(t *testing.T, subject, email string) string {
	t.Helper()
	token, err := servicetoken.Mint(ts.servicePriv, subject, email, servicetoken.DefaultIssuer, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func (ts *testRPCServer) provisionAccount(t *testing.T, username, label string) (string, string, string) {
	t.Helper()
	subject := "svc_" + testTokenLabel(t, label)
	email := testTokenLabel(t, label) + "@test.local"
	token := ts.mintToken(t, subject, email)
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	chosen, err := corev1.NewAuthServiceClient(conn).ChooseUsername(ctx, &corev1.ChooseUsernameRequest{Username: username})
	if err != nil {
		t.Fatal(err)
	}
	if chosen.Account == "" || chosen.SubjectId == "" {
		t.Fatalf("incomplete ChooseUsername response: %#v", chosen)
	}
	return token, chosen.Account, chosen.SubjectId
}

func (ts *testRPCServer) clearSeedAcme(t *testing.T) {
	t.Helper()
	db, err := sql.Open("pgx", databaseURLWithSearchPath(t, ts.databaseURL, ts.schema))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, stmt := range []string{
		`delete from slice_included_paths where slice_id in (select id from slices where account_id = 'acct_acme')`,
		`delete from slice_definition_versions where slice_id in (select id from slices where account_id = 'acct_acme')`,
		`delete from slices where account_id = 'acct_acme'`,
		`delete from account_memberships where account_id = 'acct_acme'`,
		`delete from current_path_entities where account_id = 'acct_acme'`,
		`delete from commit_entity_changes where account_id = 'acct_acme'`,
		`delete from fs_entities where account_id = 'acct_acme'`,
		`delete from accounts where id = 'acct_acme' and slug = 'acme'`,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			t.Fatalf("clear seeded acme: %v\nsql: %s", err, stmt)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func (ts *testRPCServer) createDefaultAcmeSlices(t *testing.T, ctx context.Context, conn *grpc.ClientConn) {
	t.Helper()
	clients := newTestCoreClients(conn)
	slices := corev1.NewSliceServiceClient(conn)
	ref, err := clients.repository.GetRef(ctx, &corev1.GetRefRequest{RefName: postgres.DefaultTargetRef})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := clients.changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "home"},
		TargetRef:      postgres.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "bootstrap acme test slices",
	})
	if err != nil {
		t.Fatal(err)
	}
	patchset, err := clients.changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		FileEdits: []*corev1.FileEdit{
			{Op: "mkdir", Path: "/acme/payment"},
			{Op: "mkdir", Path: "/acme/payment/shared"},
			{Op: "mkdir", Path: "/acme/backend"},
		},
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
	waitForSubmittedChangeset(t, ctx, clients.changeset, cs.Id)
	if _, err := slices.CreateSlice(ctx, &corev1.CreateSliceRequest{
		Ref:           &corev1.SliceRef{Account: "acme", Slice: "payment"},
		IncludedPaths: []string{"/acme/payment"},
		Visibility:    "private",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := slices.CreateSlice(ctx, &corev1.CreateSliceRequest{
		Ref:           &corev1.SliceRef{Account: "acme", Slice: "backend"},
		IncludedPaths: []string{"/acme/backend", "/acme/payment/shared"},
		Visibility:    "private",
	}); err != nil {
		t.Fatal(err)
	}
}

func (ts *testRPCServer) grantAccountRole(t *testing.T, subjectID, account, role string) {
	t.Helper()
	db, err := sql.Open("pgx", databaseURLWithSearchPath(t, ts.databaseURL, ts.schema))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	res, err := db.Exec(`
		insert into account_memberships(account_id, subject_id, role, created_at)
		select id, $1, $2, now()
		from accounts
		where slug = $3
		on conflict do nothing
	`, subjectID, role, account)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		t.Fatalf("grant %s on %s to %s affected %d rows, err=%v", role, account, subjectID, n, err)
	}
}

func testTokenLabel(t *testing.T, label string) string {
	t.Helper()
	raw := strings.ToLower(t.Name() + "_" + label)
	var b strings.Builder
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

func grpcAuthContext(token string) context.Context {
	return metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+token)
}

func assertGRPCCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if grpcstatus.Code(err) != want {
		t.Fatalf("grpc code = %v, want %v; err=%v", grpcstatus.Code(err), want, err)
	}
}

func assertSliceDefinitionVersion(t *testing.T, got *corev1.SliceDefinitionVersion, sliceID string, version int64, definitionHash, visibility string, includedPaths []string, requiredApprovals int32, requiredChecks []string) {
	t.Helper()
	if got == nil {
		t.Fatalf("nil slice definition version, want version %d", version)
	}
	if got.SliceId != sliceID || got.Version != version || got.DefinitionHash != definitionHash || got.Visibility != visibility || got.RequiredApprovals != requiredApprovals {
		t.Fatalf("unexpected definition version row:\n got: %#v\nwant: slice_id=%s version=%d hash=%s visibility=%s required_approvals=%d", got, sliceID, version, definitionHash, visibility, requiredApprovals)
	}
	if strings.Join(got.IncludedPaths, "\x00") != strings.Join(includedPaths, "\x00") {
		t.Fatalf("version %d included paths = %#v, want %#v", version, got.IncludedPaths, includedPaths)
	}
	if strings.Join(got.RequiredChecks, "\x00") != strings.Join(requiredChecks, "\x00") {
		t.Fatalf("version %d required checks = %#v, want %#v", version, got.RequiredChecks, requiredChecks)
	}
}

func createDirectPatchset(t *testing.T, ctx context.Context, clients testCoreClients, path, content, title string) (string, string) {
	t.Helper()
	ref, err := clients.repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	upload, err := clients.blob.UploadBlob(ctx, &corev1.UploadBlobRequest{Data: []byte(content), Slice: testPaymentSliceRef()})
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

func submitDirectFile(t *testing.T, ctx context.Context, clients testCoreClients, path, content, title string) *corev1.Changeset {
	t.Helper()
	changesetID, patchsetID := createDirectPatchset(t, ctx, clients, path, content, title)
	submitted, err := clients.changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{
		ChangesetId:               changesetID,
		ExpectedCurrentPatchsetId: patchsetID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if submitted.Status != "pending_publish" && submitted.Status != "submitted" {
		t.Fatalf("unexpected submit response: %#v", submitted)
	}
	return waitForSubmittedChangeset(t, ctx, clients.changeset, changesetID)
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

// uniqueSchema builds a Postgres-safe, unique schema name. Postgres truncates
// identifiers to 63 bytes, so the unique token must survive truncation: we trim
// the test-name hint up front to keep the whole name within the limit, leaving
// the token (and its uniqueness) fully intact. The old time-of-day suffix had
// only second resolution and was silently truncated away for long test names,
// causing reruns to collide on a stale schema.
func uniqueSchema(prefix string, t *testing.T) string {
	const maxLen = 63 // Postgres NAMEDATALEN - 1
	token := strconv.FormatInt(time.Now().UnixNano(), 36)
	hint := strings.ToLower(strings.ReplaceAll(t.Name(), "/", "_"))
	budget := maxLen - len(prefix) - len(token) - 1 // room for the "_" separator
	if budget < 0 {
		name := prefix + token
		if len(name) > maxLen {
			name = name[:maxLen]
		}
		return name
	}
	if len(hint) > budget {
		hint = hint[:budget]
	}
	return prefix + hint + "_" + token
}

func createSchema(t *testing.T, databaseURL, schema string) {
	t.Helper()
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Drop any leftover schema from an interrupted run so create is idempotent.
	if _, err := db.Exec(`drop schema if exists ` + pqIdentifier(schema) + ` cascade`); err != nil {
		t.Fatal(err)
	}
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
		t.Fatalf("GITSLICE_TEST_DATABASE_URL must be a URL connection string for RPC e2e tests")
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

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func testPaymentSliceRef() *corev1.SliceRef {
	return &corev1.SliceRef{Account: "acme", Slice: "payment"}
}

func testBackendSliceRef() *corev1.SliceRef {
	return &corev1.SliceRef{Account: "acme", Slice: "backend"}
}

func treeEntriesContainName(entries []*corev1.TreeEntry, want string) bool {
	for _, entry := range entries {
		if entry != nil && entry.Name == want {
			return true
		}
	}
	return false
}

func changesetListContains(values []*corev1.Changeset, want string) bool {
	for _, value := range values {
		if value != nil && value.Id == want {
			return true
		}
	}
	return false
}
