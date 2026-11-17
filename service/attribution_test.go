package service

import (
	"context"
	"testing"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
)

func TestAuditAttributionAcrossMutationsAndControllerDelegation(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	nic := asUser(t, handlers, "user_nic_ext", "nico")
	reviewer := asUser(t, handlers, "user_rev_ext", "reviewer")
	nicID, _ := authctx.SubjectID(nic)
	revID, _ := authctx.SubjectID(reviewer)

	seedPathInMemoryRepo(t, mem, "commit_audit_base", "/team/web/index.ts", "export const v = 1;\n")

	if _, err := handlers.Auth.CreateOrganization(nic, &corev1.CreateOrganizationRequest{Slug: "team"}); err != nil {
		t.Fatal(err)
	}
	slice, err := handlers.Slice.CreateSlice(nic, &corev1.CreateSliceRequest{
		Ref:           &corev1.SliceRef{Account: "team", Slice: "web"},
		Visibility:    "private",
		IncludedPaths: []string{"/team/web"},
	})
	if err != nil {
		t.Fatal(err)
	}
	sliceRef := slice.GetRef()

	if _, err := handlers.Slice.SetSliceMember(nic, &corev1.SetSliceMemberRequest{
		Slice: sliceRef, Username: "reviewer", Role: "writer",
	}); err != nil {
		t.Fatal(err)
	}

	// Provision delegated agent 'web-agent' controlled by nico, plus a registered daemon 'my-ci' and conversation.
	agentResp, err := handlers.Auth.CreateDelegatedAgent(nic, &corev1.CreateDelegatedAgentRequest{
		Username: "web-agent",
		Slice:    sliceRef,
		Role:     "contributor",
	})
	if err != nil {
		t.Fatal(err)
	}
	agentID := agentResp.GetAgentSubjectId()

	daemon, err := mem.Agents.RegisterDaemon(context.Background(), storage.AgentDaemonInput{
		SubjectID: nicID,
		Account:   "nico",
		Name:      "raw-daemon",
		Runtime:   "codex",
		Version:   "1.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Agents.RenameDaemon(context.Background(), daemon.GetId(), "my-ci"); err != nil {
		t.Fatal(err)
	}
	conv, err := mem.Agents.CreateConversation(context.Background(), storage.ConversationInput{
		DaemonID:  daemon.GetId(),
		SubjectID: nicID,
		SliceID:   slice.GetId(),
		Account:   sliceRef.GetAccount(),
		SliceName: sliceRef.GetSlice(),
		Title:     "Fix header",
		Role:      "contributor",
	})
	if err != nil {
		t.Fatal(err)
	}

	agentRunCtx := authctx.WithPrincipal(context.Background(), authctx.Principal{
		SubjectID:           agentID,
		SubjectKind:         "agent",
		ControllerSubjectID: nicID,
		KeyID:               "key_agent_child",
		HasSliceScope:       true,
		SliceIDs:            []string{slice.GetId()},
		MaxRole:             "contributor",
		DaemonID:            daemon.GetId(),
		ConversationID:      conv.GetId(),
	})

	// 1. Agent creates changeset and uploads patchset 1.
	cs, err := handlers.Changeset.CreateChangeset(agentRunCtx, &corev1.CreateChangesetRequest{
		AuthoringSlice: sliceRef,
		BaseCommitId:   "commit_audit_base",
		Title:          "Update index.ts",
	})
	if err != nil {
		t.Fatalf("CreateChangeset: %v", err)
	}
	if cs.GetAttribution().GetActingSubjectId() != agentID ||
		cs.GetAttribution().GetActingUsername() != "web-agent" ||
		cs.GetAttribution().GetControllerSubjectId() != nicID ||
		cs.GetAttribution().GetControllerUsername() != "nico" ||
		cs.GetAttribution().GetKeyId() != "key_agent_child" ||
		cs.GetAttribution().GetDaemonName() != "my-ci" ||
		cs.GetAttribution().GetConversationId() != conv.GetId() {
		t.Fatalf("unexpected CreateChangeset attribution: %+v", cs.GetAttribution())
	}

	// Key bound to conv.Id must reject forging a different conversation_id.
	_, err = handlers.Changeset.UpdateChangeset(agentRunCtx, &corev1.UpdateChangesetRequest{
		ChangesetId:    cs.GetId(),
		BaseCommitId:   "commit_audit_base",
		ConversationId: "cv_forged_other",
	})
	wantStatus(t, err, codes.PermissionDenied, "forged conversation_id on bound key")

	content1 := []byte("export const v = 2;\n")
	blob1, err := handlers.Blob.UploadBlob(agentRunCtx, &corev1.UploadBlobRequest{
		Slice:       sliceRef,
		ContentHash: objectid.RawContentHash(content1),
		Data:        content1,
	})
	if err != nil {
		t.Fatal(err)
	}
	ps1, err := handlers.Changeset.UpdateChangeset(agentRunCtx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.GetId(),
		BaseCommitId: "commit_audit_base",
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        "/team/web/index.ts",
			BlobId:      blob1.GetBlobId(),
			ContentHash: blob1.GetContentHash(),
			Mode:        0o100644,
		}},
	})
	if err != nil {
		t.Fatalf("UpdateChangeset ps1: %v", err)
	}
	if got := storage.FormatActorAttribution(ps1.GetAttribution(), ps1.GetAuthor()); got != "web-agent (for nico) via my-ci, conversation "+conv.GetId() {
		t.Fatalf("ps1 formatted attribution = %q", got)
	}

	// 2. Reviewer allows patchset secrets and approves changeset -> attribution records reviewer.
	revKeyCtx := authctx.WithPrincipal(context.Background(), authctx.Principal{
		SubjectID:           revID,
		SubjectKind:         "user",
		ControllerSubjectID: revID,
		KeyID:               "key_reviewer_1",
	})
	allowResp, err := handlers.Check.AllowPatchsetSecrets(revKeyCtx, &corev1.AllowPatchsetSecretsRequest{
		ChangesetId: cs.GetId(),
		PatchsetId:  ps1.GetId(),
	})
	if err != nil {
		t.Fatalf("AllowPatchsetSecrets: %v", err)
	}
	if allowResp.GetAttribution().GetActingSubjectId() != revID ||
		allowResp.GetAttribution().GetActingUsername() != "reviewer" ||
		allowResp.GetAttribution().GetControllerSubjectId() != "" ||
		allowResp.GetAttribution().GetKeyId() != "key_reviewer_1" {
		t.Fatalf("unexpected AllowPatchsetSecrets attribution: %+v", allowResp.GetAttribution())
	}

	appResp, err := handlers.Changeset.ApproveChangeset(revKeyCtx, &corev1.ApproveChangesetRequest{
		ChangesetId: cs.GetId(),
	})
	if err != nil {
		t.Fatalf("ApproveChangeset: %v", err)
	}
	if appResp.GetAttribution().GetActingSubjectId() != revID ||
		appResp.GetAttribution().GetActingUsername() != "reviewer" ||
		appResp.GetAttribution().GetKeyId() != "key_reviewer_1" {
		t.Fatalf("unexpected ApproveChangeset attribution: %+v", appResp.GetAttribution())
	}

	// 3. Human controller 'nic' submits web-agent's changeset via controller delegation:
	// submitted_attribution MUST record human 'nic' as acting_subject_id, NOT 'web-agent'!
	nicKeyCtx := authctx.WithPrincipal(context.Background(), authctx.Principal{
		SubjectID:           nicID,
		SubjectKind:         "user",
		ControllerSubjectID: nicID,
		KeyID:               "key_nic_cli",
	})
	subResp, err := handlers.Changeset.SubmitChangeset(nicKeyCtx, &corev1.SubmitChangesetRequest{
		ChangesetId: cs.GetId(),
	})
	if err != nil {
		t.Fatalf("SubmitChangeset by controller nic: %v", err)
	}
	if subResp.GetAttribution().GetActingSubjectId() != nicID ||
		subResp.GetAttribution().GetActingUsername() != "nico" ||
		subResp.GetAttribution().GetControllerSubjectId() != "" ||
		subResp.GetAttribution().GetKeyId() != "key_nic_cli" {
		t.Fatalf("expected human controller nico on submit attribution, got: %+v", subResp.GetAttribution())
	}
	if _, err := mem.Changesets.PublishPending(context.Background(), 10); err != nil {
		t.Fatal(err)
	}

	fetched, err := handlers.Changeset.GetChangeset(nic, &corev1.GetChangesetRequest{ChangesetId: cs.GetId()})
	if err != nil {
		t.Fatal(err)
	}
	if fetched.GetAttribution().GetActingUsername() != "web-agent" ||
		fetched.GetSubmittedAttribution().GetActingSubjectId() != nicID ||
		fetched.GetSubmittedAttribution().GetActingUsername() != "nico" ||
		len(fetched.GetApprovals()) != 1 ||
		fetched.GetApprovals()[0].GetAttribution().GetActingUsername() != "reviewer" {
		t.Fatalf("unexpected GetChangeset attributions: created=%+v submitted=%+v approvals=%+v",
			fetched.GetAttribution(), fetched.GetSubmittedAttribution(), fetched.GetApprovals())
	}

	// 4. CreateTag records full ActorAttribution (tested with a writer agent).
	if _, err := handlers.Slice.SetSliceMember(nic, &corev1.SetSliceMemberRequest{
		Slice: sliceRef, Username: "web-agent", Role: "writer",
	}); err != nil {
		t.Fatal(err)
	}
	agentTagCtx := authctx.WithPrincipal(context.Background(), authctx.Principal{
		SubjectID:           agentID,
		SubjectKind:         "agent",
		ControllerSubjectID: nicID,
		KeyID:               "key_release_bot",
		DaemonID:            daemon.GetId(),
	})
	tag, err := handlers.Slice.CreateTag(agentTagCtx, &corev1.CreateTagRequest{
		Slice:   sliceRef,
		Name:    "v1.0.0",
		Message: "automated release",
	})
	if err != nil {
		t.Fatalf("CreateTag: %v", err)
	}
	if got := storage.FormatActorAttribution(tag.GetAttribution(), tag.GetCreatedBy()); got != "web-agent (for nico) via my-ci" {
		t.Fatalf("CreateTag formatted attribution = %q, raw=%+v", got, tag.GetAttribution())
	}
}
