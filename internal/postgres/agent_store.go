package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/gitslice-io/gitslice/internal/objectid"
	"github.com/gitslice-io/gitslice/internal/storage"
	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
)

// AgentStore is the PostgreSQL implementation of storage.AgentStore for the
// bring-your-own-agent feature. See design/16_bring_your_own_agent.md.
type AgentStore struct {
	db *sql.DB
}

func (s *AgentStore) RegisterDaemon(ctx context.Context, in storage.AgentDaemonInput) (*corev1.AgentDaemon, error) {
	// Reuse the daemon row for the same account+name so a restart keeps its id.
	var id string
	err := s.db.QueryRowContext(ctx, `
		select id from agent_daemons where account = $1 and name = $2
	`, in.Account, in.Name).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		id, err = objectid.RandomID("agent")
		if err != nil {
			return nil, err
		}
		if _, err := s.db.ExecContext(ctx, `
			insert into agent_daemons(id, subject_id, account, name, runtime, version, status, last_seen_at)
			values ($1, $2, $3, $4, $5, $6, 'online', now())
		`, id, in.SubjectID, in.Account, in.Name, in.Runtime, in.Version); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	default:
		if _, err := s.db.ExecContext(ctx, `
			update agent_daemons set runtime = $2, version = $3, status = 'online', last_seen_at = now()
			where id = $1
		`, id, in.Runtime, in.Version); err != nil {
			return nil, err
		}
	}
	return s.GetDaemon(ctx, id)
}

func (s *AgentStore) SetDaemonStatus(ctx context.Context, daemonID, status string) error {
	res, err := s.db.ExecContext(ctx, `
		update agent_daemons set status = $2, last_seen_at = now() where id = $1
	`, daemonID, status)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return storage.ErrNotFound
	}
	return nil
}

func (s *AgentStore) GetDaemon(ctx context.Context, daemonID string) (*corev1.AgentDaemon, error) {
	row := s.db.QueryRowContext(ctx, `
		select id, account, name, runtime, version, status, last_seen_at, created_at
		from agent_daemons where id = $1
	`, daemonID)
	return scanDaemon(row)
}

