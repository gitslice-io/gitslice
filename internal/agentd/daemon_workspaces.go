package agentd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/mountd"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

type reattachedConversationKey struct {
	keyID     string
	secret    string
	expiresAt time.Time
	role      string
	runHome   string
}

func (d *Daemon) setReattachedKey(conversationID string, rk reattachedConversationKey) {
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

func (d *Daemon) takeReattachedKey(conversationID string) (reattachedConversationKey, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rk, ok := d.reattachedKeys[conversationID]
	if ok {
		delete(d.reattachedKeys, conversationID)
	}
	return rk, ok
}

func (d *Daemon) conversationRunHomeDir(conversationID string) string {
	if strings.TrimSpace(d.WorkingDir) == "" || strings.TrimSpace(conversationID) == "" {
		return ""
	}
	if _, _, err := ConversationPaths(d.WorkingDir, conversationID); err != nil {
		return ""
	}
	return filepath.Join(d.WorkingDir, "run-homes", conversationID)
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

func mountsRootForHome(home string) string {
	if home == "" {
		if dataHome := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); dataHome != "" {
			return filepath.Join(dataHome, "gitslice", "mounts")
		}
		if h := strings.TrimSpace(os.Getenv("HOME")); h != "" {
			home = h
		} else {
			home, _ = os.UserHomeDir()
		}
	}
	return filepath.Join(home, ".local", "share", "gitslice", "mounts")
}

func runtimeDirForHome(explicitHome string, env map[string]string) string {
	resolvedHome := explicitHome
	if resolvedHome == "" {
		if h := strings.TrimSpace(os.Getenv("HOME")); h != "" {
			resolvedHome = h
		} else {
			resolvedHome, _ = os.UserHomeDir()
		}
	}
	getenv := func(key string) string {
		if env != nil {
			if v, ok := env[key]; ok {
				return v
			}
		}
		return os.Getenv(key)
	}
	return mountd.RuntimeDir(explicitHome, resolvedHome, env, getenv)
}

func conversationEnvForMountRuntime(runHome string) map[string]string {
	if runHome == "" {
		return nil
	}
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return map[string]string{"XDG_RUNTIME_DIR": dir}
	}
	return nil
}

func lazyWorkspaceAt(mountsRoot, dir string) (mountd.Record, bool) {
	records, err := mountd.Records(mountsRoot)
	if err != nil {
		return mountd.Record{}, false
	}
	for _, rec := range records {
		if rec.MountPath == dir {
			return rec, true
		}
	}
	return mountd.Record{}, false
}

func (d *Daemon) lazyWorkspaceForConversation(conversationID, workdir string) (mountd.Record, string, string, bool) {
	if runHome := d.conversationRunHomeDir(conversationID); runHome != "" {
		mountsRoot := mountsRootForHome(runHome)
		if rec, ok := lazyWorkspaceAt(mountsRoot, workdir); ok {
			return rec, runHome, mountsRoot, true
		}
	}
	mountsRoot := mountsRootForHome(d.Host.Home)
	if rec, ok := lazyWorkspaceAt(mountsRoot, workdir); ok {
		return rec, d.Host.Home, mountsRoot, true
	}
	return mountd.Record{}, "", "", false
}

func (d *Daemon) workspaceMounted(dir string) bool {
	if d.IsMountedFn != nil {
		return d.IsMountedFn(dir)
	}
	return mountd.IsMounted(dir)
}

func (d *Daemon) unmountLazyWorkspace(explicitHome string, rec mountd.Record) error {
	runtimeDir := runtimeDirForHome(explicitHome, conversationEnvForMountRuntime(explicitHome))
	if _, err := mountd.CallDaemon(runtimeDir, rec.ID, mountd.ControlRequest{Op: "unmount"}); err != nil {
		if err := mountd.ForceUnmount(rec.MountPath); err != nil {
			return err
		}
	}
	for i := 0; i < 100 && d.workspaceMounted(rec.MountPath); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if d.workspaceMounted(rec.MountPath) {
		return NewError("unmount_failed", rec.MountPath+" is still mounted", "A program may be using it (lsof +D "+rec.MountPath+"); close it and try again.")
	}
	return nil
}

func (d *Daemon) unmountConversationLazyWorkspace(ctx context.Context, conversationID string, rec mountd.Record) error {
	if d.UnmountFn != nil {
		return d.UnmountFn(ctx, conversationID, rec)
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
		} else if fallbackErr := d.unmountLazyWorkspace(runHome, rec); fallbackErr == nil {
			return nil
		} else {
			return fmt.Errorf("%w (%s)", fallbackErr, out)
		}
	}
	return d.unmountLazyWorkspace(d.Host.Home, rec)
}

