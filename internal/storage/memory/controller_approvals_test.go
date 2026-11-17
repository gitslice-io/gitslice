package memory

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/objectstore/filesystem"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func mustProvisionHumanForTest(t *testing.T, ctx context.Context, stores *Stores, extID, email string) string {
	t.Helper()
	subjectID, err := stores.Auth.EnsureExternalSubject(ctx, storage.ProviderClerk, extID, email)
	if err != nil {
		t.Fatalf("EnsureExternalSubject(%s): %v", extID, err)
	}
	if err := stores.Auth.SetVerifiedEmails(ctx, subjectID, storage.ProviderClerk, []string{email}); err != nil {
		t.Fatalf("SetVerifiedEmails(%s): %v", subjectID, err)
	}
	return subjectID
}

func mustProvisionAgentForTest(t *testing.T, ctx context.Context, stores *Stores, username, ownerEmail, claimerSubjectID string) *storage.RegisteredAgent {
	t.Helper()
	agent, err := stores.Auth.RegisterAgent(ctx, storage.RegisterAgentInput{
		Username:    username,
		OwnerEmail:  ownerEmail,
		DisplayName: username,
	})
	if err != nil {
		t.Fatalf("RegisterAgent(%s): %v", username, err)
	}
	if claimerSubjectID != "" {
		if _, err := stores.Auth.AcceptClaim(ctx, claimerSubjectID, agent.SubjectID); err != nil {
			t.Fatalf("AcceptClaim(%s, %s): %v", claimerSubjectID, agent.SubjectID, err)
		}
	}
	return agent
}

func mustCreateDraftOnSlice(t *testing.T, ctx context.Context, stores *Stores, authorSubjectID string, sliceRef *corev1.SliceRef, path, content string) (*corev1.Changeset, *corev1.Patchset) {
	t.Helper()
	ref, err := stores.Repository.GetRef(ctx, storage.DefaultTargetRef)
	if err != nil {
		t.Fatalf("GetRef: %v", err)
	}
	data := []byte(content)
	blobID := objectid.BlobID(data)
	contentHash := objectid.RawContentHash(data)
	key := filesystem.BlobKey(contentHash)
	if err := stores.Objects.Put(ctx, key, bytes.NewReader(data)); err != nil {
		t.Fatalf("Objects.Put: %v", err)
	}
	if err := stores.Blobs.Upsert(ctx, blobID, contentHash, int64(len(data)), key); err != nil {
		t.Fatalf("Blobs.Upsert: %v", err)
	}

	cs, err := stores.Changesets.Create(ctx, authorSubjectID, &corev1.CreateChangesetRequest{
		AuthoringSlice: sliceRef,
		TargetRef:      storage.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          fmt.Sprintf("change %s", path),
	})
	if err != nil {
		t.Fatalf("Changesets.Create: %v", err)
	}
	ps, err := stores.Changesets.AddPatchset(ctx, cs.Id, "", &corev1.Patchset{
		BaseCommitId: ref.CommitId,
		Author:       authorSubjectID,
		ChangedPaths: []string{path},
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        path,
			BlobId:      blobID,
			ContentHash: contentHash,
			Mode:        0o100644,
		}},
		PathBases: []*corev1.PathBase{{
			Path:             path,
			BaseCommitId:     ref.CommitId,
			Exists:           false,
			EntryFingerprint: storage.MissingEntryFingerprint(),
			Check:            "entry_fingerprint",
		}},
		ReadSet:  []*corev1.PathSetEntry{{Path: path}},
		WriteSet: []*corev1.PathSetEntry{{Path: path}},
	})
	if err != nil {
		t.Fatalf("Changesets.AddPatchset: %v", err)
	}
	return cs, ps
}

