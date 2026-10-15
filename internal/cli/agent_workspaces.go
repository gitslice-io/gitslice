package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// handleCloseWorkspace tears a conversation down at the server's request: it
// cancels any in-flight turn, closes the runtime session, drops the conversation
// from the in-memory map, and removes (or archives) its on-disk workspace. The
// conversation's status is already inactive server-side and its transcript is
// persisted, so this only reclaims local state. Best-effort: a conversation not
// resident this run still has its dir removed if present.
func (d *agentDaemon) handleCloseWorkspace(closeMsg *corev1.CloseWorkspace) {
	conversationID := strings.TrimSpace(closeMsg.GetConversationId())
	if conversationID == "" {
		return
	}
	d.mu.Lock()
	conv := d.conversations[conversationID]
	delete(d.conversations, conversationID)
	d.mu.Unlock()

	workdir := ""
	if conv != nil {
		conv.cancelRun()
		conv.closeSession()
		workdir = conv.workdir
	} else if _, dir, err := agentConversationPaths(d.workingDir, conversationID); err == nil {
		workdir = dir
	}
	d.removeOrArchiveWorkspace(workdir, closeMsg.GetDeleteWorkspace())
}

// handleReconcileWorkspaces reaps the local workspace of any conversation the
// daemon holds that the server reports as no longer active. This cleans up
// conversations closed while the daemon was offline, whose live CloseWorkspace
// push was missed. Only settled (ready) conversations are reaped: one that is
// mid-hydration is either an active (re)start — and so present in the active
// set — or in progress, and must be left alone.
func (d *agentDaemon) handleReconcileWorkspaces(reconcile *corev1.ReconcileWorkspaces) {
	active := make(map[string]struct{}, len(reconcile.GetActiveConversationIds()))
	for _, id := range reconcile.GetActiveConversationIds() {
		active[strings.TrimSpace(id)] = struct{}{}
	}
	d.mu.Lock()
	stale := make([]*agentConversation, 0)
	for id, conv := range d.conversations {
		if _, ok := active[id]; ok {
			continue
		}
		if !conv.isReady() {
			continue
		}
		delete(d.conversations, id)
		stale = append(stale, conv)
	}
	d.mu.Unlock()
	for _, conv := range stale {
		conv.cancelRun()
		conv.closeSession()
		d.removeOrArchiveWorkspace(conv.workdir, true)
	}
}

// removeOrArchiveWorkspace deletes the workspace dir when deleteWorkspace is set,
// otherwise moves it under conversations/.archived/. Failures are logged, not
// fatal — the conversation is already closed server-side.
func (d *agentDaemon) removeOrArchiveWorkspace(workdir string, deleteWorkspace bool) {
	if strings.TrimSpace(workdir) == "" {
		return
	}
	if deleteWorkspace {
		if err := os.RemoveAll(workdir); err != nil {
			fmt.Fprintf(d.runner.stderr(), "agent workspace remove failed for %s: %v\n", workdir, err)
		}
		return
	}
	archiveDir := filepath.Join(d.workingDir, "conversations", ".archived")
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		fmt.Fprintf(d.runner.stderr(), "agent workspace archive mkdir failed: %v\n", err)
		return
	}
	if err := os.Rename(workdir, filepath.Join(archiveDir, filepath.Base(workdir))); err != nil {
		fmt.Fprintf(d.runner.stderr(), "agent workspace archive failed for %s: %v\n", workdir, err)
	}
}

func workspaceDirExists(workdir string) bool {
	if strings.TrimSpace(workdir) == "" {
		return false
	}
	info, err := os.Stat(workdir)
	return err == nil && info.IsDir()
}

func (d *agentDaemon) hydrateWorkspace(ctx context.Context, conv *agentConversation, sliceRef string) error {
	if err := os.MkdirAll(conv.workdir, 0o755); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, exe, "--quiet", "workspace", "init", sliceRef, "--agent-conversation", conv.id)
	cmd.Dir = conv.workdir
	cmd.Env = agentWorkspaceInitEnv(d.runner, d.cfg)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	detail := strings.TrimSpace(string(output))
	if detail == "" {
		return fmt.Errorf("workspace init failed for %s: %w", sliceRef, err)
	}
	return fmt.Errorf("workspace init failed for %s: %w: %s", sliceRef, err, detail)
}
