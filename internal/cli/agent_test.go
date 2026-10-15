package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestWorkspaceGSUserErrorOmitsCaptureDiagnostics(t *testing.T) {
	got := workspaceGSUserError("capture diagnostics: result=error total=2s\npermission denied\n")
	if got != "permission denied" {
		t.Fatalf("workspace error = %q, want permission denied", got)
	}
}

func TestAgentWorkspaceInstructionsIncludesEditableScope(t *testing.T) {
	got := agentWorkspaceInstructions([]string{"/nic/File"})
	for _, want := range []string{"/nic/File/", "only edit files", "canonical account-rooted", "gsfile:nic/File/Lol.txt", "not\n  `gsfile:Lol.txt`"} {
		if !strings.Contains(got, want) {
			t.Fatalf("instructions missing %q:\n%s", want, got)
		}
	}

	// No included paths falls back to generic scope guidance, not an empty list.
	fallback := agentWorkspaceInstructions(nil)
	if !strings.Contains(fallback, "included paths") {
		t.Fatalf("fallback instructions missing scope guidance:\n%s", fallback)
	}
}

func TestAgentWorkspaceInstructionsExplainGitsliceSideEffects(t *testing.T) {
	got := agentWorkspaceInstructions([]string{"/slices"})
	for _, want := range []string{
		"complete workspace result",
		"only after your turn completes",
		"`gs sync` is a\n  changeset-mutating rebase",
		"`gs import` is a server-side native operation",
		"local `git`\n  executable or `PATH`",
		"Existing or imported `AGENTS.md`",
		"`paths` matches changed repository paths only",
		"`workflow_dispatch`",
		"logical\n  repository-root namespace",
		"keep the source CI definition",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("instructions missing %q:\n%s", want, got)
		}
	}
	for _, notWant := range []string{
		"gs sync              pull the latest slice state",
		"Do NOT create AGENTS.md",
	} {
		if strings.Contains(got, notWant) {
			t.Fatalf("instructions unexpectedly contain %q:\n%s", notWant, got)
		}
	}
}

func TestConversationIncludedPaths(t *testing.T) {
	workdir := t.TempDir()
	gsDir := filepath.Join(workdir, ".gs")
	if err := os.MkdirAll(gsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := WorkspaceConfig{IncludedPaths: []string{"src/", "lib/"}}
	if err := writeJSONFile(filepath.Join(gsDir, "slice.json"), cfg, 0o644); err != nil {
		t.Fatal(err)
	}

	got := conversationIncludedPaths(workdir)
	if len(got) != 2 || got[0] != "src/" || got[1] != "lib/" {
		t.Fatalf("conversationIncludedPaths = %v, want [src/ lib/]", got)
	}

	// Missing config is best-effort: nil, no panic.
	if got := conversationIncludedPaths(t.TempDir()); got != nil {
		t.Fatalf("conversationIncludedPaths(empty) = %v, want nil", got)
	}
}

func TestRequireAgentWorkspaceDir(t *testing.T) {
	mkdirAll := func(t *testing.T, path string) {
		t.Helper()
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
	}
	writeFile := func(t *testing.T, path string) {
		t.Helper()
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	t.Run("empty dir is allowed", func(t *testing.T) {
		dir := t.TempDir()
		if err := (Runner{Dir: dir}).requireAgentWorkspaceDir(); err != nil {
			t.Fatalf("empty dir: unexpected error: %v", err)
		}
	})

	t.Run("prior conversations dir is allowed", func(t *testing.T) {
		dir := t.TempDir()
		mkdirAll(t, filepath.Join(dir, "conversations", "conv_1"))
		writeFile(t, filepath.Join(dir, ".DS_Store"))
		if err := (Runner{Dir: dir}).requireAgentWorkspaceDir(); err != nil {
			t.Fatalf("conversations dir: unexpected error: %v", err)
		}
	})

	t.Run("unrelated file is rejected", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "notes.txt"))
		err := (Runner{Dir: dir}).requireAgentWorkspaceDir()
		if !isUserErrorCode(err, "agent_workspace_not_empty") {
			t.Fatalf("expected agent_workspace_not_empty, got %v", err)
		}
	})

	t.Run("inside an existing workspace is rejected", func(t *testing.T) {
		dir := t.TempDir()
		mkdirAll(t, filepath.Join(dir, ".gs"))
		writeFile(t, filepath.Join(dir, ".gs", "slice.json"))
		err := (Runner{Dir: dir}).requireAgentWorkspaceDir()
		if !isUserErrorCode(err, "already_in_workspace") {
			t.Fatalf("expected already_in_workspace, got %v", err)
		}
	})
}

func TestHandleReconcileWorkspacesReapsInactiveOnly(t *testing.T) {
	root := t.TempDir()
	d := &agentDaemon{workingDir: root, conversations: map[string]*agentConversation{}}

	makeConv := func(id string, ready bool) *agentConversation {
		subdir, workdir, err := agentConversationPaths(root, id)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(workdir, 0o755); err != nil {
			t.Fatal(err)
		}
		conv := &agentConversation{id: id, workdir: workdir, workspaceSubdir: subdir, ready: make(chan struct{})}
		if ready {
			conv.setReady(nil)
		}
		d.conversations[id] = conv
		return conv
	}

	keep := makeConv("conv_keep", true)        // active + ready -> kept
	reap := makeConv("conv_reap", true)        // inactive + ready -> reaped
	pending := makeConv("conv_pending", false) // not in set but mid-hydration -> left alone

	d.handleReconcileWorkspaces(&corev1.ReconcileWorkspaces{ActiveConversationIds: []string{"conv_keep"}})

	if !workspaceDirExists(keep.workdir) {
		t.Fatalf("active workspace should be kept")
	}
	if workspaceDirExists(reap.workdir) {
		t.Fatalf("inactive workspace should be removed")
	}
	if !workspaceDirExists(pending.workdir) {
		t.Fatalf("mid-hydration workspace should be left alone")
	}

	d.mu.Lock()
	_, keepMapped := d.conversations["conv_keep"]
	_, reapMapped := d.conversations["conv_reap"]
	_, pendingMapped := d.conversations["conv_pending"]
	d.mu.Unlock()
	if !keepMapped || pendingMapped == false {
		t.Fatalf("kept/pending convs should remain mapped (keep=%v pending=%v)", keepMapped, pendingMapped)
	}
	if reapMapped {
		t.Fatalf("reaped conversation should be dropped from the map")
	}
}
