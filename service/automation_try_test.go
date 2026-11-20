package service

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func uploadEdit(t *testing.T, ctx context.Context, h *Handlers, slice *corev1.SliceRef, path, content string) *corev1.FileEdit {
	t.Helper()
	blob, err := h.Blob.UploadBlob(ctx, &corev1.UploadBlobRequest{Data: []byte(content), Slice: slice})
	if err != nil {
		t.Fatal(err)
	}
	return &corev1.FileEdit{Op: "upsert", Path: path, BlobId: blob.BlobId, ContentHash: blob.ContentHash, Mode: 0o100644}
}

// An automation can be run as defined in an open changeset, before it
// lands; one that lists secrets then needs an admin. A worker can link its
// run to an outside page.
func TestRunAutomationFromChangesetAndDetailsURL(t *testing.T) {
	mem, h := newMemoryHandlers()
	alice := asUser(t, h, "owner_alice", "alice")
	bobby := asUser(t, h, "user_bobby", "bobby")
	if _, err := h.Auth.CreateOrganization(alice, &corev1.CreateOrganizationRequest{Slug: "tryorg"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Auth.InviteAccountMember(alice, &corev1.InviteAccountMemberRequest{Account: "tryorg", Username: "bobby", Role: "writer"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Auth.RespondToInvitation(bobby, &corev1.RespondToInvitationRequest{Account: "tryorg", Accept: true}); err != nil {
		t.Fatal(err)
	}
	home := &corev1.SliceRef{Account: "tryorg", Slice: "home"}
	base := commitSliceFile(t, alice, h, mem, home, "/tryorg/ops/readme.txt", "ops\n")
	if _, err := h.Secret.CreateSecret(alice, &corev1.CreateSecretRequest{
		Account: "tryorg", Name: "HOOK_KEY", Value: "k-123",
		Scope: &corev1.SecretScope{Paths: []string{"/tryorg/ops"}},
	}); err != nil {
		t.Fatal(err)
	}
	cs, _ := proposeEdits(t, bobby, h, home, base, "ops: new automations",
		uploadEdit(t, bobby, h, home, "/tryorg/ops/.gitslice/automations.yaml", `version: 1
automations:
  try-it:
    on: [manual]
    action:
      worker: try.js
  try-secret:
    on: [manual]
    secrets: [HOOK_KEY]
    network: [hooks.example.com]
    action:
      worker: try.js
`),
		uploadEdit(t, bobby, h, home, "/tryorg/ops/.gitslice/try.js", "export default { async onEvent() {} };\n"),
	)

	dispatchSecret := "test-dispatch-secret"
	_, trigSvc, runSvc := newTriggerTestServices(mem, dispatchSecret)

	// Not landed: the default ref has no such automation.
	if _, err := trigSvc.RunTrigger(bobby, &corev1.RunTriggerRequest{Account: "tryorg", TriggerId: "ops/try-it"}); status.Code(err) != codes.NotFound {
		t.Fatalf("RunTrigger at main = %v, want NotFound", err)
	}
	// definition_from_changeset needs the changeset.
	if _, err := trigSvc.RunTrigger(bobby, &corev1.RunTriggerRequest{Account: "tryorg", TriggerId: "ops/try-it", DefinitionFromChangeset: true}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("RunTrigger without changeset = %v, want InvalidArgument", err)
	}
	run, err := trigSvc.RunTrigger(bobby, &corev1.RunTriggerRequest{Account: "tryorg", TriggerId: "ops/try-it", ChangesetId: cs.Id, DefinitionFromChangeset: true})
	if err != nil {
		t.Fatalf("writer RunTrigger from changeset: %v", err)
	}
	// A writer may not run an unreviewed script that gets secrets.
	if _, err := trigSvc.RunTrigger(bobby, &corev1.RunTriggerRequest{Account: "tryorg", TriggerId: "ops/try-secret", ChangesetId: cs.Id, DefinitionFromChangeset: true}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("writer RunTrigger(secrets) from changeset = %v, want PermissionDenied", err)
	}
	if _, err := trigSvc.RunTrigger(alice, &corev1.RunTriggerRequest{Account: "tryorg", TriggerId: "ops/try-secret", ChangesetId: cs.Id, DefinitionFromChangeset: true}); err != nil {
		t.Fatalf("admin RunTrigger(secrets) from changeset: %v", err)
	}

	// The dispatcher gets the script from the changeset's tree.
	started, err := runSvc.StartRun(context.Background(), &corev1.StartTriggerRunRequest{RunId: run.GetId(), DispatcherKey: dispatchSecret})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if got := started.GetModules()[started.GetMainModule()]; got != "export default { async onEvent() {} };\n" {
		t.Fatalf("main module = %q, want the changeset's try.js", got)
	}

	// Details URL: https only, then shown on the run.
	if _, err := runSvc.SetRunDetailsUrl(context.Background(), &corev1.SetTriggerRunDetailsUrlRequest{RunToken: started.GetRunToken(), DetailsUrl: "http://ci.example.com/1"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("SetRunDetailsUrl(http) = %v, want InvalidArgument", err)
	}
	if _, err := runSvc.SetRunDetailsUrl(context.Background(), &corev1.SetTriggerRunDetailsUrlRequest{RunToken: started.GetRunToken(), DetailsUrl: "https://ci.example.com/builds/1"}); err != nil {
		t.Fatalf("SetRunDetailsUrl: %v", err)
	}
	got, err := trigSvc.GetTriggerRun(bobby, &corev1.GetTriggerRunRequest{RunId: run.GetId()})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetDetailsUrl() != "https://ci.example.com/builds/1" {
		t.Fatalf("details_url = %q", got.GetDetailsUrl())
	}

	// Abandoned changesets cannot be tried.
	if _, err := h.Changeset.AbandonChangeset(bobby, &corev1.AbandonChangesetRequest{ChangesetId: cs.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := trigSvc.RunTrigger(bobby, &corev1.RunTriggerRequest{Account: "tryorg", TriggerId: "ops/try-it", ChangesetId: cs.Id, DefinitionFromChangeset: true}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("RunTrigger from abandoned changeset = %v, want FailedPrecondition", err)
	}
	_ = storage.DefaultTargetRef
}
