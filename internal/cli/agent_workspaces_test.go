package cli

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

// lazyConversation makes an unmounted lazy workspace whose mount path is a
// conversation's directory, as gs agent start --lazy leaves one after a
// reboot.
func lazyConversation(t *testing.T) (*agentDaemon, mountRecord, string) {
	t.Helper()
	r := Runner{Home: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard}
	d := &agentDaemon{runner: r, workingDir: t.TempDir()}
	workdir := filepath.Join(d.workingDir, "conversations", "conv-1")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatal(err)
	}
	rec := mountRecord{ID: "0123456789abcdef", Account: "acme", Slice: "payment", MountPath: workdir, ConversationID: "conv-1"}
	if err := os.MkdirAll(r.mountStateDir(rec.ID), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := r.writeMountRecord(rec); err != nil {
		t.Fatal(err)
	}
	return d, rec, workdir
}

func TestAgentWorkspaceUsableNeedsALazyWorkspaceMounted(t *testing.T) {
	d, _, workdir := lazyConversation(t)
	if d.workspaceUsable(workdir) {
		t.Fatal("an unmounted lazy workspace's empty directory counted as usable")
	}
	eager := filepath.Join(d.workingDir, "conversations", "conv-2")
	if err := os.MkdirAll(eager, 0o755); err != nil {
		t.Fatal(err)
	}
	if !d.workspaceUsable(eager) {
		t.Fatal("a checked-out workspace counted as unusable")
	}
}

func TestArchivingALazyAgentWorkspaceKeepsItsState(t *testing.T) {
	d, rec, workdir := lazyConversation(t)
	d.removeOrArchiveWorkspace(workdir, false)
	archived := filepath.Join(d.workingDir, "conversations", ".archived", "conv-1")
	if _, err := os.Stat(archived); err != nil {
		t.Fatalf("archived directory: %v", err)
	}
	got, err := d.runner.readMountRecord(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.MountPath != archived {
		t.Fatalf("the workspace's mount path is %s after archiving, want %s", got.MountPath, archived)
	}
}

func TestDeletingALazyAgentWorkspaceDeletesItsState(t *testing.T) {
	d, rec, workdir := lazyConversation(t)
	d.removeOrArchiveWorkspace(workdir, true)
	if _, err := os.Stat(workdir); !os.IsNotExist(err) {
		t.Fatalf("workspace directory after delete: %v", err)
	}
	if _, err := os.Stat(d.runner.mountStateDir(rec.ID)); !os.IsNotExist(err) {
		t.Fatalf("lazy workspace state after delete: %v", err)
	}
}
