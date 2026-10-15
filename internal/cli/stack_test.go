package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCanonicalStackCommandsRejectPreStackWorkspaceState(t *testing.T) {
	err := rejectPreStackWorkspaceState(WorkspaceState{
		CurrentChangesetID: "cs_123",
		CurrentPatchsetID:  "ps_123",
		BaseCommitID:       "cmt_123",
	})
	if err == nil {
		t.Fatal("pre-dependency workspace state unexpectedly passed")
	}
	if !strings.Contains(err.Error(), "unsupported pre-dependency format") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestStackMoveRestacksAndUpdatesWorkspaceState(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, ".gs"), 0o755); err != nil {
		t.Fatal(err)
	}
	server := newFakeStackMoveServer()
	serverAddr := startFakeStackMoveServer(t, server)
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Dir: workspace, Stdout: &stdout, Stderr: &stderr}
	if err := r.writeUserConfig(UserConfig{ServerAddr: serverAddr, Token: "secret-token", SubjectID: "user_alice"}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(workspace, ".gs", "slice.json"), WorkspaceConfig{
		Account:        "acme",
		Slice:          "payment",
		SliceID:        "slice_acme_payment",
		DefinitionHash: "sha256:def",
		IncludedPaths:  []string{"/acme/payment"},
		BaseCommitID:   "cmt_base",
	}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(workspace, ".gs", "state.json"), WorkspaceState{
		ActiveStackID:      "stk_1",
		CurrentChangesetID: "cs_moved",
		CurrentPatchsetID:  "ps_moved_1",
		BaseCommitID:       "cmt_base",
	}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(workspace, ".gs", "base_snapshot.json"), BaseSnapshot{
		CommitID: "cmt_base",
		Files:    map[string]BaseSnapshotFile{},
	}, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := r.Run(context.Background(), []string{"move", "cs_moved", "--onto", "cs_parent", "--json"}); err != nil {
		t.Fatalf("move failed: %v\nstderr:\n%s", err, stderr.String())
	}
	if server.reparentReq == nil {
		t.Fatal("ReparentStackEntry was not called")
	}
	if server.reparentReq.NewParentChangesetId != "cs_parent" || server.reparentReq.NewParentPatchsetId != "ps_parent_1" {
		t.Fatalf("unexpected reparent request: %#v", server.reparentReq)
	}
	if server.restackReq == nil {
		t.Fatal("Restack was not called")
	}
	if server.restackReq.StartChangesetId != "cs_moved" {
		t.Fatalf("unexpected restack request: %#v", server.restackReq)
	}
	state, err := r.readWorkspaceState()
	if err != nil {
		t.Fatal(err)
	}
	if state.CurrentChangesetID != "cs_moved" || state.CurrentPatchsetID != "ps_moved_2" {
		t.Fatalf("workspace state was not refreshed after restack: %#v", state)
	}
	var out stackOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("move output is not JSON: %v\n%s", err, stdout.String())
	}
	if !stringSliceContains(out.RestackedChangesets, "cs_moved") {
		t.Fatalf("move output missing restacked changeset: %#v", out)
	}
}

