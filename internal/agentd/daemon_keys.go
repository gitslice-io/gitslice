package agentd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func (d *Daemon) authServiceClient(ctx context.Context) (AuthClient, func(), context.Context, error) {
	if d.Auth != nil {
		return d.Auth, func() {}, ctx, nil
	}
	if strings.TrimSpace(d.Host.ServerAddr) == "" || strings.TrimSpace(d.Host.Token) == "" {
		return nil, nil, nil, fmt.Errorf("agent daemon has no auth credentials to manage scoped keys")
	}
	conn, err := d.Host.dial(ctx, d.Host.ServerAddr)
	if err != nil {
		return nil, nil, nil, err
	}
	return corev1.NewAuthServiceClient(conn), func() { _ = conn.Close() }, d.Host.authContext(ctx), nil
}

func (d *Daemon) ensureConversationRunHome(conv *Conversation) (string, error) {
	if strings.TrimSpace(d.WorkingDir) == "" {
		return "", fmt.Errorf("agent daemon working directory is not set")
	}
	if _, _, err := ConversationPaths(d.WorkingDir, conv.id); err != nil {
		return "", err
	}
	runHomesRoot := filepath.Join(d.WorkingDir, "run-homes")
	if err := os.MkdirAll(runHomesRoot, 0o700); err != nil {
		return "", err
	}
	_ = os.Chmod(runHomesRoot, 0o700)
	runHome := filepath.Join(runHomesRoot, conv.id)
	if err := os.MkdirAll(runHome, 0o700); err != nil {
		return "", err
	}
	_ = os.Chmod(runHome, 0o700)
	_ = os.Remove(filepath.Join(runHome, ".gitslice", "config.json"))
	conv.stateMu.Lock()
	conv.runHome = runHome
	conv.stateMu.Unlock()
	return runHome, nil
}

func (d *Daemon) conversationEnvMap(secret, runHome string) map[string]string {
	env := map[string]string{
		"HOME":          runHome,
		"XDG_DATA_HOME": filepath.Join(runHome, ".local", "share"),
		"GS_TOKEN":      secret,
	}
	serverAddr := strings.TrimSpace(d.Host.ServerAddr)
	if serverAddr == "" {
		serverAddr = d.Host.envServerAddr()
	}
	if serverAddr != "" {
		env["GS_SERVER_ADDR"] = serverAddr
		env["GITSLICE_SERVER_ADDR"] = serverAddr
	}
	if strings.TrimSpace(os.Getenv("CODEX_HOME")) == "" {
		origHome := strings.TrimSpace(d.Host.Home)
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

func (d *Daemon) mintConversationChildKey(ctx context.Context, conversationID, sliceAccount, sliceName, role string) (keyID, secret string, expiresAt time.Time, err error) {
	authClient, closeFn, callCtx, err := d.authServiceClient(ctx)
	if err != nil {
		return "", "", time.Time{}, err
	}
	defer closeFn()

	keyName := "conv-" + conversationID
	if len(keyName) > 64 {
		keyName = keyName[:64]
	}
	sliceRef := CanonicalSliceKey(sliceAccount, sliceName)
	resp, err := authClient.CreateApiKey(callCtx, &corev1.CreateApiKeyRequest{
		Name:             keyName,
		Slices:           []string{sliceRef},
		MaxRole:          role,
		ExpiresInSeconds: ChildKeyTTLSeconds,
		Purpose:          "conversation " + conversationID,
		DaemonId:         d.CurrentDaemonID(),
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

func (d *Daemon) ensureConversationChildKey(ctx context.Context, conv *Conversation) (map[string]string, error) {
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
	if secret != "" && (expiresAt.IsZero() || time.Until(expiresAt) > ChildKeyRenewWindow) {
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
		conv.closeSession()
		if rec, _, _, lazy := d.lazyWorkspaceForConversation(conv.id, conv.workdir); lazy && d.workspaceMounted(conv.workdir) {
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

func (d *Daemon) revokeConversationChildKey(ctx context.Context, keyID string) {
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

func (d *Daemon) cleanupConversationRunHome(conversationID, runHome string) {
	if strings.TrimSpace(runHome) == "" && strings.TrimSpace(d.WorkingDir) != "" && strings.TrimSpace(conversationID) != "" {
		if _, _, err := ConversationPaths(d.WorkingDir, conversationID); err == nil {
			runHome = filepath.Join(d.WorkingDir, "run-homes", conversationID)
		}
	}
	if strings.TrimSpace(runHome) != "" {
		_ = os.RemoveAll(runHome)
	}
}
