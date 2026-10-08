package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/storage"
)

// WebhookStore keeps webhooks, their pending events and their deliveries
// (design/24_webhooks.md).
type WebhookStore struct {
	db *sql.DB
}

const webhookColumns = `id, slice_id, url, events, active, secret, created_by, created_at, updated_at`

func scanWebhook(row rowScanner) (*storage.Webhook, error) {
	var w storage.Webhook
	var events []byte
	if err := row.Scan(&w.ID, &w.SliceID, &w.URL, &events, &w.Active, &w.SealedSecret, &w.CreatedBy, &w.CreatedAt, &w.UpdatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(events, &w.Events); err != nil {
		return nil, err
	}
	return &w, nil
}

func (s *WebhookStore) CreateWebhook(ctx context.Context, webhook storage.Webhook) (*storage.Webhook, error) {
	if webhook.ID == "" {
		id, err := objectid.RandomID("wh")
		if err != nil {
			return nil, err
		}
		webhook.ID = id
	}
	events, err := json.Marshal(nonNil(webhook.Events))
	if err != nil {
		return nil, err
	}
	row := s.db.QueryRowContext(ctx, `
		insert into webhooks(id, slice_id, url, events, active, secret, created_by, created_at, updated_at)
		values ($1, $2, $3, $4, $5, $6, $7, now(), now())
		returning `+webhookColumns,
		webhook.ID, webhook.SliceID, webhook.URL, events, webhook.Active, webhook.SealedSecret, webhook.CreatedBy)
	return scanWebhook(row)
}

func (s *WebhookStore) GetWebhook(ctx context.Context, id string) (*storage.Webhook, error) {
	w, err := scanWebhook(s.db.QueryRowContext(ctx, `select `+webhookColumns+` from webhooks where id = $1`, strings.TrimSpace(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return w, err
}

func (s *WebhookStore) listWebhooks(ctx context.Context, query string, args ...any) ([]storage.Webhook, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []storage.Webhook
	for rows.Next() {
		w, err := scanWebhook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *w)
	}
	return out, rows.Err()
}

func (s *WebhookStore) ListWebhooks(ctx context.Context, sliceID string) ([]storage.Webhook, error) {
	return s.listWebhooks(ctx, `select `+webhookColumns+` from webhooks where slice_id = $1 order by created_at, id`, sliceID)
}

func (s *WebhookStore) ListActiveWebhooks(ctx context.Context) ([]storage.Webhook, error) {
	return s.listWebhooks(ctx, `select `+webhookColumns+` from webhooks where active order by created_at, id`)
}

func (s *WebhookStore) UpdateWebhook(ctx context.Context, webhook storage.Webhook) (*storage.Webhook, error) {
	events, err := json.Marshal(nonNil(webhook.Events))
	if err != nil {
		return nil, err
	}
	w, err := scanWebhook(s.db.QueryRowContext(ctx, `
		update webhooks set url = $2, events = $3, active = $4, secret = $5, updated_at = now()
		where id = $1
		returning `+webhookColumns,
		webhook.ID, webhook.URL, events, webhook.Active, webhook.SealedSecret))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return w, err
}

func (s *WebhookStore) DeleteWebhook(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `delete from webhooks where id = $1`, strings.TrimSpace(id))
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *WebhookStore) AppendWebhookEvent(ctx context.Context, event storage.WebhookEvent) (*storage.WebhookEvent, error) {
	return appendWebhookEvent(ctx, s.db, event)
}

// appendWebhookEvent inserts an event with db or inside a transaction (the
// publisher records changeset.submitted in the transaction that publishes).
// clock_timestamp keeps a transaction's events in order, where now() would
// give them all the same time.
func appendWebhookEvent(ctx context.Context, db queryRower, event storage.WebhookEvent) (*storage.WebhookEvent, error) {
	if event.ID == "" {
		id, err := objectid.RandomID("evt")
		if err != nil {
			return nil, err
		}
		event.ID = id
	}
	paths, err := json.Marshal(nonNil(event.ChangedPaths))
	if err != nil {
		return nil, err
	}
	err = db.QueryRowContext(ctx, `
		insert into webhook_events(id, kind, slice_id, actor_subject_id, changeset_id, patchset_id, commit_id,
			target_ref, tag_name, check_run_id, changed_paths, data, created_at)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, clock_timestamp())
		returning created_at
	`, event.ID, event.Kind, event.SliceID, event.ActorSubjectID, event.ChangesetID, event.PatchsetID, event.CommitID,
		event.TargetRef, event.TagName, event.CheckRunID, paths, event.Data).Scan(&event.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &event, nil
}

func (s *WebhookStore) ClaimWebhookEvents(ctx context.Context, limit int, lease time.Duration) ([]storage.WebhookEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		update webhook_events set lease_until = now() + ($2::bigint * interval '1 millisecond')
		where id in (
			select id from webhook_events
			where fanned_out_at is null and (lease_until is null or lease_until < now())
			order by created_at, id
			limit $1
			for update skip locked
		)
		returning id, kind, slice_id, actor_subject_id, changeset_id, patchset_id, commit_id, target_ref,
			tag_name, check_run_id, changed_paths, data, created_at
	`, limit, lease.Milliseconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []storage.WebhookEvent
	for rows.Next() {
		var e storage.WebhookEvent
		var paths []byte
		if err := rows.Scan(&e.ID, &e.Kind, &e.SliceID, &e.ActorSubjectID, &e.ChangesetID, &e.PatchsetID, &e.CommitID,
			&e.TargetRef, &e.TagName, &e.CheckRunID, &paths, &e.Data, &e.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(paths, &e.ChangedPaths); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sortEventsOldestFirst(out)
	return out, nil
}

func (s *WebhookStore) FanOutWebhookEvent(ctx context.Context, eventID string, deliveries []storage.WebhookDelivery) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	res, err := tx.ExecContext(ctx, `update webhook_events set fanned_out_at = now(), lease_until = null where id = $1 and fanned_out_at is null`, eventID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		// Another dispatcher fanned it out after this one's lease ran out.
		return tx.Rollback()
	}
	for _, d := range deliveries {
		if _, err = insertWebhookDelivery(ctx, tx, d); err != nil {
			return err
		}
	}
	return tx.Commit()
}

const deliveryColumns = `id, webhook_id, event_id, event, status, attempts, response_status, error, request_body,
	response_body, duration_ms, created_at, coalesce(delivered_at, 'epoch'::timestamptz), next_attempt_at`

func scanDelivery(row rowScanner) (*storage.WebhookDelivery, error) {
	var d storage.WebhookDelivery
	if err := row.Scan(&d.ID, &d.WebhookID, &d.EventID, &d.Event, &d.Status, &d.Attempts, &d.ResponseStatus, &d.Error,
		&d.RequestBody, &d.ResponseBody, &d.DurationMs, &d.CreatedAt, &d.DeliveredAt, &d.NextAttemptAt); err != nil {
		return nil, err
	}
	if d.DeliveredAt.Unix() == 0 {
		d.DeliveredAt = time.Time{}
	}
	return &d, nil
}

type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func insertWebhookDelivery(ctx context.Context, db queryRower, d storage.WebhookDelivery) (*storage.WebhookDelivery, error) {
	if d.ID == "" {
		id, err := objectid.RandomID("whd")
		if err != nil {
			return nil, err
		}
		d.ID = id
	}
	if d.Status == "" {
		d.Status = storage.WebhookDeliveryPending
	}
	next := d.NextAttemptAt
	if next.IsZero() {
		next = time.Now()
	}
	return scanDelivery(db.QueryRowContext(ctx, `
		insert into webhook_deliveries(id, webhook_id, event_id, event, status, request_body, next_attempt_at, created_at)
		values ($1, $2, $3, $4, $5, $6, $7, now())
		returning `+deliveryColumns,
		d.ID, d.WebhookID, d.EventID, d.Event, d.Status, d.RequestBody, next))
}

func (s *WebhookStore) CreateWebhookDelivery(ctx context.Context, delivery storage.WebhookDelivery) (*storage.WebhookDelivery, error) {
	return insertWebhookDelivery(ctx, s.db, delivery)
}

func (s *WebhookStore) ClaimDueWebhookDeliveries(ctx context.Context, limit int, lease time.Duration) ([]storage.WebhookDelivery, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `
		update webhook_deliveries set lease_until = now() + ($2::bigint * interval '1 millisecond')
		where id in (
			select id from webhook_deliveries
			where status = 'pending' and next_attempt_at <= now() and (lease_until is null or lease_until < now())
			order by next_attempt_at, id
			limit $1
			for update skip locked
		)
		returning `+deliveryColumns, limit, lease.Milliseconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []storage.WebhookDelivery
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

func (s *WebhookStore) ClaimWebhookDelivery(ctx context.Context, id string, lease time.Duration) (*storage.WebhookDelivery, error) {
	d, err := scanDelivery(s.db.QueryRowContext(ctx, `
		update webhook_deliveries set lease_until = now() + ($2::bigint * interval '1 millisecond')
		where id = $1 and status = 'pending' and (lease_until is null or lease_until < now())
		returning `+deliveryColumns, id, lease.Milliseconds()))
	if errors.Is(err, sql.ErrNoRows) {
		if _, getErr := s.GetWebhookDelivery(ctx, id); getErr != nil {
			return nil, getErr
		}
		return nil, fmt.Errorf("%w: delivery %s is being sent or is finished", ErrConflict, id)
	}
	return d, err
}

func (s *WebhookStore) RecordWebhookAttempt(ctx context.Context, d storage.WebhookDelivery) error {
	var delivered any
	if !d.DeliveredAt.IsZero() {
		delivered = d.DeliveredAt
	}
	_, err := s.db.ExecContext(ctx, `
		update webhook_deliveries
		set status = $2, attempts = $3, response_status = $4, error = $5, response_body = $6, duration_ms = $7,
			delivered_at = $8, next_attempt_at = $9, lease_until = null
		where id = $1
	`, d.ID, d.Status, d.Attempts, d.ResponseStatus, d.Error, d.ResponseBody, d.DurationMs, delivered, d.NextAttemptAt)
	return err
}

func (s *WebhookStore) GetWebhookDelivery(ctx context.Context, id string) (*storage.WebhookDelivery, error) {
	d, err := scanDelivery(s.db.QueryRowContext(ctx, `select `+deliveryColumns+` from webhook_deliveries where id = $1`, strings.TrimSpace(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return d, err
}

func (s *WebhookStore) ListWebhookDeliveries(ctx context.Context, webhookID string, limit int) ([]storage.WebhookDelivery, error) {
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	rows, err := s.db.QueryContext(ctx, `
		select `+deliveryColumns+` from webhook_deliveries where webhook_id = $1 order by created_at desc, id desc limit $2
	`, webhookID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []storage.WebhookDelivery
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

func (s *WebhookStore) LastWebhookDeliveries(ctx context.Context, webhookIDs []string) (map[string]storage.WebhookDelivery, error) {
	out := map[string]storage.WebhookDelivery{}
	if len(webhookIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		select distinct on (webhook_id) `+deliveryColumns+`
		from webhook_deliveries where webhook_id = any($1)
		order by webhook_id, created_at desc, id desc
	`, webhookIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		out[d.WebhookID] = *d
	}
	return out, rows.Err()
}

func (s *WebhookStore) PruneWebhookHistory(ctx context.Context, before time.Time) error {
	if _, err := s.db.ExecContext(ctx, `delete from webhook_events where fanned_out_at is not null and fanned_out_at < $1`, before); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `delete from webhook_deliveries where status <> 'pending' and created_at < $1`, before)
	return err
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func sortEventsOldestFirst(events []storage.WebhookEvent) {
	for i := 1; i < len(events); i++ {
		for j := i; j > 0 && (events[j].CreatedAt.Before(events[j-1].CreatedAt) ||
			(events[j].CreatedAt.Equal(events[j-1].CreatedAt) && events[j].ID < events[j-1].ID)); j-- {
			events[j], events[j-1] = events[j-1], events[j]
		}
	}
}

var _ storage.WebhookStore = (*WebhookStore)(nil)
