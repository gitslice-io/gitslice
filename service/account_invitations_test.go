package service

import (
	"context"
	"fmt"
	"testing"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// asUser signs up a user with a username and returns their context.
func asUser(t *testing.T, handlers *Handlers, subjectID, username string) context.Context {
	t.Helper()
	subjectID, err := handlers.Auth.Auth.EnsureExternalSubject(context.Background(), storage.ProviderClerk, "clerk_"+subjectID, username+"@example.com")
	if err != nil {
		t.Fatal(err)
	}
	ctx := authctx.WithSubjectID(context.Background(), subjectID)
	if _, err := handlers.Auth.ChooseUsername(ctx, &corev1.ChooseUsernameRequest{Username: username}); err != nil {
		t.Fatalf("choose username %s: %v", username, err)
	}
	return ctx
}

func wantStatus(t *testing.T, err error, want codes.Code, what string) {
	t.Helper()
	if status.Code(err) != want {
		t.Fatalf("%s: got %v, want %s", what, err, want)
	}
}

func TestAnyoneWithAUsernameCreatesOrganizations(t *testing.T) {
	_, handlers := newMemoryHandlers()
	ann := asUser(t, handlers, "user_ann", "anna")

	created, err := handlers.Auth.CreateOrganization(ann, &corev1.CreateOrganizationRequest{Slug: "Ann-Labs"})
	if err != nil {
		t.Fatal(err)
	}
	if created.Account != "ann-labs" || len(created.Members) != 1 || created.Members[0].Username != "anna" || created.Members[0].Role != "owner" {
		t.Fatalf("created = %+v", created)
	}
	// It shows up as hers, as an organization she owns.
	authStatus, err := handlers.Auth.GetAuthStatus(ann, &corev1.GetAuthStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range authStatus.Memberships {
		if m.Account == "ann-labs" && m.Kind == "organization" && m.Role == "owner" {
			found = true
		}
	}
	if !found {
		t.Fatalf("memberships = %+v", authStatus.Memberships)
	}

	_, err = handlers.Auth.CreateOrganization(ann, &corev1.CreateOrganizationRequest{Slug: "gitslice"})
	wantStatus(t, err, codes.InvalidArgument, "reserved name")
	_, err = handlers.Auth.CreateOrganization(ann, &corev1.CreateOrganizationRequest{Slug: "ann-labs"})
	wantStatus(t, err, codes.AlreadyExists, "taken name")
	asUser(t, handlers, "user_bob", "bobby")
	_, err = handlers.Auth.CreateOrganization(ann, &corev1.CreateOrganizationRequest{Slug: "shared", OwnerUsernames: []string{"bobby"}})
	wantStatus(t, err, codes.InvalidArgument, "naming another owner")
	if _, err := handlers.Auth.CreateOrganization(ann, &corev1.CreateOrganizationRequest{Slug: "solo", OwnerUsernames: []string{"anna"}}); err != nil {
		t.Fatalf("naming only yourself: %v", err)
	}

	nameless := authctx.WithSubjectID(context.Background(), "user_nameless")
	_, err = handlers.Auth.CreateOrganization(nameless, &corev1.CreateOrganizationRequest{Slug: "nameless-org"})
	wantStatus(t, err, codes.FailedPrecondition, "no username")

	for i := 2; i < maxCreatedOrganizations; i++ {
		if _, err := handlers.Auth.CreateOrganization(ann, &corev1.CreateOrganizationRequest{Slug: fmt.Sprintf("ann-org-%d", i)}); err != nil {
			t.Fatalf("organization %d: %v", i, err)
		}
	}
	_, err = handlers.Auth.CreateOrganization(ann, &corev1.CreateOrganizationRequest{Slug: "one-too-many"})
	wantStatus(t, err, codes.ResourceExhausted, "over the limit")
}

func TestInvitationsAreAcceptedOrDeclined(t *testing.T) {
	_, handlers := newMemoryHandlers()
	ann := asUser(t, handlers, "user_ann", "anna")
	bob := asUser(t, handlers, "user_bob", "bobby")
	cat := asUser(t, handlers, "user_cat", "cathy")
	if _, err := handlers.Auth.CreateOrganization(ann, &corev1.CreateOrganizationRequest{Slug: "labs"}); err != nil {
		t.Fatal(err)
	}

	// Adding a non-member directly is refused; invite them instead.
	_, err := handlers.Auth.SetAccountMember(ann, &corev1.SetAccountMemberRequest{Account: "labs", Username: "bobby", Role: "writer"})
	wantStatus(t, err, codes.FailedPrecondition, "set-member on a non-member")

	invited, err := handlers.Auth.InviteAccountMember(ann, &corev1.InviteAccountMemberRequest{Account: "labs", Username: "bobby", Role: "reader"})
	if err != nil {
		t.Fatal(err)
	}
	if invited.Invitation.Username != "bobby" || invited.Invitation.InvitedBy != "anna" || invited.Invitation.Role != "reader" || invited.Invitation.CreatedAt == "" {
		t.Fatalf("invitation = %+v", invited.Invitation)
	}
	// Inviting again replaces the role.
	if _, err := handlers.Auth.InviteAccountMember(ann, &corev1.InviteAccountMemberRequest{Account: "labs", Username: "bobby", Role: "writer"}); err != nil {
		t.Fatal(err)
	}
	pending, err := handlers.Auth.ListAccountInvitations(ann, &corev1.ListAccountInvitationsRequest{Account: "labs"})
	if err != nil || len(pending.Invitations) != 1 || pending.Invitations[0].Role != "writer" {
		t.Fatalf("org invitations = %+v, %v", pending, err)
	}
	// Not a member until they accept.
	_, err = handlers.Auth.ListAccountMembers(bob, &corev1.ListAccountMembersRequest{Account: "labs"})
	wantStatus(t, err, codes.NotFound, "invitee before accepting")
	mine, err := handlers.Auth.ListMyInvitations(bob, &corev1.ListMyInvitationsRequest{})
	if err != nil || len(mine.Invitations) != 1 || mine.Invitations[0].Account != "labs" {
		t.Fatalf("bob's invitations = %+v, %v", mine, err)
	}
	accepted, err := handlers.Auth.RespondToInvitation(bob, &corev1.RespondToInvitationRequest{Account: "labs", Accept: true})
	if err != nil || accepted.Membership.GetRole() != "writer" || accepted.Membership.GetKind() != "organization" {
		t.Fatalf("accept = %+v, %v", accepted, err)
	}
	members, err := handlers.Auth.ListAccountMembers(bob, &corev1.ListAccountMembersRequest{Account: "labs"})
	if err != nil || len(members.Members) != 2 {
		t.Fatalf("members after accepting = %+v, %v", members, err)
	}
	_, err = handlers.Auth.RespondToInvitation(bob, &corev1.RespondToInvitationRequest{Account: "labs", Accept: true})
	wantStatus(t, err, codes.NotFound, "accepting twice")
	_, err = handlers.Auth.InviteAccountMember(ann, &corev1.InviteAccountMemberRequest{Account: "labs", Username: "bobby", Role: "admin"})
	wantStatus(t, err, codes.AlreadyExists, "inviting a member")

	// A writer cannot invite; an admin cannot invite owners or cancel owner invitations.
	_, err = handlers.Auth.InviteAccountMember(bob, &corev1.InviteAccountMemberRequest{Account: "labs", Username: "cathy", Role: "reader"})
	wantStatus(t, err, codes.PermissionDenied, "writer inviting")
	if _, err := handlers.Auth.SetAccountMember(ann, &corev1.SetAccountMemberRequest{Account: "labs", Username: "bobby", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	_, err = handlers.Auth.InviteAccountMember(bob, &corev1.InviteAccountMemberRequest{Account: "labs", Username: "cathy", Role: "owner"})
	wantStatus(t, err, codes.PermissionDenied, "admin inviting an owner")
	if _, err := handlers.Auth.InviteAccountMember(ann, &corev1.InviteAccountMemberRequest{Account: "labs", Username: "cathy", Role: "owner"}); err != nil {
		t.Fatal(err)
	}
	_, err = handlers.Auth.CancelAccountInvitation(bob, &corev1.CancelAccountInvitationRequest{Account: "labs", Username: "cathy"})
	wantStatus(t, err, codes.PermissionDenied, "admin cancelling an owner invitation")
	if _, err := handlers.Auth.CancelAccountInvitation(ann, &corev1.CancelAccountInvitationRequest{Account: "labs", Username: "cathy"}); err != nil {
		t.Fatal(err)
	}
	_, err = handlers.Auth.RespondToInvitation(cat, &corev1.RespondToInvitationRequest{Account: "labs", Accept: true})
	wantStatus(t, err, codes.NotFound, "accepting a cancelled invitation")

	// Declining removes the invitation without a membership.
	if _, err := handlers.Auth.InviteAccountMember(bob, &corev1.InviteAccountMemberRequest{Account: "labs", Username: "cathy", Role: "reader"}); err != nil {
		t.Fatal(err)
	}
	declined, err := handlers.Auth.RespondToInvitation(cat, &corev1.RespondToInvitationRequest{Account: "labs"})
	if err != nil || declined.Membership != nil {
		t.Fatalf("decline = %+v, %v", declined, err)
	}
	_, err = handlers.Auth.ListAccountMembers(cat, &corev1.ListAccountMembersRequest{Account: "labs"})
	wantStatus(t, err, codes.NotFound, "after declining")

	// Personal accounts take no invitations; outsiders cannot list an org's.
	_, err = handlers.Auth.InviteAccountMember(ann, &corev1.InviteAccountMemberRequest{Account: "anna", Username: "bobby", Role: "reader"})
	wantStatus(t, err, codes.FailedPrecondition, "inviting to a personal account")
	_, err = handlers.Auth.ListAccountInvitations(cat, &corev1.ListAccountInvitationsRequest{Account: "labs"})
	wantStatus(t, err, codes.NotFound, "outsider listing invitations")
	_, err = handlers.Auth.InviteAccountMember(ann, &corev1.InviteAccountMemberRequest{Account: "labs", Username: "nobody", Role: "reader"})
	wantStatus(t, err, codes.InvalidArgument, "inviting an unknown user")
	_, err = handlers.Auth.InviteAccountMember(ann, &corev1.InviteAccountMemberRequest{Account: "labs", Username: "cathy", Role: "boss"})
	wantStatus(t, err, codes.InvalidArgument, "unknown role")
}

func TestAccountProfiles(t *testing.T) {
	_, handlers := newMemoryHandlers()
	ann := asUser(t, handlers, "user_ann", "anna")
	bob := asUser(t, handlers, "user_bob", "bobby")
	if _, err := handlers.Auth.CreateOrganization(ann, &corev1.CreateOrganizationRequest{Slug: "labs"}); err != nil {
		t.Fatal(err)
	}

	updated, err := handlers.Auth.UpdateAccountProfile(ann, &corev1.UpdateAccountProfileRequest{
		Account: "labs", DisplayName: "  Ann's Labs ", Description: "We build things.", Website: "https://labs.example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.DisplayName != "Ann's Labs" || updated.Kind != "organization" || updated.Website != "https://labs.example.com" {
		t.Fatalf("updated = %+v", updated)
	}
	// Anyone, signed in or not, can read it.
	got, err := handlers.Auth.GetAccountProfile(context.Background(), &corev1.GetAccountProfileRequest{Account: "labs"})
	if err != nil || got.Description != "We build things." {
		t.Fatalf("anonymous profile = %+v, %v", got, err)
	}
	_, err = handlers.Auth.GetAccountProfile(context.Background(), &corev1.GetAccountProfileRequest{Account: "nobody"})
	wantStatus(t, err, codes.NotFound, "unknown account")

	_, err = handlers.Auth.UpdateAccountProfile(bob, &corev1.UpdateAccountProfileRequest{Account: "labs", DisplayName: "mine"})
	wantStatus(t, err, codes.NotFound, "outsider editing an org profile")
	_, err = handlers.Auth.UpdateAccountProfile(bob, &corev1.UpdateAccountProfileRequest{Account: "anna", DisplayName: "not ann"})
	wantStatus(t, err, codes.PermissionDenied, "editing someone else's personal profile")
	if _, err := handlers.Auth.UpdateAccountProfile(bob, &corev1.UpdateAccountProfileRequest{Account: "bobby", DisplayName: "Bob"}); err != nil {
		t.Fatalf("editing your own profile: %v", err)
	}

	for what, req := range map[string]*corev1.UpdateAccountProfileRequest{
		"long name":        {Account: "labs", DisplayName: string(make([]rune, 65))},
		"long description": {Account: "labs", Description: string(make([]byte, 281))},
		"not a URL":        {Account: "labs", Website: "labs.example.com"},
		"javascript URL":   {Account: "labs", Website: "javascript:alert(1)"},
	} {
		_, err := handlers.Auth.UpdateAccountProfile(ann, req)
		wantStatus(t, err, codes.InvalidArgument, what)
	}
}
