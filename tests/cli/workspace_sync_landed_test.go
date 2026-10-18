package cli_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// TestSyncAfterLandingDropsClosedStack: once every changeset in a
// workspace's dependency tree has landed, the tree is closed. The next sync
// moves the workspace to the head and forgets the tree instead of restacking
// it. Sync used to call Restack on the closed tree (which moved its base and
// nothing else) and kept the workspace bound to it.
func TestSyncAfterLandingDropsClosedStack(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspaceA, workspaceB := t.TempDir(), t.TempDir()
	loginTestCLI(t, ts, home, workspaceA)
	runCLI(t, home, workspaceA, "workspace", "init", "acme/payment")
	runCLI(t, home, workspaceB, "workspace", "init", "acme/payment")

	writeWorkspaceFile(t, workspaceA, "acme/payment/landed.go", "package payment\n")
	runCLI(t, home, workspaceA, "create", "--message", "lands", "--all")
	runCLI(t, home, workspaceA, "submit")
	landed := readWorkspaceStackState(t, workspaceA)
	if landed.ActiveStackID == "" || landed.CurrentChangesetID == "" {
		t.Fatalf("workspace state after submit = %+v, want the landed tree", landed)
	}

	// The head moves past the landed commit.
	writeWorkspaceFile(t, workspaceB, "acme/payment/later.go", "package payment\n")
	runCLI(t, home, workspaceB, "cs", "create", "--title", "later")
	runCLI(t, home, workspaceB, "cs", "submit")

	ctx, cancel := context.WithTimeout(grpcAuthContext(readToken(t, home)), 30*time.Second)
	defer cancel()
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	stacks := corev1.NewChangesetStackServiceClient(conn)
	before, err := stacks.GetStack(ctx, &corev1.GetStackRequest{StackId: landed.ActiveStackID})
	if err != nil {
		t.Fatalf("GetStack: %v", err)
	}
	if before.Status != "closed" {
		t.Fatalf("stack status = %q, want closed", before.Status)
	}

	out := runCLI(t, home, workspaceA, "sync")
	if !strings.Contains(out, "landed; workspace is now on the latest commit") {
		t.Fatalf("sync output does not say the changeset landed:\n%s", out)
	}
	assertWorkspaceFile(t, workspaceA, "acme/payment/landed.go", "package payment\n")
	assertWorkspaceFile(t, workspaceA, "acme/payment/later.go", "package payment\n")
	after := readWorkspaceStackState(t, workspaceA)
	if after.ActiveStackID != "" || after.CurrentChangesetID != "" || after.CurrentPatchsetID != "" {
		t.Fatalf("workspace state after sync = %+v, want no tree or changeset", after)
	}
	// Restack moves a tree's base to the target commit, so an unchanged base
	// shows sync did not call it.
	closed, err := stacks.GetStack(ctx, &corev1.GetStackRequest{StackId: landed.ActiveStackID})
	if err != nil {
		t.Fatalf("GetStack: %v", err)
	}
	if closed.BaseCommitId != before.BaseCommitId {
		t.Fatalf("closed stack base moved from %s to %s: sync restacked it", before.BaseCommitId, closed.BaseCommitId)
	}
	if status := runCLI(t, home, workspaceA, "status"); !strings.Contains(status, "status: clean") {
		t.Fatalf("status after sync is not clean:\n%s", status)
	}

	// The next change starts a new tree.
	writeWorkspaceFile(t, workspaceA, "acme/payment/next.go", "package payment\n")
	runCLI(t, home, workspaceA, "create", "--message", "next", "--all")
	if next := readWorkspaceStackState(t, workspaceA); next.ActiveStackID == "" || next.ActiveStackID == landed.ActiveStackID {
		t.Fatalf("next change tree = %q, want a new tree (old %q)", next.ActiveStackID, landed.ActiveStackID)
	}
}

type workspaceStackState struct {
	CurrentChangesetID string `json:"current_changeset_id"`
	CurrentPatchsetID  string `json:"current_patchset_id"`
	BaseCommitID       string `json:"base_commit_id"`
	ActiveStackID      string `json:"active_stack_id"`
}

func readWorkspaceStackState(t *testing.T, workspace string) workspaceStackState {
	t.Helper()
	var state workspaceStackState
	data, err := os.ReadFile(filepath.Join(workspace, ".gs", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	return state
}
