package webhooks

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/storage/memory"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestValidateURL(t *testing.T) {
	for _, tc := range []struct {
		url          string
		allowPrivate bool
		ok           bool
	}{
		{"https://example.com/hook", false, true},
		{"https://example.com:8443/hook?token=x", false, true},
		{"http://example.com/hook", false, false},
		{"http://example.com/hook", true, true},
		{"ftp://example.com/hook", true, false},
		{"https://user:pass@example.com/hook", false, false},
		{"https://127.0.0.1/hook", false, false},
		{"https://127.0.0.1/hook", true, true},
		{"https://10.1.2.3/hook", false, false},
		{"https://169.254.169.254/computeMetadata", false, false},
		{"https://[::1]/hook", false, false},
		{"https://[::ffff:10.0.0.1]/hook", false, false},
		{"https://localhost/hook", false, false},
		{"https://metadata.google.internal/hook", false, false},
		{"https://100.64.0.1/hook", false, false},
		{"", false, false},
		{"not a url", false, false},
	} {
		_, err := ValidateURL(tc.url, tc.allowPrivate)
		if (err == nil) != tc.ok {
			t.Errorf("ValidateURL(%q, %v) error = %v, want ok=%v", tc.url, tc.allowPrivate, err, tc.ok)
		}
	}
}

func TestPublicAddress(t *testing.T) {
	for addr, want := range map[string]bool{
		"8.8.8.8":          true,
		"2606:4700::1111":  true,
		"127.0.0.1":        false,
		"0.0.0.0":          false,
		"10.0.0.1":         false,
		"172.16.0.1":       false,
		"192.168.1.1":      false,
		"169.254.169.254":  false,
		"100.100.100.100":  false,
		"198.18.0.1":       false,
		"224.0.0.1":        false,
		"::1":              false,
		"fe80::1":          false,
		"fc00::1":          false,
		"::ffff:127.0.0.1": false,
		"64:ff9b::a00:1":   false,
	} {
		if got := publicAddress(netip.MustParseAddr(addr)); got != want {
			t.Errorf("publicAddress(%s) = %v, want %v", addr, got, want)
		}
	}
}

func TestDialerRefusesPrivateAddresses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	_, err := newClient(false).Get(server.URL)
	if !isBlocked(err) {
		t.Fatalf("GET %s error = %v, want the address refused", server.URL, err)
	}
	if _, err := newClient(true).Get(server.URL); err != nil {
		t.Fatalf("GET with private targets allowed: %v", err)
	}
}

func TestSignAndVerify(t *testing.T) {
	body := []byte(`{"event":"ping"}`)
	// echo -n '{"event":"ping"}' | openssl dgst -sha256 -hmac secret
	want := "sha256=4f4bb3a54e99c4a20e243485229f9b08c66e09104ba6f79c23ce647242a4ce84"
	got := Sign("secret", body)
	if got != want {
		t.Fatalf("Sign = %q, want %q", got, want)
	}
	if !Verify("secret", body, got) {
		t.Fatal("Verify rejected its own signature")
	}
	if Verify("other", body, got) || Verify("secret", []byte(`{}`), got) {
		t.Fatal("Verify accepted a wrong secret or body")
	}
}

type received struct {
	header http.Header
	body   []byte
}

type receiver struct {
	mu       sync.Mutex
	got      []received
	statuses []int
	server   *httptest.Server
}

func newReceiver(t *testing.T, statuses ...int) *receiver {
	r := &receiver{statuses: statuses}
	r.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.got = append(r.got, received{header: req.Header.Clone(), body: body})
		code := http.StatusNoContent
		if len(r.statuses) > 0 {
			code = r.statuses[0]
			r.statuses = r.statuses[1:]
		}
		r.mu.Unlock()
		w.WriteHeader(code)
		_, _ = w.Write([]byte("thanks"))
	}))
	t.Cleanup(r.server.Close)
	return r
}

