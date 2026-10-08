package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/storage"
)

// WebhookStore is the in-memory storage.WebhookStore.
type WebhookStore struct {
	b *backend
}

type memWebhookEvent struct {
	event     storage.WebhookEvent
	leaseTill time.Time
	fannedOut time.Time
}

type memWebhookDelivery struct {
	delivery  storage.WebhookDelivery
	leaseTill time.Time
}

func cloneWebhook(w storage.Webhook) storage.Webhook {
	w.Events = append([]string(nil), w.Events...)
	return w
}

func (s *WebhookStore) CreateWebhook(ctx context.Context, webhook storage.Webhook) (*storage.Webhook, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if webhook.ID == "" {
		id, err := objectid.RandomID("wh")
		if err != nil {
			return nil, err
		}
		webhook.ID = id
	}
	now := time.Now().UTC()
	webhook.CreatedAt, webhook.UpdatedAt = now, now
	s.b.webhooks[webhook.ID] = cloneWebhook(webhook)
	out := cloneWebhook(webhook)
	return &out, nil
}

func (s *WebhookStore) GetWebhook(ctx context.Context, id string) (*storage.Webhook, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	w, ok := s.b.webhooks[strings.TrimSpace(id)]
	if !ok {
		return nil, storage.ErrNotFound
	}
	out := cloneWebhook(w)
	return &out, nil
}

