package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

const (
	conversationChildKeyTTLSeconds  int64 = 12 * 3600
	conversationChildKeyRenewWindow       = 10 * time.Minute
)

type agentConversation struct {
	id              string
	workdir         string
	workspaceSubdir string
	ready           chan struct{}
	readyOnce       sync.Once
	readyErr        error

	runMu   sync.Mutex
	keyMu   sync.Mutex
	stateMu sync.Mutex

	// outMu guards the outbound event buffer. Turn output is assigned a
	// per-conversation client_seq and buffered here until the server acks it, so
	// the events survive a Connect reconnect and are resent (the server dedups by
	// client_seq). All sends for a conversation go through flushPendingLocked
	// under outMu, which keeps them strictly ordered even across a reconnect.
	outMu          sync.Mutex
	nextClientSeq  int64
	pending        []*corev1.AgentEvent // emitted, not yet acked; ascending client_seq
	ackedClientSeq int64
	// flushedSeq is the highest client_seq already handed to flushQueue, so steady
	// state sends each event once. When the live send queue changes (reconnect),
	// flushPendingLocked resets flushedSeq to ackedClientSeq and resends the rest.
	flushedSeq int64
	flushQueue *agentSendQueue

	title             string
	sliceAccount      string
	sliceName         string
	sliceID           string
	role              string
	childKeyID        string
	childSecret       string
	childExpiresAt    time.Time
	runHome           string
	processedUserSeqs map[int64]struct{} // user-message seqs already run; guarded by stateMu
	cancel            context.CancelFunc
	codexThreadID     string // runtime session to resume across turns; guarded by stateMu
	session           agentSession
}

type conversationMeta struct {
	ConversationID string `json:"conversation_id"`
	Title          string `json:"title"`
	ThreadID       string `json:"thread_id"`
	SliceAccount   string `json:"slice_account"`
	SliceName      string `json:"slice_name"`
	SliceID        string `json:"slice_id"`
	Role           string `json:"role,omitempty"`
	ChildKeyID     string `json:"child_key_id,omitempty"`
}

func (d *agentDaemon) createConversation(conversationID string) (*agentConversation, error) {
	subdir, workdir, err := agentConversationPaths(d.workingDir, conversationID)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if existing := d.conversations[conversationID]; existing != nil {
		return existing, nil
	}
	conv := &agentConversation{
		id:              conversationID,
		workdir:         workdir,
		workspaceSubdir: subdir,
		ready:           make(chan struct{}),
	}
	d.conversations[conversationID] = conv
	return conv, nil
}

func (d *agentDaemon) getConversation(conversationID string) *agentConversation {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.conversations[conversationID]
}

func (d *agentDaemon) loadExistingConversations() {
	conversationsDir := filepath.Join(d.workingDir, "conversations")
	entries, err := os.ReadDir(conversationsDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		meta, err := readConversationMeta(filepath.Join(conversationsDir, entry.Name(), agentMetaFilename))
		if err != nil {
			continue
		}
		meta.ConversationID = strings.TrimSpace(meta.ConversationID)
		if meta.ConversationID == "" || meta.ConversationID != entry.Name() {
			continue
		}
		subdir, workdir, err := agentConversationPaths(d.workingDir, meta.ConversationID)
		if err != nil {
			continue
		}
		conv := &agentConversation{
			id:              meta.ConversationID,
			workdir:         workdir,
			workspaceSubdir: subdir,
			ready:           make(chan struct{}),
		}
		conv.setConversationInfo(meta.Title, meta.SliceAccount, meta.SliceName, meta.SliceID, meta.Role)
		conv.setThreadID(meta.ThreadID)
		rk, hasReattached := d.takeReattachedKey(conv.id)
		if hasReattached {
			if oldID := strings.TrimSpace(meta.ChildKeyID); oldID != "" && oldID != rk.keyID {
				d.revokeConversationChildKey(context.Background(), oldID)
			}
			conv.stateMu.Lock()
			conv.childKeyID = rk.keyID
			conv.runHome = rk.runHome
			if conv.role == rk.role {
				conv.childSecret = rk.secret
				conv.childExpiresAt = rk.expiresAt
			}
			conv.stateMu.Unlock()
			if conv.role == rk.role {
				d.persistConversationMeta(conv)
			}
		} else {
			conv.stateMu.Lock()
			conv.childKeyID = strings.TrimSpace(meta.ChildKeyID)
			conv.stateMu.Unlock()
		}
		conv.setReady(nil)

		d.mu.Lock()
		if d.conversations[conv.id] == nil {
			d.conversations[conv.id] = conv
		}
		d.mu.Unlock()
	}
}

func (d *agentDaemon) persistConversationMeta(conv *agentConversation) {
	if err := writeConversationMeta(conv); err != nil {
		fmt.Fprintf(d.runner.stderr(), "agent metadata persist failed for conversation %s: %v\n", conv.id, err)
	}
}

