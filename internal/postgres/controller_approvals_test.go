package postgres

import (
	"context"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/objectstore/filesystem"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func provisionHumanPGTest(t *testing.T, ctx context.Context, store *DB, extID, email string) string {
	t.Helper()
	subjectID, err := store.Auth().EnsureExternalSubject(ctx, storage.ProviderClerk, extID, email)
	if err != nil {
		t.Fatalf("EnsureExternalSubject(%s): %v", extID, err)
	}
	if err := store.Auth().SetVerifiedEmails(ctx, subjectID, storage.ProviderClerk, []string{email}); err != nil {
		t.Fatalf("SetVerifiedEmails(%s): %v", subjectID, err)
	}
	return subjectID
}

func provisionAgentPGTest(t *testing.T, ctx context.Context, store *DB, username, ownerEmail, claimerSubjectID string) *storage.RegisteredAgent {
	t.Helper()
	agent, err := store.Auth().RegisterAgent(ctx, storage.RegisterAgentInput{
		Username:    username,
		OwnerEmail:  ownerEmail,
		DisplayName: username,
	})
	if err != nil {
		t.Fatalf("RegisterAgent(%s): %v", username, err)
	}
	if claimerSubjectID != "" {
		if _, err := store.Auth().AcceptClaim(ctx, claimerSubjectID, agent.SubjectID); err != nil {
			t.Fatalf("AcceptClaim(%s, %s): %v", claimerSubjectID, agent.SubjectID, err)
		}
	}
	return agent
}

func createPatchsetPGTest(t *testing.T, ctx context.Context, store *DB, objects *filesystem.Store, csID, expectedPatchsetID, authorSubjectID, kind, path, content string) *corev1.Patchset {
	t.Helper()
	ref := getTestRef(t, ctx, store)
	blobID, contentHash := upsertTestBlobObject(t, ctx, store, objects, content)
	ps, err := store.Changesets().AddPatchset(ctx, csID, expectedPatchsetID, &corev1.Patchset{
		BaseCommitId: ref.CommitId,
		Author:       authorSubjectID,
		Kind:         kind,
		ChangedPaths: []string{path},
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        path,
			BlobId:      blobID,
			ContentHash: contentHash,
			Mode:        0o100644,
		}},
		PathBases: []*corev1.PathBase{pathBaseForTestPath(t, ctx, store, ref.CommitId, path)},
		ReadSet:   []*corev1.PathSetEntry{{Path: path}},
		WriteSet:  []*corev1.PathSetEntry{{Path: path}},
	})
	if err != nil {
		t.Fatalf("AddPatchset: %v", err)
	}
	return ps
}