func (d *Daemon) HandleCloseWorkspace(closeMsg *corev1.CloseWorkspace) {
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
	} else if _, dir, err := ConversationPaths(d.WorkingDir, conversationID); err == nil {
		workdir = dir
		if meta, err := readConversationMeta(filepath.Join(workdir, MetaFilename)); err == nil {
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
	d.removeOrArchiveWorkspaceForConversation(conversationID, workdir, deleteWorkspace)
	if childKeyID != "" {
		d.revokeConversationChildKey(context.Background(), childKeyID)
	}
	if deleteWorkspace {
		d.cleanupConversationRunHome(conversationID, runHome)
	}
}

func (d *Daemon) HandleReconcileWorkspaces(reconcile *corev1.ReconcileWorkspaces) {
	active := make(map[string]struct{}, len(reconcile.GetActiveConversationIds()))
	for _, id := range reconcile.GetActiveConversationIds() {
		active[strings.TrimSpace(id)] = struct{}{}
	}
	d.mu.Lock()
	stale := make([]*Conversation, 0)
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

func (d *Daemon) removeOrArchiveWorkspace(workdir string, deleteWorkspace bool) {
	d.removeOrArchiveWorkspaceForConversation(conversationIDFromWorkdir(d.WorkingDir, workdir), workdir, deleteWorkspace)
}

func (d *Daemon) removeOrArchiveWorkspaceForConversation(conversationID, workdir string, deleteWorkspace bool) {
	if strings.TrimSpace(workdir) == "" {
		return
	}
	rec, _, mountsRoot, lazy := d.lazyWorkspaceForConversation(conversationID, workdir)
	if lazy && d.workspaceMounted(workdir) {
		if err := d.unmountConversationLazyWorkspace(context.Background(), conversationID, rec); err != nil {
			fmt.Fprintf(d.Host.stderr(), "agent workspace unmount failed for %s: %v\n", workdir, err)
			return
		}
	}
	if deleteWorkspace {
		if lazy {
			if err := os.RemoveAll(mountd.StateDir(mountsRoot, rec.ID)); err != nil {
				fmt.Fprintf(d.Host.stderr(), "agent workspace remove failed for %s: %v\n", workdir, err)
			}
		}
		if err := os.RemoveAll(workdir); err != nil {
			fmt.Fprintf(d.Host.stderr(), "agent workspace remove failed for %s: %v\n", workdir, err)
		}
		return
	}
	archiveDir := filepath.Join(d.WorkingDir, "conversations", ".archived")
	if err := os.MkdirAll(archiveDir, 0o700); err != nil {
		fmt.Fprintf(d.Host.stderr(), "agent workspace archive mkdir failed: %v\n", err)
		return
	}
	_ = os.Chmod(filepath.Dir(archiveDir), 0o700)
	_ = os.Chmod(archiveDir, 0o700)
	archived := filepath.Join(archiveDir, filepath.Base(workdir))
	if err := os.Rename(workdir, archived); err != nil {
		fmt.Fprintf(d.Host.stderr(), "agent workspace archive failed for %s: %v\n", workdir, err)
		return
	}
	if lazy {
		rec.MountPath = archived
		if err := mountd.WriteRecord(mountsRoot, rec); err != nil {
			fmt.Fprintf(d.Host.stderr(), "agent workspace archive failed for %s: %v\n", workdir, err)
		}
	}
}

func (d *Daemon) workspaceUsable(workdir string) bool {
	return d.conversationWorkspaceUsable(conversationIDFromWorkdir(d.WorkingDir, workdir), workdir)
}

func (d *Daemon) conversationWorkspaceUsable(conversationID, workdir string) bool {
	if !workspaceDirExists(workdir) {
		return false
	}
	if _, _, _, lazy := d.lazyWorkspaceForConversation(conversationID, workdir); lazy {
		return d.workspaceMounted(workdir)
	}
	return true
}

func (d *Daemon) ReattachLazyWorkspaces(ctx context.Context) {
	if strings.TrimSpace(d.WorkingDir) == "" {
		return
	}
	runHomesRoot := filepath.Join(d.WorkingDir, "run-homes")
	entries, err := os.ReadDir(runHomesRoot)
	if err != nil {
		return
	}
	conversations := filepath.Join(d.WorkingDir, "conversations")
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		convID := strings.TrimSpace(entry.Name())
		if _, _, err := ConversationPaths(d.WorkingDir, convID); err != nil {
			continue
		}
		runHome := filepath.Join(runHomesRoot, convID)
		records, err := mountd.Records(mountsRootForHome(runHome))
		if err != nil {
			continue
		}
		for _, rec := range records {
			if rec.MountPath != filepath.Join(conversations, convID) || d.workspaceMounted(rec.MountPath) {
				continue
			}
			role := "reader"
			if meta, metaErr := readConversationMeta(filepath.Join(rec.MountPath, MetaFilename)); metaErr == nil {
				if normalizedRole := strings.ToLower(strings.TrimSpace(meta.Role)); normalizedRole != "" {
					role = normalizedRole
				}
			}
			keyID, secret, expiresAt, err := d.mintConversationChildKey(ctx, convID, rec.Account, rec.Slice, role)
			if err != nil {
				fmt.Fprintf(d.Host.stderr(), "agent workspace remount failed for %s: %v\n", rec.MountPath, err)
				continue
			}
			_ = os.Remove(filepath.Join(runHome, ".gitslice", "config.json"))
			childEnv := d.conversationEnvMap(secret, runHome)
			if out, err := d.runGSWithEnv(ctx, "", childEnv, "--quiet", "mount", "--workspace", rec.ID, rec.MountPath); err != nil {
				d.revokeConversationChildKey(context.Background(), keyID)
				fmt.Fprintf(d.Host.stderr(), "agent workspace remount failed for %s: %v: %s\n", rec.MountPath, err, out)
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

func (d *Daemon) runGS(ctx context.Context, dir string, args ...string) (string, error) {
	return d.runGSWithEnv(ctx, dir, nil, args...)
}

func (d *Daemon) runGSWithEnv(ctx context.Context, dir string, envOverrides map[string]string, args ...string) (string, error) {
	if d.RunGSFn != nil {
		return d.RunGSFn(ctx, dir, envOverrides, args...)
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Dir = dir
	cmd.Env = d.workspaceInitEnvWithOverrides(envOverrides)
	output, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(output)), err
}

func (d *Daemon) workspaceInitEnv() []string {
	env := append([]string{}, os.Environ()...)
	env = append(env, "GS_SERVER_ADDR="+d.Host.ServerAddr)
	env = append(env, "GITSLICE_SERVER_ADDR="+d.Host.ServerAddr)
	if d.Host.Home != "" {
		env = append(env, "HOME="+d.Host.Home)
	}
	return env
}

func (d *Daemon) workspaceInitEnvWithOverrides(overrides map[string]string) []string {
	if len(overrides) == 0 {
		return d.workspaceInitEnv()
	}
	return MergeEnv(SanitizedRuntimeEnv(d.workspaceInitEnv()), overrides)
}

func workspaceDirExists(workdir string) bool {
	if strings.TrimSpace(workdir) == "" {
		return false
	}
	info, err := os.Stat(workdir)
	return err == nil && info.IsDir()
}

func (d *Daemon) hydrateWorkspace(ctx context.Context, conv *Conversation, sliceRef string) error {
	return d.hydrateWorkspaceWithEnv(ctx, conv, sliceRef, nil)
}

func (d *Daemon) hydrateWorkspaceWithEnv(ctx context.Context, conv *Conversation, sliceRef string, envOverrides map[string]string) error {
	conversationsDir := filepath.Dir(conv.workdir)
	if err := os.MkdirAll(conversationsDir, 0o700); err != nil {
		return err
	}
	_ = os.Chmod(conversationsDir, 0o700)
	if err := os.MkdirAll(conv.workdir, 0o700); err != nil {
		return err
	}
	_ = os.Chmod(conv.workdir, 0o700)
	_, _, _, lazy := d.lazyWorkspaceForConversation(conv.id, conv.workdir)
	if lazy && d.workspaceMounted(conv.workdir) {
		return nil
	}
	if d.Lazy || lazy {
		output, err := d.runGSWithEnv(ctx, "", envOverrides, "--quiet", "mount", sliceRef, conv.workdir, "--agent-conversation", conv.id)
		if err == nil {
			return nil
		}
		if lazy || !strings.Contains(output, "needs FUSE") {
			return fmt.Errorf("mounting the workspace for %s failed: %w: %s", sliceRef, err, output)
		}
		if rec, _, mountsRoot, ok := d.lazyWorkspaceForConversation(conv.id, conv.workdir); ok {
			_ = os.RemoveAll(mountd.StateDir(mountsRoot, rec.ID))
		}
		fmt.Fprintf(d.Host.stderr(), "lazy workspaces are not available here (%s); checking out instead\n", output)
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

func (d *Daemon) workspaceInit(ctx context.Context, conv *Conversation, sliceRef string, envOverrides map[string]string) (string, error) {
	if d.RunGSFn != nil || d.Host.WorkspaceInit == nil {
		return d.runGSWithEnv(ctx, conv.workdir, envOverrides, "--quiet", "workspace", "init", sliceRef, "--agent-conversation", conv.id)
	}
	return d.Host.WorkspaceInit(ctx, conv.workdir, conv.id, sliceRef, envOverrides)
}
