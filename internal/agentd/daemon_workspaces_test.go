package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/mountd"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func lazyConversation(t *testing.T) (*Daemon, mountd.Record, string) {
	t.Helper()
	home := t.TempDir()
	d := &Daemon{Host: HostCallbacks{Home: home}, WorkingDir: t.TempDir()}
	workdir := filepath.Join(d.WorkingDir, "conversations", "conv-1")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatal(err)
	}
	rec := mountd.Record{ID: "0123456789abcdef", Account: "acme", Slice: "payment", MountPath: workdir, ConversationID: "conv-1"}
	mountsRoot := mountsRootForHome(home)
	if err := os.MkdirAll(mountd.StateDir(mountsRoot, rec.ID), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := mountd.WriteRecord(mountsRoot, rec); err != nil {
		t.Fatal(err)
	}
	return d, rec, workdir
}

func TestAgentWorkspaceUsableNeedsALazyWorkspaceMounted(t *testing.T) {
	d, _, workdir := lazyConversation(t)
	if d.workspaceUsable(workdir) {
		t.Fatal("an unmounted lazy workspace's empty directory counted as usable")
	}
	eager := filepath.Join(d.WorkingDir, "conversations", "conv-2")
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
	archived := filepath.Join(d.WorkingDir, "conversations", ".archived", "conv-1")
	if _, err := os.Stat(archived); err != nil {
		t.Fatalf("archived directory: %v", err)
	}
	got, err := mountd.ReadRecord(mountsRootForHome(d.Host.Home), rec.ID)
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
	if _, err := os.Stat(mountd.StateDir(mountsRootForHome(d.Host.Home), rec.ID)); !os.IsNotExist(err) {
		t.Fatalf("lazy workspace state after delete: %v", err)
	}
}

func TestHandleReconcileWorkspacesReapsInactiveOnly(t *testing.T) {
	root := t.TempDir()
	d := &Daemon{WorkingDir: root, conversations: map[string]*Conversation{}}

	makeConv := func(id string, ready bool) *Conversation {
		subdir, workdir, err := ConversationPaths(root, id)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(workdir, 0o755); err != nil {
			t.Fatal(err)
		}
		conv := &Conversation{id: id, workdir: workdir, workspaceSubdir: subdir, ready: make(chan struct{})}
		if ready {
			conv.setReady(nil)
		}
		d.conversations[id] = conv
		return conv
	}

	keep := makeConv("conv_keep", true)
	reap := makeConv("conv_reap", true)
	pending := makeConv("conv_pending", false)

	d.HandleReconcileWorkspaces(&corev1.ReconcileWorkspaces{ActiveConversationIds: []string{"conv_keep"}})

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
	if !keepMapped || !pendingMapped {
		t.Fatalf("kept/pending convs should remain mapped (keep=%v pending=%v)", keepMapped, pendingMapped)
	}
	if reapMapped {
		t.Fatalf("reaped conversation should be dropped from the map")
	}
}

