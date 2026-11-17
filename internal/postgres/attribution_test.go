package postgres

import (
	"testing"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestMigration0060AndAuditAttributionPostgres(t *testing.T) {
	ctx, store := newPostgresTestStore(t)

	insertPGPersonalUser(t, ctx, store, "user_nic", "nic", "Nic")
	insertPGPersonalUser(t, ctx, store, "user_rev", "reviewer", "Reviewer")
	if err := store.Auth().SetVerifiedEmails(ctx, "user_nic", storage.ProviderClerk, []string{"nic@example.com"}); err != nil {
		t.Fatal(err)
	}
	agent := provisionAgentPGTest(t, ctx, store, "web-agent", "nic@example.com", "user_nic")

	slice, err := store.Slices().Resolve(ctx, &corev1.SliceRef{Account: "acme", Slice: "payment"})
	if err != nil {
		t.Fatal(err)
	}
	ref, err := store.Repository().GetRef(ctx, storage.DefaultTargetRef)
	if err != nil {
		t.Fatal(err)
	}

	agentCtx := authctx.WithPrincipal(ctx, authctx.Principal{
		SubjectID:           agent.SubjectID,
		SubjectKind:         "agent",
		ControllerSubjectID: "user_nic",
		KeyID:               "key_pg_child",
		DaemonID:            "agent_daemon_pg",
		ConversationID:      "cv_pg_1",
	})

	cs, err := store.Changesets().Create(agentCtx, agent.SubjectID, &corev1.CreateChangesetRequest{
		AuthoringSlice: slice.GetRef(),
		TargetRef:      storage.DefaultTargetRef,
		BaseCommitId:   ref.GetCommitId(),
		Title:          "PG attribution test",
	})
	if err != nil {
		t.Fatalf("Changesets.Create: %v", err)
	}
	if cs.GetAttribution().GetActingSubjectId() != agent.SubjectID ||
		cs.GetAttribution().GetControllerSubjectId() != "user_nic" ||
		cs.GetAttribution().GetKeyId() != "key_pg_child" ||
		cs.GetAttribution().GetDaemonId() != "agent_daemon_pg" ||
		cs.GetAttribution().GetConversationId() != "cv_pg_1" {
		t.Fatalf("unexpected PG changeset attribution: %+v", cs.GetAttribution())
	}

	data := []byte("package payment\nconst Audited = true\n")
	hash := objectid.RawContentHash(data)
	blobID := objectid.BlobID(data)
	if err := store.Blobs().Upsert(ctx, blobID, hash, int64(len(data)), "test://"+hash); err != nil {
		t.Fatal(err)
	}
	baseTreeID, err := store.Repository().RootTreeForCommit(ctx, ref.GetCommitId())
	if err != nil {
		t.Fatal(err)
	}
	ps, err := store.Changesets().AddPatchset(agentCtx, cs.GetId(), "", &corev1.Patchset{
		BaseCommitId: ref.GetCommitId(),
		BaseKind:     "commit",
		BaseTreeId:   baseTreeID,
		Author:       agent.SubjectID,
		ChangedPaths: []string{"/acme/payment/audit.go"},
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        "/acme/payment/audit.go",
			BlobId:      blobID,
			ContentHash: hash,
			Mode:        0o100644,
		}},
	})
	if err != nil {
		t.Fatalf("AddPatchset: %v", err)
	}
	if ps.GetAttribution().GetKeyId() != "key_pg_child" || ps.GetAttribution().GetControllerSubjectId() != "user_nic" {
		t.Fatalf("unexpected PG patchset attribution: %+v", ps.GetAttribution())
	}

	revCtx := authctx.WithPrincipal(ctx, authctx.Principal{
		SubjectID:           "user_rev",
		SubjectKind:         "user",
		ControllerSubjectID: "user_rev",
		KeyID:               "key_rev_pg",
	})
	allowance, created, err := store.Checks().AllowPatchsetSecrets(revCtx, cs.GetId(), ps.GetId(), "user_rev")
	if err != nil || !created {
		t.Fatalf("AllowPatchsetSecrets: created=%v err=%v", created, err)
	}
	if allowance.Attribution.ActingSubjectID != "user_rev" || allowance.Attribution.ControllerSubjectID != "" || allowance.Attribution.KeyID != "key_rev_pg" {
		t.Fatalf("unexpected PG allowance attribution: %+v", allowance.Attribution)
	}

	appResp, err := store.Changesets().Approve(revCtx, cs.GetId(), "user_rev")
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if appResp.GetAttribution().GetActingSubjectId() != "user_rev" || appResp.GetAttribution().GetKeyId() != "key_rev_pg" {
		t.Fatalf("unexpected PG approve attribution: %+v", appResp.GetAttribution())
	}

	// Human controller 'user_nic' submits -> submitted_by_subject_id records 'user_nic'.
	nicCtx := authctx.WithPrincipal(ctx, authctx.Principal{
		SubjectID:           "user_nic",
		SubjectKind:         "user",
		ControllerSubjectID: "user_nic",
		KeyID:               "key_nic_pg",
	})
	subResp, err := store.Changesets().Submit(nicCtx, cs.GetId(), ps.GetId())
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if subResp.GetAttribution().GetActingSubjectId() != "user_nic" || subResp.GetAttribution().GetKeyId() != "key_nic_pg" {
		t.Fatalf("unexpected PG submit attribution: %+v", subResp.GetAttribution())
	}
	if _, err := store.Changesets().PublishPending(ctx, 10); err != nil {
		t.Fatal(err)
	}

	reloaded, err := store.Changesets().Get(ctx, cs.GetId())
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.GetSubmittedAttribution().GetActingSubjectId() != "user_nic" ||
		reloaded.GetSubmittedAttribution().GetKeyId() != "key_nic_pg" ||
		len(reloaded.GetApprovals()) != 1 ||
		reloaded.GetApprovals()[0].GetAttribution().GetKeyId() != "key_rev_pg" {
		t.Fatalf("unexpected reloaded PG changeset: submitted=%+v approvals=%+v", reloaded.GetSubmittedAttribution(), reloaded.GetApprovals())
	}

	tag, isNew, err := store.Slices().CreateTag(agentCtx, storage.SliceTag{
		SliceID:           slice.GetId(),
		Name:              "v0.6.0",
		CommitID:          ref.GetCommitId(),
		DefinitionVersion: 1,
		Message:           "pg tag audit",
		CreatedBy:         agent.SubjectID,
	})
	if err != nil || !isNew {
		t.Fatalf("CreateTag: isNew=%v err=%v", isNew, err)
	}
	if tag.Attribution.ActingSubjectID != agent.SubjectID || tag.Attribution.ControllerSubjectID != "user_nic" || tag.Attribution.KeyID != "key_pg_child" {
		t.Fatalf("unexpected PG tag attribution: %+v", tag.Attribution)
	}
}
