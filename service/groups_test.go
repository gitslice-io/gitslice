package service

import (
	"context"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/objectstore/filesystem"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
)

func seedPathInMemoryRepo(t *testing.T, mem interface {
	PutObject(key string, data []byte)
	PutCommitWithFiles(commitID string, files []storage.FileEntry, changedPaths []string)
}, commitID, filePath, content string) {
	t.Helper()
	data := []byte(content)
	hash := objectid.RawContentHash(data)
	mem.PutObject(filesystem.BlobKey(hash), data)
	mem.PutCommitWithFiles(commitID, []storage.FileEntry{{
		Path:        filePath,
		BlobID:      objectid.BlobID(data),
		ContentHash: hash,
		Mode:        0o100644,
		Size:        int64(len(data)),
	}}, []string{filePath})
}

func TestAccountGroupCRUDAndPermissions(t *testing.T) {
	_, handlers := newMemoryHandlers()
	alice := asUser(t, handlers, "user_owner", "alice")
	writer := asUser(t, handlers, "user_writer", "writerbob")
	extAdmin := asUser(t, handlers, "user_ext_admin", "extadmin")
	outsider := asUser(t, handlers, "user_outsider", "outsider")

	if _, err := handlers.Auth.CreateOrganization(alice, &corev1.CreateOrganizationRequest{Slug: "team"}); err != nil {
		t.Fatal(err)
	}
	if _, err := handlers.Auth.InviteAccountMember(alice, &corev1.InviteAccountMemberRequest{Account: "team", Username: "writerbob", Role: "writer"}); err != nil {
		t.Fatal(err)
	}
	if _, err := handlers.Auth.RespondToInvitation(writer, &corev1.RespondToInvitationRequest{Account: "team", Accept: true}); err != nil {
		t.Fatal(err)
	}

	// Account writer cannot create a group; account owner can.
	_, err := handlers.Auth.CreateGroup(writer, &corev1.CreateGroupRequest{Account: "team", Name: "qa"})
	wantStatus(t, err, codes.PermissionDenied, "writer CreateGroup")

	created, err := handlers.Auth.CreateGroup(alice, &corev1.CreateGroupRequest{
		Name:        "@team/QA",
		Description: "Quality approvers",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Handle != "@team/qa" || created.Account != "team" || created.Name != "qa" {
		t.Fatalf("CreateGroup = %+v", created)
	}

	// Create a delegated agent owned by alice and add both outsider (non-org-member)
	// and delegated agent to @team/qa.
	agentResp, err := handlers.Auth.CreateDelegatedAgent(alice, &corev1.CreateDelegatedAgentRequest{
		Username: "qa-agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handlers.Auth.AddGroupMember(alice, &corev1.AddGroupMemberRequest{
		Name:     "@team/qa",
		Username: "outsider",
	}); err != nil {
		t.Fatal(err)
	}
	updated, err := handlers.Auth.AddGroupMember(alice, &corev1.AddGroupMemberRequest{
		Account:  "team",
		Name:     "qa",
		Username: "qa-agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.MemberCount != 2 || len(updated.Members) != 2 {
		t.Fatalf("AddGroupMember = %+v", updated)
	}

	// Account writer can read groups; non-member outsider (even though in group) cannot read account groups.
	if listResp, err := handlers.Auth.ListGroups(writer, &corev1.ListGroupsRequest{Account: "team"}); err != nil || len(listResp.Groups) != 1 {
		t.Fatalf("writer ListGroups = (%+v, %v)", listResp, err)
	}
	_, err = handlers.Auth.GetGroup(outsider, &corev1.GetGroupRequest{Account: "team", Name: "qa"})
	wantStatus(t, err, codes.PermissionDenied, "outsider GetGroup")

	// External slice admin CAN read groups of account "team", unless API key is reader-capped!
	if _, err := handlers.Slice.SetSliceMember(alice, &corev1.SetSliceMemberRequest{
		Slice:    &corev1.SliceRef{Account: "team", Slice: "home"},
		Username: "extadmin",
		Role:     "admin",
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := handlers.Auth.GetGroup(extAdmin, &corev1.GetGroupRequest{Name: "@team/qa"}); err != nil || got.Handle != "@team/qa" {
		t.Fatalf("external slice admin GetGroup = (%+v, %v)", got, err)
	}
	extAdminID, _ := authctx.SubjectID(extAdmin)
	readerCappedExt := authctx.WithPrincipal(context.Background(), authctx.Principal{
		SubjectID:   extAdminID,
		SubjectKind: "user",
		KeyID:       "key_r",
		MaxRole:     "reader",
	})
	_, err = handlers.Auth.ListGroups(readerCappedExt, &corev1.ListGroupsRequest{Account: "team"})
	wantStatus(t, err, codes.PermissionDenied, "reader-capped external slice admin ListGroups")

	// Writer cannot add/remove/delete.
	_, err = handlers.Auth.RemoveGroupMember(writer, &corev1.RemoveGroupMemberRequest{Name: "@team/qa", Username: agentResp.Account})
	wantStatus(t, err, codes.PermissionDenied, "writer RemoveGroupMember")
	_, err = handlers.Auth.DeleteGroup(writer, &corev1.DeleteGroupRequest{Name: "@team/qa"})
	wantStatus(t, err, codes.PermissionDenied, "writer DeleteGroup")

	if _, err := handlers.Auth.RemoveGroupMember(alice, &corev1.RemoveGroupMemberRequest{Name: "@team/qa", Username: "qa-agent"}); err != nil {
		t.Fatal(err)
	}
	if _, err := handlers.Auth.DeleteGroup(alice, &corev1.DeleteGroupRequest{Name: "@team/qa"}); err != nil {
		t.Fatal(err)
	}
}

func TestSliceGroupMembersAndApproversSubmitEnforcement(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	alice := asUser(t, handlers, "owner_alice", "alice")
	bobby := asUser(t, handlers, "user_bobby", "bobby")
	carol := asUser(t, handlers, "user_carol", "carol")
	aliceID, _ := authctx.SubjectID(alice)
	carolID, _ := authctx.SubjectID(carol)

	if _, err := handlers.Auth.CreateOrganization(alice, &corev1.CreateOrganizationRequest{Slug: "labs"}); err != nil {
		t.Fatal(err)
	}
	seedPathInMemoryRepo(t, mem, "commit_base", "/labs/api/main.go", "package api\n")
	if _, err := handlers.Auth.CreateGroup(alice, &corev1.CreateGroupRequest{Name: "@labs/api", Description: "API team"}); err != nil {
		t.Fatal(err)
	}
	aliceBot, err := handlers.Auth.CreateDelegatedAgent(alice, &corev1.CreateDelegatedAgentRequest{Username: "alice-bot"})
	if err != nil {
		t.Fatal(err)
	}
	carolBot, err := handlers.Auth.CreateDelegatedAgent(carol, &corev1.CreateDelegatedAgentRequest{Username: "carol-bot"})
	if err != nil {
		t.Fatal(err)
	}
	// Add carolBot (NOT carol herself!), bobby, and aliceBot to @labs/api.
	for _, member := range []string{carolBot.Account, "bobby", aliceBot.Account} {
		if _, err := handlers.Auth.AddGroupMember(alice, &corev1.AddGroupMemberRequest{Name: "@labs/api", Username: member}); err != nil {
			t.Fatal(err)
		}
	}

	// Creating slice with nonexistent approvers group is rejected.
	_, err = handlers.Slice.CreateSlice(alice, &corev1.CreateSliceRequest{
		Ref:               &corev1.SliceRef{Account: "labs", Slice: "api"},
		IncludedPaths:     []string{"/labs/api"},
		Visibility:        "private",
		RequiredApprovals: 1,
		Approvers:         "@labs/missing",
	})
	wantStatus(t, err, codes.NotFound, "CreateSlice missing approvers group")

	slice, err := handlers.Slice.CreateSlice(alice, &corev1.CreateSliceRequest{
		Ref:                    &corev1.SliceRef{Account: "labs", Slice: "api"},
		IncludedPaths:          []string{"/labs/api"},
		Visibility:             "private",
		RequiredApprovals:      1,
		RequiredHumanApprovals: 1,
		Approvers:              "@labs/api",
	})
	if err != nil {
		t.Fatal(err)
	}
	if slice.Definition.Approvers != "@labs/api" {
		t.Fatalf("slice Approvers = %q, want @labs/api", slice.Definition.Approvers)
	}

	// Share slice with @labs/api as writer -> bobby and carol-bot gain access + see it in ListSharedSlices.
	if _, err := handlers.Slice.SetSliceMember(alice, &corev1.SetSliceMemberRequest{
		Slice:    slice.Ref,
		Username: "@labs/api",
		Role:     "writer",
	}); err != nil {
		t.Fatal(err)
	}
	membersResp, err := handlers.Slice.ListSliceMembers(alice, &corev1.ListSliceMembersRequest{Slice: slice.Ref})
	if err != nil || len(membersResp.GroupMembers) != 1 || membersResp.GroupMembers[0].Handle != "@labs/api" || membersResp.GroupMembers[0].Role != "writer" {
		t.Fatalf("ListSliceMembers = %+v, %v", membersResp, err)
	}
	sharedBobby, err := handlers.Slice.ListSharedSlices(bobby, &corev1.ListSharedSlicesRequest{})
	if err != nil || len(sharedBobby.Slices) != 1 || sharedBobby.Slices[0].Role != "writer" {
		t.Fatalf("bobby ListSharedSlices = %+v, %v", sharedBobby, err)
	}

	cs, err := handlers.Changeset.CreateChangeset(alice, &corev1.CreateChangesetRequest{
		AuthoringSlice: slice.Ref,
		TargetRef:      storage.DefaultTargetRef,
		BaseCommitId:   "commit_base",
		Title:          "enforce approvers group",
	})
	if err != nil {
		t.Fatal(err)
	}
	blob, err := handlers.Blob.UploadBlob(alice, &corev1.UploadBlobRequest{
		Data:  []byte("package api\n// updated\n"),
		Slice: slice.Ref,
	})
	if err != nil {
		t.Fatal(err)
	}
	ps, err := handlers.Changeset.UpdateChangeset(alice, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: "commit_base",
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        "/labs/api/main.go",
			BlobId:      blob.BlobId,
			ContentHash: blob.ContentHash,
			Mode:        0o100644,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ps.SubmitRequirements.GetApprovers() != "@labs/api" {
		t.Fatalf("patchset SubmitRequirements.Approvers = %q, want @labs/api", ps.SubmitRequirements.GetApprovers())
	}

	// Share directly with carol (NOT in @labs/api) so she can call Approve.
	if _, err := handlers.Slice.SetSliceMember(alice, &corev1.SetSliceMemberRequest{Slice: slice.Ref, Username: "carol", Role: "writer"}); err != nil {
		t.Fatal(err)
	}
	if _, err := handlers.Changeset.ApproveChangeset(carol, &corev1.ApproveChangesetRequest{ChangesetId: cs.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := handlers.Changeset.SubmitChangeset(alice, &corev1.SubmitChangesetRequest{ChangesetId: cs.Id}); err == nil || !strings.Contains(err.Error(), "from @labs/api, has 0") {
		t.Fatalf("submit with non-group human = %v, want has 0 from @labs/api", err)
	}

	aliceBotCtx := authctx.WithPrincipal(context.Background(), authctx.Principal{
		SubjectID:           aliceBot.AgentSubjectId,
		SubjectKind:         "agent",
		ControllerSubjectID: aliceID,
	})
	if _, err := handlers.Changeset.ApproveChangeset(aliceBotCtx, &corev1.ApproveChangesetRequest{ChangesetId: cs.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := handlers.Changeset.SubmitChangeset(alice, &corev1.SubmitChangesetRequest{ChangesetId: cs.Id}); err == nil || !strings.Contains(err.Error(), "from @labs/api, has 0") {
		t.Fatalf("submit with author's own agent = %v, want has 0 from @labs/api", err)
	}

	carolBotCtx := authctx.WithPrincipal(context.Background(), authctx.Principal{
		SubjectID:           carolBot.AgentSubjectId,
		SubjectKind:         "agent",
		ControllerSubjectID: carolID,
	})
	if _, err := handlers.Changeset.ApproveChangeset(carolBotCtx, &corev1.ApproveChangesetRequest{ChangesetId: cs.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := handlers.Changeset.SubmitChangeset(alice, &corev1.SubmitChangesetRequest{ChangesetId: cs.Id}); err == nil || !strings.Contains(err.Error(), "required human approvals not satisfied:") || !strings.Contains(err.Error(), "from @labs/api, has 0") {
		t.Fatalf("submit with group agent under human gate = %v, want human approval error", err)
	}

	if _, err := handlers.Changeset.ApproveChangeset(bobby, &corev1.ApproveChangesetRequest{ChangesetId: cs.Id}); err != nil {
		t.Fatal(err)
	}
	subResp, err := handlers.Changeset.SubmitChangeset(alice, &corev1.SubmitChangesetRequest{ChangesetId: cs.Id})
	if err != nil {
		t.Fatalf("SubmitChangeset after group human approval failed: %v", err)
	}
	if subResp.Status != "submitted" && subResp.Status != "pending_publish" {
		t.Fatalf("status = %q, want submitted or pending_publish", subResp.Status)
	}

	// Revoking @labs/api from slice removes bobby's group access.
	if _, err := handlers.Slice.RemoveSliceMember(alice, &corev1.RemoveSliceMemberRequest{Slice: slice.Ref, Username: "@labs/api"}); err != nil {
		t.Fatal(err)
	}
	_, err = handlers.Slice.ResolveSlice(bobby, &corev1.ResolveSliceRequest{Ref: slice.Ref})
	wantStatus(t, err, codes.PermissionDenied, "bobby after group share revoked")
}
