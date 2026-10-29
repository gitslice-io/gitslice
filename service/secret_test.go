package service

import (
	"testing"

	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

func TestSecretsScopedToFolders(t *testing.T) {
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
		// Paths, plus the deprecated slice refs, which stand for their
		// included paths (a ref without an account means the secret's own).
		Scope: &corev1.SecretScope{Paths: []string{"/acme/services/billing/"}, Slices: []*corev1.SliceRef{paymentRef, {Slice: "docs"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := &corev1.SecretScope{Paths: []string{"/acme/docs", "/acme/payment", "/acme/services/billing"}}
	if !proto.Equal(created.GetScope(), want) || created.GetCreatedBy() != "alice" || created.GetAccount() != "acme" {
		t.Fatalf("CreateSecret = %v", created)
	}
	for _, folder := range []string{"/acme/payment", "/acme/docs/guide", "/acme/services/billing/api"} {
		values, err := mem.Slices.SecretValuesAt(alice, folder)
		if err != nil {
			t.Fatal(err)
		}
		if values["DEPLOY_TOKEN"] != "s3cret" {
			t.Fatalf("secrets at %s = %v", folder, values)
		}
	}

	// Only the account's owners and admins manage secrets; to anyone else a
	// secret does not exist.
	_, err = h.Secret.ListSecrets(bob, &corev1.ListSecretsRequest{Account: "acme"})
	wantCode(t, err, codes.PermissionDenied, "an account writer listing secrets")
	_, err = h.Secret.CreateSecret(bob, &corev1.CreateSecretRequest{Account: "acme", Name: "X", Scope: &corev1.SecretScope{Paths: []string{"/acme"}}})
	wantCode(t, err, codes.PermissionDenied, "an account writer creating a secret")
	_, err = h.Secret.GetSecret(bob, &corev1.GetSecretRequest{Id: created.Id})
	wantCode(t, err, codes.NotFound, "an account writer reading a secret")
	_, err = h.Secret.DeleteSecret(carol, &corev1.DeleteSecretRequest{Id: created.Id})
	wantCode(t, err, codes.NotFound, "an outsider deleting a secret")

	// Folders stay in the account and never get a name twice.
	for label, scope := range map[string]*corev1.SecretScope{
		"another account's slice":  {Slices: []*corev1.SliceRef{{Account: "other", Slice: "tools"}}},
		"a slice that is missing":  {Slices: []*corev1.SliceRef{{Slice: "nope"}}},
		"another account's folder": {Paths: []string{"/other/tools"}},
		"a relative folder":        {Paths: []string{"services"}},
		"an empty scope":           {},
	} {
		_, err = h.Secret.CreateSecret(alice, &corev1.CreateSecretRequest{Account: "acme", Name: "TOKEN", Scope: scope})
		wantCode(t, err, codes.InvalidArgument, label)
	}
	_, err = h.Secret.CreateSecret(alice, &corev1.CreateSecretRequest{Account: "acme", Name: "TOKEN"})
	wantCode(t, err, codes.InvalidArgument, "no scope")
	_, err = h.Secret.CreateSecret(alice, &corev1.CreateSecretRequest{Account: "acme", Name: "bad-name", Scope: &corev1.SecretScope{Paths: []string{"/acme"}}})
	wantCode(t, err, codes.InvalidArgument, "a bad name")
	_, err = h.Secret.CreateSecret(alice, &corev1.CreateSecretRequest{Account: "acme", Name: "DEPLOY_TOKEN", Scope: &corev1.SecretScope{Slices: []*corev1.SliceRef{paymentRef}}})
	wantCode(t, err, codes.FailedPrecondition, "a second DEPLOY_TOKEN on /acme/payment")

	// An update leaves alone what it does not set.
	value := "rotated"
	updated, err := h.Secret.UpdateSecret(alice, &corev1.UpdateSecretRequest{Id: created.Id, Value: &value})
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(updated.GetScope(), want) {
		t.Fatalf("scope after a value update = %v, want %v", updated.GetScope(), want)
	}
	// all_slices stands for the account root.
	updated, err = h.Secret.UpdateSecret(alice, &corev1.UpdateSecretRequest{Id: created.Id, Scope: &corev1.SecretScope{AllSlices: true}})
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(updated.GetScope(), &corev1.SecretScope{Paths: []string{"/acme"}}) {
		t.Fatalf("scope = %v, want [/acme]", updated.GetScope())
	}
	if values, _ := mem.Slices.SecretValuesAt(alice, "/acme/secret"); values["DEPLOY_TOKEN"] != "rotated" {
		t.Fatalf("secrets at /acme/secret = %v, want the rotated account-root value", values)
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

	shared, err := h.Secret.CreateSecret(alice, &corev1.CreateSecretRequest{Account: "acme", Name: "SHARED", Value: "x", Scope: &corev1.SecretScope{Paths: []string{"/acme/payment", "/acme/docs"}}})
	if err != nil {
		t.Fatal(err)
	}
	root, err := h.Secret.CreateSecret(alice, &corev1.CreateSecretRequest{Account: "acme", Name: "ROOT", Value: "r", Scope: &corev1.SecretScope{Paths: []string{"/acme"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Slice.SetSliceSecret(alice, &corev1.SetSliceSecretRequest{Slice: paymentRef, Name: "OWN", Value: "y"}); err != nil {
		t.Fatal(err)
	}
	// The slice call does not change a value other folders share.
	_, err = h.Slice.SetSliceSecret(alice, &corev1.SetSliceSecretRequest{Slice: paymentRef, Name: "SHARED", Value: "z"})
	wantCode(t, err, codes.FailedPrecondition, "setting a shared secret through one slice")

	forAdmin, err := h.Slice.ListSliceSecrets(alice, &corev1.ListSliceSecretsRequest{Slice: paymentRef})
	if err != nil {
		t.Fatal(err)
	}
	wantSecrets := []*corev1.SliceSecret{
		{Name: "OWN", SecretId: forAdmin.GetSecrets()[0].GetSecretId(), Paths: []string{"/acme/payment"}},
		{Name: "ROOT", SecretId: root.Id, Paths: []string{"/acme"}},
		{Name: "SHARED", SecretId: shared.Id, Paths: []string{"/acme/docs", "/acme/payment"}},
	}
	if len(forAdmin.GetSecrets()) != len(wantSecrets) {
		t.Fatalf("ListSliceSecrets for an admin = %v", forAdmin)
	}
	for i := range wantSecrets {
		if !proto.Equal(forAdmin.Secrets[i], wantSecrets[i]) {
			t.Fatalf("ListSliceSecrets for an admin [%d] = %v, want %v", i, forAdmin.Secrets[i], wantSecrets[i])
		}
	}
	forWriter, err := h.Slice.ListSliceSecrets(bob, &corev1.ListSliceSecretsRequest{Slice: paymentRef})
	if err != nil {
		t.Fatal(err)
	}
	if len(forWriter.GetNames()) != 3 || len(forWriter.GetSecrets()) != 0 {
		t.Fatalf("ListSliceSecrets for a writer = %v, want names only", forWriter)
	}
}

func TestRevealSecretsOnlyForAccountAdmins(t *testing.T) {
	mem, h := newSliceMemberHandlers(t)
	mem.AddAccountRole("user_carol", "acme", "writer")
	alice, bob, carol := subjectCtx("user_alice"), subjectCtx("user_bob"), subjectCtx("user_carol")
	if _, err := h.Slice.SetSliceMember(alice, &corev1.SetSliceMemberRequest{Slice: paymentRef, Username: "bob", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Secret.CreateSecret(alice, &corev1.CreateSecretRequest{Account: "acme", Name: "DEPLOY_TOKEN", Value: "s3cret", Scope: &corev1.SecretScope{Paths: []string{"/acme/payment"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Secret.CreateSecret(alice, &corev1.CreateSecretRequest{Account: "acme", Name: "NPM_TOKEN", Value: "npm-value", Scope: &corev1.SecretScope{Paths: []string{"/acme"}}}); err != nil {
		t.Fatal(err)
	}

	byPath, err := h.Secret.RevealSecrets(alice, &corev1.RevealSecretsRequest{Path: "/acme/payment/web", Names: []string{"DEPLOY_TOKEN", "NPM_TOKEN", "NOPE", "DEPLOY_TOKEN"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(byPath.GetValues()) != 2 || byPath.Values["DEPLOY_TOKEN"] != "s3cret" || byPath.Values["NPM_TOKEN"] != "npm-value" {
		t.Fatalf("values = %v", byPath.GetValues())
	}
	if len(byPath.GetMissing()) != 1 || byPath.Missing[0] != "NOPE" {
		t.Fatalf("missing = %v, want [NOPE]", byPath.GetMissing())
	}
	bySlice, err := h.Secret.RevealSliceSecrets(alice, &corev1.RevealSliceSecretsRequest{Slice: paymentRef, Names: []string{"DEPLOY_TOKEN", "NPM_TOKEN"}})
	if err != nil {
		t.Fatal(err)
	}
	if bySlice.Values["DEPLOY_TOKEN"] != "s3cret" || bySlice.Values["NPM_TOKEN"] != "npm-value" {
		t.Fatalf("slice values = %v", bySlice.GetValues())
	}

	// A slice admin and an account writer both receive the secrets inside a
	// check, but cannot read them out.
	_, err = h.Secret.RevealSliceSecrets(bob, &corev1.RevealSliceSecretsRequest{Slice: paymentRef, Names: []string{"DEPLOY_TOKEN"}})
	wantCode(t, err, codes.PermissionDenied, "a slice admin revealing a slice's secret")
	_, err = h.Secret.RevealSecrets(bob, &corev1.RevealSecretsRequest{Path: "/acme/payment", Names: []string{"DEPLOY_TOKEN"}})
	wantCode(t, err, codes.PermissionDenied, "a slice admin revealing a folder's secret")
	_, err = h.Secret.RevealSecrets(carol, &corev1.RevealSecretsRequest{Path: "/acme/payment", Names: []string{"DEPLOY_TOKEN"}})
	wantCode(t, err, codes.PermissionDenied, "an account writer revealing a secret")

	_, err = h.Secret.RevealSecrets(alice, &corev1.RevealSecretsRequest{Path: "/acme/payment"})
	wantCode(t, err, codes.InvalidArgument, "no names")
	_, err = h.Secret.RevealSecrets(alice, &corev1.RevealSecretsRequest{Path: "/acme/payment", Names: []string{"bad-name"}})
	wantCode(t, err, codes.InvalidArgument, "a bad name")
	_, err = h.Secret.RevealSecrets(alice, &corev1.RevealSecretsRequest{Path: "", Names: []string{"DEPLOY_TOKEN"}})
	wantCode(t, err, codes.InvalidArgument, "no path")
}
