package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/storage/memory"
	"gitslice.io/gitslice/internal/webhooks"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// newWebhookHandlers wires handlers the way the server does: stores wrapped
// to emit events, and a dispatcher that may reach the test's local receiver.
func newWebhookHandlers(t *testing.T) (*memory.Stores, *Handlers) {
	t.Helper()
	mem := memory.New()
	mem.AddAccount("user_alice", "acme")
	dispatcher := webhooks.NewDispatcher(webhooks.Stores{
		Webhooks:   mem.Webhooks,
		Slices:     mem.Slices,
		Changesets: mem.Changesets,
		Checks:     mem.Checks,
		Auth:       mem.Auth,
	}, webhooks.Options{WebBaseURL: "https://gitslice.test", AllowPrivateTargets: true})
	handlers := New(Stores{
		Auth:       mem.Auth,
		Blobs:      mem.Blobs,
		Changesets: webhooks.WrapChangesets(mem.Changesets, dispatcher),
		Repository: mem.Repository,
		Slices:     webhooks.WrapSlices(mem.Slices, dispatcher),
		Agents:     mem.Agents,
		Checks:     webhooks.WrapChecks(mem.Checks, dispatcher),
		Webhooks:   mem.Webhooks,
	}, mem.Objects, nil)
	handlers.Webhook.Dispatcher = dispatcher
	return mem, handlers
}

type hookReceiver struct {
	mu     sync.Mutex
	events []webhooks.Payload
	server *httptest.Server
}

func newHookReceiver(t *testing.T) *hookReceiver {
	r := &hookReceiver{}
	r.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var p webhooks.Payload
		_ = json.Unmarshal(body, &p)
		r.mu.Lock()
		r.events = append(r.events, p)
		r.mu.Unlock()
	}))
	t.Cleanup(r.server.Close)
	return r
}

func (r *hookReceiver) received(event string) []webhooks.Payload {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []webhooks.Payload
	for _, p := range r.events {
		if p.Event == event {
			out = append(out, p)
		}
	}
	return out
}

