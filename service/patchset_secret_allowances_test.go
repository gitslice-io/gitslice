package service

import (
	"context"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/agenthub"
	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/storage/memory"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
)

func setupSecretAllowanceTestSlice(t *testing.T, checksYAML string) (*memory.Stores, *Handlers, string, *agenthub.DaemonConn) {
	t.Helper()
	mem, handlers := newSliceMemberHandlers(t)
	mem.AddPersonalAccount("user_dana", "dana")
	mem.AddPersonalAccount("user_eve", "eve")
	mem.AddPersonalAccount("user_frank", "frank")

	alice := authctx.WithSubjectID(context.Background(), "user_alice")
	for _, member := range []struct {
		user string
		role string
	}{
		{user: "bob", role: "contributor"},
		{user: "dana", role: "writer"},
		{user: "eve", role: "reader"},
		{user: "frank", role: "admin"}, // slice-only admin, NOT account admin
	} {
		if _, err := handlers.Slice.SetSliceMember(alice, &corev1.SetSliceMemberRequest{
			Slice:    paymentRef,
			Username: member.user,
			Role:     member.role,
		}); err != nil {
			t.Fatalf("SetSliceMember(%s=%s): %v", member.user, member.role, err)
		}
	}

	baseCommit := commitFileViaAlice(t, alice, handlers, mem, paymentRef, "/acme/payment/.gitslice/checks.yaml", checksYAML)

	if _, err := handlers.Secret.CreateSecret(alice, &corev1.CreateSecretRequest{
		Account: "acme",
		Name:    "API_TOKEN",
		Value:   "super-secret-value",
		Scope:   &corev1.SecretScope{Paths: []string{"/acme"}},
	}); err != nil {
		t.Fatalf("CreateSecret: %v", err)
	}

	paymentSlice, err := mem.Slices.Resolve(context.Background(), paymentRef)
	if err != nil {
		t.Fatalf("Slices.Resolve: %v", err)
	}
	daemon, err := mem.Agents.RegisterDaemon(context.Background(), storage.AgentDaemonInput{
		SubjectID: "user_alice",
		Account:   "acme",
		Name:      "ci-allow-runner",
	})
	if err != nil {
		t.Fatalf("RegisterDaemon: %v", err)
	}
	if err := mem.Agents.ExposeDaemon(context.Background(), daemon.GetId(), paymentSlice.GetId(), "user_alice"); err != nil {
		t.Fatalf("ExposeDaemon: %v", err)
	}
	if _, err := mem.Slices.SetCIDaemons(context.Background(), paymentSlice.GetId(), []string{daemon.GetId()}); err != nil {
		t.Fatalf("SetCIDaemons: %v", err)
	}
	conn := handlers.Agent.hub.RegisterDaemon(daemon.GetId(), "inst-allow-1", 4)
	t.Cleanup(func() { handlers.Agent.hub.UnregisterDaemon(conn) })

	return mem, handlers, baseCommit, conn
}

func assertRunChecksHasSecret(t *testing.T, msg *corev1.RunChecks, wantSecret bool) {
	t.Helper()
	if len(msg.GetChecks()) != 1 {
		t.Fatalf("RunChecks len = %d, want 1: %#v", len(msg.GetChecks()), msg.GetChecks())
	}
	check := msg.GetChecks()[0]
	got := check.GetEnv()["API_TOKEN"]
	hasSecretEnv := false
	for _, k := range check.GetSecretEnv() {
		if strings.EqualFold(k, "API_TOKEN") {
			hasSecretEnv = true
			break
		}
	}
	if wantSecret {
		if got != "super-secret-value" || !hasSecretEnv {
			t.Fatalf("want injected API_TOKEN; env=%v secret_env=%v", check.GetEnv(), check.GetSecretEnv())
		}
		return
	}
	if got != "" || hasSecretEnv {
		t.Fatalf("unexpected secret leaked; env=%v secret_env=%v", check.GetEnv(), check.GetSecretEnv())
	}
}

