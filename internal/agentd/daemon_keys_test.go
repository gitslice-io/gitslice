package agentd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestRegisterCIDaemonUsesCIRuntime(t *testing.T) {
	msg := RegisterDaemonMessage("ci-host", CIRuntimeName, "", "", 0, true)
	if got := msg.GetRegister().GetRuntime(); got != "ci" {
		t.Fatalf("runtime = %q, want ci", got)
	}
}

func TestCIDaemonRefusesConversationStart(t *testing.T) {
	conv := &Conversation{id: "conversation-1"}
	d := &Daemon{
		conversations: map[string]*Conversation{conv.id: conv},
		Runtime:       nil,
	}
	d.HandleStartConversation(context.Background(), &corev1.StartConversation{ConversationId: conv.id})

	conv.outMu.Lock()
	defer conv.outMu.Unlock()
	if len(conv.pending) != 1 {
		t.Fatalf("events = %d, want 1", len(conv.pending))
	}
	event := conv.pending[0]
	if event.GetRole() != "system" || event.GetType() != "error" || !event.GetFinal() {
		t.Fatalf("event = %#v", event)
	}
	const want = "this daemon runs checks only (gs agent start --ci); start a conversation on another daemon"
	if event.GetText() != want {
		t.Fatalf("event text = %q, want %q", event.GetText(), want)
	}
}

