package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestContextReportsAuthAndWorkspace(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	nested := filepath.Join(workspace, "src", "pkg")
	if err := os.MkdirAll(filepath.Join(workspace, ".gs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	serverAddr := startFakeAuthStatusServer(t, "secret-token", "user_alice")
	var stdout, stderr bytes.Buffer
	r := Runner{Home: home, Dir: nested, Stdout: &stdout, Stderr: &stderr}
	if err := r.writeUserConfig(UserConfig{
		ServerAddr: serverAddr,
		Token:      "secret-token",
		SubjectID:  "stale_local_subject",
	}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(workspace, ".gs", "slice.json"), WorkspaceConfig{
		Account:        "acme",
		Slice:          "payment",
		SliceID:        "slice_acme_payment",
		DefinitionHash: "sha256:def",
		IncludedPaths:  []string{"/acme/payment"},
		BaseCommitID:   "cmt_config",
	}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(workspace, ".gs", "state.json"), WorkspaceState{
		CurrentChangesetID: "cs_123",
		CurrentPatchsetID:  "ps_123",
		BaseCommitID:       "cmt_state",
	}, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := r.Run(context.Background(), []string{"ctx", "--json"}); err != nil {
		t.Fatalf("context failed: %v\nstderr:\n%s", err, stderr.String())
	}
	var got contextOutput
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("context output is not JSON: %v\n%s", err, stdout.String())
	}
	if !got.SignedIn || got.SubjectID != "user_alice" || got.ServerAddr != serverAddr {
		t.Fatalf("unexpected auth context: %#v", got)
	}
	if got.Workspace == nil {
		t.Fatalf("expected workspace context: %#v", got)
	}
	if got.Workspace.Root != workspace || got.Workspace.Ref != "acme:payment" || got.Workspace.BaseCommitID != "cmt_state" {
		t.Fatalf("unexpected workspace context: %#v", got.Workspace)
	}
	if got.ActiveSlice != "acme:payment" || got.ActiveSliceSource != "workspace" {
		t.Fatalf("unexpected active slice: %#v", got)
	}
}