func TestStackDetachRestacksNewStackAndUpdatesWorkspaceState(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, ".gs"), 0o755); err != nil {
		t.Fatal(err)
	}
	server := newFakeStackDetachServer()
	serverAddr := startFakeStackDetachServer(t, server)
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Dir: workspace, Stdout: &stdout, Stderr: &stderr}
	if err := r.writeUserConfig(UserConfig{ServerAddr: serverAddr, Token: "secret-token", SubjectID: "user_alice"}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(workspace, ".gs", "slice.json"), WorkspaceConfig{
		Account:        "acme",
		Slice:          "payment",
		SliceID:        "slice_acme_payment",
		DefinitionHash: "sha256:def",
		IncludedPaths:  []string{"/acme/payment"},
		BaseCommitID:   "cmt_base",
	}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(workspace, ".gs", "state.json"), WorkspaceState{
		ActiveStackID:      "stk_source",
		CurrentChangesetID: "cs_child",
		CurrentPatchsetID:  "ps_child_1",
		BaseCommitID:       "cmt_base",
	}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(workspace, ".gs", "base_snapshot.json"), BaseSnapshot{
		CommitID: "cmt_base",
		Files:    map[string]BaseSnapshotFile{},
	}, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := r.Run(context.Background(), []string{"detach", "cs_child", "--message", "detached child stack", "--json"}); err != nil {
		t.Fatalf("detach failed: %v\nstderr:\n%s", err, stderr.String())
	}
	if server.detachReq == nil {
		t.Fatal("DetachStackEntry was not called")
	}
	if server.detachReq.StackId != "stk_source" || server.detachReq.ChangesetId != "cs_child" || server.detachReq.Title != "detached child stack" {
		t.Fatalf("unexpected detach request: %#v", server.detachReq)
	}
	if server.restackReq == nil {
		t.Fatal("Restack was not called")
	}
	if server.restackReq.StackId != "stk_detached" || server.restackReq.StartChangesetId != "cs_child" || server.restackReq.TargetBaseCommitId != "cmt_base" {
		t.Fatalf("unexpected restack request: %#v", server.restackReq)
	}
	state, err := r.readWorkspaceState()
	if err != nil {
		t.Fatal(err)
	}
	if state.ActiveStackID != "stk_detached" || state.CurrentChangesetID != "cs_child" || state.CurrentPatchsetID != "ps_child_2" {
		t.Fatalf("workspace state was not refreshed after detach: %#v", state)
	}
	var out stackOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("detach output is not JSON: %v\n%s", err, stdout.String())
	}
	if out.StackID != "stk_detached" || !stringSliceContains(out.RestackedChangesets, "cs_child") {
		t.Fatalf("detach output missing detached stack/restacked changeset: %#v", out)
	}
}

func TestStackRestackWritesConflictStateAndSwitchesActiveEntry(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, ".gs"), 0o755); err != nil {
		t.Fatal(err)
	}
	serverAddr := startFakeStackRestackConflictServer(t)
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Dir: workspace, Stdout: &stdout, Stderr: &stderr}
	if err := r.writeUserConfig(UserConfig{ServerAddr: serverAddr, Token: "secret-token", SubjectID: "user_alice"}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(workspace, ".gs", "slice.json"), WorkspaceConfig{
		Account:        "acme",
		Slice:          "payment",
		SliceID:        "slice_acme_payment",
		DefinitionHash: "sha256:def",
		IncludedPaths:  []string{"/acme/payment"},
		BaseCommitID:   "cmt_base",
	}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(workspace, ".gs", "state.json"), WorkspaceState{
		ActiveStackID:      "stk_1",
		CurrentChangesetID: "cs_root",
		CurrentPatchsetID:  "ps_root_1",
		BaseCommitID:       "cmt_base",
	}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(workspace, ".gs", "base_snapshot.json"), BaseSnapshot{
		CommitID: "cmt_base",
		Files:    map[string]BaseSnapshotFile{},
	}, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := r.Run(context.Background(), []string{"update-dependents", "cs_child"}); err != nil {
		t.Fatalf("update-dependents failed: %v\nstderr:\n%s", err, stderr.String())
	}
	state, err := r.readWorkspaceState()
	if err != nil {
		t.Fatal(err)
	}
	if state.CurrentChangesetID != "cs_child" || state.CurrentPatchsetID != "ps_child_2" {
		t.Fatalf("workspace state did not switch to conflicted entry: %#v", state)
	}
	conflictState, ok, err := r.readWorkspaceConflictState()
	if err != nil {
		t.Fatal(err)
	}
	if !ok || conflictState.ChangesetID != "cs_child" || conflictState.PatchsetID != "ps_child_2" || len(conflictState.Conflicts) != 1 {
		t.Fatalf("unexpected conflict state: ok=%v state=%#v", ok, conflictState)
	}
	if conflictState.Conflicts[0].Path != "/acme/payment/conflict.go" || conflictState.Conflicts[0].ConflictClass != "restack" {
		t.Fatalf("unexpected conflict record: %#v", conflictState.Conflicts[0])
	}
	marker, err := os.ReadFile(filepath.Join(workspace, "acme", "payment", "conflict.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"<<<<<<< gitslice update local",
		"(local side content was not returned)",
		"||||||| gitslice update base",
		"=======",
		">>>>>>> gitslice update remote",
	} {
		if !strings.Contains(string(marker), want) {
			t.Fatalf("conflict marker missing %q:\n%s", want, string(marker))
		}
	}
	if !strings.Contains(stdout.String(), "conflicts: child") {
		t.Fatalf("restack output missing conflict notice:\n%s", stdout.String())
	}
}

func TestWorkspaceSyncRestacksActiveStackOnCleanBaseAdvance(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, ".gs"), 0o755); err != nil {
		t.Fatal(err)
	}
	server := newFakeStackMoveServer()
	serverAddr := startFakeStackSyncServer(t, server, "cmt_new")
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Dir: workspace, Stdout: &stdout, Stderr: &stderr}
	if err := r.writeUserConfig(UserConfig{ServerAddr: serverAddr, Token: "secret-token", SubjectID: "user_alice"}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(workspace, ".gs", "slice.json"), WorkspaceConfig{
		Account:        "acme",
		Slice:          "payment",
		SliceID:        "slice_acme_payment",
		DefinitionHash: "sha256:def",
		IncludedPaths:  []string{"/acme/payment"},
		BaseCommitID:   "cmt_base",
	}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(workspace, ".gs", "state.json"), WorkspaceState{
		ActiveStackID:      "stk_1",
		CurrentChangesetID: "cs_moved",
		CurrentPatchsetID:  "ps_moved_1",
		BaseCommitID:       "cmt_base",
	}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(workspace, ".gs", "base_snapshot.json"), BaseSnapshot{
		CommitID: "cmt_base",
		Files:    map[string]BaseSnapshotFile{},
	}, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := r.Run(context.Background(), []string{"sync", "--json"}); err != nil {
		t.Fatalf("sync failed: %v\nstderr:\n%s", err, stderr.String())
	}
	if server.restackReq == nil {
		t.Fatal("Restack was not called")
	}
	if server.restackReq.TargetBaseCommitId != "cmt_new" {
		t.Fatalf("unexpected restack request: %#v", server.restackReq)
	}
	state, err := r.readWorkspaceState()
	if err != nil {
		t.Fatal(err)
	}
	if state.BaseCommitID != "cmt_new" || state.CurrentPatchsetID != "ps_moved_2" {
		t.Fatalf("workspace state was not refreshed after sync restack: %#v", state)
	}
	ws, err := r.readWorkspaceConfig()
	if err != nil {
		t.Fatal(err)
	}
	if ws.BaseCommitID != "cmt_new" {
		t.Fatalf("workspace config base = %q", ws.BaseCommitID)
	}
	base, err := r.readBaseSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if base.CommitID != "cmt_new" {
		t.Fatalf("base snapshot commit = %q", base.CommitID)
	}
	var out workspaceSyncOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("sync output is not JSON: %v\n%s", err, stdout.String())
	}
	if !stringSliceContains(out.RestackedChangesets, "cs_moved") {
		t.Fatalf("sync output missing restacked changeset: %#v", out)
	}
}

