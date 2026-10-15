package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/postgres"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

func TestChangesetUpdateAndDelete(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspace, "draft.go", "package payment\nconst Version = 1\n")
	runCLI(t, home, workspace, "cs", "create")
	writeWorkspaceFile(t, workspace, "draft.go", "package payment\nconst Version = 2\n")
	runCLI(t, home, workspace, "cs", "update")
	runCLI(t, home, workspace, "cs", "submit")
	status := runCLI(t, home, workspace, "status")
	if !strings.Contains(status, "status: clean") {
		t.Fatalf("expected clean status after update submit, got:\n%s", status)
	}

	if err := os.Remove(filepath.Join(workspace, "draft.go")); err != nil {
		t.Fatal(err)
	}
	status = runCLI(t, home, workspace, "status")
	if !strings.Contains(status, "/acme/payment/draft.go") {
		t.Fatalf("expected delete to be dirty, got:\n%s", status)
	}
	runCLI(t, home, workspace, "cs", "create", "--title", "delete draft")
	runCLI(t, home, workspace, "cs", "submit")
	status = runCLI(t, home, workspace, "status")
	if !strings.Contains(status, "status: clean") {
		t.Fatalf("expected clean status after delete submit, got:\n%s", status)
	}
}

func TestChangesetWorkflowCommandsAndServerDiff(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")

	writeWorkspaceFile(t, workspace, "workflow.go", "package payment\nconst Version = 1\n")
	localDiff := runCLI(t, home, workspace, "diff")
	for _, want := range []string{"diff --git", "a/acme/payment/workflow.go", "+const Version = 1"} {
		if !strings.Contains(localDiff, want) {
			t.Fatalf("workspace diff missing %q:\n%s", want, localDiff)
		}
	}
	nameOnly := runCLI(t, home, workspace, "diff", "--name-only")
	if strings.TrimSpace(nameOnly) != "/acme/payment/workflow.go" {
		t.Fatalf("workspace diff --name-only = %q", nameOnly)
	}
	stat := runCLI(t, home, workspace, "diff", "--stat")
	if !strings.Contains(stat, "1 changed path(s)") || !strings.Contains(stat, "/acme/payment/workflow.go") {
		t.Fatalf("workspace diff --stat missing path summary:\n%s", stat)
	}

	createOut := runCLI(t, home, workspace, "cs", "create", "--title", "workflow commands", "--json")
	_, firstPatchsetID, changesetSelector, _ := parseChangesetOutput(t, createOut)
	_, createAgainErr := runCLIFails(t, home, workspace, "cs", "create", "--title", "accidental duplicate")
	if !strings.Contains(createAgainErr, "workspace already has draft changeset "+changesetSelector) ||
		!strings.Contains(createAgainErr, "Run gs cs update to add a new patchset") {
		t.Fatalf("duplicate create did not prompt update:\n%s", createAgainErr)
	}
	show := runCLI(t, home, workspace, "cs", "show")
	for _, want := range []string{changesetSelector, "title: workflow commands", "patchsets:", "/acme/payment/workflow.go"} {
		if !strings.Contains(show, want) {
			t.Fatalf("cs show missing %q:\n%s", want, show)
		}
	}
	list := runCLI(t, home, workspace, "cs", "list", "--status", "draft")
	if !strings.Contains(list, changesetSelector) || !strings.Contains(list, "workflow commands") {
		t.Fatalf("cs list missing changeset:\n%s", list)
	}
	versions := runCLI(t, home, workspace, "cs", "versions")
	if !strings.Contains(versions, "1 current changed=1") || strings.Contains(versions, firstPatchsetID) {
		t.Fatalf("cs versions missing first patchset:\n%s", versions)
	}
	versions = runCLI(t, home, workspace, "cs", "versions", changesetSelector)
	if !strings.Contains(versions, "1 current changed=1") {
		t.Fatalf("cs versions short id missing first patchset:\n%s", versions)
	}
	firstDiff := runCLI(t, home, workspace, "cs", "diff", "--patchset", "1")
	if !strings.Contains(firstDiff, "+const Version = 1") {
		t.Fatalf("server diff for first patchset missing addition:\n%s", firstDiff)
	}

	writeWorkspaceFile(t, workspace, "workflow.go", "package payment\nconst Version = 2\n")
	updateOut := runCLI(t, home, workspace, "cs", "update", "--json")
	_, secondPatchsetID, _, _ := parseChangesetOutput(t, updateOut)
	versions = runCLI(t, home, workspace, "cs", "patchsets", changesetSelector)
	if !strings.Contains(versions, "2 current changed=1") || strings.Contains(versions, secondPatchsetID) {
		t.Fatalf("cs patchsets missing second patchset:\n%s", versions)
	}
	between := runCLI(t, home, workspace, "cs", "diff", changesetSelector, "--from", "1", "--to", "2")
	for _, want := range []string{"-const Version = 1", "+const Version = 2"} {
		if !strings.Contains(between, want) {
			t.Fatalf("server patchset diff missing %q:\n%s", want, between)
		}
	}
	serverNameOnly := runCLI(t, home, workspace, "cs", "diff", "--name-only")
	if strings.TrimSpace(serverNameOnly) != "/acme/payment/workflow.go" {
		t.Fatalf("cs diff --name-only = %q", serverNameOnly)
	}
	explain := runCLI(t, home, workspace, "cs", "explain")
	for _, want := range []string{"validation:", "submit_requirements:", "read_set:", "write_set:"} {
		if !strings.Contains(explain, want) {
			t.Fatalf("cs explain missing %q:\n%s", want, explain)
		}
	}
	status := runCLI(t, home, workspace, "cs", "status", changesetSelector)
	if !strings.Contains(status, "status: draft") {
		t.Fatalf("cs status <id> missing draft status:\n%s", status)
	}

	runCLI(t, home, workspace, "cs", "submit", changesetSelector)
	status = runCLI(t, home, workspace, "cs", "status", changesetSelector)
	if !strings.Contains(status, "status: submitted") {
		t.Fatalf("cs status <id> missing submitted status:\n%s", status)
	}

	writeWorkspaceFile(t, workspace, "abandon.go", "package payment\nconst Abandon = true\n")
	abandonOut := runCLI(t, home, workspace, "cs", "create", "--title", "abandon workflow", "--json")
	_, _, abandonSelector, _ := parseChangesetOutput(t, abandonOut)
	runCLI(t, home, workspace, "cs", "abandon", "--reason", "test cleanup")
	status = runCLI(t, home, workspace, "cs", "status", abandonSelector)
	if !strings.Contains(status, "status: abandoned") {
		t.Fatalf("cs abandon did not mark changeset abandoned:\n%s", status)
	}
}