func TestControllerCountedApprovalsMemory(t *testing.T) {
	ctx := context.Background()
	stores := New()

	humanA := mustProvisionHumanForTest(t, ctx, stores, "clerk_a", "a@example.com")
	humanB := mustProvisionHumanForTest(t, ctx, stores, "clerk_b", "b@example.com")
	humanC := mustProvisionHumanForTest(t, ctx, stores, "clerk_c", "c@example.com")

	unclaimed := mustProvisionAgentForTest(t, ctx, stores, "unclaimed-bot", "other@example.com", "")
	agentA1 := mustProvisionAgentForTest(t, ctx, stores, "agent-a1", "a@example.com", humanA)
	agentA2 := mustProvisionAgentForTest(t, ctx, stores, "agent-a2", "a@example.com", humanA)
	agentB1 := mustProvisionAgentForTest(t, ctx, stores, "agent-b1", "b@example.com", humanB)
	agentB2 := mustProvisionAgentForTest(t, ctx, stores, "agent-b2", "b@example.com", humanB)

	// SubjectForToken exposes Kind and ControllerSubjectID accurately.
	for _, tc := range []struct {
		token string
		kind  string
		ctrl  string
	}{
		{token: unclaimed.APIKey, kind: "agent", ctrl: ""},
		{token: agentA1.APIKey, kind: "agent", ctrl: humanA},
		{token: agentB1.APIKey, kind: "agent", ctrl: humanB},
	} {
		subj, err := stores.Auth.SubjectForToken(ctx, tc.token)
		if err != nil {
			t.Fatalf("SubjectForToken: %v", err)
		}
		if subj.Kind != tc.kind || subj.ControllerSubjectID != tc.ctrl {
			t.Fatalf("SubjectForToken = %+v, want kind=%q ctrl=%q", subj, tc.kind, tc.ctrl)
		}
	}

	sliceOne := &corev1.SliceRef{Account: "acme", Slice: "ctrl-one"}
	stores.PutSliceWithHumanApprovals(sliceOne, []string{"/acme/ctrl-one"}, "private", 1, 0, nil)

	cs, ps := mustCreateDraftOnSlice(t, ctx, stores, agentA1.SubjectID, sliceOne, "/acme/ctrl-one/a.txt", "v1\n")

	// Unclaimed agent cannot approve toward required_approvals.
	if _, err := stores.Changesets.Approve(ctx, cs.Id, unclaimed.SubjectID); err != nil {
		t.Fatal(err)
	}
	if _, err := stores.Changesets.Submit(ctx, cs.Id, ps.Id); err == nil || !strings.Contains(err.Error(), "requires 1 distinct non-author approval(s), has 0") {
		t.Fatalf("Submit after unclaimed agent approval = %v, want requires 1 distinct non-author approval(s), has 0", err)
	}

	// Owner humanA approving own agent agentA1 is excluded; sibling agentA2 is also excluded.
	if _, err := stores.Changesets.Approve(ctx, cs.Id, humanA); err != nil {
		t.Fatal(err)
	}
	if _, err := stores.Changesets.Approve(ctx, cs.Id, agentA2.SubjectID); err != nil {
		t.Fatal(err)
	}
	if _, err := stores.Changesets.Submit(ctx, cs.Id, ps.Id); err == nil || !strings.Contains(err.Error(), "requires 1 distinct non-author approval(s), has 0") {
		t.Fatalf("Submit after same-controller approvals = %v, want requires 1 distinct non-author approval(s), has 0", err)
	}

	// Claimed agent from distinct controller humanB satisfies required_approvals=1.
	if _, err := stores.Changesets.Approve(ctx, cs.Id, agentB1.SubjectID); err != nil {
		t.Fatal(err)
	}
	if _, err := stores.Changesets.Submit(ctx, cs.Id, ps.Id); err != nil {
		t.Fatalf("Submit after distinct-controller agent approval failed: %v", err)
	}

	// Two agents under the same controller count as only 1 approval when required_approvals=2.
	sliceTwo := &corev1.SliceRef{Account: "acme", Slice: "ctrl-two"}
	stores.PutSliceWithHumanApprovals(sliceTwo, []string{"/acme/ctrl-two"}, "private", 2, 0, nil)
	cs2, ps2 := mustCreateDraftOnSlice(t, ctx, stores, humanA, sliceTwo, "/acme/ctrl-two/b.txt", "v1\n")

	if _, err := stores.Changesets.Approve(ctx, cs2.Id, agentB1.SubjectID); err != nil {
		t.Fatal(err)
	}
	if _, err := stores.Changesets.Approve(ctx, cs2.Id, agentB2.SubjectID); err != nil {
		t.Fatal(err)
	}
	if _, err := stores.Changesets.Submit(ctx, cs2.Id, ps2.Id); err == nil || !strings.Contains(err.Error(), "requires 2 distinct non-author approval(s), has 1") {
		t.Fatalf("Submit with 2 agents sharing controller = %v, want has 1", err)
	}
	if _, err := stores.Changesets.Approve(ctx, cs2.Id, humanC); err != nil {
		t.Fatal(err)
	}
	if _, err := stores.Changesets.Submit(ctx, cs2.Id, ps2.Id); err != nil {
		t.Fatalf("Submit with 2nd distinct controller failed: %v", err)
	}
}