func writeConversationMeta(conv *agentConversation) error {
	meta := conv.meta()
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(conv.workdir, ".agent-meta-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	removeTmp := true
	defer func() {
		if removeTmp {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, filepath.Join(conv.workdir, agentMetaFilename)); err != nil {
		return err
	}
	removeTmp = false
	return nil
}

func readConversationMeta(path string) (conversationMeta, error) {
	var meta conversationMeta
	data, err := os.ReadFile(path)
	if err != nil {
		return meta, err
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return meta, err
	}
	return meta, nil
}

func (c *agentConversation) waitReady(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.ready:
		c.stateMu.Lock()
		defer c.stateMu.Unlock()
		return c.readyErr
	}
}

func (c *agentConversation) isReady() bool {
	select {
	case <-c.ready:
		c.stateMu.Lock()
		defer c.stateMu.Unlock()
		return c.readyErr == nil
	default:
		return false
	}
}

func (c *agentConversation) setReady(err error) {
	c.readyOnce.Do(func() {
		c.stateMu.Lock()
		c.readyErr = err
		c.stateMu.Unlock()
		close(c.ready)
	})
}

func (c *agentConversation) setConversationInfo(title, sliceAccount, sliceName, sliceID, role string) {
	role = strings.ToLower(strings.TrimSpace(role))
	if role == "" {
		role = "contributor"
	}
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if (c.role != "" && c.role != role) ||
		(c.sliceAccount != "" && (c.sliceAccount != sliceAccount || c.sliceName != sliceName)) {
		c.childSecret = ""
		c.childExpiresAt = time.Time{}
	}
	c.title = title
	c.sliceAccount = sliceAccount
	c.sliceName = sliceName
	c.sliceID = sliceID
	c.role = role
}

func (c *agentConversation) getTitle() string {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.title
}

func (c *agentConversation) getChildKeyID() string {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.childKeyID
}

func (c *agentConversation) getRunHome() string {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.runHome
}

func (c *agentConversation) meta() conversationMeta {
	threadID := c.getThreadID()
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return conversationMeta{
		ConversationID: c.id,
		Title:          c.title,
		ThreadID:       threadID,
		SliceAccount:   c.sliceAccount,
		SliceName:      c.sliceName,
		SliceID:        c.sliceID,
		Role:           c.role,
		ChildKeyID:     c.childKeyID,
	}
}

func (c *agentConversation) setCancel(cancel context.CancelFunc) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.cancel = cancel
}

func (c *agentConversation) clearCancel(cancel context.CancelFunc) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.cancel = nil
}

func (c *agentConversation) getThreadID() string {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.codexThreadID
}

func (c *agentConversation) setThreadID(id string) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.codexThreadID = id
}

// markUserSeq records that the user message with this persisted seq is being
// run and reports whether the caller should proceed. A repeat seq is a
// redelivered duplicate (the server resends unanswered user messages after
// ConversationStarted) and returns false. Dedup is a seen-set rather than a
// high-water mark so that two in-flight messages whose goroutines acquire
// runMu out of seq order both still run — a lower seq must never be silently
// dropped. seq 0 (legacy server) always runs.
func (c *agentConversation) markUserSeq(seq int64) bool {
	if seq <= 0 {
		return true
	}
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if _, ok := c.processedUserSeqs[seq]; ok {
		return false
	}
	if c.processedUserSeqs == nil {
		c.processedUserSeqs = map[int64]struct{}{}
	}
	c.processedUserSeqs[seq] = struct{}{}
	return true
}

func (c *agentConversation) ensureSession(ctx context.Context, runtime agentRuntime, freshThreadHistory func(context.Context) (string, error)) (agentSession, error) {
	c.stateMu.Lock()
	if c.session != nil && c.session.Alive() {
		session := c.session
		c.stateMu.Unlock()
		return session, nil
	}
	// A session that died between turns (e.g. the app-server crashed) is stale;
	// drop and tear it down so this turn transparently opens a fresh one and
	// resumes via the persisted thread id.
	stale := c.session
	c.session = nil
	resume := c.codexThreadID
	c.stateMu.Unlock()
	if stale != nil {
		stale.Close()
	}

	// freshThreadHistory seeds a *cold* thread (no resume id, or resume failed)
	// with the persisted transcript so the agent keeps its conversational context
	// even after losing its workspace/.agent-meta (fresh-dir restart, new machine).
	// The runtime invokes it only on the thread/start path, so a successful resume
	// pays no fetch cost.
	session, err := runtime.OpenSession(ctx, c.workdir, resume, agentWorkspaceInstructions(conversationIncludedPaths(c.workdir)), freshThreadHistory)
	if err != nil {
		return nil, err
	}
	c.stateMu.Lock()
	c.session = session
	c.stateMu.Unlock()
	return session, nil
}

func (c *agentConversation) closeSession() {
	c.stateMu.Lock()
	session := c.session
	c.session = nil
	c.stateMu.Unlock()
	if session != nil {
		session.Close()
	}
}

func (c *agentConversation) cancelRun() {
	c.stateMu.Lock()
	cancel := c.cancel
	c.stateMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

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