func (s *WebhookStore) list(match func(storage.Webhook) bool) []storage.Webhook {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	var out []storage.Webhook
	for _, w := range s.b.webhooks {
		if match(w) {
			out = append(out, cloneWebhook(w))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (s *WebhookStore) ListWebhooks(ctx context.Context, sliceID string) ([]storage.Webhook, error) {
	return s.list(func(w storage.Webhook) bool { return w.SliceID == sliceID }), nil
}

func (s *WebhookStore) ListActiveWebhooks(ctx context.Context) ([]storage.Webhook, error) {
	return s.list(func(w storage.Webhook) bool { return w.Active }), nil
}

func (s *WebhookStore) UpdateWebhook(ctx context.Context, webhook storage.Webhook) (*storage.Webhook, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	current, ok := s.b.webhooks[webhook.ID]
	if !ok {
		return nil, storage.ErrNotFound
	}
	current.URL, current.Events, current.Active, current.SealedSecret = webhook.URL, append([]string(nil), webhook.Events...), webhook.Active, webhook.SealedSecret
	current.UpdatedAt = time.Now().UTC()
	s.b.webhooks[webhook.ID] = current
	out := cloneWebhook(current)
	return &out, nil
}

func (s *WebhookStore) DeleteWebhook(ctx context.Context, id string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if _, ok := s.b.webhooks[id]; !ok {
		return storage.ErrNotFound
	}
	delete(s.b.webhooks, id)
	for did, d := range s.b.webhookDeliveries {
		if d.delivery.WebhookID == id {
			delete(s.b.webhookDeliveries, did)
		}
	}
	return nil
}

func (s *WebhookStore) AppendWebhookEvent(ctx context.Context, event storage.WebhookEvent) (*storage.WebhookEvent, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	return s.b.appendWebhookEventLocked(event)
}

// appendWebhookEventLocked records an event; the publisher calls it while it
// holds the lock, as the Postgres store does inside its transaction.
func (b *backend) appendWebhookEventLocked(event storage.WebhookEvent) (*storage.WebhookEvent, error) {
	if event.ID == "" {
		id, err := objectid.RandomID("evt")
		if err != nil {
			return nil, err
		}
		event.ID = id
	}
	event.CreatedAt = time.Now().UTC()
	event.ChangedPaths = append([]string(nil), event.ChangedPaths...)
	b.webhookEvents = append(b.webhookEvents, &memWebhookEvent{event: event})
	out := event
	return &out, nil
}

func (s *WebhookStore) ClaimWebhookEvents(ctx context.Context, limit int, lease time.Duration) ([]storage.WebhookEvent, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if limit <= 0 {
		limit = 50
	}
	now := time.Now()
	var out []storage.WebhookEvent
	for _, e := range s.b.webhookEvents {
		if len(out) >= limit {
			break
		}
		if !e.fannedOut.IsZero() || now.Before(e.leaseTill) {
			continue
		}
		e.leaseTill = now.Add(lease)
		event := e.event
		event.ChangedPaths = append([]string(nil), event.ChangedPaths...)
		out = append(out, event)
	}
	return out, nil
}

func (s *WebhookStore) FanOutWebhookEvent(ctx context.Context, eventID string, deliveries []storage.WebhookDelivery) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	for _, e := range s.b.webhookEvents {
		if e.event.ID != eventID {
			continue
		}
		if !e.fannedOut.IsZero() {
			return nil
		}
		e.fannedOut = time.Now()
		for _, d := range deliveries {
			if _, err := s.b.insertDeliveryLocked(d); err != nil {
				return err
			}
		}
		return nil
	}
	return storage.ErrNotFound
}

func (b *backend) insertDeliveryLocked(d storage.WebhookDelivery) (*storage.WebhookDelivery, error) {
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
	b.next++
	// Creation times are strictly increasing, so "newest first" is stable.
	d.CreatedAt = time.Now().UTC().Add(time.Duration(b.next))
	if d.NextAttemptAt.IsZero() {
		d.NextAttemptAt = time.Now()
	}
	d.RequestBody = append([]byte(nil), d.RequestBody...)
	b.webhookDeliveries[d.ID] = &memWebhookDelivery{delivery: d}
	out := d
	return &out, nil
}

func (s *WebhookStore) CreateWebhookDelivery(ctx context.Context, delivery storage.WebhookDelivery) (*storage.WebhookDelivery, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	return s.b.insertDeliveryLocked(delivery)
}

func (s *WebhookStore) ClaimDueWebhookDeliveries(ctx context.Context, limit int, lease time.Duration) ([]storage.WebhookDelivery, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if limit <= 0 {
		limit = 20
	}
	now := time.Now()
	var due []*memWebhookDelivery
	for _, d := range s.b.webhookDeliveries {
		if d.delivery.Status == storage.WebhookDeliveryPending && !d.delivery.NextAttemptAt.After(now) && !now.Before(d.leaseTill) {
			due = append(due, d)
		}
	}
	sort.Slice(due, func(i, j int) bool { return due[i].delivery.NextAttemptAt.Before(due[j].delivery.NextAttemptAt) })
	var out []storage.WebhookDelivery
	for _, d := range due {
		if len(out) >= limit {
			break
		}
		d.leaseTill = now.Add(lease)
		out = append(out, d.delivery)
	}
	return out, nil
}

func (s *WebhookStore) ClaimWebhookDelivery(ctx context.Context, id string, lease time.Duration) (*storage.WebhookDelivery, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	d, ok := s.b.webhookDeliveries[id]
	if !ok {
		return nil, storage.ErrNotFound
	}
	now := time.Now()
	if d.delivery.Status != storage.WebhookDeliveryPending || now.Before(d.leaseTill) {
		return nil, fmt.Errorf("%w: delivery %s is being sent or is finished", storage.ErrConflict, id)
	}
	d.leaseTill = now.Add(lease)
	out := d.delivery
	return &out, nil
}

func (s *WebhookStore) RecordWebhookAttempt(ctx context.Context, delivery storage.WebhookDelivery) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	d, ok := s.b.webhookDeliveries[delivery.ID]
	if !ok {
		return storage.ErrNotFound
	}
	created := d.delivery.CreatedAt
	d.delivery = delivery
	d.delivery.CreatedAt = created
	d.leaseTill = time.Time{}
	return nil
}

func (s *WebhookStore) GetWebhookDelivery(ctx context.Context, id string) (*storage.WebhookDelivery, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	d, ok := s.b.webhookDeliveries[strings.TrimSpace(id)]
	if !ok {
		return nil, storage.ErrNotFound
	}
	out := d.delivery
	return &out, nil
}

func (s *WebhookStore) ListWebhookDeliveries(ctx context.Context, webhookID string, limit int) ([]storage.WebhookDelivery, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	var out []storage.WebhookDelivery
	for _, d := range s.b.webhookDeliveries {
		if d.delivery.WebhookID == webhookID {
			out = append(out, d.delivery)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *WebhookStore) LastWebhookDeliveries(ctx context.Context, webhookIDs []string) (map[string]storage.WebhookDelivery, error) {
	out := map[string]storage.WebhookDelivery{}
	for _, id := range webhookIDs {
		list, _ := s.ListWebhookDeliveries(ctx, id, 1)
		if len(list) == 1 {
			out[id] = list[0]
		}
	}
	return out, nil
}

func (s *WebhookStore) PruneWebhookHistory(ctx context.Context, before time.Time) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	kept := s.b.webhookEvents[:0]
	for _, e := range s.b.webhookEvents {
		if e.fannedOut.IsZero() || e.fannedOut.After(before) {
			kept = append(kept, e)
		}
	}
	s.b.webhookEvents = kept
	for id, d := range s.b.webhookDeliveries {
		if d.delivery.Status != storage.WebhookDeliveryPending && d.delivery.CreatedAt.Before(before) {
			delete(s.b.webhookDeliveries, id)
		}
	}
	return nil
}

var _ storage.WebhookStore = (*WebhookStore)(nil)
