package memory

import (
	"context"
	"errors"
	"testing"

	"github.com/gitslice-io/gitslice/internal/storage"
	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
)

func TestChangesetStoreDoesNotResolveDeprecatedHandle(t *testing.T) {
	ctx := context.Background()
	stores := New()
	ref := &corev1.SliceRef{Account: "acme", Slice: "payment"}
	if _, err := stores.Slices.Create(ctx, "user_acme", ref, []string{"/acme/payment"}, "private", 0, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := stores.Changesets.Create(ctx, "user_acme", &corev1.CreateChangesetRequest{AuthoringSlice: ref})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := stores.Changesets.Get(ctx, "acme:payment@1"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Get(deprecated handle) error = %v, want ErrNotFound", err)
	}
	if _, err := stores.Changesets.Get(ctx, cs.Id); err != nil {
		t.Fatalf("Get(full id) error = %v", err)
	}
}
