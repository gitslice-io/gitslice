package authz

import (
	"context"
	"errors"
	"testing"

	"github.com/gitslice-io/gitslice/internal/storage"
	"github.com/gitslice-io/gitslice/internal/storage/memory"
	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
)

func TestAuthorizeReadVisibility(t *testing.T) {
	mem := memory.New()
	mem.AddAccountRole("user_member", "acme", "member")
	authorizer := New(mem.Auth)

	if err := authorizer.Authorize(context.Background(), "user_outsider", authzTestSlice("public"), ActionRead); err != nil {
		t.Fatalf("public read for outsider = %v, want nil", err)
	}
	if err := authorizer.Authorize(context.Background(), "user_outsider", authzTestSlice("private"), ActionRead); !errors.Is(err, storage.ErrUnauthorized) {
		t.Fatalf("private read for outsider = %v, want unauthorized", err)
	}
	if err := authorizer.Authorize(context.Background(), "user_member", authzTestSlice("private"), ActionRead); err != nil {
		t.Fatalf("private read for member = %v, want nil", err)
	}
}

func TestAuthorizeRoleCapabilities(t *testing.T) {
	mem := memory.New()
	mem.AddAccountRole("user_admin", "acme", "admin")
	mem.AddAccountRole("user_writer", "acme", "writer")
	mem.AddAccountRole("user_reader", "acme", "reader")
	authorizer := New(mem.Auth)
	slice := authzTestSlice("private")

	if err := authorizer.Authorize(context.Background(), "user_admin", slice, ActionAdmin); err != nil {
		t.Fatalf("admin action for admin = %v, want nil", err)
	}
	if err := authorizer.Authorize(context.Background(), "user_writer", slice, ActionWrite); err != nil {
		t.Fatalf("write action for writer = %v, want nil", err)
	}
	if err := authorizer.Authorize(context.Background(), "user_writer", slice, ActionAdmin); !errors.Is(err, storage.ErrUnauthorized) {
		t.Fatalf("admin action for writer = %v, want unauthorized", err)
	}
	if err := authorizer.Authorize(context.Background(), "user_reader", slice, ActionWrite); !errors.Is(err, storage.ErrUnauthorized) {
		t.Fatalf("write action for reader = %v, want unauthorized", err)
	}
}

func authzTestSlice(visibility string) *corev1.Slice {
	return &corev1.Slice{
		Ref: &corev1.SliceRef{Account: "acme", Slice: "payment"},
		Definition: &corev1.SliceDefinition{
			IncludedPaths: []string{"/acme/payment"},
			Visibility:    visibility,
		},
	}
}
