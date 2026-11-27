package service

import (
	"context"
	"fmt"
	"testing"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestClaimAndMergeLocksEvictWhenIdle(t *testing.T) {
	_, handlers := newMemoryHandlers()
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		daemonID := fmt.Sprintf("daemon-ephemeral-%d", i)
		conn := handlers.Agent.hub.RegisterDaemon(daemonID, daemonID+"/inst", 2)
		handlers.Agent.dispatcher.dispatchQueued(ctx, daemonID)
		handlers.Agent.hub.UnregisterDaemon(conn)
	}
	if got := handlers.Agent.dispatcher.claimLocks.Len(); got != 0 {
		t.Fatalf("claimLocks.Len() = %d, want 0 when idle", got)
	}

	cs := &corev1.Changeset{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
		TargetRef:      storage.DefaultTargetRef,
	}
	unlock, err := handlers.Changeset.lockMergeTarget(ctx, cs)
	if err != nil {
		t.Fatalf("lockMergeTarget() error = %v", err)
	}
	if got := handlers.Changeset.changesetCore.mergeLocks.Len(); got != 1 {
		t.Fatalf("mergeLocks.Len() while held = %d, want 1", got)
	}
	unlock()
	if got := handlers.Changeset.changesetCore.mergeLocks.Len(); got != 0 {
		t.Fatalf("mergeLocks.Len() after unlock = %d, want 0", got)
	}
}
