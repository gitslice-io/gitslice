package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

type reattachedConversationKey struct {
	keyID     string
	secret    string
	expiresAt time.Time
	role      string
	runHome   string
}

func (d *agentDaemon) setReattachedKey(conversationID string, rk reattachedConversationKey) {
	if strings.TrimSpace(conversationID) == "" || strings.TrimSpace(rk.keyID) == "" {
		return
	}
	d.mu.Lock()
	if d.reattachedKeys == nil {
		d.reattachedKeys = map[string]reattachedConversationKey{}
	}
	d.reattachedKeys[conversationID] = rk
	d.mu.Unlock()
}

func (d *agentDaemon) takeReattachedKey(conversationID string) (reattachedConversationKey, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rk, ok := d.reattachedKeys[conversationID]
	if ok {
		delete(d.reattachedKeys, conversationID)
	}
	return rk, ok
}

func (d *agentDaemon) conversationRunHomeDir(conversationID string) string {
	if strings.TrimSpace(d.workingDir) == "" || strings.TrimSpace(conversationID) == "" {
		return ""
	}
	if _, _, err := agentConversationPaths(d.workingDir, conversationID); err != nil {
		return ""
	}
	return filepath.Join(d.workingDir, "run-homes", conversationID)
}

func conversationIDFromWorkdir(workingDir, workdir string) string {
	if strings.TrimSpace(workingDir) == "" || strings.TrimSpace(workdir) == "" {
		return ""
	}
	if filepath.Dir(workdir) == filepath.Join(workingDir, "conversations") {
		return filepath.Base(workdir)
	}
	return ""
}

// conversationRunner is the daemon's Runner confined to one conversation's run
// home: its mount state and sockets live under that home, as those of the gs
// processes the daemon starts with HOME=runHome do. A host XDG_RUNTIME_DIR,
// which those processes honor, is passed on explicitly.
func (d *agentDaemon) conversationRunner(conversationID string) Runner {
	r := d.runner
	if runHome := d.conversationRunHomeDir(conversationID); runHome != "" {
		r.Home = runHome
		if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
			r.Env = map[string]string{"XDG_RUNTIME_DIR": dir}
		}
	}
	return r
}

func (d *agentDaemon) lazyWorkspaceForConversation(conversationID, workdir string) (mountRecord, Runner, bool) {
	if runHome := d.conversationRunHomeDir(conversationID); runHome != "" {
		convRunner := d.conversationRunner(conversationID)
		if rec, ok := convRunner.lazyWorkspaceAt(workdir); ok {
			return rec, convRunner, true
		}
	}
	if rec, ok := d.runner.lazyWorkspaceAt(workdir); ok {
		return rec, d.runner, true
	}
	return mountRecord{}, Runner{}, false
}

func (d *agentDaemon) workspaceMounted(dir string) bool {
	if d.isMountedFn != nil {
		return d.isMountedFn(dir)
	}
	return isMounted(dir)
}

