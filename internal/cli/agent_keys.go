package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func (d *agentDaemon) authServiceClient(ctx context.Context) (agentAuthClient, func(), context.Context, error) {
	if d.auth != nil {
		return d.auth, func() {}, ctx, nil
	}
	if strings.TrimSpace(d.cfg.ServerAddr) == "" || strings.TrimSpace(d.cfg.Token) == "" {
		return nil, nil, nil, fmt.Errorf("agent daemon has no auth credentials to manage scoped keys")
	}
	conn, err := dial(ctx, d.cfg.ServerAddr)
	if err != nil {
		return nil, nil, nil, err
	}
	return corev1.NewAuthServiceClient(conn), func() { _ = conn.Close() }, authContext(ctx, d.cfg), nil
}

func (d *agentDaemon) ensureConversationRunHome(conv *agentConversation) (string, error) {
	if strings.TrimSpace(d.workingDir) == "" {
		return "", fmt.Errorf("agent daemon working directory is not set")
	}
	if _, _, err := agentConversationPaths(d.workingDir, conv.id); err != nil {
		return "", err
	}
	runHomesRoot := filepath.Join(d.workingDir, "run-homes")
	if err := os.MkdirAll(runHomesRoot, 0o700); err != nil {
		return "", err
	}
	_ = os.Chmod(runHomesRoot, 0o700)
	runHome := filepath.Join(runHomesRoot, conv.id)
	if err := os.MkdirAll(runHome, 0o700); err != nil {
		return "", err
	}
	_ = os.Chmod(runHome, 0o700)
	// Per-run home must never carry a daemon-wide .gitslice/config.json.
	_ = os.Remove(filepath.Join(runHome, ".gitslice", "config.json"))
	conv.stateMu.Lock()
	conv.runHome = runHome
	conv.stateMu.Unlock()
	return runHome, nil
}

func (d *agentDaemon) conversationEnvMap(secret, runHome string) map[string]string {
	env := map[string]string{
		"HOME":          runHome,
		"XDG_DATA_HOME": filepath.Join(runHome, ".local", "share"),
		"GS_TOKEN":      secret,
	}
	serverAddr := strings.TrimSpace(d.cfg.ServerAddr)
	if serverAddr == "" {
		serverAddr = envServerAddrOverride()
	}
	if serverAddr != "" {
		env["GS_SERVER_ADDR"] = serverAddr
		env["GITSLICE_SERVER_ADDR"] = serverAddr
	}
	if strings.TrimSpace(os.Getenv("CODEX_HOME")) == "" {
		origHome := strings.TrimSpace(d.runner.Home)
		if origHome == "" {
			origHome = strings.TrimSpace(os.Getenv("HOME"))
		}
		if origHome == "" {
			origHome, _ = os.UserHomeDir()
		}
		if origHome != "" {
			codexDir := filepath.Join(origHome, ".codex")
			if info, err := os.Stat(codexDir); err == nil && info.IsDir() {
				env["CODEX_HOME"] = codexDir
			}
		}
	}
	return env
}

func (d *agentDaemon) mintConversationChildKey(ctx context.Context, conversationID, sliceAccount, sliceName, role string) (keyID, secret string, expiresAt time.Time, err error) {
	authClient, closeFn, callCtx, err := d.authServiceClient(ctx)
	if err != nil {
		return "", "", time.Time{}, err
	}
	defer closeFn()

	keyName := "conv-" + conversationID
	if len(keyName) > 64 {
		keyName = keyName[:64]
	}
	sliceRef := canonicalSliceKey(sliceAccount, sliceName)
	resp, err := authClient.CreateApiKey(callCtx, &corev1.CreateApiKeyRequest{
		Name:             keyName,
		Slices:           []string{sliceRef},
		MaxRole:          role,
		ExpiresInSeconds: conversationChildKeyTTLSeconds,
		Purpose:          "conversation " + conversationID,
		DaemonId:         d.currentDaemonID(),
		ConversationId:   conversationID,
	})
	if err != nil {
		return "", "", time.Time{}, err
	}
	newKeyID := strings.TrimSpace(resp.GetKey().GetId())
	newSecret := strings.TrimSpace(resp.GetSecret())
	if newKeyID == "" || newSecret == "" {
		return "", "", time.Time{}, fmt.Errorf("server returned incomplete child API key for conversation %s", conversationID)
	}
	var newExpiresAt time.Time
	if rawExp := strings.TrimSpace(resp.GetKey().GetExpiresAt()); rawExp != "" {
		if parsed, parseErr := time.Parse(time.RFC3339, rawExp); parseErr == nil {
			newExpiresAt = parsed
		}
	}
	return newKeyID, newSecret, newExpiresAt, nil
}

