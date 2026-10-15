package cli_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestStaleDisjointUpdatePreservesFinalState(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	seedWorkspace := t.TempDir()
	loginTestCLI(t, ts, home, seedWorkspace)
	token := readToken(t, home)
	runCLI(t, home, seedWorkspace, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, seedWorkspace, "shared.go", "package payment\nconst Shared = true\n")
	runCLI(t, home, seedWorkspace, "cs", "create", "--title", "seed shared")
	runCLI(t, home, seedWorkspace, "cs", "submit")

	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	runCLI(t, home, workspaceA, "workspace", "init", "acme/payment")
	runCLI(t, home, workspaceB, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspaceA, "shared.go", "package payment\nconst Shared = false\n")
	writeWorkspaceFile(t, workspaceB, "new_disjoint.go", "package payment\nconst NewDisjoint = true\n")
	runCLI(t, home, workspaceA, "cs", "create", "--title", "update shared")
	runCLI(t, home, workspaceB, "cs", "create", "--title", "add disjoint")
	runCLI(t, home, workspaceB, "cs", "submit")
	runCLI(t, home, workspaceA, "cs", "submit")

	cloneDir := cloneSlice(t, ts, token)
	assertProjectedFile(t, cloneDir, "acme/payment/shared.go", "package payment\nconst Shared = false\n")
	assertProjectedFile(t, cloneDir, "acme/payment/new_disjoint.go", "package payment\nconst NewDisjoint = true\n")
}

func TestDeleteUpdateConflicts(t *testing.T) {
	tests := []struct {
		name        string
		firstAction string
	}{
		{name: "delete_then_update", firstAction: "delete"},
		{name: "update_then_delete", firstAction: "update"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := startTestServer(t)
			home := t.TempDir()
			seedWorkspace := t.TempDir()
			loginTestCLI(t, ts, home, seedWorkspace)
			runCLI(t, home, seedWorkspace, "workspace", "init", "acme/payment")
			writeWorkspaceFile(t, seedWorkspace, "du.go", "package payment\nconst Value = 1\n")
			runCLI(t, home, seedWorkspace, "cs", "create", "--title", "seed du")
			runCLI(t, home, seedWorkspace, "cs", "submit")

			deleteWorkspace := t.TempDir()
			updateWorkspace := t.TempDir()
			runCLI(t, home, deleteWorkspace, "workspace", "init", "acme/payment")
			runCLI(t, home, updateWorkspace, "workspace", "init", "acme/payment")
			if err := os.Remove(filepath.Join(deleteWorkspace, "acme", "payment", "du.go")); err != nil {
				t.Fatal(err)
			}
			writeWorkspaceFile(t, updateWorkspace, "acme/payment/du.go", "package payment\nconst Value = 2\n")
			runCLI(t, home, deleteWorkspace, "cs", "create", "--title", "delete du")
			runCLI(t, home, updateWorkspace, "cs", "create", "--title", "update du")

			firstWorkspace := deleteWorkspace
			secondWorkspace := updateWorkspace
			if tt.firstAction == "update" {
				firstWorkspace = updateWorkspace
				secondWorkspace = deleteWorkspace
			}
			runCLI(t, home, firstWorkspace, "cs", "submit")
			_, stderr := runCLIFails(t, home, secondWorkspace, "cs", "submit")
			assertConflictError(t, stderr)
		})
	}
}

func TestSameNewPathConcurrentOnlyOneSubmitSucceeds(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	firstWorkspace := t.TempDir()
	loginTestCLI(t, ts, home, firstWorkspace)

	const workers = 6
	workspaces := make([]string, workers)
	for i := 0; i < workers; i++ {
		workspace := t.TempDir()
		workspaces[i] = workspace
		runCLI(t, home, workspace, "workspace", "init", "acme/payment")
		writeWorkspaceFile(t, workspace, "same_new.go", fmt.Sprintf("package payment\nconst Winner = %d\n", i))
		runCLI(t, home, workspace, "cs", "create", "--title", fmt.Sprintf("same new %d", i))
	}
	var wg sync.WaitGroup
	results := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := runCLIResult(home, workspaces[i], "cs", "submit")
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)

	successes := 0
	conflicts := 0
	for err := range results {
		if err == nil {
			successes++
			continue
		}
		if strings.Contains(err.Error(), "FailedPrecondition") || strings.Contains(err.Error(), "conflict") {
			conflicts++
			continue
		}
		t.Fatalf("unexpected submit error: %v", err)
	}
	if successes != 1 || conflicts != workers-1 {
		t.Fatalf("expected one success and %d conflicts, got successes=%d conflicts=%d", workers-1, successes, conflicts)
	}
}

func TestConcurrentDisjointSubmitFinalProjection(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	firstWorkspace := t.TempDir()
	loginTestCLI(t, ts, home, firstWorkspace)
	token := readToken(t, home)

	const workers = 10
	workspaces := make([]string, workers)
	for i := 0; i < workers; i++ {
		workspace := t.TempDir()
		workspaces[i] = workspace
		runCLI(t, home, workspace, "workspace", "init", "acme/payment")
		rel := fmt.Sprintf("concurrent/f_%02d.go", i)
		body := fmt.Sprintf("package concurrent\nconst File%d = %d\n", i, i)
		writeWorkspaceFile(t, workspace, rel, body)
		runCLI(t, home, workspace, "cs", "create", "--title", fmt.Sprintf("concurrent %02d", i))
	}
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := runCLIResult(home, workspaces[i], "cs", "submit"); err != nil {
				errs <- err
				return
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	cloneDir := cloneSlice(t, ts, token)
	for i := 0; i < workers; i++ {
		rel := fmt.Sprintf("acme/payment/concurrent/f_%02d.go", i)
		body := fmt.Sprintf("package concurrent\nconst File%d = %d\n", i, i)
		assertProjectedFile(t, cloneDir, rel, body)
	}
}

func TestRestartPreservesSubmittedState(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspace, "restart.go", "package payment\n")
	runCLI(t, home, workspace, "cs", "create")
	runCLI(t, home, workspace, "cs", "submit")
	ts.restart(t)
	status := runCLI(t, home, workspace, "cs", "status")
	if !strings.Contains(status, "status: submitted") {
		t.Fatalf("expected submitted state after restart, got:\n%s", status)
	}
}