func TestPatchsetAuthorExclusionAndMechanicalSubmitMergeMemory(t *testing.T) {
	ctx := context.Background()
	stores := New()

	humanA := mustProvisionHumanForTest(t, ctx, stores, "clerk_a", "a@example.com")
	humanB := mustProvisionHumanForTest(t, ctx, stores, "clerk_b", "b@example.com")
	humanC := mustProvisionHumanForTest(t, ctx, stores, "clerk_c", "c@example.com")
	agentB := mustProvisionAgentForTest(t, ctx, stores, "agent-b", "b@example.com", humanB)

	sliceRef := &corev1.SliceRef{Account: "acme", Slice: "patchset-ctrl"}
	stores.PutSliceWithHumanApprovals(sliceRef, []string{"/acme/patchset-ctrl"}, "private", 1, 0, nil)

	// Non-mechanical patchset authored by agentB excludes humanB's controller.
	cs1, ps1 := mustCreateDraftOnSlice(t, ctx, stores, humanA, sliceRef, "/acme/patchset-ctrl/one.txt", "one\n")
	ps1UpdatedData := []byte("two\n")
	ps1UpdatedBlobID := objectid.BlobID(ps1UpdatedData)
	ps1UpdatedHash := objectid.RawContentHash(ps1UpdatedData)
	ps1UpdatedKey := filesystem.BlobKey(ps1UpdatedHash)
	if err := stores.Objects.Put(ctx, ps1UpdatedKey, bytes.NewReader(ps1UpdatedData)); err != nil {
		t.Fatal(err)
	}
	if err := stores.Blobs.Upsert(ctx, ps1UpdatedBlobID, ps1UpdatedHash, int64(len(ps1UpdatedData)), ps1UpdatedKey); err != nil {
		t.Fatal(err)
	}
	ps2, err := stores.Changesets.AddPatchset(ctx, cs1.Id, ps1.Id, &corev1.Patchset{
		BaseCommitId: ps1.BaseCommitId,
		Author:       agentB.SubjectID,
		ChangedPaths: []string{"/acme/patchset-ctrl/one.txt"},
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        "/acme/patchset-ctrl/one.txt",
			BlobId:      ps1UpdatedBlobID,
			ContentHash: ps1UpdatedHash,
			Mode:        0o100644,
		}},
		PathBases: []*corev1.PathBase{{
			Path:             "/acme/patchset-ctrl/one.txt",
			BaseCommitId:     ps1.BaseCommitId,
			Exists:           false,
			EntryFingerprint: storage.MissingEntryFingerprint(),
			Check:            "entry_fingerprint",
		}},
		ReadSet:  []*corev1.PathSetEntry{{Path: "/acme/patchset-ctrl/one.txt"}},
		WriteSet: []*corev1.PathSetEntry{{Path: "/acme/patchset-ctrl/one.txt"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stores.Changesets.Approve(ctx, cs1.Id, humanB); err != nil {
		t.Fatal(err)
	}
	if _, err := stores.Changesets.Submit(ctx, cs1.Id, ps2.Id); err == nil || !strings.Contains(err.Error(), "requires 1 distinct non-author approval(s), has 0") {
		t.Fatalf("Submit after non-mechanical patchset author controller approval = %v, want has 0", err)
	}
	if _, err := stores.Changesets.Approve(ctx, cs1.Id, humanC); err != nil {
		t.Fatal(err)
	}
	if _, err := stores.Changesets.Submit(ctx, cs1.Id, ps2.Id); err != nil {
		t.Fatalf("Submit after humanC approval failed: %v", err)
	}

	// Mechanical SubmitMergeKind patchset authored by agentB does NOT exclude humanB.
	cs2, ps2A := mustCreateDraftOnSlice(t, ctx, stores, humanA, sliceRef, "/acme/patchset-ctrl/two.txt", "mech1\n")
	ps2B, err := stores.Changesets.AddPatchset(ctx, cs2.Id, ps2A.Id, &corev1.Patchset{
		BaseCommitId: ps2A.BaseCommitId,
		Author:       agentB.SubjectID,
		Kind:         storage.SubmitMergeKind,
		ChangedPaths: []string{"/acme/patchset-ctrl/two.txt"},
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        "/acme/patchset-ctrl/two.txt",
			BlobId:      ps2A.FileEdits[0].BlobId,
			ContentHash: ps2A.FileEdits[0].ContentHash,
			Mode:        0o100644,
		}},
		PathBases: []*corev1.PathBase{{
			Path:             "/acme/patchset-ctrl/two.txt",
			BaseCommitId:     ps2A.BaseCommitId,
			Exists:           false,
			EntryFingerprint: storage.MissingEntryFingerprint(),
			Check:            "entry_fingerprint",
		}},
		ReadSet:  []*corev1.PathSetEntry{{Path: "/acme/patchset-ctrl/two.txt"}},
		WriteSet: []*corev1.PathSetEntry{{Path: "/acme/patchset-ctrl/two.txt"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stores.Changesets.Approve(ctx, cs2.Id, humanB); err != nil {
		t.Fatal(err)
	}
	if _, err := stores.Changesets.Submit(ctx, cs2.Id, ps2B.Id); err != nil {
		t.Fatalf("Submit after SubmitMergeKind author controller approval failed: %v", err)
	}
}

func TestRequiredHumanApprovalsAndBlockClearMemory(t *testing.T) {
	ctx := context.Background()
	stores := New()

	humanA := mustProvisionHumanForTest(t, ctx, stores, "clerk_a", "a@example.com")
	humanB := mustProvisionHumanForTest(t, ctx, stores, "clerk_b", "b@example.com")
	agentB := mustProvisionAgentForTest(t, ctx, stores, "agent-b", "b@example.com", humanB)

	sliceRef := &corev1.SliceRef{Account: "acme", Slice: "human-req"}
	stores.PutSliceWithHumanApprovals(sliceRef, []string{"/acme/human-req"}, "private", 1, 1, nil)

	cs, ps := mustCreateDraftOnSlice(t, ctx, stores, humanA, sliceRef, "/acme/human-req/file.txt", "hello\n")

	// Claimed agent approval meets total=1 controller approval, but NOT human=1.
	if _, err := stores.Changesets.Approve(ctx, cs.Id, agentB.SubjectID); err != nil {
		t.Fatal(err)
	}
	_, err := stores.Changesets.Submit(ctx, cs.Id, ps.Id)
	wantBlock := "required human approvals not satisfied: requires 1 human approval(s), has 0"
	if err == nil || !strings.Contains(err.Error(), wantBlock) {
		t.Fatalf("Submit error = %v, want %q", err, wantBlock)
	}

	gotCS, err := stores.Changesets.Get(ctx, cs.Id)
	if err != nil {
		t.Fatal(err)
	}
	if gotCS.SubmitBlockedReason != wantBlock {
		t.Fatalf("SubmitBlockedReason = %q, want %q", gotCS.SubmitBlockedReason, wantBlock)
	}
	if !storage.IsRequirementBlock(gotCS.SubmitBlockedReason) {
		t.Fatalf("IsRequirementBlock(%q) = false, want true", gotCS.SubmitBlockedReason)
	}

	// Author self-approval also cannot satisfy required_human_approvals.
	if _, err := stores.Changesets.Approve(ctx, cs.Id, humanA); err != nil {
		t.Fatal(err)
	}
	if _, err := stores.Changesets.Submit(ctx, cs.Id, ps.Id); err == nil || !strings.Contains(err.Error(), wantBlock) {
		t.Fatalf("Submit after author human self-approval = %v, want %q", err, wantBlock)
	}

	// Approving by a distinct human satisfies required_human_approvals and clears submit_blocked_reason.
	if _, err := stores.Changesets.Approve(ctx, cs.Id, humanB); err != nil {
		t.Fatal(err)
	}
	gotAfterApprove, err := stores.Changesets.Get(ctx, cs.Id)
	if err != nil {
		t.Fatal(err)
	}
	if gotAfterApprove.SubmitBlockedReason != "" {
		t.Fatalf("SubmitBlockedReason after human approval = %q, want cleared", gotAfterApprove.SubmitBlockedReason)
	}
	if _, err := stores.Changesets.Submit(ctx, cs.Id, ps.Id); err != nil {
		t.Fatalf("Submit after human approval failed: %v", err)
	}
}
