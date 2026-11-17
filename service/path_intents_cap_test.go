package service

import (
	"fmt"
	"testing"

	"gitslice.io/gitslice/internal/intents"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
)

// One subject can't keep more than intents.MaxActivePerSubject intents open in
// a slice: each changeset update matches against every active intent.
func TestPathIntentsAreCappedPerSubject(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	alice := asUser(t, handlers, "user_alice", "alice")
	seedPathInMemoryRepo(t, mem, "commit_intent_cap_base", "/team/app/main.go", "package main\n")
	if _, err := handlers.Auth.CreateOrganization(alice, &corev1.CreateOrganizationRequest{Slug: "team"}); err != nil {
		t.Fatal(err)
	}
	slice, err := handlers.Slice.CreateSlice(alice, &corev1.CreateSliceRequest{
		Ref:           &corev1.SliceRef{Account: "team", Slice: "app"},
		Visibility:    "private",
		IncludedPaths: []string{"/team/app"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < intents.MaxActivePerSubject; i++ {
		if _, err := handlers.Changeset.CreatePathIntent(alice, &corev1.CreatePathIntentRequest{
			Slice:   slice.GetRef(),
			Pattern: fmt.Sprintf("/team/app/dir%d/**", i),
		}); err != nil {
			t.Fatalf("intent %d: %v", i, err)
		}
	}
	_, err = handlers.Changeset.CreatePathIntent(alice, &corev1.CreatePathIntentRequest{
		Slice:   slice.GetRef(),
		Pattern: "/team/app/one-more/**",
	})
	wantCode(t, err, codes.ResourceExhausted, "intent over the per-subject cap")
}
