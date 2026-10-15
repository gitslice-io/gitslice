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

	// With every changeset in the tree submitted, the workspace keeps working
	// after the next sync. The tree's base used to stay behind, so status
	// failed with the base mismatch.
	writeWorkspaceFile(t, workspaceB, "acme/payment/third.go", "package payment\n")
	runCLI(t, home, workspaceB, "cs", "create", "--title", "remote third.go")
	runCLI(t, home, workspaceB, "cs", "submit")
	runCLI(t, home, workspaceA, "sync")
	assertWorkspaceFile(t, workspaceA, "acme/payment/third.go", "package payment\n")
	runCLI(t, home, workspaceA, "status")
	writeWorkspaceFile(t, workspaceA, "acme/payment/next.go", "package payment\n")
	runCLI(t, home, workspaceA, "create", "--message", "next change", "--all")
	runCLI(t, home, workspaceA, "submit")
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

// TestSyncMergesUpstreamChangesToTheSameFile: syncing a draft onto a head that
// changed a file the draft also edits merges the two. Sync used to put the
// draft's copy over the upstream change, so submitting reverted it. Edits to
// the same lines become a conflict patchset, written with markers, that cannot
// be submitted until a new patchset resolves it.
func TestSyncMergesUpstreamChangesToTheSameFile(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspaceA, workspaceB := t.TempDir(), t.TempDir()
	loginTestCLI(t, ts, home, workspaceA)
	runCLI(t, home, workspaceA, "workspace", "init", "acme/payment")
	lines := func(ls ...string) string { return strings.Join(ls, "\n") + "\n" }
	const file = "acme/payment/shared.txt"
	writeWorkspaceFile(t, workspaceA, file, lines("one", "two", "three", "four", "five", "six", "seven"))
	runCLI(t, home, workspaceA, "create", "--message", "shared file", "--all", "--submit")

	// Different lines: the sync merges them, and submitting keeps both.
	draft := t.TempDir()
	runCLI(t, home, draft, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, draft, file, lines("ONE", "two", "three", "four", "five", "six", "seven"))
	runCLI(t, home, draft, "create", "--message", "draft edits line one", "--all")
	runCLI(t, home, workspaceB, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspaceB, file, lines("one", "two", "three", "four", "five", "six", "SEVEN"))
	runCLI(t, home, workspaceB, "create", "--message", "upstream edits line seven", "--all", "--submit")
	runCLI(t, home, draft, "sync")
	merged := lines("ONE", "two", "three", "four", "five", "six", "SEVEN")
	assertWorkspaceFile(t, draft, file, merged)
	runCLI(t, home, draft, "submit")
	verify := t.TempDir()
	runCLI(t, home, verify, "workspace", "init", "acme/payment")
	assertWorkspaceFile(t, verify, file, merged)

	// The same line: a conflict with markers; it cannot be submitted until a
	// new patchset resolves it.
	conflicted := t.TempDir()
	runCLI(t, home, conflicted, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, conflicted, file, lines("ONE", "two", "three", "mine", "five", "six", "SEVEN"))
	runCLI(t, home, conflicted, "create", "--message", "draft edits line four", "--all")
	upstream := t.TempDir()
	runCLI(t, home, upstream, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, upstream, file, lines("ONE", "two", "three", "theirs", "five", "six", "SEVEN"))
	runCLI(t, home, upstream, "create", "--message", "upstream edits line four", "--all", "--submit")
	if out := runCLI(t, home, conflicted, "sync"); !strings.Contains(out, "conflicts: 1 path(s)\n  /acme/payment/shared.txt") {
		t.Fatalf("sync should name the conflict:\n%s", out)
	}
	assertWorkspaceFile(t, conflicted, file, lines("ONE", "two", "three", "<<<<<<< changeset", "mine", "=======", "theirs", ">>>>>>> upstream", "five", "six", "SEVEN"))
	if out, err := runCLIResult(home, conflicted, "submit"); err == nil || !strings.Contains(err.Error(), "unresolved patchset conflicts") {
		t.Fatalf("submitting unresolved conflicts: %v\n%s", err, out)
	}
	writeWorkspaceFile(t, conflicted, file, lines("ONE", "two", "three", "both", "five", "six", "SEVEN"))
	runCLI(t, home, conflicted, "modify", "--all")
	runCLI(t, home, conflicted, "submit")
	final := t.TempDir()
	runCLI(t, home, final, "workspace", "init", "acme/payment")
	assertWorkspaceFile(t, final, file, lines("ONE", "two", "three", "both", "five", "six", "SEVEN"))
}