func TestWebhookLifecycle(t *testing.T) {
	_, handlers := newWebhookHandlers(t)
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	rcv := newHookReceiver(t)
	home := &corev1.SliceRef{Account: "acme", Slice: "home"}

	created, err := handlers.Webhook.CreateWebhook(ctx, &corev1.CreateWebhookRequest{
		Slice:  home,
		Url:    rcv.server.URL + "/hook",
		Events: []string{"changeset.created, changeset.submitted", "changeset.created"},
		Secret: "s3cret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !created.Active || !created.HasSecret || len(created.Events) != 2 || created.Slice.GetSlice() != "home" {
		t.Fatalf("created = %+v", created)
	}

	// Events from the changeset service reach it.
	ref, err := handlers.Repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := handlers.Changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: home,
		TargetRef:      storage.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "add note",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := rcv.received("changeset.created")
	if len(got) != 1 || got[0].Changeset == nil || got[0].Changeset.ID != cs.Id || got[0].Changeset.Title != "add note" ||
		got[0].Changeset.URL != "https://gitslice.test/cs/"+storage.ShortChangesetID(cs.Id) {
		t.Fatalf("changeset.created deliveries = %+v", got)
	}

	uploaded, err := handlers.Blob.UploadBlob(ctx, &corev1.UploadBlobRequest{Data: []byte("hello\n"), Slice: home})
	if err != nil {
		t.Fatal(err)
	}
	patchset, err := handlers.Changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		FileEdits:    []*corev1.FileEdit{{Op: "upsert", Path: "/acme/notes.txt", BlobId: uploaded.BlobId, ContentHash: uploaded.ContentHash, Mode: 0o100644}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handlers.Changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{ChangesetId: cs.Id, ExpectedCurrentPatchsetId: patchset.Id}); err != nil {
		t.Fatal(err)
	}
	if published, err := handlers.Changeset.Changesets.PublishPending(ctx, 10); err != nil || published != 1 {
		t.Fatalf("PublishPending = %d, %v", published, err)
	}
	submitted := rcv.received("changeset.submitted")
	if len(submitted) != 1 || submitted[0].Commit == nil || submitted[0].Commit.ID == "" || len(submitted[0].Commit.ChangedPaths) != 1 ||
		submitted[0].Changeset.Status != "submitted" {
		t.Fatalf("changeset.submitted deliveries = %+v", submitted)
	}
	if n := len(rcv.received("changeset.updated")); n != 0 {
		t.Fatalf("got %d changeset.updated deliveries without subscribing", n)
	}

	listed, err := handlers.Webhook.ListWebhooks(ctx, &corev1.ListWebhooksRequest{Slice: home})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Webhooks) != 1 || listed.Webhooks[0].LastDelivery == nil || listed.Webhooks[0].LastDelivery.Status != storage.WebhookDeliverySucceeded {
		t.Fatalf("listed = %+v", listed.Webhooks)
	}
	deliveries, err := handlers.Webhook.ListWebhookDeliveries(ctx, &corev1.ListWebhookDeliveriesRequest{WebhookId: created.Id})
	if err != nil {
		t.Fatal(err)
	}
	if len(deliveries.Deliveries) != 2 || deliveries.Deliveries[0].RequestBody == "" {
		t.Fatalf("deliveries = %+v", deliveries.Deliveries)
	}

	ping, err := handlers.Webhook.PingWebhook(ctx, &corev1.PingWebhookRequest{WebhookId: created.Id})
	if err != nil {
		t.Fatal(err)
	}
	if ping.Status != storage.WebhookDeliverySucceeded || ping.Event != "ping" || ping.ResponseStatus != http.StatusOK {
		t.Fatalf("ping = %+v", ping)
	}
	again, err := handlers.Webhook.RedeliverWebhookDelivery(ctx, &corev1.RedeliverWebhookDeliveryRequest{DeliveryId: ping.Id})
	if err != nil {
		t.Fatal(err)
	}
	if again.Id == ping.Id || again.EventId != ping.EventId || again.Status != storage.WebhookDeliverySucceeded {
		t.Fatalf("redelivery = %+v", again)
	}

	inactive := false
	updated, err := handlers.Webhook.UpdateWebhook(ctx, &corev1.UpdateWebhookRequest{WebhookId: created.Id, Active: &inactive, ClearSecret: true, UpdateEvents: true, Events: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Active || updated.HasSecret || len(updated.Events) != 1 || updated.Events[0] != "*" || updated.Url != created.Url {
		t.Fatalf("updated = %+v", updated)
	}
	if _, err := handlers.Webhook.DeleteWebhook(ctx, &corev1.DeleteWebhookRequest{WebhookId: created.Id}); err != nil {
		t.Fatal(err)
	}
	listed, err = handlers.Webhook.ListWebhooks(ctx, &corev1.ListWebhooksRequest{Slice: home})
	if err != nil || len(listed.Webhooks) != 0 {
		t.Fatalf("after delete: %+v, %v", listed, err)
	}
}

func TestWebhooksNeedSliceAdmins(t *testing.T) {
	mem, handlers := newWebhookHandlers(t)
	mem.AddAccountRole("user_carol", "acme", "member")
	alice := authctx.WithSubjectID(context.Background(), "user_alice")
	carol := authctx.WithSubjectID(context.Background(), "user_carol")
	mallory := authctx.WithSubjectID(context.Background(), "user_mallory")
	home := &corev1.SliceRef{Account: "acme", Slice: "home"}

	created, err := handlers.Webhook.CreateWebhook(alice, &corev1.CreateWebhookRequest{Slice: home, Url: "https://hooks.example.com/x", Events: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}
	for name, ctx := range map[string]context.Context{"a member": carol, "a stranger": mallory} {
		_, err := handlers.Webhook.CreateWebhook(ctx, &corev1.CreateWebhookRequest{Slice: home, Url: "https://hooks.example.com/y", Events: []string{"*"}})
		if code := status.Code(err); code != codes.PermissionDenied && code != codes.NotFound {
			t.Fatalf("%s creating: %v", name, err)
		}
		if _, err := handlers.Webhook.ListWebhooks(ctx, &corev1.ListWebhooksRequest{Slice: home}); err == nil {
			t.Fatalf("%s listed webhooks", name)
		}
		if _, err := handlers.Webhook.ListWebhookDeliveries(ctx, &corev1.ListWebhookDeliveriesRequest{WebhookId: created.Id}); err == nil {
			t.Fatalf("%s listed deliveries", name)
		}
		if _, err := handlers.Webhook.DeleteWebhook(ctx, &corev1.DeleteWebhookRequest{WebhookId: created.Id}); err == nil {
			t.Fatalf("%s deleted the webhook", name)
		}
	}
	if _, err := handlers.Webhook.ListWebhooks(context.Background(), &corev1.ListWebhooksRequest{Slice: home}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("anonymous list: %v", err)
	}
}

func TestWebhookValidation(t *testing.T) {
	_, handlers := newWebhookHandlers(t)
	handlers.Webhook.Dispatcher = webhooks.NewDispatcher(webhooks.Stores{}, webhooks.Options{})
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	home := &corev1.SliceRef{Account: "acme", Slice: "home"}
	for _, tc := range []struct {
		name   string
		url    string
		events []string
		secret string
	}{
		{"plain http", "http://hooks.example.com/x", []string{"*"}, ""},
		{"loopback", "https://127.0.0.1/x", []string{"*"}, ""},
		{"metadata server", "https://169.254.169.254/x", []string{"*"}, ""},
		{"no events", "https://hooks.example.com/x", nil, ""},
		{"unknown event", "https://hooks.example.com/x", []string{"push", "deploy"}, ""},
		{"long secret", "https://hooks.example.com/x", []string{"push"}, string(make([]byte, 300))},
	} {
		_, err := handlers.Webhook.CreateWebhook(ctx, &corev1.CreateWebhookRequest{Slice: home, Url: tc.url, Events: tc.events, Secret: tc.secret})
		wantStatus(t, err, codes.InvalidArgument, tc.name)
	}
	for i := range maxWebhooksPerSlice {
		if _, err := handlers.Webhook.CreateWebhook(ctx, &corev1.CreateWebhookRequest{Slice: home, Url: fmt.Sprintf("https://hooks.example.com/%d", i), Events: []string{"push"}}); err != nil {
			t.Fatal(err)
		}
	}
	_, err := handlers.Webhook.CreateWebhook(ctx, &corev1.CreateWebhookRequest{Slice: home, Url: "https://hooks.example.com/one-more", Events: []string{"push"}})
	wantStatus(t, err, codes.FailedPrecondition, "webhook limit")
}