func TestLazyWorkspaceIsolationReattachTeardownAndRotation(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	hostHome := t.TempDir()
	hostDataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", hostDataHome)

	workdir := filepath.Join(root, "conversations", "conv-lazy")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatal(err)
	}
	runHome := filepath.Join(root, "run-homes", "conv-lazy")
	runMountsRoot := mountsRootForHome(runHome)
	rec := mountd.Record{
		ID:             "0123456789abcdef",
		Account:        "acme",
		Slice:          "app",
		MountPath:      workdir,
		ConversationID: "conv-lazy",
	}
	if err := os.MkdirAll(mountd.StateDir(runMountsRoot, rec.ID), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := mountd.WriteRecord(runMountsRoot, rec); err != nil {
		t.Fatal(err)
	}

	metaBytes, err := json.Marshal(ConversationMeta{
		ConversationID: "conv-lazy",
		Title:          "Lazy Conv",
		ThreadID:       "th_prev",
		SliceAccount:   "acme",
		SliceName:      "app",
		SliceID:        "sl_1",
		Role:           "contributor",
		ChildKeyID:     "key_prev",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workdir, MetaFilename), metaBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	auth := &fakeAgentAuthClient{}
	mounted := false
	var gsCalls [][]string
	var gsEnvs []map[string]string
	var trace []string

	d := &Daemon{
		Host: HostCallbacks{
			Home:       hostHome,
			ServerAddr: "127.0.0.1:9090",
			Token:      "host-daemon-token",
		},
		Auth:          auth,
		BaseCtx:       ctx,
		WorkingDir:    root,
		DaemonID:      "agent_daemon_lazy",
		conversations: map[string]*Conversation{},
		IsMountedFn: func(dir string) bool {
			return dir == workdir && mounted
		},
		RunGSFn: func(_ context.Context, _ string, envOverrides map[string]string, args ...string) (string, error) {
			gsCalls = append(gsCalls, args)
			gsEnvs = append(gsEnvs, envOverrides)
			if slices.Contains(args, "mount") {
				trace = append(trace, "mount:"+envOverrides["GS_TOKEN"])
				mounted = true
			}
			return "", nil
		},
		UnmountFn: func(_ context.Context, conversationID string, r mountd.Record) error {
			if _, err := os.Stat(filepath.Join(mountd.StateDir(runMountsRoot, r.ID), "mount.json")); err != nil {
				t.Fatalf("mount.json missing during unmount for %s: %v", conversationID, err)
			}
			trace = append(trace, "unmount")
			mounted = false
			return nil
		},
	}

	// 1) ReattachLazyWorkspaces fails closed when key minting errors.
	auth.createErr = errors.New("mint refused")
	d.ReattachLazyWorkspaces(ctx)
	if len(gsCalls) != 0 {
		t.Fatalf("ReattachLazyWorkspaces must fail closed on CreateApiKey error, got calls: %v", gsCalls)
	}
	auth.createErr = nil

	// 2) ReattachLazyWorkspaces mints child key and mounts with isolated HOME + XDG_DATA_HOME + GS_TOKEN.
	d.ReattachLazyWorkspaces(ctx)
	if len(gsCalls) != 1 || !slices.Equal(gsCalls[0], []string{"--quiet", "mount", "--workspace", rec.ID, workdir}) {
		t.Fatalf("unexpected reattach gs calls: %v", gsCalls)
	}
	if gsEnvs[0]["HOME"] != runHome || gsEnvs[0]["XDG_DATA_HOME"] != filepath.Join(runHome, ".local", "share") || gsEnvs[0]["GS_TOKEN"] != "gs_tok_fake_1" {
		t.Fatalf("reattach env leaked or incomplete: %+v", gsEnvs[0])
	}
	if !d.conversationWorkspaceUsable("conv-lazy", workdir) {
		t.Fatal("expected mounted lazy workspace under run-homes to be usable")
	}

	// 3) LoadExistingConversations revokes previous daemon run's stale key and adopts reattached key.
	d.LoadExistingConversations()
	if len(auth.revokeReqs) != 1 || auth.revokeReqs[0].GetKeyId() != "key_prev" {
		t.Fatalf("expected stale key_prev revoked on LoadExistingConversations, revokes=%+v", auth.revokeReqs)
	}
	conv := d.getConversation("conv-lazy")
	if conv == nil || conv.getThreadID() != "th_prev" || conv.getChildKeyID() != "key_fake_1" {
		t.Fatalf("loaded conv missing preserved metadata or reattached key: %+v", conv)
	}

	// 4) Child-key rotation on mounted lazy workspace unmounts old mountd -> revokes old key -> remounts with new token.
	trace = nil
	conv.stateMu.Lock()
	conv.childExpiresAt = time.Now().Add(2 * time.Minute)
	conv.stateMu.Unlock()
	if _, err := d.ensureConversationChildKey(ctx, conv); err != nil {
		t.Fatalf("ensureConversationChildKey rotation: %v", err)
	}
	wantTrace := []string{"unmount", "mount:gs_tok_fake_2"}
	if !slices.Equal(trace, wantTrace) {
		t.Fatalf("rotation trace = %v, want %v", trace, wantTrace)
	}
	if len(auth.revokeReqs) != 2 || auth.revokeReqs[1].GetKeyId() != "key_fake_1" {
		t.Fatalf("expected key_fake_1 revoked during rotation, revokes=%+v", auth.revokeReqs)
	}
	if conv.getChildKeyID() != "key_fake_2" {
		t.Fatalf("rotated childKeyID = %q, want key_fake_2", conv.getChildKeyID())
	}

	// 5) HandleCloseWorkspace with deleteWorkspace=false unmounts first, archives workdir, updates mount.json, revokes key, keeps runHome.
	d.HandleCloseWorkspace(&corev1.CloseWorkspace{ConversationId: "conv-lazy", DeleteWorkspace: false})
	if mounted {
		t.Fatal("expected archive close to unmount lazy workspace")
	}
	archivedWorkdir := filepath.Join(root, "conversations", ".archived", "conv-lazy")
	if _, err := os.Stat(archivedWorkdir); err != nil {
		t.Fatalf("expected archived workdir at %s: %v", archivedWorkdir, err)
	}
	updatedRec, err := mountd.ReadRecord(runMountsRoot, rec.ID)
	if err != nil || updatedRec.MountPath != archivedWorkdir {
		t.Fatalf("archived mount record = %+v, err=%v", updatedRec, err)
	}
	if len(auth.revokeReqs) != 3 || auth.revokeReqs[2].GetKeyId() != "key_fake_2" {
		t.Fatalf("expected key_fake_2 revoked on close, revokes=%+v", auth.revokeReqs)
	}

	// 6) HandleCloseWorkspace with deleteWorkspace=true unmounts while mount.json exists, deletes mount state + runHome, and revokes key.
	convDel, err := d.createConversation("conv-del")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(convDel.workdir, 0o700); err != nil {
		t.Fatal(err)
	}
	convDel.setConversationInfo("Del", "acme", "app", "sl_1", "reader")
	convDel.setReady(nil)
	if _, err := d.ensureConversationChildKey(ctx, convDel); err != nil {
		t.Fatal(err)
	}
	delHome := filepath.Join(root, "run-homes", "conv-del")
	delMountsRoot := mountsRootForHome(delHome)
	delRec := mountd.Record{ID: "fedcba9876543210", Account: "acme", Slice: "app", MountPath: convDel.workdir, ConversationID: "conv-del"}
	if err := os.MkdirAll(mountd.StateDir(delMountsRoot, delRec.ID), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := mountd.WriteRecord(delMountsRoot, delRec); err != nil {
		t.Fatal(err)
	}
	d.IsMountedFn = func(dir string) bool {
		return dir == convDel.workdir
	}
	unmountChecked := false
	d.UnmountFn = func(_ context.Context, conversationID string, r mountd.Record) error {
		if _, err := os.Stat(filepath.Join(mountd.StateDir(delMountsRoot, r.ID), "mount.json")); err != nil {
			t.Fatalf("mount.json missing during delete close unmount for %s: %v", conversationID, err)
		}
		unmountChecked = true
		return nil
	}
	d.HandleCloseWorkspace(&corev1.CloseWorkspace{ConversationId: "conv-del", DeleteWorkspace: true})
	if !unmountChecked {
		t.Fatal("expected UnmountFn to run on delete close")
	}
	if _, err := os.Stat(delHome); !os.IsNotExist(err) {
		t.Fatalf("expected runHome %s removed after delete close, err=%v", delHome, err)
	}
	if len(auth.revokeReqs) != 4 || auth.revokeReqs[3].GetKeyId() != "key_fake_3" {
		t.Fatalf("expected key_fake_3 revoked on delete close, revokes=%+v", auth.revokeReqs)
	}
}
