package postgres

import (
	"fmt"
	"sort"
	"sync"
	"testing"

	"gitslice.io/gitslice/internal/storage"
)

func TestAppendTriggerRunLogConcurrentLinearSeq(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	created, err := store.Triggers().CreateTriggerRuns(ctx, []storage.TriggerRun{{
		Account:     "acme",
		TriggerID:   "lint",
		Folder:      "/acme/payment",
		Action:      "webhook",
		EventID:     "evt_concurrent_logs",
		EventKind:   "changeset.published",
		TargetRef:   DefaultTargetRef,
		MaxAttempts: 1,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 1 {
		t.Fatalf("created runs = %d, want 1", len(created))
	}
	runID := created[0].ID

	const workers = 16
	seqs := make([]int64, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		i := i
		go func() {
			defer wg.Done()
			seqs[i], errs[i] = store.Triggers().AppendTriggerRunLog(ctx, runID, "stdout", fmt.Sprintf("line %d\n", i))
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d AppendTriggerRunLog error: %v", i, err)
		}
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	for i, seq := range seqs {
		if seq != int64(i+1) {
			t.Fatalf("sorted seqs = %v, want 1..%d", seqs, workers)
		}
	}

	logs, err := store.Triggers().ListTriggerRunLogs(ctx, runID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != workers {
		t.Fatalf("stored logs = %d, want %d", len(logs), workers)
	}
}
