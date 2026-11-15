package service

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"testing"

	"gitslice.io/gitslice/internal/avatar"
	"gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
)

func testPicture(t *testing.T, c color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 40, 30))
	for y := 0; y < 30; y++ {
		for x := 0; x < 40; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestAccountAvatars(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	ann := asUser(t, handlers, "user_ann", "anna")
	bob := asUser(t, handlers, "user_bob", "bobby")
	if _, err := handlers.Auth.CreateOrganization(ann, &corev1.CreateOrganizationRequest{Slug: "labs"}); err != nil {
		t.Fatal(err)
	}

	// Without an upload, an account shows the picture generated from its
	// name, and so does a name no account has.
	data, version := handlers.Auth.AccountAvatar(context.Background(), "anna")
	if version != "" || !bytes.Equal(data, avatar.Pixel("anna")) {
		t.Fatalf("before upload: version %q, generated %v", version, bytes.Equal(data, avatar.Pixel("anna")))
	}
	if data, _ := handlers.Auth.AccountAvatar(context.Background(), "nobody"); !bytes.Equal(data, avatar.Pixel("nobody")) {
		t.Fatal("an unknown name should get its generated picture")
	}

	red := testPicture(t, color.RGBA{R: 255, A: 255})
	profile, err := handlers.Auth.SetAccountAvatar(ann, &corev1.SetAccountAvatarRequest{Account: "anna", Image: red})
	if err != nil {
		t.Fatal(err)
	}
	if len(profile.AvatarVersion) != 16 {
		t.Fatalf("avatar version = %q, want 16 hex digits", profile.AvatarVersion)
	}
	data, version = handlers.Auth.AccountAvatar(context.Background(), "anna")
	if version != profile.AvatarVersion {
		t.Fatalf("served version %q, want %q", version, profile.AvatarVersion)
	}
	if img, err := png.Decode(bytes.NewReader(data)); err != nil || img.Bounds().Dx() != 30 || img.Bounds().Dy() != 30 {
		t.Fatalf("served picture: %v, %v; want the 30x30 center square", err, img)
	}
	// The profile reports it to anyone.
	got, err := handlers.Auth.GetAccountProfile(context.Background(), &corev1.GetAccountProfileRequest{Account: "anna"})
	if err != nil || got.AvatarVersion != profile.AvatarVersion {
		t.Fatalf("profile = %+v, %v", got, err)
	}
	// Editing the rest of the profile keeps the picture.
	if updated, err := handlers.Auth.UpdateAccountProfile(ann, &corev1.UpdateAccountProfileRequest{Account: "anna", DisplayName: "Ann"}); err != nil || updated.AvatarVersion != profile.AvatarVersion {
		t.Fatalf("after a profile edit: %+v, %v", updated, err)
	}

	// A new upload replaces the old one and deletes its object.
	stored, err := mem.Auth.GetAccountProfile(context.Background(), "anna")
	if err != nil {
		t.Fatal(err)
	}
	firstKey := stored.AvatarKey
	blue, err := handlers.Auth.SetAccountAvatar(ann, &corev1.SetAccountAvatarRequest{Account: "anna", Image: testPicture(t, color.RGBA{B: 255, A: 255})})
	if err != nil || blue.AvatarVersion == profile.AvatarVersion {
		t.Fatalf("second upload: %+v, %v", blue, err)
	}
	if _, err := mem.Objects.Get(context.Background(), firstKey, 0, 0); err == nil {
		t.Fatalf("the replaced picture %s is still stored", firstKey)
	}

	// Removing it brings back the generated picture.
	removed, err := handlers.Auth.SetAccountAvatar(ann, &corev1.SetAccountAvatarRequest{Account: "anna"})
	if err != nil || removed.AvatarVersion != "" {
		t.Fatalf("remove: %+v, %v", removed, err)
	}
	if data, version := handlers.Auth.AccountAvatar(context.Background(), "anna"); version != "" || !bytes.Equal(data, avatar.Pixel("anna")) {
		t.Fatal("after removing, the account should show its generated picture")
	}

	// The same people as for the profile: owners and admins for an
	// organization, the person for a personal account.
	if _, err := handlers.Auth.SetAccountAvatar(ann, &corev1.SetAccountAvatarRequest{Account: "labs", Image: red}); err != nil {
		t.Fatalf("an owner setting the org's picture: %v", err)
	}
	_, err = handlers.Auth.SetAccountAvatar(bob, &corev1.SetAccountAvatarRequest{Account: "labs", Image: red})
	wantStatus(t, err, codes.NotFound, "outsider setting an org's picture")
	_, err = handlers.Auth.SetAccountAvatar(bob, &corev1.SetAccountAvatarRequest{Account: "anna", Image: red})
	wantStatus(t, err, codes.PermissionDenied, "setting someone else's picture")
	_, err = handlers.Auth.SetAccountAvatar(context.Background(), &corev1.SetAccountAvatarRequest{Account: "anna", Image: red})
	wantStatus(t, err, codes.Unauthenticated, "signed out")
	_, err = handlers.Auth.SetAccountAvatar(ann, &corev1.SetAccountAvatarRequest{Account: "anna", Image: []byte("<svg/>")})
	wantStatus(t, err, codes.InvalidArgument, "not a picture")
}