func TestMigration0053BackfillAndIndexPostgres(t *testing.T) {
	ctx, store := newPostgresTestStore(t)

	var indexExists bool
	if err := store.db.QueryRowContext(ctx, `
		select exists (
			select 1
			from pg_indexes
			where schemaname = current_schema()
			  and tablename = 'subjects'
			  and indexname = 'idx_subjects_controller_subject_id'
		)
	`).Scan(&indexExists); err != nil {
		t.Fatal(err)
	}
	if !indexExists {
		t.Fatal("expected partial index idx_subjects_controller_subject_id on subjects")
	}

	human := provisionHumanPGTest(t, ctx, store, "mig_human", "mig-human@example.com")
	claimedAgent := provisionAgentPGTest(t, ctx, store, "mig-claimed", "mig-human@example.com", human)
	unclaimedAgent := provisionAgentPGTest(t, ctx, store, "mig-unclaimed", "other@example.com", "")

	// Simulate pre-0053 rows where controller_subject_id was NULL and re-run 0053 via Migrate.
	if _, err := store.db.ExecContext(ctx, `
		update subjects
		set controller_subject_id = null
		where id in ($1, $2, $3)
	`, human, claimedAgent.SubjectID, unclaimedAgent.SubjectID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
		delete from schema_migrations
		where filename = '0053_subject_controllers_and_human_approvals.sql'
	`); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("re-running Migrate for 0053 failed: %v", err)
	}

	subjects, err := store.Auth().SubjectsByIDs(ctx, []string{human, claimedAgent.SubjectID, unclaimedAgent.SubjectID})
	if err != nil {
		t.Fatal(err)
	}
	if got := subjects[human]; got.Kind != "user" || got.ControllerSubjectID != human {
		t.Fatalf("backfilled user = %+v, want kind=user controller=%s", got, human)
	}
	if got := subjects[claimedAgent.SubjectID]; got.Kind != "agent" || got.ControllerSubjectID != human {
		t.Fatalf("backfilled claimed agent = %+v, want kind=agent controller=%s", got, human)
	}
	if got := subjects[unclaimedAgent.SubjectID]; got.Kind != "agent" || got.ControllerSubjectID != "" {
		t.Fatalf("unclaimed agent = %+v, want kind=agent controller=\"\"", got)
	}
}

func TestSubjectControllersAndSliceHumanApprovalsPostgres(t *testing.T) {
	ctx, store := newPostgresTestStore(t)

	human := provisionHumanPGTest(t, ctx, store, "ctrl_human", "ctrl@example.com")
	agent := provisionAgentPGTest(t, ctx, store, "ctrl-agent", "ctrl@example.com", "")

	before, err := store.Auth().SubjectForToken(ctx, agent.APIKey)
	if err != nil {
		t.Fatal(err)
	}
	if before.Kind != "agent" || before.ControllerSubjectID != "" {
		t.Fatalf("SubjectForToken before claim = %+v, want unclaimed agent", before)
	}

	if _, err := store.Auth().AcceptClaim(ctx, human, agent.SubjectID); err != nil {
		t.Fatal(err)
	}
	after, err := store.Auth().SubjectForToken(ctx, agent.APIKey)
	if err != nil {
		t.Fatal(err)
	}
	if after.Kind != "agent" || after.ControllerSubjectID != human {
		t.Fatalf("SubjectForToken after claim = %+v, want controller=%s", after, human)
	}

	// Slice CreateDefinition, UpdateDefinition, ListDefinitionVersions roundtrip required_human_approvals.
	ref := &corev1.SliceRef{Account: "acme", Slice: "human-gate-pg"}
	created, err := store.Slices().CreateDefinition(ctx, human, ref, &corev1.SliceDefinition{
		IncludedPaths:          []string{"/acme/human-gate-pg"},
		Visibility:             "private",
		RequiredApprovals:      2,
		RequiredHumanApprovals: 1,
		RequiredChecks:         []string{"unit"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Definition.RequiredHumanApprovals != 1 {
		t.Fatalf("created RequiredHumanApprovals = %d, want 1", created.Definition.RequiredHumanApprovals)
	}

	updated, err := store.Slices().UpdateDefinition(ctx, human, created.Id, created.DefinitionHash, &corev1.SliceDefinition{
		IncludedPaths:          []string{"/acme/human-gate-pg"},
		Visibility:             "private",
		RequiredApprovals:      3,
		RequiredHumanApprovals: 2,
		RequiredChecks:         []string{"unit"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.RequiredHumanApprovals != 2 {
		t.Fatalf("updated RequiredHumanApprovals = %d, want 2", updated.RequiredHumanApprovals)
	}

	resolved, err := store.Slices().Resolve(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Definition.RequiredHumanApprovals != 2 {
		t.Fatalf("resolved RequiredHumanApprovals = %d, want 2", resolved.Definition.RequiredHumanApprovals)
	}

	versions, err := store.Slices().ListDefinitionVersions(ctx, created.Id, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 2 || versions[0].RequiredHumanApprovals != 2 || versions[1].RequiredHumanApprovals != 1 {
		t.Fatalf("ListDefinitionVersions = %+v, want [2, 1]", versions)
	}
}

func TestControllerCountedApprovalsAndHumanGatePostgres(t *testing.T) {
	ctx, store, objects := newPostgresTestStoreWithObjects(t)

	humanA := provisionHumanPGTest(t, ctx, store, "pg_a", "pga@example.com")
	humanB := provisionHumanPGTest(t, ctx, store, "pg_b", "pgb@example.com")
	humanC := provisionHumanPGTest(t, ctx, store, "pg_c", "pgc@example.com")

	unclaimed := provisionAgentPGTest(t, ctx, store, "pg-unclaimed", "other@example.com", "")
	agentA := provisionAgentPGTest(t, ctx, store, "pg-agent-a", "pga@example.com", humanA)
	agentB1 := provisionAgentPGTest(t, ctx, store, "pg-agent-b1", "pgb@example.com", humanB)
	agentB2 := provisionAgentPGTest(t, ctx, store, "pg-agent-b2", "pgb@example.com", humanB)

	// Case 1: controller-counted approvals + unclaimed exclusion.
	sliceTwoRef := &corev1.SliceRef{Account: "acme", Slice: "pg-ctrl-two"}
	if _, err := store.Slices().CreateDefinition(ctx, "user_alice", sliceTwoRef, &corev1.SliceDefinition{
		IncludedPaths:     []string{"/acme/pg-ctrl-two"},
		Visibility:        "private",
		RequiredApprovals: 2,
	}); err != nil {
		t.Fatal(err)
	}
	ref := getTestRef(t, ctx, store)
	cs, err := store.Changesets().Create(ctx, agentA.SubjectID, &corev1.CreateChangesetRequest{
		AuthoringSlice: sliceTwoRef,
		TargetRef:      DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "pg controller counted",
	})
	if err != nil {
		t.Fatal(err)
	}
	ps := createPatchsetPGTest(t, ctx, store, objects, cs.Id, "", agentA.SubjectID, "", "/acme/pg-ctrl-two/a.txt", "a1\n")

	for _, approver := range []string{unclaimed.SubjectID, humanA, agentB1.SubjectID, agentB2.SubjectID} {
		if _, err := store.Changesets().Approve(ctx, cs.Id, approver); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Changesets().Submit(ctx, cs.Id, ps.Id); err == nil || !strings.Contains(err.Error(), "requires 2 distinct non-author approval(s), has 1") {
		t.Fatalf("Submit with unclaimed + owner + 2 same-controller agents = %v, want has 1", err)
	}
	if _, err := store.Changesets().Approve(ctx, cs.Id, humanC); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Changesets().Submit(ctx, cs.Id, ps.Id); err != nil {
		t.Fatalf("Submit after 2nd distinct controller failed: %v", err)
	}

	// Case 2: non-mechanical patchset excludes controller; SubmitMergeKind does NOT.
	sliceOneRef := &corev1.SliceRef{Account: "acme", Slice: "pg-ctrl-one"}
	if _, err := store.Slices().CreateDefinition(ctx, "user_alice", sliceOneRef, &corev1.SliceDefinition{
		IncludedPaths:          []string{"/acme/pg-ctrl-one"},
		Visibility:             "private",
		RequiredApprovals:      1,
		RequiredHumanApprovals: 1,
	}); err != nil {
		t.Fatal(err)
	}
	ref2 := getTestRef(t, ctx, store)
	csMech, err := store.Changesets().Create(ctx, humanA, &corev1.CreateChangesetRequest{
		AuthoringSlice: sliceOneRef,
		TargetRef:      DefaultTargetRef,
		BaseCommitId:   ref2.CommitId,
		Title:          "pg patchset exclusion and human gate",
	})
	if err != nil {
		t.Fatal(err)
	}
	ps1 := createPatchsetPGTest(t, ctx, store, objects, csMech.Id, "", humanA, "", "/acme/pg-ctrl-one/b.txt", "b1\n")
	// Add non-mechanical patchset authored by agentB1 -> excludes humanB.
	ps2 := createPatchsetPGTest(t, ctx, store, objects, csMech.Id, ps1.Id, agentB1.SubjectID, "", "/acme/pg-ctrl-one/b.txt", "b2\n")
	// Add mechanical SubmitMergeKind patchset authored by humanC -> must NOT exclude humanC!
	ps3 := createPatchsetPGTest(t, ctx, store, objects, csMech.Id, ps2.Id, humanC, storage.SubmitMergeKind, "/acme/pg-ctrl-one/b.txt", "b3\n")

	if _, err := store.Changesets().Approve(ctx, csMech.Id, humanB); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Changesets().Submit(ctx, csMech.Id, ps3.Id); err == nil || !strings.Contains(err.Error(), "requires 1 distinct non-author approval(s), has 0") {
		t.Fatalf("Submit with excluded non-mechanical patchset author controller = %v, want has 0", err)
	}

	// Claimed agent from fresh controller satisfies total=1, leaving required human approvals unmet.
	agentD := provisionAgentPGTest(t, ctx, store, "pg-agent-d", "pgd@example.com", provisionHumanPGTest(t, ctx, store, "pg_d", "pgd@example.com"))
	if _, err := store.Changesets().Approve(ctx, csMech.Id, agentD.SubjectID); err != nil {
		t.Fatal(err)
	}
	wantHumanBlock := "required human approvals not satisfied: requires 1 human approval(s), has 0"
	if _, err := store.Changesets().Submit(ctx, csMech.Id, ps3.Id); err == nil || !strings.Contains(err.Error(), wantHumanBlock) {
		t.Fatalf("Submit with agent approval under human gate = %v, want %q", err, wantHumanBlock)
	}
	gotCS, err := store.Changesets().Get(ctx, csMech.Id)
	if err != nil {
		t.Fatal(err)
	}
	if gotCS.SubmitBlockedReason != wantHumanBlock {
		t.Fatalf("SubmitBlockedReason = %q, want %q", gotCS.SubmitBlockedReason, wantHumanBlock)
	}

	// Approve by humanC (whose only patchset was SubmitMergeKind) clears block and allows Submit.
	if _, err := store.Changesets().Approve(ctx, csMech.Id, humanC); err != nil {
		t.Fatal(err)
	}
	gotAfterApprove, err := store.Changesets().Get(ctx, csMech.Id)
	if err != nil {
		t.Fatal(err)
	}
	if gotAfterApprove.SubmitBlockedReason != "" {
		t.Fatalf("SubmitBlockedReason after human approval = %q, want empty", gotAfterApprove.SubmitBlockedReason)
	}
	if _, err := store.Changesets().Submit(ctx, csMech.Id, ps3.Id); err != nil {
		t.Fatalf("Submit after humanC approval failed: %v", err)
	}
}
