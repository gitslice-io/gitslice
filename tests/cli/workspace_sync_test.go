package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceSyncUpdatesCleanWorkspace(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	loginTestCLI(t, ts, home, workspaceA)
	runCLI(t, home, workspaceA, "workspace", "init", "acme/payment")
	runCLI(t, home, workspaceB, "workspace", "init", "acme/payment")

	const remoteContent = "package payment\nconst SyncedRemote = true\n"
	writeWorkspaceFile(t, workspaceB, "remote_sync.go", remoteContent)
	runCLI(t, home, workspaceB, "cs", "create", "--title", "remote sync")
	runCLI(t, home, workspaceB, "cs", "submit")

	out := runCLI(t, home, workspaceA, "sync")
	if !strings.Contains(out, "updated 1 remote path(s)") {
		t.Fatalf("sync output did not report remote update:\n%s", out)
	}
	assertWorkspaceFile(t, workspaceA, "acme/payment/remote_sync.go", remoteContent)
	status := runCLI(t, home, workspaceA, "status")
	if !strings.Contains(status, "status: clean") {
		t.Fatalf("expected clean status after sync, got:\n%s", status)
	}
}

func TestWorkspaceSyncRebasesDraftChangeset(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	loginTestCLI(t, ts, home, workspaceA)
	runCLI(t, home, workspaceA, "workspace", "init", "acme/payment")
	runCLI(t, home, workspaceB, "workspace", "init", "acme/payment")

	const localContent = "package payment\nconst LocalSync = true\n"
	writeWorkspaceFile(t, workspaceA, "local_sync.go", localContent)
	runCLI(t, home, workspaceA, "cs", "create", "--title", "local sync draft")

	const remoteContent = "package payment\nconst RemoteSync = true\n"
	writeWorkspaceFile(t, workspaceB, "remote_sync.go", remoteContent)
	runCLI(t, home, workspaceB, "cs", "create", "--title", "remote sync")
	runCLI(t, home, workspaceB, "cs", "submit")

	raw := runCLI(t, home, workspaceA, "sync", "--json")
	var synced struct {
		Status         string   `json:"status"`
		PatchsetNumber int64    `json:"patchset_number"`
		UpdatedPaths   []string `json:"updated_paths"`
		LocalPaths     []string `json:"local_paths"`
		ConflictCount  int      `json:"conflict_count"`
	}
	if err := json.Unmarshal([]byte(raw), &synced); err != nil {
		t.Fatalf("sync output is not JSON: %v\n%s", err, raw)
	}
	if synced.Status != "synced" || synced.PatchsetNumber != 2 || synced.ConflictCount != 0 {
		t.Fatalf("unexpected sync output: %#v raw=%s", synced, raw)
	}
	if !containsString(synced.UpdatedPaths, "/acme/payment/remote_sync.go") || !containsString(synced.LocalPaths, "/acme/payment/local_sync.go") {
		t.Fatalf("sync output missing expected path sets: %#v", synced)
	}
	assertWorkspaceFile(t, workspaceA, "local_sync.go", localContent)
	assertWorkspaceFile(t, workspaceA, "acme/payment/remote_sync.go", remoteContent)

	runCLI(t, home, workspaceA, "cs", "submit")
	verifyWorkspace := t.TempDir()
	runCLI(t, home, verifyWorkspace, "workspace", "init", "acme/payment")
	assertWorkspaceFile(t, verifyWorkspace, "acme/payment/local_sync.go", localContent)
	assertWorkspaceFile(t, verifyWorkspace, "acme/payment/remote_sync.go", remoteContent)
}