func (r *receiver) requests() []received {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]received(nil), r.got...)
}

type fixture struct {
	mem        *memory.Stores
	dispatcher *Dispatcher
	payment    *corev1.Slice
	web        *corev1.Slice
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	mem := memory.New()
	mem.AddAccount("user_alice", "acme")
	payment := mem.PutSlice(&corev1.SliceRef{Account: "acme", Slice: "payment"}, []string{"/acme/payment"}, "private")
	web := mem.PutSlice(&corev1.SliceRef{Account: "acme", Slice: "web"}, []string{"/acme/web"}, "private")
	dispatcher := NewDispatcher(Stores{
		Webhooks:   mem.Webhooks,
		Slices:     mem.Slices,
		Changesets: mem.Changesets,
		Checks:     mem.Checks,
		Auth:       mem.Auth,
	}, Options{WebBaseURL: "https://gitslice.test/", AllowPrivateTargets: true})
	return &fixture{mem: mem, dispatcher: dispatcher, payment: payment, web: web}
}

func (f *fixture) hook(t *testing.T, slice *corev1.Slice, url, secret string, events ...string) *storage.Webhook {
	t.Helper()
	hook, err := f.mem.Webhooks.CreateWebhook(context.Background(), storage.Webhook{SliceID: slice.Id, URL: url, Events: events, Active: true, SealedSecret: secret, CreatedBy: "user_alice"})
	if err != nil {
		t.Fatal(err)
	}
	f.dispatcher.WebhooksChanged()
	return hook
}

