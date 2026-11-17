package rpc_test

import (
	"context"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
)

func TestRPCScopedAndChildAPIKeysWithPostgres(t *testing.T) {
	ts := startRPCServer(t)
	ownerToken := ts.loginViaGRPC(t, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()

	ownerCtx := grpcAuthContext(ownerToken)
	auth := corev1.NewAuthServiceClient(conn)
	slices := corev1.NewSliceServiceClient(conn)
	clients := newTestCoreClients(conn)

	// Create /acme/publicdocs via acme/home, then make acme/publicdocs public and seed acme/payment.
	headRef, err := clients.repository.GetRef(ownerCtx, &corev1.GetRefRequest{RefName: "refs/global/main"})
	if err != nil {
		t.Fatal(err)
	}
	csPub, err := clients.changeset.CreateChangeset(ownerCtx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "home"},
		TargetRef:      "refs/global/main",
		BaseCommitId:   headRef.CommitId,
		Title:          "seed publicdocs",
	})
	if err != nil {
		t.Fatal(err)
	}
	psPub, err := clients.changeset.UpdateChangeset(ownerCtx, &corev1.UpdateChangesetRequest{
		ChangesetId:  csPub.Id,
		BaseCommitId: headRef.CommitId,
		FileEdits:    []*corev1.FileEdit{{Op: "mkdir", Path: "/acme/publicdocs"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := clients.changeset.SubmitChangeset(ownerCtx, &corev1.SubmitChangesetRequest{
		ChangesetId:               csPub.Id,
		ExpectedCurrentPatchsetId: psPub.Id,
	}); err != nil {
		t.Fatal(err)
	}
	waitForSubmittedChangeset(t, ownerCtx, clients.changeset, csPub.Id)

	if _, err := slices.CreateSlice(ownerCtx, &corev1.CreateSliceRequest{
		Ref:           &corev1.SliceRef{Account: "acme", Slice: "publicdocs"},
		IncludedPaths: []string{"/acme/publicdocs"},
		Visibility:    "public",
	}); err != nil {
		t.Fatalf("CreateSlice publicdocs: %v", err)
	}
	csID, psID := createDirectPatchset(t, ownerCtx, clients, "/acme/payment/scoped.txt", "scoped ok\n", "seed scoped file")
	if _, err := clients.changeset.SubmitChangeset(ownerCtx, &corev1.SubmitChangesetRequest{
		ChangesetId:               csID,
		ExpectedCurrentPatchsetId: psID,
	}); err != nil {
		t.Fatal(err)
	}
	waitForSubmittedChangeset(t, ownerCtx, clients.changeset, csID)

	// Owner mints a writer-capped key scoped only to acme/payment.
	parentResp, err := auth.CreateApiKey(ownerCtx, &corev1.CreateApiKeyRequest{
		Name:             "rpc-writer",
		Slices:           []string{"acme/payment"},
		MaxRole:          "writer",
		ExpiresInSeconds: 3600,
		Purpose:          "rpc e2e",
	})
	if err != nil {
		t.Fatalf("CreateApiKey: %v", err)
	}
	if !strings.HasPrefix(parentResp.Secret, storage.APIKeyPrefix) ||
		parentResp.Key.MaxRole != "writer" ||
		len(parentResp.Key.Slices) != 1 || parentResp.Key.Slices[0] != "acme/payment" {
		t.Fatalf("parent CreateApiKey = %+v", parentResp)
	}

	parentCtx := grpcAuthContext(parentResp.Secret)
	// Writer-capped key can propose and submit changes on acme/payment, but cannot
	// create organizations or manage members or access acme/backend or acme/publicdocs.
	if _, err := slices.ResolveSlice(parentCtx, &corev1.ResolveSliceRequest{
		Ref: &corev1.SliceRef{Account: "acme", Slice: "payment"},
	}); err != nil {
		t.Fatalf("scoped writer ResolveSlice acme/payment = %v", err)
	}
	if _, err := slices.ResolveSlice(parentCtx, &corev1.ResolveSliceRequest{
		Ref: &corev1.SliceRef{Account: "acme", Slice: "backend"},
	}); err == nil {
		t.Fatal("expected out-of-scope acme/backend to fail")
	} else {
		assertGRPCCode(t, err, codes.PermissionDenied)
	}
	if _, err := slices.ResolveSlice(parentCtx, &corev1.ResolveSliceRequest{
		Ref: &corev1.SliceRef{Account: "acme", Slice: "publicdocs"},
	}); err == nil {
		t.Fatal("expected out-of-scope public slice acme/publicdocs to fail")
	} else {
		assertGRPCCode(t, err, codes.PermissionDenied)
	}
	listedSlices, err := slices.ListSlices(parentCtx, &corev1.ListSlicesRequest{Account: "acme"})
	if err != nil {
		t.Fatalf("scoped ListSlices: %v", err)
	}
	if len(listedSlices.Slices) != 1 || listedSlices.Slices[0].GetRef().GetSlice() != "payment" {
		t.Fatalf("scoped ListSlices = %+v, want only payment", listedSlices.Slices)
	}
	if _, err := auth.CreateOrganization(parentCtx, &corev1.CreateOrganizationRequest{Slug: "denied-org"}); err == nil {
		t.Fatal("expected CreateOrganization to fail on restricted key")
	} else {
		assertGRPCCode(t, err, codes.PermissionDenied)
	}

	// Attenuated child key narrows role to reader; widening rejected.
	childResp, err := auth.CreateApiKey(parentCtx, &corev1.CreateApiKeyRequest{
		Name:    "rpc-reader-child",
		MaxRole: "reader",
	})
	if err != nil {
		t.Fatalf("child CreateApiKey: %v", err)
	}
	if childResp.Key.ParentKeyId != parentResp.Key.Id || childResp.Key.MaxRole != "reader" ||
		len(childResp.Key.Slices) != 1 || childResp.Key.Slices[0] != "acme/payment" {
		t.Fatalf("child key = %+v", childResp.Key)
	}
	if _, err := auth.CreateApiKey(parentCtx, &corev1.CreateApiKeyRequest{
		Slices: []string{"acme/backend"},
	}); err == nil {
		t.Fatal("expected widening child slice scope to fail")
	} else {
		assertGRPCCode(t, err, codes.PermissionDenied)
	}

	childCtx := grpcAuthContext(childResp.Secret)
	if _, err := clients.changeset.CreateChangeset(childCtx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
	}); err == nil {
		t.Fatal("expected reader child key CreateChangeset to fail")
	} else {
		assertGRPCCode(t, err, codes.PermissionDenied)
	}

	// Revoking parent cascades in Postgres and invalidates child immediately.
	if _, err := auth.RevokeApiKey(ownerCtx, &corev1.RevokeApiKeyRequest{KeyId: parentResp.Key.Id}); err != nil {
		t.Fatalf("RevokeApiKey: %v", err)
	}
	for name, kctx := range map[string]context.Context{"parent": parentCtx, "child": childCtx} {
		if _, err := auth.GetAuthStatus(kctx, &corev1.GetAuthStatusRequest{}); err == nil {
			t.Fatalf("%s key still works after parent revoke", name)
		} else {
			assertGRPCCode(t, err, codes.Unauthenticated)
		}
	}
}