func TestStackUpRequiresExplicitChildWhenMultipleChildren(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, ".gs"), 0o755); err != nil {
		t.Fatal(err)
	}
	server := newFakeStackMoveServer()
	serverAddr := startFakeStackMoveServer(t, server)
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Dir: workspace, Stdout: &stdout, Stderr: &stderr}
	if err := r.writeUserConfig(UserConfig{ServerAddr: serverAddr, Token: "secret-token", SubjectID: "user_alice"}); err != nil {
		t.Fatal(err)
	}
	writeStackWorkspaceForTest(t, workspace, "stk_1", "cs_root", "ps_root_1")

	err := r.Run(context.Background(), []string{"up"})
	if !isUserErrorCode(err, "ambiguous_stack_navigation") {
		t.Fatalf("up err = %v, want ambiguous_stack_navigation", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("ambiguous up wrote stdout:\n%s", stdout.String())
	}
}

func TestStackCommandJSONUsesStackTreeSchema(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, ".gs"), 0o755); err != nil {
		t.Fatal(err)
	}
	server := newFakeStackMoveServer()
	serverAddr := startFakeStackMoveServer(t, server)
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Dir: workspace, Stdout: &stdout, Stderr: &stderr}
	if err := r.writeUserConfig(UserConfig{ServerAddr: serverAddr, Token: "secret-token", SubjectID: "user_alice"}); err != nil {
		t.Fatal(err)
	}
	writeStackWorkspaceForTest(t, workspace, "stk_1", "cs_moved", "ps_moved_1")

	if err := r.Run(context.Background(), []string{"deps", "--json"}); err != nil {
		t.Fatalf("deps --json failed: %v\nstderr:\n%s", err, stderr.String())
	}
	var out stackOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("stack output is not JSON: %v\n%s", err, stdout.String())
	}
	if out.StackID != "stk_1" || out.ActiveChangesetID != "cs_moved" || out.RootChangesetID != "cs_root" || len(out.Entries) != 3 {
		t.Fatalf("unexpected stack JSON: %#v", out)
	}
	child := out.Entries[1]
	if child.ChangesetID != "cs_parent" || child.ParentChangesetID != "cs_root" || child.ParentPatchsetID != "ps_root_1" || child.Depth != 1 || child.DisplayOrder != 2 {
		t.Fatalf("unexpected child stack entry JSON: %#v", child)
	}
}