func TestTagCreatedIsDeliveredSigned(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	rcv := newReceiver(t)
	hook := f.hook(t, f.payment, rcv.server.URL+"/release", "s3cret", storage.WebhookEventTagCreated)
	f.hook(t, f.web, rcv.server.URL+"/other", "", "*")

	slices := WrapSlices(f.mem.Slices, f.dispatcher)
	if _, _, err := slices.CreateTag(ctx, storage.SliceTag{SliceID: f.payment.Id, Name: "v1.2.3", CommitID: "sha256:abc", Message: "Release", CreatedBy: "user_alice"}); err != nil {
		t.Fatal(err)
	}
	got := rcv.requests()
	if len(got) != 1 {
		t.Fatalf("deliveries = %d, want 1 (only the payment slice's webhook)", len(got))
	}
	req := got[0]
	if req.header.Get("X-Gitslice-Event") != "tag.created" || req.header.Get("X-Gitslice-Hook-ID") != hook.ID || req.header.Get("User-Agent") != UserAgent {
		t.Fatalf("headers = %v", req.header)
	}
	if !Verify("s3cret", req.body, req.header.Get(SignatureHeader)) {
		t.Fatalf("signature %q does not verify", req.header.Get(SignatureHeader))
	}
	var p Payload
	if err := json.Unmarshal(req.body, &p); err != nil {
		t.Fatal(err)
	}
	if p.Event != "tag.created" || p.Tag == nil || p.Tag.Name != "v1.2.3" || p.Tag.CommitID != "sha256:abc" || p.Tag.Message != "Release" {
		t.Fatalf("payload = %s", req.body)
	}
	if p.Slice == nil || p.Slice.FullName != "acme/payment" || p.Slice.URL != "https://gitslice.test/slices/acme/payment" {
		t.Fatalf("payload slice = %+v", p.Slice)
	}
	if p.ID == "" || p.ID != req.header.Get("X-Gitslice-Event-ID") {
		t.Fatalf("payload id = %q, header %q", p.ID, req.header.Get("X-Gitslice-Event-ID"))
	}

	// Creating the same tag again is not a new event.
	if _, _, err := slices.CreateTag(ctx, storage.SliceTag{SliceID: f.payment.Id, Name: "v1.2.3", CommitID: "sha256:abc", CreatedBy: "user_alice"}); err != nil {
		t.Fatal(err)
	}
	if err := f.dispatcher.DrainOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(rcv.requests()); n != 1 {
		t.Fatalf("deliveries after re-creating the tag = %d, want 1", n)
	}
	deliveries, err := f.mem.Webhooks.ListWebhookDeliveries(ctx, hook.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(deliveries) != 1 || deliveries[0].Status != storage.WebhookDeliverySucceeded || deliveries[0].ResponseStatus != http.StatusNoContent || deliveries[0].Attempts != 1 {
		t.Fatalf("deliveries = %+v", deliveries)
	}
}

func TestFailedDeliveryIsRetriedLater(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	rcv := newReceiver(t, http.StatusInternalServerError, http.StatusOK)
	hook := f.hook(t, f.payment, rcv.server.URL, "", "*")

	before := time.Now()
	f.dispatcher.Emit(ctx, storage.WebhookEvent{Kind: storage.WebhookEventTagCreated, SliceID: f.payment.Id, TagName: "v1", CommitID: "sha256:1"})
	deliveries, err := f.mem.Webhooks.ListWebhookDeliveries(ctx, hook.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(deliveries) != 1 {
		t.Fatalf("deliveries = %+v", deliveries)
	}
	d := deliveries[0]
	if d.Status != storage.WebhookDeliveryPending || d.Attempts != 1 || d.ResponseStatus != 500 || d.Error == "" || d.ResponseBody != "thanks" {
		t.Fatalf("after a 500: %+v", d)
	}
	if wait := d.NextAttemptAt.Sub(before); wait < 50*time.Second || wait > 2*time.Minute {
		t.Fatalf("next attempt in %s, want about a minute", wait)
	}
	// Not due yet: nothing is sent.
	if err := f.dispatcher.DrainOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(rcv.requests()); n != 1 {
		t.Fatalf("requests = %d before the retry is due", n)
	}

	// Redelivering sends the same body with the same event id.
	again, err := f.dispatcher.Redeliver(ctx, &d)
	if err != nil {
		t.Fatal(err)
	}
	if again.Status != storage.WebhookDeliverySucceeded || again.EventID != d.EventID || again.ID == d.ID {
		t.Fatalf("redelivery = %+v", again)
	}
	got := rcv.requests()
	if len(got) != 2 || string(got[0].body) != string(got[1].body) {
		t.Fatalf("redelivered body differs: %d requests", len(got))
	}
}

func TestDeliveryGivesUpAfterMaxAttempts(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	rcv := newReceiver(t, 500, 500, 500, 500, 500, 500, 500, 500)
	hook := f.hook(t, f.payment, rcv.server.URL, "", "*")
	f.dispatcher.Emit(ctx, storage.WebhookEvent{Kind: storage.WebhookEventTagCreated, SliceID: f.payment.Id, TagName: "v1"})
	for i := 1; i < MaxAttempts; i++ {
		deliveries, _ := f.mem.Webhooks.ListWebhookDeliveries(ctx, hook.ID, 1)
		d := deliveries[0]
		if d.Status != storage.WebhookDeliveryPending {
			t.Fatalf("attempt %d: status %s", d.Attempts, d.Status)
		}
		// Make it due and attempt again.
		if _, err := f.mem.Webhooks.ClaimWebhookDelivery(ctx, d.ID, time.Minute); err != nil {
			t.Fatal(err)
		}
		f.dispatcher.attempt(ctx, d)
	}
	deliveries, _ := f.mem.Webhooks.ListWebhookDeliveries(ctx, hook.ID, 1)
	if d := deliveries[0]; d.Status != storage.WebhookDeliveryFailed || d.Attempts != MaxAttempts {
		t.Fatalf("after %d attempts: %+v", MaxAttempts, d)
	}
}

func TestPushGoesToSlicesThatIncludeTheChangedPaths(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	rcv := newReceiver(t)
	paymentPush := f.hook(t, f.payment, rcv.server.URL+"/payment-push", "", storage.WebhookEventPush)
	webPush := f.hook(t, f.web, rcv.server.URL+"/web-push", "", storage.WebhookEventPush)
	webSubmitted := f.hook(t, f.web, rcv.server.URL+"/web-submitted", "", storage.WebhookEventChangesetSubmitted)

	f.dispatcher.Emit(ctx, storage.WebhookEvent{
		Kind:         storage.WebhookEventChangesetSubmitted,
		SliceID:      f.web.Id,
		CommitID:     "sha256:landed",
		TargetRef:    "refs/heads/main",
		ChangedPaths: []string{"/acme/payment/pay.go", "/acme/web/index.html", "/acme/docs/readme.md"},
	})
	byPath := map[string]Payload{}
	for _, req := range rcv.requests() {
		var p Payload
		if err := json.Unmarshal(req.body, &p); err != nil {
			t.Fatal(err)
		}
		byPath[req.header.Get("X-Gitslice-Hook-ID")] = p
	}
	if len(byPath) != 3 {
		t.Fatalf("deliveries = %d, want push to both slices and submitted to web", len(byPath))
	}
	pay := byPath[paymentPush.ID]
	if pay.Event != "push" || pay.Commit == nil || len(pay.Commit.ChangedPaths) != 1 || pay.Commit.ChangedPaths[0] != "/acme/payment/pay.go" || pay.Slice.FullName != "acme/payment" {
		t.Fatalf("payment push = %+v %+v", pay, pay.Commit)
	}
	if web := byPath[webPush.ID]; web.Event != "push" || len(web.Commit.ChangedPaths) != 1 || web.Commit.ChangedPaths[0] != "/acme/web/index.html" {
		t.Fatalf("web push = %+v", web.Commit)
	}
	if sub := byPath[webSubmitted.ID]; sub.Event != "changeset.submitted" || len(sub.Commit.ChangedPaths) != 3 {
		t.Fatalf("web submitted = %+v", sub)
	}
	if pay.ID == byPath[webSubmitted.ID].ID {
		t.Fatal("push and submitted share an event id")
	}
}

func TestInactiveWebhookGetsNothingButPing(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	rcv := newReceiver(t)
	hook := f.hook(t, f.payment, rcv.server.URL, "", "*")
	hook.Active = false
	if _, err := f.mem.Webhooks.UpdateWebhook(ctx, *hook); err != nil {
		t.Fatal(err)
	}
	f.dispatcher.WebhooksChanged()
	f.dispatcher.Emit(ctx, storage.WebhookEvent{Kind: storage.WebhookEventTagCreated, SliceID: f.payment.Id, TagName: "v1"})
	if err := f.dispatcher.DrainOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(rcv.requests()); n != 0 {
		t.Fatalf("an inactive webhook got %d deliveries", n)
	}
	ping, err := f.dispatcher.Ping(ctx, hook, "user_alice")
	if err != nil {
		t.Fatal(err)
	}
	if ping.Status != storage.WebhookDeliverySucceeded || ping.Event != "ping" {
		t.Fatalf("ping = %+v", ping)
	}
	var p Payload
	if err := json.Unmarshal(rcv.requests()[0].body, &p); err != nil {
		t.Fatal(err)
	}
	if p.Hook == nil || p.Hook.ID != hook.ID || p.Zen == "" {
		t.Fatalf("ping payload = %+v", p)
	}
}

func TestBlockedTargetFailsWithoutRetry(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.dispatcher = NewDispatcher(f.dispatcher.stores, Options{})
	rcv := newReceiver(t)
	// Saved before the rules applied, or a name that now resolves inside.
	hook := f.hook(t, f.payment, rcv.server.URL, "", "*")
	ping, err := f.dispatcher.Ping(ctx, hook, "")
	if err != nil {
		t.Fatal(err)
	}
	if ping.Status != storage.WebhookDeliveryFailed || ping.Error == "" || len(rcv.requests()) != 0 {
		t.Fatalf("ping to a private address = %+v", ping)
	}
}