func TestAllowPatchsetSecretsGrantsSecretsToOnePatchsetOnly(t *testing.T) {
	checksYAML := "version: 1\nchecks:\n  unit:\n    run: echo ok\n"
	mem, handlers, baseCommit, conn := setupSecretAllowanceTestSlice(t, checksYAML)

	bob := authctx.WithSubjectID(context.Background(), "user_bob")   // contributor
	dana := authctx.WithSubjectID(context.Background(), "user_dana") // writer
	eve := authctx.WithSubjectID(context.Background(), "user_eve")   // reader
	alice := authctx.WithSubjectID(context.Background(), "user_alice")

	// Contributor patchset 1 initially runs secretless on the CI daemon.
	cs, ps1 := createDispatchTestPatchset(t, bob, handlers, paymentRef, baseCommit, "/acme/payment/app.go", "package payment\nconst V = 1\n", "contrib ps1")
	msg1 := waitForRunChecksMessage(t, conn)
	assertRunChecksHasSecret(t, msg1, false)
	run1 := msg1.GetChecks()[0].GetRunId()

	// Reader and contributor cannot allow secrets.
	for _, tc := range []struct {
		name string
		ctx  context.Context
	}{
		{name: "reader", ctx: eve},
		{name: "contributor", ctx: bob},
	} {
		_, err := handlers.Check.AllowPatchsetSecrets(tc.ctx, &corev1.AllowPatchsetSecretsRequest{ChangesetId: cs.GetId()})
		wantCode(t, err, codes.PermissionDenied, tc.name+" AllowPatchsetSecrets")
	}

	// Writer allows secrets on ps1 while run1 is still open: run1 is canceled and run2 dispatches with secrets.
	allowResp, err := handlers.Check.AllowPatchsetSecrets(dana, &corev1.AllowPatchsetSecretsRequest{ChangesetId: cs.GetId()})
	if err != nil {
		t.Fatalf("writer AllowPatchsetSecrets: %v", err)
	}
	if allowResp.GetChangesetId() != cs.GetId() || allowResp.GetPatchsetId() != ps1.GetId() || allowResp.GetAllowedBySubjectId() != "user_dana" {
		t.Fatalf("AllowPatchsetSecrets = %+v", allowResp)
	}
	if len(allowResp.GetRerunChecks()) != 1 || allowResp.GetRerunChecks()[0].GetAttempt() != 2 {
		t.Fatalf("RerunChecks = %+v, want 1 rerun at attempt 2", allowResp.GetRerunChecks())
	}

	canceled := waitForCancelCheckMessage(t, conn)
	if canceled.GetRunId() != run1 {
		t.Fatalf("canceled run id = %q, want %q", canceled.GetRunId(), run1)
	}
	msg2 := waitForRunChecksMessage(t, conn)
	assertRunChecksHasSecret(t, msg2, true)
	if msg2.GetPatchsetId() != ps1.GetId() || msg2.GetChecks()[0].GetRunId() != allowResp.GetRerunChecks()[0].GetId() {
		t.Fatalf("msg2 = %#v, want run %s on %s", msg2, allowResp.GetRerunChecks()[0].GetId(), ps1.GetId())
	}

	// Idempotent repeat call keeps original actor and does not churn already-allowed checks.
	repeatResp, err := handlers.Check.AllowPatchsetSecrets(alice, &corev1.AllowPatchsetSecretsRequest{ChangesetId: cs.GetId()})
	if err != nil {
		t.Fatalf("repeat AllowPatchsetSecrets: %v", err)
	}
	if repeatResp.GetAllowedBySubjectId() != "user_dana" || len(repeatResp.GetRerunChecks()) != 0 {
		t.Fatalf("repeat AllowPatchsetSecrets = %+v, want original user_dana and 0 reruns", repeatResp)
	}

	// Pushing patchset 2 clears secret access: ps2 runs secretless again until allowed.
	upload2, err := handlers.Blob.UploadBlob(bob, &corev1.UploadBlobRequest{
		Data:  []byte("package payment\nconst V = 2\n"),
		Slice: paymentRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	ps2, err := handlers.Changeset.UpdateChangeset(bob, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.GetId(),
		BaseCommitId: baseCommit,
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        "/acme/payment/app.go",
			BlobId:      upload2.BlobId,
			ContentHash: upload2.ContentHash,
			Mode:        0o100644,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	msg3 := waitForRunChecksMessage(t, conn)
	if msg3.GetPatchsetId() != ps2.GetId() {
		t.Fatalf("ps2 dispatch patchset = %q, want %q", msg3.GetPatchsetId(), ps2.GetId())
	}
	assertRunChecksHasSecret(t, msg3, false)

	// Archived slice rejects AllowPatchsetSecrets.
	if _, err := handlers.Slice.SetSliceArchived(alice, &corev1.SetSliceArchivedRequest{Slice: paymentRef, Archived: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := handlers.Check.AllowPatchsetSecrets(dana, &corev1.AllowPatchsetSecretsRequest{ChangesetId: cs.GetId()}); err == nil {
		t.Fatal("expected archived slice error")
	} else {
		wantCode(t, err, codes.FailedPrecondition, "archived AllowPatchsetSecrets")
	}
	_ = mem
}

func TestAllowPatchsetSecretsGitslicePathsRequireAccountAdmin(t *testing.T) {
	checksYAML := "version: 1\nchecks:\n  unit:\n    run: echo ok\n"
	mem, handlers, baseCommit, conn := setupSecretAllowanceTestSlice(t, checksYAML)
	mem.AddPersonalAccount("user_acc_admin", "acctadmin")
	mem.AddAccountRole("user_acc_admin", "acme", "admin")

	bob := authctx.WithSubjectID(context.Background(), "user_bob")            // contributor
	dana := authctx.WithSubjectID(context.Background(), "user_dana")          // slice writer
	frank := authctx.WithSubjectID(context.Background(), "user_frank")        // slice admin (not account admin)
	accAdmin := authctx.WithSubjectID(context.Background(), "user_acc_admin") // account admin

	editedYAML := "version: 1\nchecks:\n  unit:\n    run: echo exfiltrate\n"
	cs, ps := createDispatchTestPatchset(t, bob, handlers, paymentRef, baseCommit, "/acme/payment/.gitslice/checks.yaml", editedYAML, "edit checks yaml")

	// Initial run has no secrets.
	msg1 := waitForRunChecksMessage(t, conn)
	assertRunChecksHasSecret(t, msg1, false)
	if _, err := mem.Checks.UpdateCheckRunStatus(context.Background(), msg1.GetChecks()[0].GetRunId(), "passed", 0, "ok"); err != nil {
		t.Fatal(err)
	}

	// Neither slice writer nor slice-only admin may allow secrets on a .gitslice/ patchset.
	for _, tc := range []struct {
		name string
		ctx  context.Context
	}{
		{name: "slice writer", ctx: dana},
		{name: "slice admin", ctx: frank},
	} {
		_, err := handlers.Check.AllowPatchsetSecrets(tc.ctx, &corev1.AllowPatchsetSecretsRequest{ChangesetId: cs.GetId()})
		wantCode(t, err, codes.PermissionDenied, tc.name+" AllowPatchsetSecrets on .gitslice/")
	}

	// Account admin CAN allow secrets on the .gitslice/ patchset.
	allowResp, err := handlers.Check.AllowPatchsetSecrets(accAdmin, &corev1.AllowPatchsetSecretsRequest{ChangesetId: cs.GetId()})
	if err != nil {
		t.Fatalf("account admin AllowPatchsetSecrets: %v", err)
	}
	if allowResp.GetAllowedBySubjectId() != "user_acc_admin" || len(allowResp.GetRerunChecks()) != 1 {
		t.Fatalf("account admin AllowPatchsetSecrets = %+v", allowResp)
	}
	msg2 := waitForRunChecksMessage(t, conn)
	assertRunChecksHasSecret(t, msg2, true)
	if msg2.GetPatchsetId() != ps.GetId() {
		t.Fatalf("msg2 patchset = %q, want %q", msg2.GetPatchsetId(), ps.GetId())
	}

	// Allowing secrets on a .gitslice/ patchset still does NOT bypass .gitslice/ submit approval.
	if _, err := mem.Checks.UpdateCheckRunStatus(context.Background(), msg2.GetChecks()[0].GetRunId(), "passed", 0, "ok"); err != nil {
		t.Fatal(err)
	}
	if _, err := handlers.Changeset.ApproveChangeset(dana, &corev1.ApproveChangesetRequest{ChangesetId: cs.GetId()}); err != nil {
		t.Fatal(err)
	}
	if _, err := handlers.Changeset.SubmitChangeset(accAdmin, &corev1.SubmitChangesetRequest{
		ChangesetId:               cs.GetId(),
		ExpectedCurrentPatchsetId: ps.GetId(),
	}); err == nil || !strings.Contains(err.Error(), ".gitslice") {
		t.Fatalf("SubmitChangeset without owner/admin ApproveChangeset = %v, want .gitslice approval error", err)
	}
}
