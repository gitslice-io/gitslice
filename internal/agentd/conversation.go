package agentd

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

type Conversation struct {
	id              string
	workdir         string
	workspaceSubdir string
	ready           chan struct{}
	readyOnce       sync.Once
	readyErr        error

	runMu   sync.Mutex
	keyMu   sync.Mutex
	stateMu sync.Mutex

	outMu          sync.Mutex
	nextClientSeq  int64
	pending        []*corev1.AgentEvent
	ackedClientSeq int64
	flushedSeq     int64
	flushQueue     *SendQueue

	title             string
	sliceAccount      string
	sliceName         string
	sliceID           string
	role              string
	childKeyID        string
	childSecret       string
	childExpiresAt    time.Time
	runHome           string
	processedUserSeqs map[int64]struct{}
	cancel            context.CancelFunc
	codexThreadID     string
	session           Session
}

type ConversationMeta struct {
	ConversationID string `json:"conversation_id"`
	Title          string `json:"title"`
	ThreadID       string `json:"thread_id"`
	SliceAccount   string `json:"slice_account"`
	SliceName      string `json:"slice_name"`
	SliceID        string `json:"slice_id"`
	Role           string `json:"role,omitempty"`
	ChildKeyID     string `json:"child_key_id,omitempty"`
}

func (d *Daemon) createConversation(conversationID string) (*Conversation, error) {
	subdir, workdir, err := ConversationPaths(d.WorkingDir, conversationID)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if existing := d.conversations[conversationID]; existing != nil {
		return existing, nil
	}
	if d.conversations == nil {
		d.conversations = map[string]*Conversation{}
	}
	conv := &Conversation{
		id:              conversationID,
		workdir:         workdir,
		workspaceSubdir: subdir,
		ready:           make(chan struct{}),
	}
	d.conversations[conversationID] = conv
	return conv, nil
}

func (d *Daemon) getConversation(conversationID string) *Conversation {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.conversations[conversationID]
}

func (d *Daemon) LoadExistingConversations() {
	conversationsDir := filepath.Join(d.WorkingDir, "conversations")
	entries, err := os.ReadDir(conversationsDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		meta, err := readConversationMeta(filepath.Join(conversationsDir, entry.Name(), MetaFilename))
		if err != nil {
			continue
		}
		meta.ConversationID = strings.TrimSpace(meta.ConversationID)
		if meta.ConversationID == "" || meta.ConversationID != entry.Name() {
			continue
		}
		subdir, workdir, err := ConversationPaths(d.WorkingDir, meta.ConversationID)
		if err != nil {
			continue
		}
		conv := &Conversation{
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
		if d.conversations == nil {
			d.conversations = map[string]*Conversation{}
		}
		if d.conversations[conv.id] == nil {
			d.conversations[conv.id] = conv
		}
		d.mu.Unlock()
	}
}

func (d *Daemon) persistConversationMeta(conv *Conversation) {
	if err := writeConversationMeta(conv); err != nil {
		fmt.Fprintf(d.Host.stderr(), "agent metadata persist failed for conversation %s: %v\n", conv.id, err)
	}
}

func writeConversationMeta(conv *Conversation) error {
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
	if err := os.Rename(tmpPath, filepath.Join(conv.workdir, MetaFilename)); err != nil {
		return err
	}
	removeTmp = false
	return nil
}

func readConversationMeta(path string) (ConversationMeta, error) {
	var meta ConversationMeta
	data, err := os.ReadFile(path)
	if err != nil {
		return meta, err
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return meta, err
	}
	return meta, nil
}

func (c *Conversation) waitReady(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.ready:
		c.stateMu.Lock()
		defer c.stateMu.Unlock()
		return c.readyErr
	}
}

func (c *Conversation) isReady() bool {
	select {
	case <-c.ready:
		c.stateMu.Lock()
		defer c.stateMu.Unlock()
		return c.readyErr == nil
	default:
		return false
	}
}

func (c *Conversation) setReady(err error) {
	c.readyOnce.Do(func() {
		c.stateMu.Lock()
		c.readyErr = err
		c.stateMu.Unlock()
		close(c.ready)
	})
}

func (c *Conversation) setConversationInfo(title, sliceAccount, sliceName, sliceID, role string) {
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

func (c *Conversation) getTitle() string {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.title
}

func (c *Conversation) getChildKeyID() string {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.childKeyID
}

func (c *Conversation) getRunHome() string {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.runHome
}

func (c *Conversation) meta() ConversationMeta {
	threadID := c.getThreadID()
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return ConversationMeta{
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

func (c *Conversation) setCancel(cancel context.CancelFunc) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.cancel = cancel
}

func (c *Conversation) clearCancel(cancel context.CancelFunc) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.cancel = nil
}

func (c *Conversation) getThreadID() string {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.codexThreadID
}

func (c *Conversation) setThreadID(id string) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.codexThreadID = id
}

func (c *Conversation) markUserSeq(seq int64) bool {
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

func (c *Conversation) ensureSession(ctx context.Context, runtime Runtime, freshThreadHistory func(context.Context) (string, error)) (Session, error) {
	c.stateMu.Lock()
	if c.session != nil && c.session.Alive() {
		session := c.session
		c.stateMu.Unlock()
		return session, nil
	}
	stale := c.session
	c.session = nil
	resume := c.codexThreadID
	c.stateMu.Unlock()
	if stale != nil {
		stale.Close()
	}

	session, err := runtime.OpenSession(ctx, c.workdir, resume, WorkspaceInstructions(ConversationIncludedPaths(c.workdir)), freshThreadHistory)
	if err != nil {
		return nil, err
	}
	c.stateMu.Lock()
	c.session = session
	c.stateMu.Unlock()
	return session, nil
}

func (c *Conversation) closeSession() {
	c.stateMu.Lock()
	session := c.session
	c.session = nil
	c.stateMu.Unlock()
	if session != nil {
		session.Close()
	}
}

func (c *Conversation) cancelRun() {
	c.stateMu.Lock()
	cancel := c.cancel
	c.stateMu.Unlock()
	if cancel != nil {
		cancel()
	}
}
