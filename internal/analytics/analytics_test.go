package analytics

import (
	"context"
	"testing"
)

func TestNewReturnsNoopClientWhenAPIKeyEmpty(t *testing.T) {
	client, err := New("", "", "")
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if client == nil {
		t.Fatal("New returned nil client")
	}
	if _, ok := client.(noopClient); !ok {
		t.Fatalf("New returned %T, want noopClient", client)
	}

	ctx := context.Background()
	client.Capture(ctx, Event{
		Name:       EventSliceViewed,
		DistinctID: "user_123",
		Props: map[string]any{
			PropSliceID: "slice_123",
		},
	})
	client.Identify(ctx, "user_123", map[string]any{
		PropMethod: "clerk",
	})
	if err := client.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
}

func TestNewReturnsPostHogClientWhenAPIKeyPresent(t *testing.T) {
	client, err := New("phc_test", "", "production")
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if client == nil {
		t.Fatal("New returned nil client")
	}
	if err := client.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
}

func TestWithEnvironmentDoesNotMutateCallerAndSetsEnv(t *testing.T) {
	original := map[string]any{PropSliceID: "slice_123"}
	merged := withEnvironment(original, "staging")

	if merged[PropEnvironment] != "staging" {
		t.Fatalf("environment = %v, want staging", merged[PropEnvironment])
	}
	if merged[PropSliceID] != "slice_123" {
		t.Fatalf("slice_id = %v, want slice_123", merged[PropSliceID])
	}
	if _, ok := original[PropEnvironment]; ok {
		t.Fatal("withEnvironment mutated the caller's map")
	}
}
