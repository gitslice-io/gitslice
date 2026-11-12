package cli

import "testing"

func TestSyncConflictsExitCode(t *testing.T) {
	if err := syncConflictsError(0); err != nil {
		t.Fatalf("no conflicts: %v", err)
	}
	if code := exitCodeForError(syncConflictsError(2)); code != 3 {
		t.Fatalf("exit code = %d, want 3", code)
	}
}
