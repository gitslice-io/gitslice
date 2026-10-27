package service

import (
	"testing"

	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

func TestSecretsScopedToSlices(t *testing.T) {
	mem, h := newSliceMemberHandlers(t)
	mem.AddAccountRole("user_bob", "acme", "writer")
	mem.AddPersonalAccount("user_dave", "dave")
	mem.AddAccountRole("user_dave", "other", "owner")
	mem.PutSlice(&corev1.SliceRef{Account: "other", Slice: "tools"}, []string{"/other/tools"}, "private")
	alice, bob, carol := subjectCtx("user_alice"), subjectCtx("user_bob"), subjectCtx("user_carol")

	created, err := h.Secret.CreateSecret(alice, &corev1.CreateSecretRequest{
		Account: "acme",
		Name:    "DEPLOY_TOKEN",
		Value:   "s3cret",
		// A ref without an account means the secret's own.
		Scope: &corev1.SecretScope{Slices: []*corev1.SliceRef{paymentRef, {Slice: "docs"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := &corev1.SecretScope{Slices: []*corev1.SliceRef{docsRef, paymentRef}}
	if !proto.Equal(created.GetScope(), want) || created.GetCreatedBy() != "alice" || created.GetAccount() != "acme" {
		t.Fatalf("CreateSecret = %v", created)
	}
	for _, ref := range []*corev1.SliceRef{paymentRef, docsRef} {
		slice, err := mem.Slices.Resolve(alice, ref)
		if err != nil {
			t.Fatal(err)
		}
		values, err := mem.Slices.GetSliceSecrets(alice, slice.Id)
		if err != nil {
			t.Fatal(err)
		}
		if values["DEPLOY_TOKEN"] != "s3cret" {
			t.Fatalf("%s/%s secrets = %v", ref.Account, ref.Slice, values)
		}
	}

	// Only the account's owners and admins manage secrets; to anyone else a
	// secret does not exist.
	_, err = h.Secret.ListSecrets(bob, &corev1.ListSecretsRequest{Account: "acme"})
	wantCode(t, err, codes.PermissionDenied, "an account writer listing secrets")
	_, err = h.Secret.CreateSecret(bob, &corev1.CreateSecretRequest{Account: "acme", Name: "X", Scope: &corev1.SecretScope{AllSlices: true}})
	wantCode(t, err, codes.PermissionDenied, "an account writer creating a secret")
	_, err = h.Secret.GetSecret(bob, &corev1.GetSecretRequest{Id: created.Id})
	wantCode(t, err, codes.NotFound, "an account writer reading a secret")
	_, err = h.Secret.DeleteSecret(carol, &corev1.DeleteSecretRequest{Id: created.Id})
	wantCode(t, err, codes.NotFound, "an outsider deleting a secret")

	// Scopes stay in the account and never give a slice a name twice.
	_, err = h.Secret.CreateSecret(alice, &corev1.CreateSecretRequest{Account: "acme", Name: "TOKEN", Scope: &corev1.SecretScope{Slices: []*corev1.SliceRef{{Account: "other", Slice: "tools"}}}})
	wantCode(t, err, codes.InvalidArgument, "another account's slice")
	_, err = h.Secret.CreateSecret(alice, &corev1.CreateSecretRequest{Account: "acme", Name: "TOKEN", Scope: &corev1.SecretScope{Slices: []*corev1.SliceRef{{Slice: "nope"}}}})
	wantCode(t, err, codes.InvalidArgument, "a slice that does not exist")
	_, err = h.Secret.CreateSecret(alice, &corev1.CreateSecretRequest{Account: "acme", Name: "TOKEN"})
	wantCode(t, err, codes.InvalidArgument, "no scope")
	_, err = h.Secret.CreateSecret(alice, &corev1.CreateSecretRequest{Account: "acme", Name: "bad-name", Scope: &corev1.SecretScope{AllSlices: true}})
	wantCode(t, err, codes.InvalidArgument, "a bad name")
	_, err = h.Secret.CreateSecret(alice, &corev1.CreateSecretRequest{Account: "acme", Name: "DEPLOY_TOKEN", Scope: &corev1.SecretScope{Slices: []*corev1.SliceRef{paymentRef}}})
	wantCode(t, err, codes.FailedPrecondition, "a second DEPLOY_TOKEN on payment")

	// An update leaves alone what it does not set.
	value := "rotated"
	updated, err := h.Secret.UpdateSecret(alice, &corev1.UpdateSecretRequest{Id: created.Id, Value: &value})
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(updated.GetScope(), want) {
		t.Fatalf("scope after a value update = %v, want %v", updated.GetScope(), want)
	}
	updated, err = h.Secret.UpdateSecret(alice, &corev1.UpdateSecretRequest{Id: created.Id, Scope: &corev1.SecretScope{AllSlices: true}})
	if err != nil {
		t.Fatal(err)
	}
	if !updated.GetScope().GetAllSlices() || len(updated.GetScope().GetSlices()) != 0 {
		t.Fatalf("scope = %v, want all slices", updated.GetScope())
	}
	secretSlice, _ := mem.Slices.Resolve(alice, secretRef)
	if values, _ := mem.Slices.GetSliceSecrets(alice, secretSlice.Id); values["DEPLOY_TOKEN"] != "rotated" {
		t.Fatalf("acme/secret secrets = %v, want the rotated all-slices value", values)
	}

	listed, err := h.Secret.ListSecrets(alice, &corev1.ListSecretsRequest{Account: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.GetSecrets()) != 1 || listed.Secrets[0].GetId() != created.Id {
		t.Fatalf("ListSecrets = %v", listed)
	}
	if _, err := h.Secret.DeleteSecret(alice, &corev1.DeleteSecretRequest{Id: created.Id}); err != nil {
		t.Fatal(err)
	}
	_, err = h.Secret.GetSecret(alice, &corev1.GetSecretRequest{Id: created.Id})
	wantCode(t, err, codes.NotFound, "a deleted secret")
}

func TestListSliceSecretsShowsSourcesToAdmins(t *testing.T) {
	mem, h := newSliceMemberHandlers(t)
	mem.AddAccountRole("user_bob", "acme", "writer")
	alice, bob := subjectCtx("user_alice"), subjectCtx("user_bob")

	shared, err := h.Secret.CreateSecret(alice, &corev1.CreateSecretRequest{Account: "acme", Name: "SHARED", Value: "x", Scope: &corev1.SecretScope{Slices: []*corev1.SliceRef{paymentRef, docsRef}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Slice.SetSliceSecret(alice, &corev1.SetSliceSecretRequest{Slice: paymentRef, Name: "OWN", Value: "y"}); err != nil {
		t.Fatal(err)
	}
	// The slice call does not change a value other slices share.
	_, err = h.Slice.SetSliceSecret(alice, &corev1.SetSliceSecretRequest{Slice: paymentRef, Name: "SHARED", Value: "z"})
	wantCode(t, err, codes.FailedPrecondition, "setting a shared secret through one slice")

	forAdmin, err := h.Slice.ListSliceSecrets(alice, &corev1.ListSliceSecretsRequest{Slice: paymentRef})
	if err != nil {
		t.Fatal(err)
	}
	wantSecrets := []*corev1.SliceSecret{
		{Name: "OWN", SecretId: forAdmin.GetSecrets()[0].GetSecretId()},
		{Name: "SHARED", SecretId: shared.Id, SharedWith: []*corev1.SliceRef{docsRef}},
	}
	if len(forAdmin.GetSecrets()) != 2 || !proto.Equal(forAdmin.Secrets[0], wantSecrets[0]) || !proto.Equal(forAdmin.Secrets[1], wantSecrets[1]) {
		t.Fatalf("ListSliceSecrets for an admin = %v", forAdmin)
	}
	forWriter, err := h.Slice.ListSliceSecrets(bob, &corev1.ListSliceSecretsRequest{Slice: paymentRef})
	if err != nil {
		t.Fatal(err)
	}
	if len(forWriter.GetNames()) != 2 || len(forWriter.GetSecrets()) != 0 {
		t.Fatalf("ListSliceSecrets for a writer = %v, want names only", forWriter)
	}
}
