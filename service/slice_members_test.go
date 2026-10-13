package service

import (
	"context"
	"testing"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/authz"
	"gitslice.io/gitslice/internal/storage/memory"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// newSliceMemberHandlers has alice owning account acme with a private
// payment slice, a private secret slice and a public docs slice; bob and
// carol have their own accounts and no role in acme.
func newSliceMemberHandlers(t *testing.T) (*memory.Stores, *Handlers) {
	t.Helper()
	mem, h := newMemoryHandlers()
	mem.AddPersonalAccount("user_alice", "alice")
	mem.AddAccountRole("user_alice", "acme", "owner")
	mem.AddPersonalAccount("user_bob", "bob")
	mem.AddPersonalAccount("user_carol", "carol")
	mem.PutSlice(&corev1.SliceRef{Account: "acme", Slice: "payment"}, []string{"/acme/payment"}, "private")
	mem.PutSlice(&corev1.SliceRef{Account: "acme", Slice: "secret"}, []string{"/acme/secret"}, "private")
	mem.PutSlice(&corev1.SliceRef{Account: "acme", Slice: "docs"}, []string{"/acme/docs"}, "public")
	return mem, h
}

func subjectCtx(subject string) context.Context {
	return authctx.WithSubjectID(context.Background(), subject)
}

func wantCode(t *testing.T, err error, code codes.Code, what string) {
	t.Helper()
	if status.Code(err) != code {
		t.Fatalf("%s: err = %v, want %s", what, err, code)
	}
}

var (
	paymentRef = &corev1.SliceRef{Account: "acme", Slice: "payment"}
	secretRef  = &corev1.SliceRef{Account: "acme", Slice: "secret"}
	docsRef    = &corev1.SliceRef{Account: "acme", Slice: "docs"}
)

func TestSliceMembersGrantOneSlice(t *testing.T) {
	mem, h := newSliceMemberHandlers(t)
	bob := subjectCtx("user_bob")

	_, err := h.Slice.ResolveSlice(bob, &corev1.ResolveSliceRequest{Ref: paymentRef})
	wantCode(t, err, codes.PermissionDenied, "bob before sharing")
	_, err = h.Slice.SetSliceMember(bob, &corev1.SetSliceMemberRequest{Slice: paymentRef, Username: "bob", Role: "writer"})
	wantCode(t, err, codes.PermissionDenied, "bob sharing with himself")

	alice := subjectCtx("user_alice")
	_, err = h.Slice.SetSliceMember(alice, &corev1.SetSliceMemberRequest{Slice: paymentRef, Username: "bob", Role: "owner"})
	wantCode(t, err, codes.InvalidArgument, "an account role on a slice")
	_, err = h.Slice.SetSliceMember(alice, &corev1.SetSliceMemberRequest{Slice: paymentRef, Username: "nobody", Role: "writer"})
	wantCode(t, err, codes.NotFound, "an unknown username")
	member, err := h.Slice.SetSliceMember(alice, &corev1.SetSliceMemberRequest{Slice: paymentRef, Username: "Bob", Role: "writer"})
	if err != nil {
		t.Fatal(err)
	}
	if member.Username != "bob" || member.Role != "writer" || member.AddedBy != "alice" || member.Kind != "person" || member.CreatedAt == "" {
		t.Fatalf("member = %+v", member)
	}

	// Bob reads and writes payment, and nothing else of acme's.
	payment, err := h.Slice.ResolveSlice(bob, &corev1.ResolveSliceRequest{Ref: paymentRef})
	if err != nil {
		t.Fatal(err)
	}
	if err := authz.New(mem.Auth).Authorize(bob, "user_bob", payment, authz.ActionWrite); err != nil {
		t.Fatalf("bob cannot write payment: %v", err)
	}
	if authz.New(mem.Auth).Authorize(bob, "user_bob", payment, authz.ActionAdmin) == nil {
		t.Fatal("a writer administers the slice")
	}
	_, err = h.Slice.ResolveSlice(bob, &corev1.ResolveSliceRequest{Ref: secretRef})
	wantCode(t, err, codes.PermissionDenied, "bob reading another slice")
	listed, err := h.Slice.ListSlices(bob, &corev1.ListSlicesRequest{Account: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, slice := range listed.Slices {
		names[slice.Ref.Slice] = true
	}
	if !names["payment"] || !names["docs"] || names["secret"] || len(listed.Slices) != 2 {
		t.Fatalf("bob lists %v", names)
	}
	shared, err := h.Slice.ListSharedSlices(bob, &corev1.ListSharedSlicesRequest{})
	if err != nil || len(shared.Slices) != 1 || shared.Slices[0].Slice.Ref.Slice != "payment" || shared.Slices[0].Role != "writer" {
		t.Fatalf("shared with bob = %+v, %v", shared, err)
	}

	// Files of payment read by path, as gs init does, are bob's to read;
	// secret's are not. A public slice's are anyone's.
	for _, tt := range []struct {
		subject, account, path string
		want                   bool
	}{
		{"user_bob", "acme", "/acme/payment/api.go", true},
		{"user_bob", "acme", "/acme/payment", true},
		{"user_bob", "acme", "/acme/secret/key", false},
		{"user_bob", "acme", "/acme/paymentx/file", false},
		{"user_carol", "acme", "/acme/payment/api.go", false},
		{"user_carol", "acme", "/acme/docs/readme.md", true},
		// Anonymous readers go through the slice, not by path.
		{"", "acme", "/acme/docs/readme.md", false},
	} {
		err := ensurePathRead(context.Background(), mem.Auth, mem.Slices, tt.subject, tt.account, tt.path)
		if (err == nil) != tt.want {
			t.Fatalf("%s reading %s: err = %v, want allowed %v", tt.subject, tt.path, err, tt.want)
		}
	}

	// Members are listed to those with a role, not to a public slice's readers.
	members, err := h.Slice.ListSliceMembers(bob, &corev1.ListSliceMembersRequest{Slice: paymentRef})
	if err != nil || len(members.Members) != 1 || members.Members[0].Username != "bob" {
		t.Fatalf("payment members = %+v, %v", members, err)
	}
	_, err = h.Slice.ListSliceMembers(subjectCtx("user_carol"), &corev1.ListSliceMembersRequest{Slice: paymentRef})
	wantCode(t, err, codes.NotFound, "carol listing a private slice's members")
	_, err = h.Slice.ListSliceMembers(subjectCtx("user_carol"), &corev1.ListSliceMembersRequest{Slice: docsRef})
	wantCode(t, err, codes.PermissionDenied, "carol listing a public slice's members")
}

func TestSliceAdminsManageMembersButNotTheSlice(t *testing.T) {
	_, h := newSliceMemberHandlers(t)
	alice, bob, carol := subjectCtx("user_alice"), subjectCtx("user_bob"), subjectCtx("user_carol")
	if _, err := h.Slice.SetSliceMember(alice, &corev1.SetSliceMemberRequest{Slice: paymentRef, Username: "bob", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	// A slice admin shares the slice further...
	if _, err := h.Slice.SetSliceMember(bob, &corev1.SetSliceMemberRequest{Slice: paymentRef, Username: "carol", Role: "reader"}); err != nil {
		t.Fatal(err)
	}
	// ...but cannot widen it, point it at their own CI daemon, set its
	// secrets, or delete it: those stay with the account.
	payment, err := h.Slice.ResolveSlice(bob, &corev1.ResolveSliceRequest{Ref: paymentRef})
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.Slice.UpdateSliceDefinition(bob, &corev1.UpdateSliceDefinitionRequest{
		SliceId:                payment.Id,
		ExpectedDefinitionHash: payment.DefinitionHash,
		Definition:             &corev1.SliceDefinition{IncludedPaths: []string{"/acme"}, Visibility: "private"},
	})
	wantCode(t, err, codes.PermissionDenied, "a slice admin widening the slice")
	_, err = h.Slice.SetSliceCIDaemon(bob, &corev1.SetSliceCIDaemonRequest{Slice: paymentRef, DaemonId: "agent_bob"})
	wantCode(t, err, codes.PermissionDenied, "a slice admin setting the CI daemon")
	_, err = h.Slice.SetSliceSecret(bob, &corev1.SetSliceSecretRequest{Slice: paymentRef, Name: "TOKEN", Value: "x"})
	wantCode(t, err, codes.PermissionDenied, "a slice admin setting a secret")
	_, err = h.Slice.DeleteSlice(bob, &corev1.DeleteSliceRequest{SliceId: payment.Id})
	wantCode(t, err, codes.PermissionDenied, "a slice admin deleting the slice")

	// A reader cannot remove others, but can leave; removing twice is not found.
	_, err = h.Slice.RemoveSliceMember(carol, &corev1.RemoveSliceMemberRequest{Slice: paymentRef, Username: "bob"})
	wantCode(t, err, codes.PermissionDenied, "a reader removing an admin")
	if _, err := h.Slice.RemoveSliceMember(carol, &corev1.RemoveSliceMemberRequest{Slice: paymentRef}); err != nil {
		t.Fatalf("carol leaving: %v", err)
	}
	if _, err := h.Slice.RemoveSliceMember(alice, &corev1.RemoveSliceMemberRequest{Slice: paymentRef, Username: "bob"}); err != nil {
		t.Fatalf("alice removing bob: %v", err)
	}
	_, err = h.Slice.RemoveSliceMember(alice, &corev1.RemoveSliceMemberRequest{Slice: paymentRef, Username: "bob"})
	wantCode(t, err, codes.NotFound, "removing bob again")
	_, err = h.Slice.ResolveSlice(bob, &corev1.ResolveSliceRequest{Ref: paymentRef})
	wantCode(t, err, codes.PermissionDenied, "bob after removal")
}