func TestWorkspaceSyncLineMergesNonOverlappingTextByDefault(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	seedWorkspace := t.TempDir()
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	loginTestCLI(t, ts, home, seedWorkspace)
	runCLI(t, home, seedWorkspace, "workspace", "init", "acme/payment")

	const baseContent = "package payment\nconst LocalValue = \"base\"\nconst RemoteValue = \"base\"\n"
	writeWorkspaceFile(t, seedWorkspace, "merge_default.go", baseContent)
	runCLI(t, home, seedWorkspace, "cs", "create", "--title", "seed merge base")
	runCLI(t, home, seedWorkspace, "cs", "submit")

	runCLI(t, home, workspaceA, "workspace", "init", "acme/payment")
	runCLI(t, home, workspaceB, "workspace", "init", "acme/payment")

	const localContent = "package payment\nconst LocalValue = \"local\"\nconst RemoteValue = \"base\"\n"
	writeWorkspaceFile(t, workspaceA, "acme/payment/merge_default.go", localContent)
	runCLI(t, home, workspaceA, "cs", "create", "--title", "local merge edit")

	const remoteContent = "package payment\nconst LocalValue = \"base\"\nconst RemoteValue = \"remote\"\n"
	writeWorkspaceFile(t, workspaceB, "acme/payment/merge_default.go", remoteContent)
	runCLI(t, home, workspaceB, "cs", "create", "--title", "remote merge edit")
	runCLI(t, home, workspaceB, "cs", "submit")

	raw := runCLI(t, home, workspaceA, "sync", "--json")
	var synced struct {
		Status        string   `json:"status"`
		MergeStrategy string   `json:"merge_strategy"`
		MergedPaths   []string `json:"merged_paths"`
		ConflictCount int      `json:"conflict_count"`
	}
	if err := json.Unmarshal([]byte(raw), &synced); err != nil {
		t.Fatalf("sync output is not JSON: %v\n%s", err, raw)
	}
	if synced.Status != "synced" || synced.MergeStrategy != "line" || synced.ConflictCount != 0 {
		t.Fatalf("unexpected sync output: %#v raw=%s", synced, raw)
	}
	if !containsString(synced.MergedPaths, "/acme/payment/merge_default.go") {
		t.Fatalf("sync output missing merged path: %#v", synced)
	}
	const mergedContent = "package payment\nconst LocalValue = \"local\"\nconst RemoteValue = \"remote\"\n"
	assertWorkspaceFile(t, workspaceA, "acme/payment/merge_default.go", mergedContent)

	runCLI(t, home, workspaceA, "cs", "submit")
	verifyWorkspace := t.TempDir()
	runCLI(t, home, verifyWorkspace, "workspace", "init", "acme/payment")
	assertWorkspaceFile(t, verifyWorkspace, "acme/payment/merge_default.go", mergedContent)
}

func TestWorkspaceSyncRecordsAndResolvesConflicts(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	loginTestCLI(t, ts, home, workspaceA)
	runCLI(t, home, workspaceA, "workspace", "init", "acme/payment")
	runCLI(t, home, workspaceB, "workspace", "init", "acme/payment")

	writeWorkspaceFile(t, workspaceA, "sync_conflict.go", "package payment\nconst Value = \"local\"\n")
	runCLI(t, home, workspaceA, "cs", "create", "--title", "local sync conflict")

	writeWorkspaceFile(t, workspaceB, "sync_conflict.go", "package payment\nconst Value = \"remote\"\n")
	runCLI(t, home, workspaceB, "cs", "create", "--title", "remote sync conflict")
	runCLI(t, home, workspaceB, "cs", "submit")

	out := runCLI(t, home, workspaceA, "sync")
	if !strings.Contains(out, "conflicts: 1 path(s)") || !strings.Contains(out, "/acme/payment/sync_conflict.go") {
		t.Fatalf("sync output missing conflict:\n%s", out)
	}
	conflicted, err := os.ReadFile(filepath.Join(workspaceA, "sync_conflict.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<<<<<<< gitslice local", "const Value = \"local\"", "const Value = \"remote\"", ">>>>>>> gitslice remote"} {
		if !strings.Contains(string(conflicted), want) {
			t.Fatalf("conflict file missing %q:\n%s", want, string(conflicted))
		}
	}
	status := runCLI(t, home, workspaceA, "status")
	if !strings.Contains(status, "conflicts: 1 unresolved path(s)") {
		t.Fatalf("status did not report conflict:\n%s", status)
	}
	_, submitErr := runCLIFails(t, home, workspaceA, "cs", "submit")
	if !strings.Contains(submitErr, "unresolved sync conflicts") {
		t.Fatalf("submit did not reject unresolved conflicts:\n%s", submitErr)
	}

	const resolvedContent = "package payment\nconst Value = \"resolved\"\n"
	writeWorkspaceFile(t, workspaceA, "sync_conflict.go", resolvedContent)
	runCLI(t, home, workspaceA, "cs", "update")
	runCLI(t, home, workspaceA, "cs", "submit")

	verifyWorkspace := t.TempDir()
	runCLI(t, home, verifyWorkspace, "workspace", "init", "acme/payment")
	assertWorkspaceFile(t, verifyWorkspace, "acme/payment/sync_conflict.go", resolvedContent)
}