func TestStackSubmitNoWatchAndPolling(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, ".gs"), 0o755); err != nil {
		t.Fatal(err)
	}
	server := newFakeStackSubmitServer()
	serverAddr := startFakeStackSubmitServer(t, server)
	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Dir: workspace, Stdout: &stdout, Stderr: &stderr}
	if err := r.writeUserConfig(UserConfig{ServerAddr: serverAddr, Token: "secret-token", SubjectID: "user_alice"}); err != nil {
		t.Fatal(err)
	}
	writeStackWorkspaceForTest(t, workspace, "stk_1", "cs_root", "ps_root_1")

	if err := r.Run(context.Background(), []string{"submit", "--with-dependencies", "--no-watch", "--json"}); err != nil {
		t.Fatalf("submit --with-dependencies --no-watch failed: %v\nstderr:\n%s", err, stderr.String())
	}
	if server.submitReq == nil || server.submitReq.StackId != "stk_1" {
		t.Fatalf("unexpected no-watch submit request: %#v", server.submitReq)
	}
	var noWatchOut struct {
		DependencyID string `json:"dependency_id"`
		Status       string `json:"status"`
		Results      []struct {
			ChangesetID string `json:"changeset_id"`
			Status      string `json:"status"`
		} `json:"results"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &noWatchOut); err != nil {
		t.Fatalf("submit no-watch output is not JSON: %v\n%s", err, stdout.String())
	}
	if noWatchOut.DependencyID != "stk_1" || noWatchOut.Status != "submitted" || len(noWatchOut.Results) != 1 || noWatchOut.Results[0].Status != "pending_publish" {
		t.Fatalf("unexpected no-watch submit output: %#v", noWatchOut)
	}

	stdout.Reset()
	stderr.Reset()
	server.submitReq = nil
	server.getChangesetCalls = 0
	server.getRefCalls = 0
	if err := r.Run(context.Background(), []string{"submit", "--with-dependencies", "--watch-timeout", "200ms"}); err != nil {
		t.Fatalf("submit --with-dependencies with polling failed: %v\nstderr:\n%s", err, stderr.String())
	}
	if server.submitReq == nil || server.submitReq.StackId != "stk_1" {
		t.Fatalf("unexpected watched submit request: %#v", server.submitReq)
	}
	if server.getChangesetCalls == 0 || server.getRefCalls == 0 {
		t.Fatalf("watched submit did not poll changeset/ref: changeset=%d ref=%d", server.getChangesetCalls, server.getRefCalls)
	}
	if !strings.Contains(stdout.String(), "pending_publish") || !strings.Contains(stdout.String(), "root") {
		t.Fatalf("watched submit output missing entry status:\n%s", stdout.String())
	}
}

func writeStackWorkspaceForTest(t *testing.T, workspace, stackID, changesetID, patchsetID string) {
	t.Helper()
	if err := writeJSONFile(filepath.Join(workspace, ".gs", "slice.json"), WorkspaceConfig{
		Account:        "acme",
		Slice:          "payment",
		SliceID:        "slice_acme_payment",
		DefinitionHash: "sha256:def",
		IncludedPaths:  []string{"/acme/payment"},
		BaseCommitID:   "cmt_base",
	}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(workspace, ".gs", "state.json"), WorkspaceState{
		ActiveStackID:      stackID,
		CurrentChangesetID: changesetID,
		CurrentPatchsetID:  patchsetID,
		BaseCommitID:       "cmt_base",
	}, 0o644); err != nil {
		t.Fatal(err)
	}
}

func stringSliceContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
