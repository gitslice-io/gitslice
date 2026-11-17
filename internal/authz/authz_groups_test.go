package authz

import (
	"context"
	"testing"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

type fakeGroupAuthStore struct {
	storage.AuthStore
	accountRoles map[string]string // account:subject -> role
	sliceRoles   map[string]string // sliceID:subject -> role
	groupRoles   map[string]string // sliceID:subject -> role
}

func (f *fakeGroupAuthStore) AccountRole(_ context.Context, subjectID, account string) (string, error) {
	if role, ok := f.accountRoles[account+":"+subjectID]; ok {
		return role, nil
	}
	return "", storage.ErrNotFound
}

func (f *fakeGroupAuthStore) SliceRole(_ context.Context, subjectID, sliceID string) (string, error) {
	if role, ok := f.sliceRoles[sliceID+":"+subjectID]; ok {
		return role, nil
	}
	return "", storage.ErrNotFound
}

func (f *fakeGroupAuthStore) SliceGroupRole(_ context.Context, subjectID, sliceID string) (string, error) {
	if role, ok := f.groupRoles[sliceID+":"+subjectID]; ok {
		return role, nil
	}
	return "", storage.ErrNotFound
}

func TestAuthorizerCombinesDirectAndGroupSliceRolesWithKeyBounds(t *testing.T) {
	store := &fakeGroupAuthStore{
		accountRoles: map[string]string{},
		sliceRoles: map[string]string{
			"slice_1:user_both": "reader",
			"slice_2:user_both": "admin",
		},
		groupRoles: map[string]string{
			"slice_1:user_both":  "writer",
			"slice_2:user_both":  "contributor",
			"slice_1:user_group": "admin",
		},
	}
	a := New(store)
	ctx := context.Background()
	slice1 := &corev1.Slice{Id: "slice_1", Ref: &corev1.SliceRef{Account: "acme", Slice: "one"}, Definition: &corev1.SliceDefinition{Visibility: "private"}}
	slice2 := &corev1.Slice{Id: "slice_2", Ref: &corev1.SliceRef{Account: "acme", Slice: "two"}, Definition: &corev1.SliceDefinition{Visibility: "private"}}

	// Group writer beats direct reader.
	if role, ok, err := a.EffectiveRole(ctx, "user_both", slice1); err != nil || !ok || role != "writer" {
		t.Fatalf("slice1 user_both role = (%q, %v, %v), want writer", role, ok, err)
	}
	// Direct admin beats group contributor.
	if role, ok, err := a.EffectiveRole(ctx, "user_both", slice2); err != nil || !ok || role != "admin" {
		t.Fatalf("slice2 user_both role = (%q, %v, %v), want admin", role, ok, err)
	}
	// Group admin alone satisfies ActionManageMembers.
	if err := a.Authorize(ctx, "user_group", slice1, ActionManageMembers); err != nil {
		t.Fatalf("group admin Authorize(ManageMembers) = %v, want nil", err)
	}
	// Non-member without group or direct role is rejected.
	if err := a.Authorize(ctx, "user_outsider", slice1, ActionRead); err == nil {
		t.Fatal("outsider unexpectedly allowed on private slice")
	}

	// API key MaxRole caps group-granted role; SliceScope restricts slices even with group access.
	cappedCtx := authctx.WithPrincipal(ctx, authctx.Principal{
		SubjectID: "user_group",
		KeyID:     "key_1",
		MaxRole:   "contributor",
	})
	if role, ok, err := a.EffectiveRole(cappedCtx, "user_group", slice1); err != nil || !ok || role != "contributor" {
		t.Fatalf("key-capped group admin role = (%q, %v, %v), want contributor", role, ok, err)
	}
	if err := a.Authorize(cappedCtx, "user_group", slice1, ActionManageMembers); err == nil {
		t.Fatal("key-capped group admin unexpectedly allowed ActionManageMembers")
	}
	if err := a.Authorize(cappedCtx, "user_group", slice1, ActionPropose); err != nil {
		t.Fatalf("key-capped group admin ActionPropose = %v, want nil", err)
	}

	scopedCtx := authctx.WithPrincipal(ctx, authctx.Principal{
		SubjectID:     "user_group",
		KeyID:         "key_2",
		HasSliceScope: true,
		SliceIDs:      []string{"slice_2"},
	})
	if err := a.Authorize(scopedCtx, "user_group", slice1, ActionRead); err == nil {
		t.Fatal("slice-scoped key unexpectedly allowed unscoped group slice")
	}
}
