package rpc_test

import (
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
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

func testPaymentSliceRef() *corev1.SliceRef {
	return &corev1.SliceRef{Account: "acme", Slice: "payment"}
}

func testBackendSliceRef() *corev1.SliceRef {
	return &corev1.SliceRef{Account: "acme", Slice: "backend"}
}
