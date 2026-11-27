package postgres

import (
	"errors"
	"testing"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestSliceDefinitionValidation(t *testing.T) {
	ref := &corev1.SliceRef{Account: "nic", Slice: "tools"}
	def, err := storage.ValidateSliceDefinition(ref, &corev1.SliceDefinition{
		IncludedPaths:     []string{"/nic/tools", "nic/tools"},
		RequiredApprovals: 1,
		RequiredChecks:    []string{"unit", "unit"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if def.Visibility != "private" {
		t.Fatalf("visibility = %q, want private", def.Visibility)
	}
	if len(def.IncludedPaths) != 1 || def.IncludedPaths[0] != "/nic/tools" {
		t.Fatalf("included = %#v, want [/nic/tools]", def.IncludedPaths)
	}
	if def.RequiredApprovals != 1 || len(def.RequiredChecks) != 1 || def.RequiredChecks[0] != "unit" {
		t.Fatalf("submit settings = %d %#v, want 1 [unit]", def.RequiredApprovals, def.RequiredChecks)
	}
	if _, err := storage.ValidateSliceDefinition(ref, &corev1.SliceDefinition{IncludedPaths: []string{"/nic/tools"}, Visibility: "account"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("account visibility err = %v, want ErrInvalid", err)
	}
	if _, err := storage.ValidateSliceDefinition(ref, &corev1.SliceDefinition{IncludedPaths: []string{"/other/tools"}, Visibility: "private"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("outside account err = %v, want ErrInvalid", err)
	}
	if _, err := storage.ValidateSliceDefinition(ref, &corev1.SliceDefinition{IncludedPaths: []string{"/nic"}, Visibility: "private"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("custom account-root err = %v, want ErrInvalid", err)
	}
	if _, err := storage.ValidateSliceDefinition(ref, &corev1.SliceDefinition{IncludedPaths: []string{"/nic/tools,/nic/docs"}, Visibility: "private"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("comma-separated include err = %v, want ErrInvalid", err)
	}
	if _, err := storage.ValidateSliceDefinition(ref, &corev1.SliceDefinition{IncludedPaths: []string{"/nic/tools"}, Visibility: "private", RequiredApprovals: -1}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("negative approvals err = %v, want ErrInvalid", err)
	}
	homeDef, err := storage.ValidateSliceDefinition(&corev1.SliceRef{Account: "nic", Slice: "home"}, &corev1.SliceDefinition{IncludedPaths: []string{"/nic"}, Visibility: "private"})
	if err != nil {
		t.Fatal(err)
	}
	if len(homeDef.IncludedPaths) != 1 || homeDef.IncludedPaths[0] != "/nic" {
		t.Fatalf("home included = %#v, want [/nic]", homeDef.IncludedPaths)
	}
}

func TestSliceStoreListPublic(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	before, _, err := store.Slices().ListSorted(ctx, "acme", "public", "", "", 50, false, storage.SliceSortName)
	if err != nil {
		t.Fatal(err)
	}
	for _, slice := range before {
		if slice.Definition.GetVisibility() == "public" {
			continue
		}
		t.Fatalf("ListPublic returned a non-public slice: %#v", slice)
	}
	all, _, err := store.Slices().ListSorted(ctx, "acme", "all", "", "", 50, false, storage.SliceSortName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Slices().Create(ctx, "user_alice", &corev1.SliceRef{Account: "acme", Slice: "open"}, []string{"/acme/open"}, "public", 0, nil); err != nil {
		t.Fatal(err)
	}
	after, _, err := store.Slices().ListSorted(ctx, "acme", "public", "", "", 50, false, storage.SliceSortName)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before)+1 || !sliceNamed(after, "open") {
		t.Fatalf("ListPublic after creating a public slice = %#v", after)
	}
	// A page starts after the named slice.
	rest, _, err := store.Slices().ListSorted(ctx, "acme", "public", "", "open", 50, false, storage.SliceSortName)
	if err != nil {
		t.Fatal(err)
	}
	if sliceNamed(rest, "open") {
		t.Fatalf("a page after \"open\" must not include it: %#v", rest)
	}
	one, _, err := store.Slices().ListSorted(ctx, "acme", "all", "", "", 1, false, storage.SliceSortName)
	if err != nil || len(one) != 1 {
		t.Fatalf("a limit of 1 gave %d slices (%v)", len(one), err)
	}
	allAfter, _, err := store.Slices().ListSorted(ctx, "acme", "all", "", "", 50, false, storage.SliceSortName)
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