func (d *agentDaemon) ensureConversationChildKey(ctx context.Context, conv *agentConversation) (map[string]string, error) {
	conv.keyMu.Lock()
	defer conv.keyMu.Unlock()

	conv.stateMu.Lock()
	sliceAccount := conv.sliceAccount
	sliceName := conv.sliceName
	role := conv.role
	secret := conv.childSecret
	expiresAt := conv.childExpiresAt
	oldKeyID := conv.childKeyID
	conv.stateMu.Unlock()

	if strings.TrimSpace(sliceAccount) == "" || strings.TrimSpace(sliceName) == "" {
		return nil, fmt.Errorf("conversation %s is missing slice scope", conv.id)
	}
	if role == "" {
		role = "contributor"
	}
	if secret != "" && (expiresAt.IsZero() || time.Until(expiresAt) > conversationChildKeyRenewWindow) {
		runHome, err := d.ensureConversationRunHome(conv)
		if err != nil {
			return nil, err
		}
		return d.conversationEnvMap(secret, runHome), nil
	}

	newKeyID, newSecret, newExpiresAt, err := d.mintConversationChildKey(ctx, conv.id, sliceAccount, sliceName, role)
	if err != nil {
		return nil, err
	}

	runHome, err := d.ensureConversationRunHome(conv)
	if err != nil {
		d.revokeConversationChildKey(context.Background(), newKeyID)
		return nil, err
	}
	newEnv := d.conversationEnvMap(newSecret, runHome)

	if oldKeyID != "" && oldKeyID != newKeyID {
		// Force runtime subprocess restart on rotation so it picks up the fresh token.
		conv.closeSession()
		if rec, _, lazy := d.lazyWorkspaceForConversation(conv.id, conv.workdir); lazy && d.workspaceMounted(conv.workdir) {
			// A running mountd freezes GS_TOKEN at startup; shut it down cleanly
			// before revoking oldKeyID and remount with newEnv.
			if err := d.unmountConversationLazyWorkspace(ctx, conv.id, rec); err != nil {
				d.revokeConversationChildKey(context.Background(), newKeyID)
				return nil, fmt.Errorf("unmounting lazy workspace before key rotation failed: %w", err)
			}
			d.revokeConversationChildKey(ctx, oldKeyID)
			if out, err := d.runGSWithEnv(ctx, "", newEnv, "--quiet", "mount", "--workspace", rec.ID, conv.workdir); err != nil {
				d.revokeConversationChildKey(context.Background(), newKeyID)
				return nil, fmt.Errorf("remounting lazy workspace after key rotation failed: %w: %s", err, out)
			}
		} else {
			d.revokeConversationChildKey(ctx, oldKeyID)
		}
	}

	conv.stateMu.Lock()
	conv.childKeyID = newKeyID
	conv.childSecret = newSecret
	conv.childExpiresAt = newExpiresAt
	conv.stateMu.Unlock()
	d.persistConversationMeta(conv)

	return newEnv, nil
}

func (d *agentDaemon) revokeConversationChildKey(ctx context.Context, keyID string) {
	keyID = strings.TrimSpace(keyID)
	if keyID == "" {
		return
	}
	if ctx == nil || ctx.Err() != nil {
		ctx = context.Background()
	}
	revokeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	authClient, closeFn, callCtx, err := d.authServiceClient(revokeCtx)
	if err != nil {
		return
	}
	defer closeFn()
	_, _ = authClient.RevokeApiKey(callCtx, &corev1.RevokeApiKeyRequest{KeyId: keyID})
}

func (d *agentDaemon) cleanupConversationRunHome(conversationID, runHome string) {
	if strings.TrimSpace(runHome) == "" && strings.TrimSpace(d.workingDir) != "" && strings.TrimSpace(conversationID) != "" {
		if _, _, err := agentConversationPaths(d.workingDir, conversationID); err == nil {
			runHome = filepath.Join(d.workingDir, "run-homes", conversationID)
		}
	}
	if strings.TrimSpace(runHome) != "" {
		_ = os.RemoveAll(runHome)
	}
}
