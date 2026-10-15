package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

type agentConversation struct {
	id              string
	workdir         string
	workspaceSubdir string
	ready           chan struct{}
	readyOnce       sync.Once
	readyErr        error

	runMu   sync.Mutex
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
		conv.setConversationInfo(meta.Title, meta.SliceAccount, meta.SliceName, meta.SliceID)
		conv.setThreadID(meta.ThreadID)
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

func (c *agentConversation) setConversationInfo(title, sliceAccount, sliceName, sliceID string) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.title = title
	c.sliceAccount = sliceAccount
	c.sliceName = sliceName
	c.sliceID = sliceID
}

func (c *agentConversation) getTitle() string {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.title
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
