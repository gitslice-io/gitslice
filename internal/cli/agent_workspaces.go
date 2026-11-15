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
	// A lazy workspace is unmounted first; its state goes with it, or
	// follows it into the archive (gs mount --workspace re-attaches it).
	rec, lazy := d.runner.lazyWorkspaceAt(workdir)
	if lazy && isMounted(workdir) {
		if err := unmountLazyWorkspace(rec); err != nil {
			fmt.Fprintf(d.runner.stderr(), "agent workspace unmount failed for %s: %v\n", workdir, err)
			return
		}
	}
	if deleteWorkspace {
		if lazy {
			if err := os.RemoveAll(d.runner.mountStateDir(rec.ID)); err != nil {
				fmt.Fprintf(d.runner.stderr(), "agent workspace remove failed for %s: %v\n", workdir, err)
			}
		}
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
	archived := filepath.Join(archiveDir, filepath.Base(workdir))
	if err := os.Rename(workdir, archived); err != nil {
		fmt.Fprintf(d.runner.stderr(), "agent workspace archive failed for %s: %v\n", workdir, err)
		return
	}
	if lazy {
		rec.MountPath = archived
		if err := d.runner.writeMountRecord(rec); err != nil {
			fmt.Fprintf(d.runner.stderr(), "agent workspace archive failed for %s: %v\n", workdir, err)
		}
	}
}

// workspaceUsable reports whether a conversation's workspace is there to
// work in: its directory exists and, for a lazy workspace, is mounted.
func (d *agentDaemon) workspaceUsable(workdir string) bool {
	if !workspaceDirExists(workdir) {
		return false
	}
	if _, lazy := d.runner.lazyWorkspaceAt(workdir); lazy {
		return isMounted(workdir)
	}
	return true
}

// reattachLazyWorkspaces mounts again the lazy workspaces of this daemon's
// conversations that are not mounted (after a reboot, or a mount daemon that
// died), so their files and metadata are there when the conversations load.
func (d *agentDaemon) reattachLazyWorkspaces(ctx context.Context) {
	records, err := d.runner.mountRecords()
	if err != nil {
		return
	}
	conversations := filepath.Join(d.workingDir, "conversations")
	for _, rec := range records {
		if filepath.Dir(rec.MountPath) != conversations || isMounted(rec.MountPath) {
			continue
		}
		if out, err := d.runGS(ctx, "", "--quiet", "mount", "--workspace", rec.ID, rec.MountPath); err != nil {
			fmt.Fprintf(d.runner.stderr(), "agent workspace remount failed for %s: %v: %s\n", rec.MountPath, err, out)
		}
	}
}

// runGS runs this gs binary with the daemon's environment.
func (d *agentDaemon) runGS(ctx context.Context, dir string, args ...string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Dir = dir
	cmd.Env = agentWorkspaceInitEnv(d.runner, d.cfg)
	output, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(output)), err
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
	_, lazy := d.runner.lazyWorkspaceAt(conv.workdir)
	if lazy && isMounted(conv.workdir) {
		return nil
	}
	if d.lazy || lazy {
		// Lazy: a mount (re-attached if the conversation had one), whose
		// edits stay local until the turn's capture, as a checkout's do.
		output, err := d.runGS(ctx, "", "--quiet", "mount", sliceRef, conv.workdir, "--agent-conversation", conv.id)
		if err == nil {
			return nil
		}
		if lazy || !strings.Contains(output, "needs FUSE") {
			return fmt.Errorf("mounting the workspace for %s failed: %w: %s", sliceRef, err, output)
		}
		// No FUSE here: forget the workspace the attempt made, and check out.
		if rec, ok := d.runner.lazyWorkspaceAt(conv.workdir); ok {
			_ = os.RemoveAll(d.runner.mountStateDir(rec.ID))
		}
		fmt.Fprintf(d.runner.stderr(), "lazy workspaces are not available here (%s); checking out instead\n", output)
	}
	output, err := d.runGS(ctx, conv.workdir, "--quiet", "workspace", "init", sliceRef, "--agent-conversation", conv.id)
	if err == nil {
		return nil
	}
	detail := strings.TrimSpace(output)
	if detail == "" {
		return fmt.Errorf("workspace init failed for %s: %w", sliceRef, err)
	}
	return fmt.Errorf("workspace init failed for %s: %w: %s", sliceRef, err, detail)
}
