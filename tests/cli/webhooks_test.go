package cli_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/webhooks"
)

type webhookRequest struct {
	header  http.Header
	body    []byte
	payload webhooks.Payload
}

type webhookReceiver struct {
	mu   sync.Mutex
	got  []webhookRequest
	code int
}

func (r *webhookReceiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	var p webhooks.Payload
	_ = json.Unmarshal(body, &p)
	r.mu.Lock()
	r.got = append(r.got, webhookRequest{header: req.Header.Clone(), body: body, payload: p})
	code := r.code
	r.mu.Unlock()
	if code == 0 {
		code = http.StatusOK
	}
	w.WriteHeader(code)
	_, _ = w.Write([]byte("ok"))
}

// waitFor returns the first request for event, waiting a little for
// deliveries that happen after the command returns.
func (r *webhookReceiver) waitFor(t *testing.T, event string) webhookRequest {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		for _, req := range r.got {
			if req.payload.Event == event {
				r.mu.Unlock()
				return req
			}
		}
		r.mu.Unlock()
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no %s delivery arrived", event)
	return webhookRequest{}
}

// TestWebhooksDeliverSliceEvents creates webhooks with the CLI and checks
// that tagging and landing changes reach them, signed, and that the delivery
// log, ping and redeliver work.
func TestWebhooksDeliverSliceEvents(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	payment := t.TempDir()
	loginTestCLI(t, ts, home, payment)
	runCLI(t, home, payment, "workspace", "init", "acme/payment")

	rcv := &webhookReceiver{}
	endpoint := httptest.NewServer(rcv)
	t.Cleanup(endpoint.Close)

	out, _ := runCLIStreamsWithInput(t, home, payment, "s3cret\n", "webhook", "create", "--url", endpoint.URL+"/release",
		"--event", "tag.created", "--event", "push,changeset.submitted", "--secret-stdin", "--json")
	var created struct {
		ID        string   `json:"id"`
		Slice     string   `json:"slice"`
		Events    []string `json:"events"`
		HasSecret bool     `json:"has_secret"`
		Active    bool     `json:"active"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatalf("webhook create --json: %v\n%s", err, out)
	}
	if created.ID == "" || created.Slice != "acme/payment" || len(created.Events) != 3 || !created.HasSecret || !created.Active {
		t.Fatalf("created = %+v", created)
	}
	if strings.Contains(runCLI(t, home, payment, "webhook", "list", "--json"), "s3cret") {
		t.Fatal("webhook list shows the secret")
	}

	// Ping answers at once.
	if ping := runCLI(t, home, payment, "webhook", "ping", created.ID); !strings.Contains(ping, "succeeded") || !strings.Contains(ping, "HTTP 200") {
		t.Fatalf("ping output:\n%s", ping)
	}
	if req := rcv.waitFor(t, "ping"); req.payload.Hook == nil || req.payload.Hook.ID != created.ID {
		t.Fatalf("ping payload = %+v", req.payload)
	}

	// Landing a change sends changeset.submitted and push.
	submitWorkspaceFile(t, home, payment, "release.go", "package payment\nconst Release = 1\n", "release prep")
	submitted := rcv.waitFor(t, "changeset.submitted")
	if submitted.payload.Changeset == nil || submitted.payload.Changeset.Title != "release prep" || submitted.payload.Commit == nil ||
		!strings.HasPrefix(submitted.payload.Commit.ID, "sha256:") {
		t.Fatalf("changeset.submitted payload:\n%s", submitted.body)
	}
	push := rcv.waitFor(t, "push")
	if push.payload.Commit == nil || push.payload.Commit.ID != submitted.payload.Commit.ID || len(push.payload.Commit.ChangedPaths) != 1 ||
		!strings.HasSuffix(push.payload.Commit.ChangedPaths[0], "/release.go") {
		t.Fatalf("push payload:\n%s", push.body)
	}

	// Tagging a release sends tag.created, signed with the secret.
	runCLI(t, home, payment, "tag", "create", "v1.0.0", "-m", "first release")
	tag := rcv.waitFor(t, "tag.created")
	if !webhooks.Verify("s3cret", tag.body, tag.header.Get(webhooks.SignatureHeader)) {
		t.Fatalf("signature %q does not verify", tag.header.Get(webhooks.SignatureHeader))
	}
	if tag.payload.Tag == nil || tag.payload.Tag.Name != "v1.0.0" || tag.payload.Tag.Message != "first release" ||
		tag.payload.Slice == nil || tag.payload.Slice.FullName != "acme/payment" || tag.header.Get("X-Gitslice-Event") != "tag.created" {
		t.Fatalf("tag.created payload:\n%s", tag.body)
	}

	// The delivery log has them, and redelivering keeps the event id.
	var log struct {
		Deliveries []struct {
			ID      string `json:"id"`
			Event   string `json:"event"`
			EventID string `json:"event_id"`
			Status  string `json:"status"`
		} `json:"deliveries"`
	}
	if err := json.Unmarshal([]byte(runCLI(t, home, payment, "webhook", "deliveries", created.ID, "--json")), &log); err != nil {
		t.Fatal(err)
	}
	if len(log.Deliveries) != 4 || log.Deliveries[0].Event != "tag.created" || log.Deliveries[0].Status != "succeeded" {
		t.Fatalf("deliveries = %+v", log.Deliveries)
	}
	var again struct {
		ID      string `json:"id"`
		EventID string `json:"event_id"`
		Status  string `json:"status"`
	}
	if err := json.Unmarshal([]byte(runCLI(t, home, payment, "webhook", "redeliver", log.Deliveries[0].ID, "--json")), &again); err != nil {
		t.Fatal(err)
	}
	if again.ID == log.Deliveries[0].ID || again.EventID != log.Deliveries[0].EventID || again.Status != "succeeded" {
		t.Fatalf("redeliver = %+v", again)
	}

	// A failing endpoint is reported and retried later.
	rcv.mu.Lock()
	rcv.code = http.StatusServiceUnavailable
	rcv.mu.Unlock()
	if _, stderr := runCLIFails(t, home, payment, "webhook", "ping", created.ID); !strings.Contains(stderr, "503") {
		t.Fatalf("failed ping should name the status:\n%s", stderr)
	}

	// Switched off, it gets nothing.
	runCLI(t, home, payment, "webhook", "update", created.ID, "--active", "false", "--clear-secret")
	rcv.mu.Lock()
	before := len(rcv.got)
	rcv.mu.Unlock()
	runCLI(t, home, payment, "tag", "create", "v1.0.1")
	time.Sleep(500 * time.Millisecond)
	rcv.mu.Lock()
	after := len(rcv.got)
	rcv.mu.Unlock()
	if after != before {
		t.Fatalf("an inactive webhook got %d deliveries", after-before)
	}

	if _, stderr := runCLIFails(t, home, payment, "webhook", "create", "--url", "ftp://example.com", "--event", "push"); !strings.Contains(stderr, "https") {
		t.Fatalf("bad URL should be refused:\n%s", stderr)
	}
	if _, stderr := runCLIFails(t, home, payment, "webhook", "delete", created.ID); !strings.Contains(stderr, "--yes") {
		t.Fatalf("delete without --yes:\n%s", stderr)
	}
	runCLI(t, home, payment, "webhook", "delete", created.ID, "--yes")
	if listed := runCLI(t, home, payment, "webhook", "list"); !strings.Contains(listed, "no webhooks") {
		t.Fatalf("after delete:\n%s", listed)
	}
}