func (d *agentDaemon) unmountConversationLazyWorkspace(ctx context.Context, conversationID string, rec mountRecord) error {
	if d.unmountFn != nil {
		return d.unmountFn(ctx, conversationID, rec)
	}
	if runHome := d.conversationRunHomeDir(conversationID); runHome != "" {
		envOverrides := map[string]string{
			"HOME":          runHome,
			"XDG_DATA_HOME": filepath.Join(runHome, ".local", "share"),
		}
		if out, err := d.runGSWithEnv(ctx, "", envOverrides, "--quiet", "unmount", rec.MountPath); err == nil {
			return nil
		} else if !d.workspaceMounted(rec.MountPath) {
			return nil
		} else if fallbackErr := d.conversationRunner(conversationID).unmountLazyWorkspace(rec); fallbackErr == nil {
			return nil
		} else {
			return fmt.Errorf("%w (%s)", fallbackErr, out)
		}
	}
	return d.runner.unmountLazyWorkspace(rec)
}

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
	childKeyID := ""
	runHome := ""
	if conv != nil {
		conv.cancelRun()
		conv.closeSession()
		workdir = conv.workdir
		childKeyID = conv.getChildKeyID()
		runHome = conv.getRunHome()
	} else if _, dir, err := agentConversationPaths(d.workingDir, conversationID); err == nil {
		workdir = dir
		if meta, err := readConversationMeta(filepath.Join(workdir, agentMetaFilename)); err == nil {
			childKeyID = strings.TrimSpace(meta.ChildKeyID)
		}
	}
	if rk, ok := d.takeReattachedKey(conversationID); ok && childKeyID == "" {
		childKeyID = rk.keyID
		if runHome == "" {
			runHome = rk.runHome
		}
	}

	deleteWorkspace := closeMsg.GetDeleteWorkspace()
	// Unmount and remove/archive BEFORE deleting runHome so mount.json under
	// run-homes/<id> remains readable for clean FUSE unmount or archival.
	d.removeOrArchiveWorkspaceForConversation(conversationID, workdir, deleteWorkspace)
	if childKeyID != "" {
		d.revokeConversationChildKey(context.Background(), childKeyID)
	}
	if deleteWorkspace {
		d.cleanupConversationRunHome(conversationID, runHome)
	}
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
		d.removeOrArchiveWorkspaceForConversation(conv.id, conv.workdir, true)
		if keyID := conv.getChildKeyID(); keyID != "" {
			d.revokeConversationChildKey(context.Background(), keyID)
		}
		d.cleanupConversationRunHome(conv.id, conv.getRunHome())
	}
}

// removeOrArchiveWorkspace deletes the workspace dir when deleteWorkspace is set,
// otherwise moves it under conversations/.archived/. Failures are logged, not
// fatal — the conversation is already closed server-side.
func (d *agentDaemon) removeOrArchiveWorkspace(workdir string, deleteWorkspace bool) {
	d.removeOrArchiveWorkspaceForConversation(conversationIDFromWorkdir(d.workingDir, workdir), workdir, deleteWorkspace)
}

func (d *agentDaemon) removeOrArchiveWorkspaceForConversation(conversationID, workdir string, deleteWorkspace bool) {
	if strings.TrimSpace(workdir) == "" {
		return
	}
	// A lazy workspace is unmounted first; its state goes with it, or
	// follows it into the archive (gs mount --workspace re-attaches it).
	rec, recRunner, lazy := d.lazyWorkspaceForConversation(conversationID, workdir)
	if lazy && d.workspaceMounted(workdir) {
		if err := d.unmountConversationLazyWorkspace(context.Background(), conversationID, rec); err != nil {
			fmt.Fprintf(d.runner.stderr(), "agent workspace unmount failed for %s: %v\n", workdir, err)
			return
		}
	}
	if deleteWorkspace {
		if lazy {
			if err := os.RemoveAll(recRunner.mountStateDir(rec.ID)); err != nil {
				fmt.Fprintf(d.runner.stderr(), "agent workspace remove failed for %s: %v\n", workdir, err)
			}
		}
		if err := os.RemoveAll(workdir); err != nil {
			fmt.Fprintf(d.runner.stderr(), "agent workspace remove failed for %s: %v\n", workdir, err)
		}
		return
	}
	archiveDir := filepath.Join(d.workingDir, "conversations", ".archived")
	if err := os.MkdirAll(archiveDir, 0o700); err != nil {
		fmt.Fprintf(d.runner.stderr(), "agent workspace archive mkdir failed: %v\n", err)
		return
	}
	_ = os.Chmod(filepath.Dir(archiveDir), 0o700)
	_ = os.Chmod(archiveDir, 0o700)
	archived := filepath.Join(archiveDir, filepath.Base(workdir))
	if err := os.Rename(workdir, archived); err != nil {
		fmt.Fprintf(d.runner.stderr(), "agent workspace archive failed for %s: %v\n", workdir, err)
		return
	}
	if lazy {
		rec.MountPath = archived
		if err := recRunner.writeMountRecord(rec); err != nil {
			fmt.Fprintf(d.runner.stderr(), "agent workspace archive failed for %s: %v\n", workdir, err)
		}
	}
}

