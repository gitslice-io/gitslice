package postgres

import (
	"errors"
	"testing"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestSliceDefinitionValidation(t *testing.T) {
	ref := &corev1.SliceRef{Account: "nic", Slice: "tools"}
	included, visibility, requiredApprovals, requiredChecks, err := validateSliceDefinition(ref, []string{"/nic/tools", "nic/tools"}, "", 1, []string{"unit", "unit"})
	if err != nil {
		t.Fatal(err)
	}
	if visibility != "private" {
		t.Fatalf("visibility = %q, want private", visibility)
	}
	if len(included) != 1 || included[0] != "/nic/tools" {
		t.Fatalf("included = %#v, want [/nic/tools]", included)
	}
	if requiredApprovals != 1 || len(requiredChecks) != 1 || requiredChecks[0] != "unit" {
		t.Fatalf("submit settings = %d %#v, want 1 [unit]", requiredApprovals, requiredChecks)
	}
	if _, _, _, _, err := validateSliceDefinition(ref, []string{"/nic/tools"}, "account", 0, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("account visibility err = %v, want ErrInvalid", err)
	}
	if _, _, _, _, err := validateSliceDefinition(ref, []string{"/other/tools"}, "private", 0, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("outside account err = %v, want ErrInvalid", err)
	}
	if _, _, _, _, err := validateSliceDefinition(ref, []string{"/nic"}, "private", 0, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("custom account-root err = %v, want ErrInvalid", err)
	}
	if _, _, _, _, err := validateSliceDefinition(ref, []string{"/nic/tools,/nic/docs"}, "private", 0, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("comma-separated include err = %v, want ErrInvalid", err)
	}
	if _, _, _, _, err := validateSliceDefinition(ref, []string{"/nic/tools"}, "private", -1, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("negative approvals err = %v, want ErrInvalid", err)
	}
	homeIncluded, _, _, _, err := validateSliceDefinition(&corev1.SliceRef{Account: "nic", Slice: "home"}, []string{"/nic"}, "private", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(homeIncluded) != 1 || homeIncluded[0] != "/nic" {
		t.Fatalf("home included = %#v, want [/nic]", homeIncluded)
	}
}

func TestSliceStoreListPublic(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	before, err := store.Slices().ListPublic(ctx, "acme", "", 50, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, slice := range before {
		if slice.Definition.GetVisibility() == "public" {
			continue
		}
		t.Fatalf("ListPublic returned a non-public slice: %#v", slice)
	}
	all, err := store.Slices().List(ctx, "acme", "", 50, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Slices().Create(ctx, "user_alice", &corev1.SliceRef{Account: "acme", Slice: "open"}, []string{"/acme/open"}, "public", 0, nil); err != nil {
		t.Fatal(err)
	}
	after, err := store.Slices().ListPublic(ctx, "acme", "", 50, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before)+1 || !sliceNamed(after, "open") {
		t.Fatalf("ListPublic after creating a public slice = %#v", after)
	}
	// A page starts after the named slice.
	rest, err := store.Slices().ListPublic(ctx, "acme", "open", 50, false)
	if err != nil {
		t.Fatal(err)
	}
	if sliceNamed(rest, "open") {
		t.Fatalf("a page after \"open\" must not include it: %#v", rest)
	}
	one, err := store.Slices().List(ctx, "acme", "", 1, false)
	if err != nil || len(one) != 1 {
		t.Fatalf("a limit of 1 gave %d slices (%v)", len(one), err)
	}
	allAfter, err := store.Slices().List(ctx, "acme", "", 50, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(allAfter) != len(all)+1 {
		t.Fatalf("List should include the new slice too: %d -> %d", len(all), len(allAfter))
	}
}

func TestSliceIDNoCollisionBetweenHyphenatedAccountsAndSlices(t *testing.T) {
	ctx, store := newPostgresTestStore(t)

	subA, err := store.Auth().EnsureExternalSubject(ctx, "clerk", "ext_alice_hyphen", "alice-h@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Auth().ChooseUsername(ctx, subA, "alice-h"); err != nil {
		t.Fatal(err)
	}
	subAB, err := store.Auth().EnsureExternalSubject(ctx, "clerk", "ext_alice_h_bob", "alice-h-bob@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Auth().ChooseUsername(ctx, subAB, "alice-h-bob"); err != nil {
		t.Fatal(err)
	}

	// Previously ("alice-h", "bob-home") and ("alice-h-bob", "home") both mapped
	// to slice_alice_h_bob_home. Verify both coexist cleanly with distinct IDs.
	created, err := store.Slices().Create(
		ctx,
		subA,
		&corev1.SliceRef{Account: "alice-h", Slice: "bob-home"},
		[]string{"/alice-h/bob-home"},
		"private",
		0,
		nil,
	)
	if err != nil {
		t.Fatalf("Create(alice-h:bob-home) = %v, want nil", err)
	}
	homeSlice, err := store.Slices().Resolve(ctx, &corev1.SliceRef{Account: "alice-h-bob", Slice: "home"})
	if err != nil {
		t.Fatalf("Resolve(alice-h-bob:home) = %v, want nil", err)
	}
	if created.Id == homeSlice.Id {
		t.Fatalf("slice IDs collided: both got %q", created.Id)
	}
	if got := homeSlice.Definition.GetIncludedPaths(); len(got) != 1 || got[0] != "/alice-h-bob" {
		t.Fatalf("alice-h-bob:home included paths = %v, want [/alice-h-bob]", got)
	}
	if got := created.Definition.GetIncludedPaths(); len(got) != 1 || got[0] != "/alice-h/bob-home" {
		t.Fatalf("alice-h:bob-home included paths = %v, want [/alice-h/bob-home]", got)
	}
}

func sliceNamed(slices []*corev1.Slice, name string) bool {
	for _, slice := range slices {
		if slice.Ref.Slice == name {
			return true
		}
	}
	return false
}
