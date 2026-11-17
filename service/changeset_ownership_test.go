package service

import (
	"context"
	"testing"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
)

// design/36 section 2: only an owner (author, author's controller,
// collaborator, slice admin) may push to, edit, submit or abandon a changeset.
func TestChangesetOwnershipControllerCollaboratorsAndHandoff(t *testing.T) {
	mem, h := newMemoryHandlers()
	h.Auth.AgentSignupEnabled = true
	ctx := context.Background()

	human, err := mem.Auth.EnsureExternalSubject(ctx, storage.ProviderClerk, "clerk_own", "own@example.com")
	if err != nil {
		t.Fatal(err)
	}
	h.Auth.ClerkUsers = fakeClerkUsers{emails: map[string][]string{"clerk_own": {"own@example.com"}}}
	agent, err := h.Auth.RegisterAgent(ctx, &corev1.RegisterAgentRequest{Username: "own-agent", OwnerEmail: "own@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	humanCtx := authctx.WithSubjectID(ctx, human)
	if _, err := h.Auth.AcceptClaim(humanCtx, &corev1.AcceptClaimRequest{AgentSubjectId: agent.SubjectId}); err != nil {
		t.Fatal(err)
	}
	// The agent is a writer of acme; its controller is not a member at all.
	mem.AddAccountRole(agent.SubjectId, "acme", "writer")
	mem.AddPersonalAccount("user_other", "other")
	mem.AddPersonalAccount("user_alice", "alice")
	mem.AddAccountRole("user_other", "acme", "writer")
	agentCtx := authctx.WithSubjectID(ctx, agent.SubjectId)
	mem.AddPersonalAccount("user_bystander", "bystander")
	otherCtx := subjectCtx("user_other")
	adminCtx := subjectCtx("user_alice") // admin of acme

	sliceRef := &corev1.SliceRef{Account: "acme", Slice: "home"}
	ref, err := h.Repository.GetRef(adminCtx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	cs, ps := createStandaloneChangesetWithPatchsetForSlice(t, agentCtx, h, sliceRef, ref.CommitId, "agent work", "/acme/own.txt", "v1\n")

	pushEdit := func(who context.Context, expected, content string) error {
		// Blobs are uploaded as the agent: a non-member controller has no slice role of its own.
		up, err := h.Blob.UploadBlob(agentCtx, &corev1.UploadBlobRequest{Data: []byte(content), Slice: sliceRef})
		if err != nil {
			return err
		}
		_, err = h.Changeset.UpdateChangeset(who, &corev1.UpdateChangesetRequest{
			ChangesetId:               cs.Id,
			ExpectedCurrentPatchsetId: expected,
			BaseCommitId:              ref.CommitId,
			FileEdits: []*corev1.FileEdit{{
				Op: "upsert", Path: "/acme/own.txt", BlobId: up.BlobId, ContentHash: up.ContentHash, Mode: 0o100644,
			}},
		})
		return err
	}

	// A writer who is not an owner cannot push, edit or abandon.
	wantCode(t, pushEdit(otherCtx, ps.Id, "hijack\n"), codes.PermissionDenied, "non-owner writer UpdateChangeset")
	title := "hijacked"
	_, err = h.Changeset.UpdateChangesetInfo(otherCtx, &corev1.UpdateChangesetInfoRequest{ChangesetId: cs.Id, Title: &title})
	wantCode(t, err, codes.PermissionDenied, "non-owner writer UpdateChangesetInfo")
	_, err = h.Changeset.AbandonChangeset(otherCtx, &corev1.AbandonChangesetRequest{ChangesetId: cs.Id})
	wantCode(t, err, codes.PermissionDenied, "non-owner writer AbandonChangeset")
	_, err = h.Changeset.AddChangesetCollaborator(otherCtx, &corev1.AddChangesetCollaboratorRequest{ChangesetId: cs.Id, Username: "other"})
	wantCode(t, err, codes.PermissionDenied, "non-owner adding collaborator")

	// The agent's controller owns it, though not a member of acme.
	if err := pushEdit(humanCtx, ps.Id, "from controller\n"); err != nil {
		t.Fatalf("controller UpdateChangeset: %v", err)
	}
	got, err := h.Changeset.GetChangeset(adminCtx, &corev1.GetChangesetRequest{ChangesetId: cs.Id})
	if err != nil {
		t.Fatal(err)
	}
	// The patchset records the controller (the person), not the agent.
	if last := got.Patchsets[len(got.Patchsets)-1]; last.Author == "own-agent" {
		t.Fatalf("patchset author = %q, want the controller", last.Author)
	}

	// A slice admin may add a collaborator, who may then push; removal revokes it.
	if _, err := h.Changeset.AddChangesetCollaborator(adminCtx, &corev1.AddChangesetCollaboratorRequest{ChangesetId: cs.Id, Username: "other"}); err != nil {
		t.Fatalf("admin AddChangesetCollaborator: %v", err)
	}
	cur, _ := h.Changeset.GetChangeset(adminCtx, &corev1.GetChangesetRequest{ChangesetId: cs.Id})
	if len(cur.Collaborators) != 1 {
		t.Fatalf("collaborators = %v", cur.Collaborators)
	}
	if err := pushEdit(otherCtx, cur.CurrentPatchsetId, "from collaborator\n"); err != nil {
		t.Fatalf("collaborator UpdateChangeset: %v", err)
	}
	if _, err := h.Changeset.RemoveChangesetCollaborator(agentCtx, &corev1.RemoveChangesetCollaboratorRequest{ChangesetId: cs.Id, Username: "other"}); err != nil {
		t.Fatalf("author RemoveChangesetCollaborator: %v", err)
	}
	cur, _ = h.Changeset.GetChangeset(adminCtx, &corev1.GetChangesetRequest{ChangesetId: cs.Id})
	wantCode(t, pushEdit(otherCtx, cur.CurrentPatchsetId, "after removal\n"), codes.PermissionDenied, "removed collaborator")

	// A collaborator must be able to propose on the slice.
	_, err = h.Changeset.AddChangesetCollaborator(adminCtx, &corev1.AddChangesetCollaboratorRequest{ChangesetId: cs.Id, Username: "bystander"})
	wantCode(t, err, codes.FailedPrecondition, "collaborator without propose access")

	// Handoff keeps the original author and makes the target the owner.
	handed, err := h.Changeset.HandoffChangeset(agentCtx, &corev1.HandoffChangesetRequest{ChangesetId: cs.Id, Username: "alice"})
	if err != nil {
		t.Fatalf("HandoffChangeset: %v", err)
	}
	if handed.Author != "alice" || handed.OriginalAuthor != "own-agent" {
		t.Fatalf("after handoff author=%q original=%q", handed.Author, handed.OriginalAuthor)
	}
	// The previous author no longer owns it.
	_, err = h.Changeset.AbandonChangeset(agentCtx, &corev1.AbandonChangesetRequest{ChangesetId: cs.Id})
	wantCode(t, err, codes.PermissionDenied, "previous author after handoff")
	// A second handoff keeps the first original author.
	again, err := h.Changeset.HandoffChangeset(adminCtx, &corev1.HandoffChangesetRequest{ChangesetId: cs.Id, Username: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if again.OriginalAuthor != "own-agent" {
		t.Fatalf("original author after second handoff = %q", again.OriginalAuthor)
	}
}

func TestStackMutationsRequireOwnership(t *testing.T) {
	mem, h := newMemoryHandlers()
	mem.AddAccountRole("user_other", "acme", "writer")
	mem.AddAccountRole("user_owner", "acme", "writer")
	owner, other, admin := subjectCtx("user_owner"), subjectCtx("user_other"), subjectCtx("user_alice")
	slice := &corev1.SliceRef{Account: "acme", Slice: "home"}
	ref, err := h.Repository.GetRef(admin, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	stack, err := h.Stack.CreateStack(owner, &corev1.CreateStackRequest{AuthoringSlice: slice, BaseCommitId: ref.CommitId, Title: "s"})
	if err != nil {
		t.Fatal(err)
	}
	stack2, err := h.Stack.CreateStack(owner, &corev1.CreateStackRequest{AuthoringSlice: slice, BaseCommitId: ref.CommitId, Title: "s2"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Stack.AddStackEntry(owner, &corev1.AddStackEntryRequest{StackId: stack2.Id, Title: "one"}); err != nil {
		t.Fatalf("owner AddStackEntry: %v", err)
	}
	_, err = h.Stack.AddStackEntry(other, &corev1.AddStackEntryRequest{StackId: stack.Id, Title: "intruder"})
	wantCode(t, err, codes.PermissionDenied, "non-owner writer AddStackEntry")
	_, err = h.Stack.SubmitStack(other, &corev1.SubmitStackRequest{StackId: stack.Id})
	wantCode(t, err, codes.PermissionDenied, "non-owner writer SubmitStack")
	if _, err := h.Stack.AddStackEntry(admin, &corev1.AddStackEntryRequest{StackId: stack.Id, Title: "admin"}); err != nil {
		t.Fatalf("admin AddStackEntry: %v", err)
	}
}

// CreateChangeset with a stack_id takes the same ownership as AddStackEntry,
// and moving an entry needs ownership of everything below it.
func TestCreateChangesetIntoStackAndReparentSubtreeRequireOwnership(t *testing.T) {
	mem, h := newMemoryHandlers()
	mem.AddAccountRole("user_other", "acme", "writer")
	mem.AddAccountRole("user_owner", "acme", "writer")
	owner, other, admin := subjectCtx("user_owner"), subjectCtx("user_other"), subjectCtx("user_alice")
	slice := &corev1.SliceRef{Account: "acme", Slice: "home"}
	ref, err := h.Repository.GetRef(admin, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	stack, err := h.Stack.CreateStack(owner, &corev1.CreateStackRequest{AuthoringSlice: slice, BaseCommitId: ref.CommitId, Title: "s"})
	if err != nil {
		t.Fatal(err)
	}

	// A non-owner cannot attach a changeset to someone else's stack by way of CreateChangeset.
	_, err = h.Changeset.CreateChangeset(other, &corev1.CreateChangesetRequest{
		AuthoringSlice: slice, BaseCommitId: ref.CommitId, Title: "sneaky", StackId: stack.Id,
	})
	wantCode(t, err, codes.PermissionDenied, "non-owner CreateChangeset into another's stack")
	if _, err := h.Changeset.CreateChangeset(owner, &corev1.CreateChangesetRequest{
		AuthoringSlice: slice, BaseCommitId: ref.CommitId, Title: "mine", StackId: stack.Id,
	}); err != nil {
		t.Fatalf("owner CreateChangeset into own stack: %v", err)
	}
	// Standalone changesets stay slice-level.
	if _, err := h.Changeset.CreateChangeset(other, &corev1.CreateChangesetRequest{
		AuthoringSlice: slice, BaseCommitId: ref.CommitId, Title: "standalone",
	}); err != nil {
		t.Fatalf("standalone CreateChangeset: %v", err)
	}

	// Reparent: owner owns the root entry but not its child, which the admin added.
	root, err := h.Stack.GetStack(owner, &corev1.GetStackRequest{StackId: stack.Id})
	if err != nil || len(root.Entries) != 1 {
		t.Fatalf("stack entries = %+v, %v", root, err)
	}
	rootID := root.Entries[0].ChangesetId
	up, err := h.Blob.UploadBlob(owner, &corev1.UploadBlobRequest{Data: []byte("root\n"), Slice: slice})
	if err != nil {
		t.Fatal(err)
	}
	ps, err := h.Changeset.UpdateChangeset(owner, &corev1.UpdateChangesetRequest{
		ChangesetId: rootID, BaseCommitId: ref.CommitId,
		FileEdits: []*corev1.FileEdit{{Op: "upsert", Path: "/acme/root.txt", BlobId: up.BlobId, ContentHash: up.ContentHash, Mode: 0o100644}},
	})
	if err != nil {
		t.Fatal(err)
	}
	child, err := h.Stack.AddStackEntry(admin, &corev1.AddStackEntryRequest{
		StackId: stack.Id, Title: "admin child", ParentChangesetId: rootID, ParentPatchsetId: ps.Id,
	})
	if err != nil {
		t.Fatalf("admin AddStackEntry under owner's entry: %v", err)
	}
	_, err = h.Stack.ReparentStackEntry(owner, &corev1.ReparentStackEntryRequest{StackId: stack.Id, ChangesetId: rootID})
	wantCode(t, err, codes.PermissionDenied, "reparent over a descendant the caller does not own")
	_ = child
}