// workspaceUsable reports whether a conversation's workspace is there to
// work in: its directory exists and, for a lazy workspace, is mounted.
func (d *agentDaemon) workspaceUsable(workdir string) bool {
	return d.conversationWorkspaceUsable(conversationIDFromWorkdir(d.workingDir, workdir), workdir)
}

func (d *agentDaemon) conversationWorkspaceUsable(conversationID, workdir string) bool {
	if !workspaceDirExists(workdir) {
		return false
	}
	if _, _, lazy := d.lazyWorkspaceForConversation(conversationID, workdir); lazy {
		return d.workspaceMounted(workdir)
	}
	return true
}

// reattachLazyWorkspaces mounts again the lazy workspaces of this daemon's
// conversations that are not mounted (after a reboot, or a mount daemon that
// died), so their files and metadata are there when the conversations load.
func (d *agentDaemon) reattachLazyWorkspaces(ctx context.Context) {
	if strings.TrimSpace(d.workingDir) == "" {
		return
	}
	runHomesRoot := filepath.Join(d.workingDir, "run-homes")
	entries, err := os.ReadDir(runHomesRoot)
	if err != nil {
		return
	}
	conversations := filepath.Join(d.workingDir, "conversations")
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		convID := strings.TrimSpace(entry.Name())
		if _, _, err := agentConversationPaths(d.workingDir, convID); err != nil {
			continue
		}
		convRunner := d.conversationRunner(convID)
		records, err := convRunner.mountRecords()
		if err != nil {
			continue
		}
		runHome := filepath.Join(runHomesRoot, convID)
		for _, rec := range records {
			if rec.MountPath != filepath.Join(conversations, convID) || d.workspaceMounted(rec.MountPath) {
				continue
			}
			role := "reader"
			if meta, metaErr := readConversationMeta(filepath.Join(rec.MountPath, agentMetaFilename)); metaErr == nil {
				if normalizedRole := strings.ToLower(strings.TrimSpace(meta.Role)); normalizedRole != "" {
					role = normalizedRole
				}
			}
			keyID, secret, expiresAt, err := d.mintConversationChildKey(ctx, convID, rec.Account, rec.Slice, role)
			if err != nil {
				fmt.Fprintf(d.runner.stderr(), "agent workspace remount failed for %s: %v\n", rec.MountPath, err)
				continue
			}
			_ = os.Remove(filepath.Join(runHome, ".gitslice", "config.json"))
			childEnv := d.conversationEnvMap(secret, runHome)
			if out, err := d.runGSWithEnv(ctx, "", childEnv, "--quiet", "mount", "--workspace", rec.ID, rec.MountPath); err != nil {
				d.revokeConversationChildKey(context.Background(), keyID)
				fmt.Fprintf(d.runner.stderr(), "agent workspace remount failed for %s: %v: %s\n", rec.MountPath, err, out)
				continue
			}
			d.setReattachedKey(convID, reattachedConversationKey{
				keyID:     keyID,
				secret:    secret,
				expiresAt: expiresAt,
				role:      role,
				runHome:   runHome,
			})
		}
	}
}

// runGS runs this gs binary with the daemon's environment.
func (d *agentDaemon) runGS(ctx context.Context, dir string, args ...string) (string, error) {
	return d.runGSWithEnv(ctx, dir, nil, args...)
}