func TestChangesetStatusWatchAfterNoWatchSubmit(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspace, "watch.go", "package payment\nconst Watch = true\n")
	createOut := runCLI(t, home, workspace, "cs", "create", "--title", "watch submit", "--json")
	changesetID, _, changesetSelector, _ := parseChangesetOutput(t, createOut)

	submitOut := runCLI(t, home, workspace, "cs", "submit", "--no-watch", "--json")
	var submit struct {
		ChangesetID string `json:"changeset_id"`
		Status      string `json:"status"`
	}
	if err := json.Unmarshal([]byte(submitOut), &submit); err != nil {
		t.Fatalf("submit output is not JSON: %v\n%s", err, submitOut)
	}
	if submit.ChangesetID != changesetID {
		t.Fatalf("submit changed changeset id: %#v want %s", submit, changesetID)
	}
	if submit.Status != "pending_publish" && submit.Status != "submitted" {
		t.Fatalf("unexpected submit status: %#v", submit)
	}

	status := runCLI(t, home, workspace, "cs", "status", "--watch", "--watch-timeout", "5s", changesetSelector)
	if !strings.Contains(status, "status: submitted") {
		t.Fatalf("cs status --watch did not reach submitted:\n%s", status)
	}
}

func TestOutsideSliceEditRejected(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspace, "acme/backend/app.go", "package backend\n")
	_, stderr := runCLIFails(t, home, workspace, "status")
	if !strings.Contains(stderr, "outside the workspace slice") {
		t.Fatalf("expected outside-slice rejection, got stderr:\n%s", stderr)
	}
}

func TestDisjointStaleChangesetsCanSubmit(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	loginTestCLI(t, ts, home, workspaceA)
	runCLI(t, home, workspaceA, "workspace", "init", "acme/payment")
	runCLI(t, home, workspaceB, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspaceA, "a.go", "package payment\nconst A = 1\n")
	writeWorkspaceFile(t, workspaceB, "b.go", "package payment\nconst B = 1\n")
	runCLI(t, home, workspaceA, "cs", "create", "--title", "add a")
	runCLI(t, home, workspaceB, "cs", "create", "--title", "add b")
	runCLI(t, home, workspaceA, "cs", "submit")
	runCLI(t, home, workspaceB, "cs", "submit")
}

func TestSamePathConflictRejected(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	loginTestCLI(t, ts, home, workspaceA)
	runCLI(t, home, workspaceA, "workspace", "init", "acme/payment")
	runCLI(t, home, workspaceB, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspaceA, "conflict.go", "package payment\nconst Conflict = 1\n")
	writeWorkspaceFile(t, workspaceB, "conflict.go", "package payment\nconst Conflict = 2\n")
	runCLI(t, home, workspaceA, "cs", "create", "--title", "conflict one")
	runCLI(t, home, workspaceB, "cs", "create", "--title", "conflict two")
	runCLI(t, home, workspaceA, "cs", "submit")
	_, stderr := runCLIFails(t, home, workspaceB, "cs", "submit")
	if !strings.Contains(stderr, "FailedPrecondition") && !strings.Contains(stderr, "conflict") {
		t.Fatalf("expected same-path conflict, got stderr:\n%s", stderr)
	}
}

