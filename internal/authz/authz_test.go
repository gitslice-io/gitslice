package authz

import (
	"context"
	"errors"
	"testing"

	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/storage/memory"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestAuthorizeReadVisibility(t *testing.T) {
	mem := memory.New()
	mem.AddAccountRole("user_member", "acme", "member")
	authorizer := New(mem.Auth)

	if err := authorizer.Authorize(context.Background(), "", authzTestSlice("public"), ActionRead); err != nil {
		t.Fatalf("public read for anonymous subject = %v, want nil", err)
	}
	if err := authorizer.Authorize(context.Background(), "user_outsider", authzTestSlice("public"), ActionRead); err != nil {
		t.Fatalf("public read for outsider = %v, want nil", err)
	}
	if err := authorizer.Authorize(context.Background(), "", authzTestSlice("private"), ActionRead); !errors.Is(err, storage.ErrUnauthenticated) {
		t.Fatalf("private read for anonymous subject = %v, want unauthenticated", err)
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

func TestAuthorizeArchivedBlocksMutations(t *testing.T) {
	mem := memory.New()
	mem.AddAccountRole("user_admin", "acme", "admin")
	authorizer := New(mem.Auth)
	slice := authzTestSlice("private")
	slice.Archived = true

	if err := authorizer.Authorize(context.Background(), "user_admin", slice, ActionRead); err != nil {
		t.Fatalf("archived read for admin = %v, want nil", err)
	}
	if err := authorizer.Authorize(context.Background(), "user_admin", slice, ActionWrite); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("archived write for admin = %v, want ErrConflict", err)
	}
	if err := authorizer.Authorize(context.Background(), "user_admin", slice, ActionAdmin); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("archived admin for admin = %v, want ErrConflict", err)
	}
	if err := authorizer.AuthorizeAllowArchived(context.Background(), "user_admin", slice, ActionAdmin); err != nil {
		t.Fatalf("AuthorizeAllowArchived for admin = %v, want nil", err)
	}
	if err := authorizer.Authorize(context.Background(), "user_outsider", slice, ActionAdmin); !errors.Is(err, storage.ErrUnauthorized) {
		t.Fatalf("archived admin for outsider = %v, want ErrUnauthorized", err)
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