func (d *agentDaemon) runGSWithEnv(ctx context.Context, dir string, envOverrides map[string]string, args ...string) (string, error) {
	if d.runGSFn != nil {
		return d.runGSFn(ctx, dir, envOverrides, args...)
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Dir = dir
	cmd.Env = agentWorkspaceInitEnvWithOverrides(d.runner, d.cfg, envOverrides)
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
	return d.hydrateWorkspaceWithEnv(ctx, conv, sliceRef, nil)
}

func (d *agentDaemon) hydrateWorkspaceWithEnv(ctx context.Context, conv *agentConversation, sliceRef string, envOverrides map[string]string) error {
	conversationsDir := filepath.Dir(conv.workdir)
	if err := os.MkdirAll(conversationsDir, 0o700); err != nil {
		return err
	}
	_ = os.Chmod(conversationsDir, 0o700)
	if err := os.MkdirAll(conv.workdir, 0o700); err != nil {
		return err
	}
	_ = os.Chmod(conv.workdir, 0o700)
	_, _, lazy := d.lazyWorkspaceForConversation(conv.id, conv.workdir)
	if lazy && d.workspaceMounted(conv.workdir) {
		return nil
	}
	if d.lazy || lazy {
		// Lazy: a mount (re-attached if the conversation had one), whose
		// edits stay local until the turn's capture, as a checkout's do.
		output, err := d.runGSWithEnv(ctx, "", envOverrides, "--quiet", "mount", sliceRef, conv.workdir, "--agent-conversation", conv.id)
		if err == nil {
			return nil
		}
		if lazy || !strings.Contains(output, "needs FUSE") {
			return fmt.Errorf("mounting the workspace for %s failed: %w: %s", sliceRef, err, output)
		}
		// No FUSE here: forget the workspace the attempt made, and check out.
		if rec, recRunner, ok := d.lazyWorkspaceForConversation(conv.id, conv.workdir); ok {
			_ = os.RemoveAll(recRunner.mountStateDir(rec.ID))
		}
		fmt.Fprintf(d.runner.stderr(), "lazy workspaces are not available here (%s); checking out instead\n", output)
	}
	output, err := d.workspaceInit(ctx, conv, sliceRef, envOverrides)
	if err == nil {
		return nil
	}
	detail := strings.TrimSpace(output)
	if detail == "" {
		return fmt.Errorf("workspace init failed for %s: %w", sliceRef, err)
	}
	return fmt.Errorf("workspace init failed for %s: %w: %s", sliceRef, err, detail)
}

// workspaceInit checks the slice out into the conversation's workspace
// directory. It runs in this process, in a Runner scoped to that directory and
// to the conversation's identity (see childRunner), not as a gs subprocess
// whose output has to be read back.
func (d *agentDaemon) workspaceInit(ctx context.Context, conv *agentConversation, sliceRef string, envOverrides map[string]string) (string, error) {
	if d.runGSFn != nil {
		return d.runGSFn(ctx, conv.workdir, envOverrides, "--quiet", "workspace", "init", sliceRef, "--agent-conversation", conv.id)
	}
	var stderr bytes.Buffer
	child := d.childRunner(conv.workdir, envOverrides, io.Discard)
	child.Stderr = io.MultiWriter(&stderr, d.runner.stderr())
	err := child.runWorkspaceInit(ctx, commandOptions{Format: "text", Quiet: true, AgentConversationID: conv.id}, sliceRef)
	return strings.TrimSpace(stderr.String()), err
}

// childRunner is the daemon's Runner for the work it used to hand to a gs
// subprocess: rooted at dir, with the conversation's identity (envOverrides:
// GS_TOKEN, HOME and the like) and the daemon's server, and with the host's
// own credentials hidden. Nothing is shared with the daemon's Runner but its
// settings, so conversations run side by side.
func (d *agentDaemon) childRunner(dir string, envOverrides map[string]string, stdout io.Writer) Runner {
	env := map[string]string{}
	if addr := strings.TrimSpace(d.cfg.ServerAddr); addr != "" {
		env["GS_SERVER_ADDR"] = addr
		env["GITSLICE_SERVER_ADDR"] = addr
	}
	if len(envOverrides) == 0 {
		if token := strings.TrimSpace(d.cfg.Token); token != "" {
			env["GS_TOKEN"] = token
		}
	}
	for key, value := range envOverrides {
		env[key] = value
	}
	child := d.runner
	child.Dir = dir
	child.Stdout = stdout
	child.Stderr = d.runner.stderr()
	child.Stdin = strings.NewReader("")
	child.Env = env
	if home := strings.TrimSpace(env["HOME"]); home != "" {
		child.Home = home
	}
	return child
}