func TestChangesetAbandonAndSubmitIdempotency(t *testing.T) {
	ts := startTestServer(t)
	token := ts.loginViaGRPC(t, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	clients := newTestCoreClients(conn)

	submittedID, patchsetID := createDirectPatchset(t, ctx, clients, "/acme/payment/idempotent_submit.go", "package payment\nconst Idempotent = true\n", "idempotent submit")
	first, err := clients.changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{
		ChangesetId:               submittedID,
		ExpectedCurrentPatchsetId: patchsetID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "pending_publish" && first.Status != "submitted" {
		t.Fatalf("unexpected first submit response: %#v", first)
	}
	published := waitForSubmittedChangeset(t, ctx, clients.changeset, submittedID)
	second, err := clients.changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{
		ChangesetId:               submittedID,
		ExpectedCurrentPatchsetId: patchsetID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != "submitted" || second.CommitId != published.CommitId {
		t.Fatalf("expected idempotent submitted response with same commit, got %#v want commit %s", second, published.CommitId)
	}

	abandoned, err := clients.changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
		TargetRef:      postgres.DefaultTargetRef,
		Title:          "abandon draft",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := clients.changeset.AbandonChangeset(ctx, &corev1.AbandonChangesetRequest{ChangesetId: abandoned.Id, Reason: "test"}); err != nil {
		t.Fatal(err)
	}
	got, err := clients.changeset.GetChangeset(ctx, &corev1.GetChangesetRequest{ChangesetId: abandoned.Id})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "abandoned" {
		t.Fatalf("expected abandoned status, got %#v", got)
	}
	_, err = clients.changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{ChangesetId: abandoned.Id})
	if grpcstatus.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected abandoned submit to fail with FailedPrecondition, got %v", err)
	}
}

// TestChangesetCaptureCreatesThenUpdates verifies the agent-oriented `cs capture`
// command: a no-op when the workspace is clean, a create on first edits, and an
// update (new patchset) on subsequent edits.
func TestChangesetCaptureCreatesThenUpdates(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")

	// Clean workspace: capture is a no-op.
	clean := runCLI(t, home, workspace, "cs", "capture")
	if !strings.Contains(clean, "no changes to capture") {
		t.Fatalf("expected no-op capture on clean workspace, got:\n%s", clean)
	}

	writeWorkspaceFile(t, workspace, "capture_one.go", "package payment\nconst One = 1\n")
	first := runCLI(t, home, workspace, "cs", "capture", "--title", "agent work")
	if !strings.Contains(first, "patchset 1") {
		t.Fatalf("expected first capture to create patchset 1, got:\n%s", first)
	}

	writeWorkspaceFile(t, workspace, "capture_two.go", "package payment\nconst Two = 2\n")
	second := runCLI(t, home, workspace, "cs", "capture")
	if !strings.Contains(second, "patchset 2") {
		t.Fatalf("expected second capture to add patchset 2, got:\n%s", second)
	}

	third := runCLI(t, home, workspace, "cs", "capture")
	if !strings.Contains(third, "no changes to capture") {
		t.Fatalf("expected repeated capture to be a no-op, got:\n%s", third)
	}
	if strings.Contains(third, "patchset 3") {
		t.Fatalf("repeated capture created patchset 3:\n%s", third)
	}

	// The captures must land on the same changeset, and the no-op capture must
	// leave the current patchset unchanged: exactly two patchsets exist.
	versionsRaw := runCLI(t, home, workspace, "cs", "versions", "--json")
	var versions struct {
		Patchsets []struct {
			Number int64 `json:"number"`
		} `json:"patchsets"`
	}
	if err := json.Unmarshal([]byte(versionsRaw), &versions); err != nil {
		t.Fatalf("parse cs versions json: %v\n%s", err, versionsRaw)
	}
	if len(versions.Patchsets) != 2 {
		t.Fatalf("expected exactly 2 patchsets after no-op capture, got %d:\n%s", len(versions.Patchsets), versionsRaw)
	}

	// Simulate the agent daemon's per-turn workspace, which reuses the linked
	// changeset but does not carry the local current-patchset id forward. A
	// repeat capture with no edits must still be a no-op (detected against the
	// server's current patchset), not a phantom "captured patchset 2".
	statePath := filepath.Join(workspace, ".gs", "state.json")
	var st map[string]any
	if err := readJSONFile(statePath, &st); err != nil {
		t.Fatalf("read workspace state: %v", err)
	}
	st["current_patchset_id"] = ""
	stateJSON, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("marshal workspace state: %v", err)
	}
	if err := os.WriteFile(statePath, stateJSON, 0o644); err != nil {
		t.Fatalf("write workspace state: %v", err)
	}
	noopAfterReset := runCLI(t, home, workspace, "cs", "capture")
	if !strings.Contains(noopAfterReset, "no changes to capture") {
		t.Fatalf("expected no-op capture after losing local patchset id, got:\n%s", noopAfterReset)
	}
	if strings.Contains(noopAfterReset, "patchset 3") {
		t.Fatalf("capture after losing local patchset id created a phantom patchset:\n%s", noopAfterReset)
	}
}
