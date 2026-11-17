package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func insertPGPersonalUser(t *testing.T, ctx context.Context, store *DB, subjectID, username, displayName string) {
	t.Helper()
	if _, err := store.db.ExecContext(ctx, `
		insert into subjects(id, kind, display_name, controller_subject_id, created_at)
		values ($1, 'user', $2, $1, now())
		on conflict (id) do nothing
	`, subjectID, displayName); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
		insert into accounts(id, slug, kind, display_name, created_at, updated_at)
		values ($1, $2, 'personal', $3, now(), now())
		on conflict (id) do nothing
	`, "acct_"+username, username, displayName); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
		insert into account_memberships(account_id, subject_id, role, created_at)
		values ($1, $2, 'owner', now())
		on conflict do nothing
	`, "acct_"+username, subjectID); err != nil {
		t.Fatal(err)
	}
}

func TestMigration0058AndAccountGroupsStorePostgres(t *testing.T) {
	ctx, store := newPostgresTestStore(t)

	for _, table := range []string{"account_groups", "account_group_members", "slice_group_members"} {
		var exists bool
		if err := store.db.QueryRowContext(ctx, `
			select exists (
				select 1 from information_schema.tables
				where table_schema = current_schema() and table_name = $1
			)
		`, table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("missing table %q after migration 0058", table)
		}
	}

	insertPGPersonalUser(t, ctx, store, "user_alice", "alice", "Alice")
	insertPGPersonalUser(t, ctx, store, "user_eve", "eve", "Eve")
	if err := store.Auth().SetVerifiedEmails(ctx, "user_alice", storage.ProviderClerk, []string{"alice@example.com"}); err != nil {
		t.Fatal(err)
	}
	agent := provisionAgentPGTest(t, ctx, store, "qa-bot", "alice@example.com", "user_alice")

	auth, slices := store.Auth(), store.Slices()

	// Short group names (e.g. "qa") and normalization work; duplicate rejects with ErrConflict.
	gQA, err := auth.CreateGroup(ctx, "acme", "QA", "QA team", "user_alice")
	if err != nil {
		t.Fatal(err)
	}
	if gQA.Handle != "@acme/qa" || gQA.Name != "qa" || gQA.Description != "QA team" {
		t.Fatalf("CreateGroup = %+v", gQA)
	}
	if _, err := auth.CreateGroup(ctx, "acme", "qa", "dup", "user_alice"); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("duplicate CreateGroup = %v, want ErrConflict", err)
	}

	// Add an external user and a claimed agent to @acme/qa.
	gQA, err = auth.AddGroupMember(ctx, "acme", "qa", "user_eve", "user_alice")
	if err != nil {
		t.Fatal(err)
	}
	gQA, err = auth.AddGroupMember(ctx, "acme", "qa", agent.SubjectID, "user_alice")
	if err != nil {
		t.Fatal(err)
	}
	if gQA.MemberCount != 2 || len(gQA.Members) != 2 {
		t.Fatalf("group after 2 adds = %+v", gQA)
	}
	if sid, err := auth.SubjectIDForUsername(ctx, "eve"); err != nil || sid != "user_eve" {
		t.Fatalf("SubjectIDForUsername(eve) = (%q, %v)", sid, err)
	}
	if sid, err := auth.SubjectIDForUsername(ctx, "qa-bot"); err != nil || sid != agent.SubjectID {
		t.Fatalf("SubjectIDForUsername(qa-bot) = (%q, %v)", sid, err)
	}

	// Share slice with @acme/qa as writer -> Eve & qa-bot see it via SliceGroupRole, ListVisible, ListShared.
	gm, err := slices.SetGroupMember(ctx, "slice_acme_payment", gQA.ID, "writer", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if gm.Handle != "@acme/qa" || gm.Role != "writer" {
		t.Fatalf("SetGroupMember = %+v", gm)
	}
	if role, err := auth.SliceGroupRole(ctx, "user_eve", "slice_acme_payment"); err != nil || role != "writer" {
		t.Fatalf("SliceGroupRole(eve) = (%q, %v), want writer", role, err)
	}
	if visible, err := slices.ListVisible(ctx, "acme", "user_eve", "", 50, false); err != nil || len(visible) != 1 || visible[0].Ref.Slice != "payment" {
		t.Fatalf("ListVisible(eve) = %+v, %v", visible, err)
	}
	// Adding a second group with higher role picks highest across direct and group shares.
	gLead, err := auth.CreateGroup(ctx, "acme", "lead", "", "user_alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.AddGroupMember(ctx, "acme", "lead", "user_eve", "user_alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := slices.SetGroupMember(ctx, "slice_acme_payment", gLead.ID, "admin", "alice"); err != nil {
		t.Fatal(err)
	}
	if role, err := auth.SliceGroupRole(ctx, "user_eve", "slice_acme_payment"); err != nil || role != "admin" {
		t.Fatalf("highest SliceGroupRole(eve) = (%q, %v), want admin", role, err)
	}
	if shared, err := slices.ListShared(ctx, "user_eve"); err != nil || len(shared) != 1 || shared[0].Role != "admin" {
		t.Fatalf("ListShared(eve) = %+v, %v", shared, err)
	}

	groupMembers, err := slices.ListGroupMembers(ctx, "slice_acme_payment")
	if err != nil || len(groupMembers) != 2 {
		t.Fatalf("ListGroupMembers = %+v, %v", groupMembers, err)
	}
	if err := slices.RemoveGroupMember(ctx, "slice_acme_payment", gLead.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.RemoveGroupMember(ctx, "acme", "qa", "user_eve"); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.SliceGroupRole(ctx, "user_eve", "slice_acme_payment"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("SliceGroupRole(eve) after removal = %v, want ErrNotFound", err)
	}
	// Deleting group cleans up membership cleanly.
	if err := auth.DeleteGroup(ctx, "acme", "qa"); err != nil {
		t.Fatal(err)
	}
	groups, err := auth.ListGroups(ctx, "acme")
	if err != nil || len(groups) != 1 || groups[0].Handle != "@acme/lead" {
		t.Fatalf("ListGroups after delete = %+v, %v", groups, err)
	}
}

func TestSliceApproversGroupSubmitGatePostgres(t *testing.T) {
	ctx, store, objects := newPostgresTestStoreWithObjects(t)

	author := provisionHumanPGTest(t, ctx, store, "grp_author", "grp-author@example.com")
	outsider := provisionHumanPGTest(t, ctx, store, "grp_outsider", "grp-outsider@example.com")
	revHuman := provisionHumanPGTest(t, ctx, store, "grp_revhuman", "grp-revhuman@example.com")
	ctrl := provisionHumanPGTest(t, ctx, store, "grp_ctrl", "grp-ctrl@example.com")
	agent1 := provisionAgentPGTest(t, ctx, store, "grp-agent1", "grp-ctrl@example.com", ctrl)
	agent2 := provisionAgentPGTest(t, ctx, store, "grp-agent2", "grp-ctrl@example.com", ctrl)

	group, err := store.Auth().CreateGroup(ctx, "acme", "api", "API approvers", author)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{agent1.SubjectID, agent2.SubjectID, revHuman} {
		if _, err := store.Auth().AddGroupMember(ctx, "acme", group.Name, id, author); err != nil {
			t.Fatal(err)
		}
	}

	sliceRef := &corev1.SliceRef{Account: "acme", Slice: "pg-approvers"}
	created, err := store.Slices().CreateDefinition(ctx, author, sliceRef, &corev1.SliceDefinition{
		IncludedPaths:          []string{"/acme/pg-approvers"},
		Visibility:             "private",
		RequiredApprovals:      1,
		RequiredHumanApprovals: 1,
		Approvers:              "@acme/api",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Definition.Approvers != "@acme/api" {
		t.Fatalf("created Approvers = %q, want @acme/api", created.Definition.Approvers)
	}

	versions, err := store.Slices().ListDefinitionVersions(ctx, created.Id, 10)
	if err != nil || len(versions) != 1 || versions[0].Approvers != "@acme/api" {
		t.Fatalf("ListDefinitionVersions = %+v, %v", versions, err)
	}

	ref := getTestRef(t, ctx, store)
	cs, err := store.Changesets().Create(ctx, author, &corev1.CreateChangesetRequest{
		AuthoringSlice: sliceRef,
		TargetRef:      DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "approvers gate test",
	})
	if err != nil {
		t.Fatal(err)
	}
	ps := createPatchsetPGTest(t, ctx, store, objects, cs.Id, "", author, "", "/acme/pg-approvers/a.go", "package a\n")
	if ps.SubmitRequirements.GetApprovers() != "@acme/api" {
		t.Fatalf("patchset SubmitRequirements.Approvers = %q, want @acme/api", ps.SubmitRequirements.GetApprovers())
	}
	gotCreated, err := store.Changesets().Get(ctx, cs.Id)
	if err != nil {
		t.Fatal(err)
	}
	if gotCreated.SubmitRequirements.GetApprovers() != "@acme/api" {
		t.Fatalf("changeset SubmitRequirements.Approvers = %q, want @acme/api", gotCreated.SubmitRequirements.GetApprovers())
	}

	// Outsider human approval does not count for @acme/api.
	if _, err := store.Changesets().Approve(ctx, cs.Id, outsider); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Changesets().Submit(ctx, cs.Id, ps.Id); err == nil || !strings.Contains(err.Error(), "from @acme/api, has 0") {
		t.Fatalf("Submit with outsider approval = %v, want 0 from @acme/api", err)
	}

	// Claimed group agents satisfy total=1 (deduped to ctrl), leaving human gate unmet.
	if _, err := store.Changesets().Approve(ctx, cs.Id, agent1.SubjectID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Changesets().Approve(ctx, cs.Id, agent2.SubjectID); err != nil {
		t.Fatal(err)
	}
	wantHumanBlockPrefix := "required human approvals not satisfied:"
	if _, err := store.Changesets().Submit(ctx, cs.Id, ps.Id); err == nil || !strings.Contains(err.Error(), wantHumanBlockPrefix) || !strings.Contains(err.Error(), "from @acme/api, has 0") {
		t.Fatalf("Submit with group agent approvals = %v, want %q has 0", err, wantHumanBlockPrefix)
	}
	gotCS, err := store.Changesets().Get(ctx, cs.Id)
	if err != nil || !strings.HasPrefix(gotCS.SubmitBlockedReason, wantHumanBlockPrefix) {
		t.Fatalf("SubmitBlockedReason = %q (%v), want prefix %q", gotCS.SubmitBlockedReason, err, wantHumanBlockPrefix)
	}

	// Group human member approves -> clears SubmitBlockedReason and succeeds.
	if _, err := store.Changesets().Approve(ctx, cs.Id, revHuman); err != nil {
		t.Fatal(err)
	}
	gotCS, err = store.Changesets().Get(ctx, cs.Id)
	if err != nil || gotCS.SubmitBlockedReason != "" {
		t.Fatalf("SubmitBlockedReason after group human approval = %q (%v), want empty", gotCS.SubmitBlockedReason, err)
	}
	if _, err := store.Changesets().Submit(ctx, cs.Id, ps.Id); err != nil {
		t.Fatalf("Submit after group human approval failed: %v", err)
	}
}
