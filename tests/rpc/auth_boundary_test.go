package rpc_test

import (
	"context"
	"testing"

	"gitslice.io/gitslice/internal/postgres"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	healthv1 "google.golang.org/grpc/health/grpc_health_v1"
)

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
	// Listing an account's slices needs no sign-in, but the account must exist.
	_, err = corev1.NewSliceServiceClient(conn).ListSlices(context.Background(), &corev1.ListSlicesRequest{Account: "no-such-account"})
	assertGRPCCode(t, err, codes.NotFound)
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
	// An outsider lists only the public slices; acme/payment is private.
	outsiderList, err := slices.ListSlices(outsiderCtx, &corev1.ListSlicesRequest{Account: "acme"})
	if err != nil {
		t.Fatalf("outsider ListSlices: %v", err)
	}
	for _, slice := range outsiderList.Slices {
		if slice.GetDefinition().GetVisibility() != "public" {
			t.Fatalf("outsider ListSlices returned a non-public slice: %#v", slice)
		}
	}

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
	bobToken := ts.loginViaGRPC(t, "bobby")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()

	aliceCtx := grpcAuthContext(aliceToken)
	bobCtx := grpcAuthContext(bobToken)
	clients := newTestCoreClients(conn)
	slices := corev1.NewSliceServiceClient(conn)

	visibilityChangeset := submitDirectFile(t, aliceCtx, clients, "/acme/payment/authz_visibility.go", "package payment\nconst Visibility = true\n", "visibility authz")
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
	anonymousCtx := context.Background()
	anonymousHistory, err := clients.repository.ListCommits(anonymousCtx, &corev1.ListCommitsRequest{
		RefName: postgres.DefaultTargetRef,
		Slice:   testPaymentSliceRef(),
		Limit:   10,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCommitSetIncludes(t, anonymousHistory.Commits, visibilityChangeset.CommitId)
	anonymousChangesets, err := clients.changeset.ListChangesets(anonymousCtx, &corev1.ListChangesetsRequest{
		AuthoringSlice: testPaymentSliceRef(),
		Limit:          10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !changesetListContains(anonymousChangesets.Changesets, visibilityChangeset.Id) {
		t.Fatalf("anonymous public changesets missing %s: %#v", visibilityChangeset.Id, anonymousChangesets.Changesets)
	}
	anonymousChangeset, err := clients.changeset.GetChangeset(anonymousCtx, &corev1.GetChangesetRequest{ChangesetId: visibilityChangeset.Id})
	if err != nil {
		t.Fatal(err)
	}
	if anonymousChangeset.Id != visibilityChangeset.Id {
		t.Fatalf("anonymous GetChangeset id = %q, want %q", anonymousChangeset.Id, visibilityChangeset.Id)
	}
	anonymousDiff, err := clients.changeset.DiffChangeset(anonymousCtx, &corev1.DiffChangesetRequest{ChangesetId: visibilityChangeset.Id})
	if err != nil {
		t.Fatal(err)
	}
	if !containsString(anonymousDiff.ChangedPaths, "/acme/payment/authz_visibility.go") {
		t.Fatalf("anonymous public changeset diff paths = %#v", anonymousDiff.ChangedPaths)
	}
}