func TestDaemonChildKeyMintingRenewalIsolationAndRevocation(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "conversations"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "run-homes"), 0o700); err != nil {
		t.Fatal(err)
	}
	origHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(origHome, ".gitslice"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(origHome, ".gitslice", "config.json"), []byte(`{"token":"host-secret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(origHome, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", "")

	auth := &fakeAgentAuthClient{}
	runtime := &fakeAgentRuntime{skipHistory: true}
	d := &Daemon{
		Host: HostCallbacks{
			Home:       origHome,
			ServerAddr: "127.0.0.1:9090",
			Token:      "daemon-root-token",
		},
		Auth:          auth,
		Runtime:       runtime,
		BaseCtx:       ctx,
		WorkingDir:    root,
		DaemonID:      "agent_daemon_1",
		AllowedSlices: []string{"acme/app"},
		conversations: map[string]*Conversation{},
	}

	// Out-of-scope slice is rejected without minting a key.
	d.HandleStartConversation(ctx, &corev1.StartConversation{
		ConversationId: "conv-disallowed",
		Slice:          &corev1.SliceRef{Account: "acme", Slice: "secret"},
		Role:           "reader",
	})
	if len(auth.createReqs) != 0 {
		t.Fatalf("expected 0 CreateApiKey calls for disallowed slice, got %d", len(auth.createReqs))
	}

	// Fail-closed when CreateApiKey fails.
	auth.createErr = errors.New("mint refused")
	d.HandleStartConversation(ctx, &corev1.StartConversation{
		ConversationId: "conv-fail",
		Slice:          &corev1.SliceRef{Account: "acme", Slice: "app"},
		Role:           "writer",
	})
	failConv := d.getConversation("conv-fail")
	if failConv == nil || failConv.waitReady(ctx) == nil {
		t.Fatalf("expected conv-fail to fail closed when child key minting fails")
	}
	auth.createErr = nil

	// Successful mint, isolated runHome without .gitslice/config.json, and CODEX_HOME preserved.
	conv, err := d.createConversation("conv-iso")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(conv.workdir, 0o700); err != nil {
		t.Fatal(err)
	}
	conv.setConversationInfo("Fix bug", "acme", "app", "sl_1", "writer")
	conv.setReady(nil)
	runtime.session = &fakeAgentSession{threadID: "th_1", err: errors.New("stop after run")}

	d.HandleUserMessage(ctx, &corev1.DeliverUserMessage{ConversationId: "conv-iso", Text: "hello", Seq: 1})
	if len(auth.createReqs) != 1 {
		t.Fatalf("CreateApiKey calls = %d, want 1", len(auth.createReqs))
	}
	req := auth.createReqs[0]
	if !slices.Equal(req.GetSlices(), []string{"acme/app"}) ||
		req.GetMaxRole() != "writer" ||
		req.GetExpiresInSeconds() != ChildKeyTTLSeconds ||
		req.GetPurpose() != "conversation conv-iso" ||
		req.GetDaemonId() != "agent_daemon_1" ||
		req.GetConversationId() != "conv-iso" {
		t.Fatalf("unexpected CreateApiKey request: %+v", req)
	}
	expectedRunHome := filepath.Join(root, "run-homes", "conv-iso")
	if runtime.gotEnv["HOME"] != expectedRunHome {
		t.Fatalf("runtime HOME = %q, want %q", runtime.gotEnv["HOME"], expectedRunHome)
	}
	if runtime.gotEnv["XDG_DATA_HOME"] != filepath.Join(expectedRunHome, ".local", "share") {
		t.Fatalf("runtime XDG_DATA_HOME = %q, want %q", runtime.gotEnv["XDG_DATA_HOME"], filepath.Join(expectedRunHome, ".local", "share"))
	}
	if runtime.gotEnv["GS_TOKEN"] != "gs_tok_fake_1" || runtime.gotEnv["GS_SERVER_ADDR"] != "127.0.0.1:9090" {
		t.Fatalf("unexpected runtime GS env: %+v", runtime.gotEnv)
	}
	if runtime.gotEnv["CODEX_HOME"] != filepath.Join(origHome, ".codex") {
		t.Fatalf("runtime CODEX_HOME = %q, want %q", runtime.gotEnv["CODEX_HOME"], filepath.Join(origHome, ".codex"))
	}
	if _, err := os.Stat(filepath.Join(expectedRunHome, ".gitslice", "config.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runHome must not contain .gitslice/config.json, stat err=%v", err)
	}

	// Metadata persists child_key_id and role, never the secret.
	d.persistConversationMeta(conv)
	metaBytes, err := os.ReadFile(filepath.Join(conv.workdir, MetaFilename))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(metaBytes), "gs_tok_fake_1") || !strings.Contains(string(metaBytes), "key_fake_1") || !strings.Contains(string(metaBytes), "writer") {
		t.Fatalf("unexpected persisted meta JSON:\n%s", string(metaBytes))
	}

	// Near-expiry key rotates cleanly, closing warm session and revoking previous key.
	warmSession := &fakeAgentSession{threadID: "th_1"}
	conv.stateMu.Lock()
	conv.session = warmSession
	conv.childExpiresAt = time.Now().Add(2 * time.Minute)
	conv.stateMu.Unlock()
	runtime.session = &fakeAgentSession{threadID: "th_2", err: errors.New("stop after rotate")}

	d.HandleUserMessage(ctx, &corev1.DeliverUserMessage{ConversationId: "conv-iso", Text: "turn 2", Seq: 2})
	if len(auth.createReqs) != 2 || len(auth.revokeReqs) != 1 || auth.revokeReqs[0].GetKeyId() != "key_fake_1" {
		t.Fatalf("expected rotation create+revoke, creates=%d revokes=%+v", len(auth.createReqs), auth.revokeReqs)
	}
	if warmSession.closed != 1 {
		t.Fatalf("expected warm session to close on key rotation, closed=%d", warmSession.closed)
	}
	if runtime.gotEnv["GS_TOKEN"] != "gs_tok_fake_2" {
		t.Fatalf("rotated GS_TOKEN = %q, want gs_tok_fake_2", runtime.gotEnv["GS_TOKEN"])
	}

	// Closing workspace revokes current key and removes runHome.
	d.HandleCloseWorkspace(&corev1.CloseWorkspace{ConversationId: "conv-iso", DeleteWorkspace: true})
	if len(auth.revokeReqs) != 2 || auth.revokeReqs[1].GetKeyId() != "key_fake_2" {
		t.Fatalf("expected key_fake_2 revoked on close, revokes=%+v", auth.revokeReqs)
	}
	if _, err := os.Stat(expectedRunHome); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected runHome removed on close, stat err=%v", err)
	}

	// ReconcileWorkspaces also revokes child key and removes runHome.
	conv2, err := d.createConversation("conv-stale")
	if err != nil {
		t.Fatal(err)
	}
	conv2.setConversationInfo("Stale", "acme", "app", "sl_1", "reader")
	conv2.setReady(nil)
	if _, err := d.ensureConversationChildKey(ctx, conv2); err != nil {
		t.Fatal(err)
	}
	staleHome := filepath.Join(root, "run-homes", "conv-stale")
	d.HandleReconcileWorkspaces(&corev1.ReconcileWorkspaces{})
	if len(auth.revokeReqs) != 3 || auth.revokeReqs[2].GetKeyId() != "key_fake_3" {
		t.Fatalf("expected key_fake_3 revoked on reconcile, revokes=%+v", auth.revokeReqs)
	}
	if _, err := os.Stat(staleHome); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected staleHome removed on reconcile, stat err=%v", err)
	}
}