func (s *AgentStore) ListDaemons(ctx context.Context, subjectID string) ([]*corev1.AgentDaemon, error) {
	rows, err := s.db.QueryContext(ctx, `
		select id, account, name, runtime, version, status, last_seen_at, created_at
		from agent_daemons where subject_id = $1 order by created_at asc
	`, subjectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*corev1.AgentDaemon
	for rows.Next() {
		d, err := scanDaemon(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *AgentStore) CreateConversation(ctx context.Context, in storage.ConversationInput) (*corev1.Conversation, error) {
	id, err := objectid.RandomID("conv")
	if err != nil {
		return nil, err
	}
	subdir := "conversations/" + id
	if _, err := s.db.ExecContext(ctx, `
		insert into agent_conversations(id, daemon_id, subject_id, slice_id, account, slice_name, title, status, workspace_subdir, next_seq)
		values ($1, $2, $3, $4, $5, $6, $7, 'active', $8, 1)
	`, id, in.DaemonID, in.SubjectID, in.SliceID, in.Account, in.SliceName, in.Title, subdir); err != nil {
		return nil, err
	}
	return s.GetConversation(ctx, id)
}

func (s *AgentStore) GetConversation(ctx context.Context, conversationID string) (*corev1.Conversation, error) {
	row := s.db.QueryRowContext(ctx, `
		select id, daemon_id, slice_id, account, slice_name, title, status, workspace_subdir, created_at, updated_at
		from agent_conversations where id = $1
	`, conversationID)
	return scanConversation(row)
}

func (s *AgentStore) ListConversations(ctx context.Context, filter storage.ConversationFilter) ([]*corev1.Conversation, error) {
	rows, err := s.db.QueryContext(ctx, `
		select id, daemon_id, slice_id, account, slice_name, title, status, workspace_subdir, created_at, updated_at
		from agent_conversations
		where ($1 = '' or slice_id = $1) and ($2 = '' or daemon_id = $2) and ($3 = '' or subject_id = $3)
		order by created_at asc
	`, filter.SliceID, filter.DaemonID, filter.SubjectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*corev1.Conversation
	for rows.Next() {
		c, err := scanConversation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *AgentStore) SetConversationStatus(ctx context.Context, conversationID, status string) error {
	res, err := s.db.ExecContext(ctx, `
		update agent_conversations set status = $2, updated_at = now() where id = $1
	`, conversationID, status)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return storage.ErrNotFound
	}
	return nil
}

func (s *AgentStore) AppendEvent(ctx context.Context, conversationID, role, eventType, text, dataJSON, itemID string) (*corev1.ConversationEvent, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var seq int64
	err = tx.QueryRowContext(ctx, `
		update agent_conversations set next_seq = next_seq + 1, updated_at = now()
		where id = $1 returning next_seq - 1
	`, conversationID).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, storage.ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	id, err := objectid.RandomID("ev")
	if err != nil {
		return nil, err
	}
	var createdAt time.Time
	err = tx.QueryRowContext(ctx, `
		insert into agent_conversation_events(id, conversation_id, seq, role, type, text, data_json, item_id)
		values ($1, $2, $3, $4, $5, $6, $7, $8) returning created_at
	`, id, conversationID, seq, role, eventType, text, dataJSON, itemID).Scan(&createdAt)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &corev1.ConversationEvent{
		Id:             id,
		ConversationId: conversationID,
		Seq:            seq,
		Role:           role,
		Type:           eventType,
		Text:           text,
		DataJson:       dataJSON,
		CreatedAt:      createdAt.UTC().Format(time.RFC3339Nano),
		ItemId:         itemID,
	}, nil
}

func (s *AgentStore) ListEvents(ctx context.Context, conversationID string, afterSeq int64) ([]*corev1.ConversationEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
		select id, conversation_id, seq, role, type, text, data_json, created_at, item_id
		from agent_conversation_events
		where conversation_id = $1 and seq > $2 order by seq asc
	`, conversationID, afterSeq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*corev1.ConversationEvent
	for rows.Next() {
		var (
			ev        corev1.ConversationEvent
			createdAt time.Time
		)
		if err := rows.Scan(&ev.Id, &ev.ConversationId, &ev.Seq, &ev.Role, &ev.Type, &ev.Text, &ev.DataJson, &createdAt, &ev.ItemId); err != nil {
			return nil, err
		}
		ev.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
		out = append(out, &ev)
	}
	return out, rows.Err()
}

func (s *AgentStore) ListEventsRange(ctx context.Context, conversationID string, afterSeq, beforeSeq int64) ([]*corev1.ConversationEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
		select id, conversation_id, seq, role, type, text, data_json, created_at, item_id
		from agent_conversation_events
		where conversation_id = $1 and seq > $2 and ($3 <= 0 or seq <= $3)
		order by seq asc
	`, conversationID, afterSeq, beforeSeq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*corev1.ConversationEvent
	for rows.Next() {
		var (
			ev        corev1.ConversationEvent
			createdAt time.Time
		)
		if err := rows.Scan(&ev.Id, &ev.ConversationId, &ev.Seq, &ev.Role, &ev.Type, &ev.Text, &ev.DataJson, &createdAt, &ev.ItemId); err != nil {
			return nil, err
		}
		ev.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
		out = append(out, &ev)
	}
	return out, rows.Err()
}

func (s *AgentStore) LatestEventSeq(ctx context.Context, conversationID string) (int64, error) {
	var seq sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `
		select max(seq) from agent_conversation_events where conversation_id = $1
	`, conversationID).Scan(&seq); err != nil {
		return 0, err
	}
	if !seq.Valid {
		return 0, nil
	}
	return seq.Int64, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanDaemon(row rowScanner) (*corev1.AgentDaemon, error) {
	var (
		d                     corev1.AgentDaemon
		lastSeenAt, createdAt time.Time
	)
	if err := row.Scan(&d.Id, &d.Account, &d.Name, &d.Runtime, &d.Version, &d.Status, &lastSeenAt, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, storage.ErrNotFound
		}
		return nil, err
	}
	d.LastSeenAt = lastSeenAt.UTC().Format(time.RFC3339Nano)
	d.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
	return &d, nil
}

func scanConversation(row rowScanner) (*corev1.Conversation, error) {
	var (
		c                    corev1.Conversation
		account, sliceName   string
		createdAt, updatedAt time.Time
	)
	if err := row.Scan(&c.Id, &c.DaemonId, &c.SliceId, &account, &sliceName, &c.Title, &c.Status, &c.WorkspaceSubdir, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, storage.ErrNotFound
		}
		return nil, err
	}
	c.Slice = &corev1.SliceRef{Account: account, Slice: sliceName}
	c.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
	c.UpdatedAt = updatedAt.UTC().Format(time.RFC3339Nano)
	return &c, nil
}
