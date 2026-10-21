package rpc_test

import (
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/postgres"
	"gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
)

func TestRPCChangesetTitleAndDescription(t *testing.T) {
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

	// A message sent as the title is split: the subject is the title, the rest
	// the description, whoever sends it.
	cs, err := clients.changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: testPaymentSliceRef(),
		TargetRef:      postgres.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "Add retries to the payment client\n\nRetries transient errors three times, with backoff.\n\n- idempotency keys are reused",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cs.Title != "Add retries to the payment client" {
		t.Fatalf("title = %q, want the subject line", cs.Title)
	}
	if want := "Retries transient errors three times, with backoff.\n\n- idempotency keys are reused"; cs.Description != want {
		t.Fatalf("description = %q, want %q", cs.Description, want)
	}

	// The title and description are edited while the changeset is open.
	edited, err := clients.changeset.UpdateChangesetInfo(ctx, &corev1.UpdateChangesetInfoRequest{
		ChangesetId: cs.Id,
		Description: strPtr("Retries transient errors, with backoff and jitter."),
	})
	if err != nil {
		t.Fatal(err)
	}
	if edited.Title != cs.Title || edited.Description != "Retries transient errors, with backoff and jitter." {
		t.Fatalf("after editing only the description: %q / %q", edited.Title, edited.Description)
	}
	edited, err = clients.changeset.UpdateChangesetInfo(ctx, &corev1.UpdateChangesetInfoRequest{ChangesetId: cs.Id, Title: strPtr("Retry transient payment errors")})
	if err != nil {
		t.Fatal(err)
	}
	if edited.Title != "Retry transient payment errors" || !strings.HasPrefix(edited.Description, "Retries transient errors") {
		t.Fatalf("after editing only the title: %q / %q", edited.Title, edited.Description)
	}
	for _, bad := range []*corev1.UpdateChangesetInfoRequest{
		{ChangesetId: cs.Id},
		{ChangesetId: cs.Id, Title: strPtr("   ")},
	} {
		if _, err := clients.changeset.UpdateChangesetInfo(ctx, bad); err == nil {
			t.Fatalf("UpdateChangesetInfo(%v) was accepted", bad)
		}
	}

	// Landing uses both as the commit's message, a subject and a body.
	upload, err := clients.blob.UploadBlob(ctx, &corev1.UploadBlobRequest{Data: []byte("package payment\n"), Slice: testPaymentSliceRef()})
	if err != nil {
		t.Fatal(err)
	}
	patchset, err := clients.changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		FileEdits:    []*corev1.FileEdit{{Op: "upsert", Path: "/acme/payment/described/retry.go", BlobId: upload.BlobId, ContentHash: upload.ContentHash, Mode: 0o100644}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := clients.changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{ChangesetId: cs.Id, ExpectedCurrentPatchsetId: patchset.Id}); err != nil {
		t.Fatal(err)
	}
	landed := waitForSubmittedChangeset(t, ctx, clients.changeset, cs.Id)
	commit, err := clients.repository.GetCommit(ctx, &corev1.GetCommitRequest{CommitId: landed.CommitId})
	if err != nil {
		t.Fatal(err)
	}
	if want := "Retry transient payment errors\n\nRetries transient errors, with backoff and jitter."; commit.Message != want {
		t.Fatalf("commit message = %q, want %q", commit.Message, want)
	}

	// Once landed, the text is the commit's: it is no longer editable.
	_, err = clients.changeset.UpdateChangesetInfo(ctx, &corev1.UpdateChangesetInfoRequest{ChangesetId: cs.Id, Description: strPtr("rewritten")})
	assertGRPCCode(t, err, codes.FailedPrecondition)
}

func strPtr(s string) *string { return &s }
