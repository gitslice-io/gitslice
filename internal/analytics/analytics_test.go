package analytics

import (
	"context"
	"testing"
)

func TestNewReturnsNoopClientWhenAPIKeyEmpty(t *testing.T) {
	client, err := New("", "")
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
	client, err := New("phc_test", "")
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
