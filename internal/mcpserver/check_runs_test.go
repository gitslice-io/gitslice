package mcpserver

import (
	"testing"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestLatestPatchsetCheckRuns(t *testing.T) {
	runs := []*corev1.CheckRun{
		{CheckName: "build", PatchsetId: "ps_1", Attempt: 1, Status: "failed", CreatedAt: "2026-01-01T00:01:00Z"},
		{CheckName: "test", PatchsetId: "ps_1", Attempt: 1, Status: "passed", CreatedAt: "2026-01-01T00:01:10Z"},
		{CheckName: "build", PatchsetId: "ps_1", Attempt: 2, Status: "passed", CreatedAt: "2026-01-01T00:01:20Z"},
		{CheckName: "lint", PatchsetId: "ps_2", Attempt: 1, Status: "passed", CreatedAt: "2026-01-01T00:01:30Z"},
		nil,
		{CheckName: "deploy", PatchsetId: "ps_1", Attempt: 1, Status: "pending", CreatedAt: "2026-01-01T00:01:05Z"},
		{CheckName: "deploy", PatchsetId: "ps_1", Attempt: 1, Status: "passed", CreatedAt: "2026-01-01T00:01:15Z"}, // tie-break by CreatedAt
	}

	got := LatestPatchsetCheckRuns(runs, "ps_1")
	if len(got) != 3 {
		t.Fatalf("expected 3 checks, got %d", len(got))
	}

	// Should be sorted by CheckName: build, deploy, test
	if got[0].CheckName != "build" || got[0].Attempt != 2 || got[0].Status != "passed" {
		t.Errorf("got[0] = %+v, want build attempt 2 passed", got[0])
	}
	if got[1].CheckName != "deploy" || got[1].CreatedAt != "2026-01-01T00:01:15Z" || got[1].Status != "passed" {
		t.Errorf("got[1] = %+v, want deploy with created_at 2026-01-01T00:01:15Z passed", got[1])
	}
	if got[2].CheckName != "test" || got[2].Attempt != 1 {
		t.Errorf("got[2] = %+v, want test attempt 1", got[2])
	}

	// Without patchset filter: includes all patchsets
	all := LatestPatchsetCheckRuns(runs, "")
	if len(all) != 4 {
		t.Fatalf("expected 4 checks, got %d", len(all))
	}
	if all[2].CheckName != "lint" {
		t.Errorf("all[2] = %+v, want lint", all[2])
	}
}
