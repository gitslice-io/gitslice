package postgres

import (
	"errors"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/storage"
)

func TestWebhookStoreLifecycle(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	hooks := store.Webhooks()

	created, err := hooks.CreateWebhook(ctx, storage.Webhook{
		SliceID: "slice_acme_payment", URL: "https://hooks.example.com/a", Events: []string{"push", "tag.created"},
		Active: true, SealedSecret: "enc:v1:xyz", CreatedBy: "user_alice",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.CreatedAt.IsZero() || len(created.Events) != 2 {
		t.Fatalf("created = %+v", created)
	}
	inactive, err := hooks.CreateWebhook(ctx, storage.Webhook{SliceID: "slice_acme_payment", URL: "https://hooks.example.com/b", Events: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := hooks.ListWebhooks(ctx, "slice_acme_payment")
	if err != nil || len(listed) != 2 {
		t.Fatalf("ListWebhooks = %+v, %v", listed, err)
	}
	active, err := hooks.ListActiveWebhooks(ctx)
	if err != nil || len(active) != 1 || active[0].ID != created.ID {
		t.Fatalf("ListActiveWebhooks = %+v, %v", active, err)
	}
	created.URL = "https://hooks.example.com/a2"
	created.Events = []string{"*"}
	created.SealedSecret = ""
	updated, err := hooks.UpdateWebhook(ctx, *created)
	if err != nil {
		t.Fatal(err)
	}
	got, err := hooks.GetWebhook(ctx, created.ID)
	if err != nil || got.URL != "https://hooks.example.com/a2" || got.SealedSecret != "" || len(got.Events) != 1 || !updated.UpdatedAt.After(created.CreatedAt.Add(-time.Second)) {
		t.Fatalf("GetWebhook after update = %+v, %v", got, err)
	}

	// Events are claimed once, then fanned out once.
	event, err := hooks.AppendWebhookEvent(ctx, storage.WebhookEvent{
		Kind: storage.WebhookEventTagCreated, SliceID: "slice_acme_payment", TagName: "v1", CommitID: "sha256:1",
		ChangedPaths: []string{"/acme/payment/a.go"}, Data: `{"message":"m"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := hooks.ClaimWebhookEvents(ctx, 10, time.Minute)
	if err != nil || len(claimed) != 1 || claimed[0].ID != event.ID || claimed[0].TagName != "v1" || len(claimed[0].ChangedPaths) != 1 || claimed[0].Data != `{"message":"m"}` {
		t.Fatalf("ClaimWebhookEvents = %+v, %v", claimed, err)
	}
	if again, err := hooks.ClaimWebhookEvents(ctx, 10, time.Minute); err != nil || len(again) != 0 {
		t.Fatalf("second claim = %+v, %v (want none while leased)", again, err)
	}
	delivery := storage.WebhookDelivery{WebhookID: created.ID, EventID: event.ID, Event: event.Kind, RequestBody: []byte(`{"id":1}`)}
	if err := hooks.FanOutWebhookEvent(ctx, event.ID, []storage.WebhookDelivery{delivery}); err != nil {
		t.Fatal(err)
	}
	// A second fan-out (after a lease ran out) adds nothing.
	if err := hooks.FanOutWebhookEvent(ctx, event.ID, []storage.WebhookDelivery{delivery}); err != nil {
		t.Fatal(err)
	}
	deliveries, err := hooks.ListWebhookDeliveries(ctx, created.ID, 10)
	if err != nil || len(deliveries) != 1 {
		t.Fatalf("deliveries = %+v, %v", deliveries, err)
	}
	d := deliveries[0]
	if d.Status != storage.WebhookDeliveryPending || string(d.RequestBody) != `{"id":1}` || !d.DeliveredAt.IsZero() && d.DeliveredAt.Unix() > 0 {
		t.Fatalf("delivery = %+v", d)
	}

	due, err := hooks.ClaimDueWebhookDeliveries(ctx, 10, time.Minute)
	if err != nil || len(due) != 1 || due[0].ID != d.ID {
		t.Fatalf("ClaimDueWebhookDeliveries = %+v, %v", due, err)
	}
	if _, err := hooks.ClaimWebhookDelivery(ctx, d.ID, time.Minute); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("claiming a leased delivery: %v, want ErrConflict", err)
	}
	d.Attempts = 1
	d.Status = storage.WebhookDeliveryPending
	d.ResponseStatus = 500
	d.Error = "the endpoint answered 500"
	d.ResponseBody = "oops"
	d.NextAttemptAt = time.Now().Add(time.Hour)
	if err := hooks.RecordWebhookAttempt(ctx, d); err != nil {
		t.Fatal(err)
	}
	if due, err := hooks.ClaimDueWebhookDeliveries(ctx, 10, time.Minute); err != nil || len(due) != 0 {
		t.Fatalf("claimed a delivery not due yet: %+v, %v", due, err)
	}
	// The lease ended with the attempt, so it can be claimed directly.
	if _, err := hooks.ClaimWebhookDelivery(ctx, d.ID, time.Minute); err != nil {
		t.Fatal(err)
	}
	d.Attempts = 2
	d.Status = storage.WebhookDeliverySucceeded
	d.ResponseStatus = 200
	d.Error = ""
	d.DeliveredAt = time.Now()
	if err := hooks.RecordWebhookAttempt(ctx, d); err != nil {
		t.Fatal(err)
	}
	if _, err := hooks.ClaimWebhookDelivery(ctx, d.ID, time.Minute); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("claiming a finished delivery: %v, want ErrConflict", err)
	}
	stored, err := hooks.GetWebhookDelivery(ctx, d.ID)
	if err != nil || stored.Status != storage.WebhookDeliverySucceeded || stored.Attempts != 2 || stored.ResponseStatus != 200 || stored.DeliveredAt.IsZero() {
		t.Fatalf("stored delivery = %+v, %v", stored, err)
	}

	ping, err := hooks.CreateWebhookDelivery(ctx, storage.WebhookDelivery{WebhookID: created.ID, EventID: "evt_ping", Event: "ping", RequestBody: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	last, err := hooks.LastWebhookDeliveries(ctx, []string{created.ID, inactive.ID})
	if err != nil || len(last) != 1 || last[created.ID].ID != ping.ID {
		t.Fatalf("LastWebhookDeliveries = %+v, %v", last, err)
	}

	// Pruning drops finished history, not pending deliveries.
	if err := hooks.PruneWebhookHistory(ctx, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	deliveries, err = hooks.ListWebhookDeliveries(ctx, created.ID, 10)
	if err != nil || len(deliveries) != 1 || deliveries[0].ID != ping.ID {
		t.Fatalf("after prune = %+v, %v", deliveries, err)
	}
	var events int
	if err := store.db.QueryRowContext(ctx, `select count(*) from webhook_events`).Scan(&events); err != nil || events != 0 {
		t.Fatalf("events after prune = %d, %v", events, err)
	}

	if err := hooks.DeleteWebhook(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := hooks.GetWebhook(ctx, created.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("GetWebhook after delete: %v", err)
	}
	if _, err := hooks.GetWebhookDelivery(ctx, ping.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("delivery after its webhook was deleted: %v", err)
	}
	if err := hooks.DeleteWebhook(ctx, created.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("deleting twice: %v", err)
	}
}
