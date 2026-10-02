package cli_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// TestWorkspaceSyncsDependencyTreeRepeatedly checks that a workspace whose
// draft lives in a dependency tree (gs create) can sync more than once. Sync
// restacks the tree onto the new head. The tree's base commit used to stay
// at the old head, so the next sync or modify failed with "workspace base does
// not match the active dependency tree base".
func TestWorkspaceSyncsDependencyTreeRepeatedly(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	loginTestCLI(t, ts, home, workspaceA)
	runCLI(t, home, workspaceA, "workspace", "init", "acme/payment")
	runCLI(t, home, workspaceB, "workspace", "init", "acme/payment")

	writeWorkspaceFile(t, workspaceA, "acme/payment/draft.go", "package payment\nconst Draft = 1\n")
	runCLI(t, home, workspaceA, "create", "--message", "draft in a tree", "--all")

	for _, name := range []string{"first.go", "second.go"} {
		writeWorkspaceFile(t, workspaceB, "acme/payment/"+name, "package payment\n")
		runCLI(t, home, workspaceB, "cs", "create", "--title", "remote "+name)
		runCLI(t, home, workspaceB, "cs", "submit")
		runCLI(t, home, workspaceA, "sync")
		assertWorkspaceFile(t, workspaceA, "acme/payment/"+name, "package payment\n")
	}

	writeWorkspaceFile(t, workspaceA, "acme/payment/draft.go", "package payment\nconst Draft = 2\n")
	runCLI(t, home, workspaceA, "modify", "--all")
	runCLI(t, home, workspaceA, "submit")
	verify := t.TempDir()
	runCLI(t, home, verify, "workspace", "init", "acme/payment")
	assertWorkspaceFile(t, verify, "acme/payment/draft.go", "package payment\nconst Draft = 2\n")
	assertWorkspaceFile(t, verify, "acme/payment/second.go", "package payment\n")
}

// TestSyncRestackDispatchesChecks checks that the patchset a sync restack
// creates goes to the slice's CI daemon. Restack used to build its patchsets
// without the check dispatcher, so a synced draft never got results for its
// required checks.
func TestSyncRestackDispatchesChecks(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	loginTestCLI(t, ts, home, workspaceA)
	runCLI(t, home, workspaceA, "workspace", "init", "acme/payment")
	runCLI(t, home, workspaceB, "workspace", "init", "acme/payment")

	writeWorkspaceFile(t, workspaceB, "acme/payment/.gitslice/checks.yaml", "version: 1\nchecks:\n  unit:\n    run: \"true\"\n")
	runCLI(t, home, workspaceB, "cs", "create", "--title", "checks")
	runCLI(t, home, workspaceB, "cs", "submit")
	runCLI(t, home, workspaceA, "sync")

	ctx, cancel := context.WithTimeout(grpcAuthContext(readToken(t, home)), 30*time.Second)
	defer cancel()
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	stream, err := corev1.NewAgentServiceClient(conn).Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := stream.Send(&corev1.DaemonMessage{Payload: &corev1.DaemonMessage_Register{Register: &corev1.RegisterDaemon{
		Name: "restack-ci", Runtime: "test", Version: "0.0.1", AllowHostExec: true,
	}}}); err != nil {
		t.Fatalf("send register: %v", err)
	}
	reg, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv registered: %v", err)
	}
	runCLI(t, home, workspaceA, "slice", "set-ci-daemon", "acme/payment", reg.GetRegistered().GetDaemonId())
	// Every changeset in the slice reaches the daemon, so wait for the
	// draft's own patchset. The stream's context bounds the wait.
	waitRunChecks := func(changesetID, patchsetID string) {
		t.Helper()
		for {
			msg, err := stream.Recv()
			if err != nil {
				t.Fatalf("no RunChecks for patchset %s: %v", patchsetID, err)
			}
			if run := msg.GetRunChecks(); run != nil && run.ChangesetId == changesetID && run.PatchsetId == patchsetID {
				return
			}
		}
	}

	writeWorkspaceFile(t, workspaceA, "acme/payment/a.go", "package payment\n")
	runCLI(t, home, workspaceA, "create", "--message", "draft", "--all")
	draft := workspaceState(t, workspaceA)
	waitRunChecks(draft.CurrentChangesetID, draft.CurrentPatchsetID)

	writeWorkspaceFile(t, workspaceB, "acme/payment/b.go", "package payment\n")
	runCLI(t, home, workspaceB, "cs", "create", "--title", "moves head")
	runCLI(t, home, workspaceB, "cs", "submit")
	runCLI(t, home, workspaceA, "sync")
	restacked := workspaceState(t, workspaceA)
	if restacked.CurrentPatchsetID == draft.CurrentPatchsetID {
		t.Fatal("sync did not restack the draft")
	}
	waitRunChecks(restacked.CurrentChangesetID, restacked.CurrentPatchsetID)
}

type workspaceStateFile struct {
	CurrentChangesetID string `json:"current_changeset_id"`
	CurrentPatchsetID  string `json:"current_patchset_id"`
}

func workspaceState(t *testing.T, workspace string) workspaceStateFile {
	t.Helper()
	var state workspaceStateFile
	data, err := os.ReadFile(filepath.Join(workspace, ".gs", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	return state
}
