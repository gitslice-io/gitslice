package webhooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/paths"
	"gitslice.io/gitslice/internal/secretbox"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

const (
	// UserAgent identifies deliveries.
	UserAgent = "Gitslice-Webhook/1.0"
	// SignatureHeader carries "sha256=<hex HMAC-SHA256 of the body>" when the
	// webhook has a secret.
	SignatureHeader = "X-Gitslice-Signature-256"

	// MaxAttempts is how many times a delivery is tried before it fails.
	MaxAttempts = 7

	maxResponseBody = 2 << 10
	// inlineWait is how long the request that caused an event waits for its
	// delivery. Hosts that throttle CPU between requests (Cloud Run) barely
	// run background work, so delivering inside the request is what makes
	// webhooks prompt there.
	inlineWait     = 2 * time.Second
	inlineDrains   = 4
	eventLease     = time.Minute
	deliveryLease  = 2 * time.Minute
	batchSize      = 50
	maxBatches     = 10
	sendWorkers    = 8
	pollInterval   = 30 * time.Second
	pruneInterval  = time.Hour
	historyKeep    = 30 * 24 * time.Hour
	hookCacheTTL   = 30 * time.Second
	deliveryWindow = 30 * time.Second
)

// retryDelays is the wait after each failed attempt.
var retryDelays = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour, 12 * time.Hour}

// Stores are what the dispatcher reads to fan out and describe events.
type Stores struct {
	Webhooks   storage.WebhookStore
	Slices     storage.SliceStore
	Changesets storage.ChangesetStore
	Checks     storage.CheckStore
	Auth       storage.AuthStore
}

// Options configure a Dispatcher.
type Options struct {
	// Secrets opens sealed webhook secrets; nil means they are stored plain.
	Secrets *secretbox.Box
	// WebBaseURL is the web app's origin, for links in payloads.
	WebBaseURL string
	// AllowPrivateTargets permits http and non-public addresses (tests and
	// local development only).
	AllowPrivateTargets bool
	// Client replaces the delivery HTTP client (tests).
	Client *http.Client
}

// Dispatcher fans recorded events out to webhooks and delivers them.
type Dispatcher struct {
	stores Stores
	opts   Options
	client *http.Client
	nudge  chan struct{}
	inline chan struct{}

	mu          sync.Mutex
	hasHooks    bool
	hooksCached time.Time
}

// NewDispatcher returns a dispatcher; call Run to deliver in the background.
func NewDispatcher(stores Stores, opts Options) *Dispatcher {
	client := opts.Client
	if client == nil {
		client = newClient(opts.AllowPrivateTargets)
	}
	opts.WebBaseURL = strings.TrimRight(opts.WebBaseURL, "/")
	return &Dispatcher{
		stores: stores,
		opts:   opts,
		client: client,
		nudge:  make(chan struct{}, 1),
		inline: make(chan struct{}, inlineDrains),
	}
}

// ValidateURL checks a webhook URL under this dispatcher's target rules.
func (d *Dispatcher) ValidateURL(raw string) (string, error) {
	return ValidateURL(raw, d.opts.AllowPrivateTargets)
}

// Emit records an event and delivers it (see Flush).
func (d *Dispatcher) Emit(ctx context.Context, event storage.WebhookEvent) {
	if _, err := d.stores.Webhooks.AppendWebhookEvent(context.WithoutCancel(ctx), event); err != nil {
		slog.WarnContext(ctx, "webhook event not recorded", "event", event.Kind, "changeset", event.ChangesetID, "error", err)
		return
	}
	d.Flush(ctx)
}

// Flush delivers recorded events now, waiting up to two seconds, unless no
// webhook is active (then the background loop marks them handled).
func (d *Dispatcher) Flush(ctx context.Context) {
	if !d.mayHaveHooks(ctx) {
		return
	}
	select {
	case d.inline <- struct{}{}:
	default:
		d.Nudge()
		return
	}
	done := make(chan struct{})
	go func() {
		defer func() {
			<-d.inline
			close(done)
		}()
		drainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), deliveryWindow)
		defer cancel()
		d.drainLogged(drainCtx)
	}()
	timer := time.NewTimer(inlineWait)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	case <-ctx.Done():
	}
}

// Nudge asks the background loop to drain now.
func (d *Dispatcher) Nudge() {
	select {
	case d.nudge <- struct{}{}:
	default:
	}
}

// WebhooksChanged drops the cached "any active webhooks" answer.
func (d *Dispatcher) WebhooksChanged() {
	d.mu.Lock()
	d.hooksCached = time.Time{}
	d.mu.Unlock()
}

func (d *Dispatcher) mayHaveHooks(ctx context.Context) bool {
	d.mu.Lock()
	if time.Since(d.hooksCached) < hookCacheTTL {
		has := d.hasHooks
		d.mu.Unlock()
		return has
	}
	d.mu.Unlock()
	hooks, err := d.stores.Webhooks.ListActiveWebhooks(ctx)
	if err != nil {
		return true
	}
	d.mu.Lock()
	d.hasHooks = len(hooks) > 0
	d.hooksCached = time.Now()
	d.mu.Unlock()
	return len(hooks) > 0
}

// Run drains until ctx ends: on a nudge, every 30 seconds for retries, and
// prunes history older than 30 days every hour.
func (d *Dispatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	prune := time.NewTicker(pruneInterval)
	defer prune.Stop()
	d.drainLogged(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-prune.C:
			if err := d.stores.Webhooks.PruneWebhookHistory(ctx, time.Now().Add(-historyKeep)); err != nil && ctx.Err() == nil {
				slog.Warn("prune webhook history", "error", err)
			}
			continue
		case <-ticker.C:
		case <-d.nudge:
		}
		d.drainLogged(ctx)
	}
}

func (d *Dispatcher) drainLogged(ctx context.Context) {
	if err := d.DrainOnce(ctx); err != nil && ctx.Err() == nil {
		slog.Warn("webhook drain", "error", err)
	}
}

// DrainOnce fans out recorded events and sends the deliveries that are due.
func (d *Dispatcher) DrainOnce(ctx context.Context) error {
	for range maxBatches {
		events, err := d.stores.Webhooks.ClaimWebhookEvents(ctx, batchSize, eventLease)
		if err != nil {
			return fmt.Errorf("claim events: %w", err)
		}
		if len(events) == 0 {
			break
		}
		hooks, err := d.stores.Webhooks.ListActiveWebhooks(ctx)
		if err != nil {
			return fmt.Errorf("list webhooks: %w", err)
		}
		slices := map[string]*corev1.Slice{}
		for _, event := range events {
			deliveries := d.fanOut(ctx, event, hooks, slices)
			if err := d.stores.Webhooks.FanOutWebhookEvent(ctx, event.ID, deliveries); err != nil {
				slog.Warn("fan out webhook event", "event", event.ID, "error", err)
			}
		}
		if len(events) < batchSize {
			break
		}
	}
	for range maxBatches {
		due, err := d.stores.Webhooks.ClaimDueWebhookDeliveries(ctx, batchSize, deliveryLease)
		if err != nil {
			return fmt.Errorf("claim deliveries: %w", err)
		}
		if len(due) == 0 {
			break
		}
		var wg sync.WaitGroup
		sem := make(chan struct{}, sendWorkers)
		for _, delivery := range due {
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer func() {
					<-sem
					wg.Done()
				}()
				d.attempt(ctx, delivery)
			}()
		}
		wg.Wait()
		if len(due) < batchSize {
			break
		}
	}
	return nil
}

func subscribes(hook storage.Webhook, kind string) bool {
	for _, event := range hook.Events {
		if event == kind || event == storage.WebhookEventsAll {
			return true
		}
	}
	return false
}

// eventContext is what an event refers to, read once per fan-out.
type eventContext struct {
	slice     *corev1.Slice
	changeset *corev1.Changeset
	checkRun  *corev1.CheckRun
	names     map[string]string
}

func (d *Dispatcher) fanOut(ctx context.Context, event storage.WebhookEvent, hooks []storage.Webhook, slices map[string]*corev1.Slice) []storage.WebhookDelivery {
	if len(hooks) == 0 {
		return nil
	}
	ec, err := d.loadContext(ctx, event)
	if err != nil {
		slog.Warn("webhook event context", "event", event.ID, "kind", event.Kind, "error", err)
		return nil
	}
	now := time.Now()
	var out []storage.WebhookDelivery
	for _, hook := range hooks {
		if ec.slice != nil && hook.SliceID == ec.slice.Id && subscribes(hook, event.Kind) {
			body, err := json.Marshal(d.payload(event, ec, nil))
			if err == nil {
				out = append(out, storage.WebhookDelivery{WebhookID: hook.ID, EventID: event.ID, Event: event.Kind, RequestBody: body, NextAttemptAt: now})
			}
		}
		// push: a landed commit that touches the webhook's slice, whichever
		// slice authored it.
		if event.Kind == storage.WebhookEventChangesetSubmitted && subscribes(hook, storage.WebhookEventPush) {
			hookSlice, ok := slices[hook.SliceID]
			if !ok {
				hookSlice, _ = d.stores.Slices.Get(ctx, hook.SliceID)
				slices[hook.SliceID] = hookSlice
			}
			if hookSlice == nil || hookSlice.Definition == nil {
				continue
			}
			var touched []string
			for _, p := range event.ChangedPaths {
				if paths.InAnyPrefix(hookSlice.Definition.IncludedPaths, p) {
					touched = append(touched, p)
				}
			}
			if len(touched) == 0 {
				continue
			}
			push := event
			push.Kind = storage.WebhookEventPush
			push.ID = event.ID + ".push"
			push.ChangedPaths = touched
			pushContext := *ec
			pushContext.slice = hookSlice
			// The changeset is described only to its own slice's webhooks.
			if ec.slice == nil || ec.slice.Id != hookSlice.Id {
				pushContext.changeset = nil
			}
			body, err := json.Marshal(d.payload(push, &pushContext, nil))
			if err == nil {
				out = append(out, storage.WebhookDelivery{WebhookID: hook.ID, EventID: push.ID, Event: push.Kind, RequestBody: body, NextAttemptAt: now})
			}
		}
	}
	return out
}

func (d *Dispatcher) loadContext(ctx context.Context, event storage.WebhookEvent) (*eventContext, error) {
	ec := &eventContext{}
	if event.ChangesetID != "" {
		cs, err := d.stores.Changesets.Get(ctx, event.ChangesetID)
		if err != nil {
			return nil, fmt.Errorf("changeset %s: %w", event.ChangesetID, err)
		}
		ec.changeset = cs
	}
	switch {
	case event.SliceID != "":
		slice, err := d.stores.Slices.Get(ctx, event.SliceID)
		if err != nil {
			return nil, fmt.Errorf("slice %s: %w", event.SliceID, err)
		}
		ec.slice = slice
	case ec.changeset != nil && ec.changeset.AuthoringSlice != nil:
		slice, err := d.stores.Slices.Resolve(ctx, ec.changeset.AuthoringSlice)
		if err != nil {
			return nil, fmt.Errorf("slice of changeset %s: %w", event.ChangesetID, err)
		}
		ec.slice = slice
	}
	if event.CheckRunID != "" && d.stores.Checks != nil {
		if run, err := d.stores.Checks.GetCheckRun(ctx, event.CheckRunID); err == nil {
			ec.checkRun = run
		}
	}
	var subjects []string
	if event.ActorSubjectID != "" {
		subjects = append(subjects, event.ActorSubjectID)
	}
	if ec.changeset != nil && ec.changeset.Author != "" {
		subjects = append(subjects, ec.changeset.Author)
	}
	if len(subjects) > 0 && d.stores.Auth != nil {
		ec.names, _ = d.stores.Auth.UsernamesForSubjects(ctx, subjects)
	}
	return ec, nil
}

// Payload is the JSON body of a delivery (design/24_webhooks.md).
type Payload struct {
	// ID is the event id: redeliveries keep it, so receivers can drop
	// duplicates.
	ID        string            `json:"id"`
	Event     string            `json:"event"`
	CreatedAt string            `json:"created_at"`
	Slice     *PayloadSlice     `json:"slice,omitempty"`
	Sender    *PayloadUser      `json:"sender,omitempty"`
	Hook      *PayloadHook      `json:"hook,omitempty"`
	Tag       *PayloadTag       `json:"tag,omitempty"`
	Changeset *PayloadChangeset `json:"changeset,omitempty"`
	Commit    *PayloadCommit    `json:"commit,omitempty"`
	CheckRun  *PayloadCheckRun  `json:"check_run,omitempty"`
	// Zen is set on ping.
	Zen string `json:"zen,omitempty"`
}

type PayloadSlice struct {
	ID       string `json:"id"`
	Account  string `json:"account"`
	Name     string `json:"name"`
	FullName string `json:"full_name"`
	URL      string `json:"url,omitempty"`
}

type PayloadUser struct {
	Username string `json:"username"`
}

type PayloadHook struct {
	ID     string   `json:"id"`
	URL    string   `json:"url"`
	Events []string `json:"events"`
}

type PayloadTag struct {
	Name     string `json:"name"`
	CommitID string `json:"commit_id"`
	Message  string `json:"message,omitempty"`
}

type PayloadChangeset struct {
	ID             string `json:"id"`
	Handle         string `json:"handle"`
	Number         int64  `json:"number,omitempty"`
	Title          string `json:"title"`
	Status         string `json:"status"`
	Author         string `json:"author,omitempty"`
	TargetRef      string `json:"target_ref"`
	PatchsetID     string `json:"patchset_id,omitempty"`
	PatchsetNumber int64  `json:"patchset_number,omitempty"`
	CommitID       string `json:"commit_id,omitempty"`
	URL            string `json:"url,omitempty"`
}

type PayloadCommit struct {
	ID           string   `json:"id"`
	TargetRef    string   `json:"target_ref,omitempty"`
	ChangedPaths []string `json:"changed_paths,omitempty"`
}

type PayloadCheckRun struct {
	ID         string `json:"id,omitempty"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Summary    string `json:"summary,omitempty"`
	ExitCode   int32  `json:"exit_code,omitempty"`
	PatchsetID string `json:"patchset_id,omitempty"`
}

func (d *Dispatcher) payload(event storage.WebhookEvent, ec *eventContext, hook *storage.Webhook) Payload {
	created := event.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	p := Payload{ID: event.ID, Event: event.Kind, CreatedAt: created.UTC().Format(time.RFC3339)}
	if ec.slice != nil {
		p.Slice = d.payloadSlice(ec.slice)
	}
	if name := ec.names[event.ActorSubjectID]; name != "" {
		p.Sender = &PayloadUser{Username: name}
	}
	if hook != nil {
		p.Hook = &PayloadHook{ID: hook.ID, URL: hook.URL, Events: nonNilStrings(hook.Events)}
	}
	var extra map[string]string
	if event.Data != "" {
		_ = json.Unmarshal([]byte(event.Data), &extra)
	}
	if event.TagName != "" {
		p.Tag = &PayloadTag{Name: event.TagName, CommitID: event.CommitID, Message: extra["message"]}
	}
	if cs := ec.changeset; cs != nil {
		handle := storage.ShortChangesetID(cs.Id)
		pc := &PayloadChangeset{
			ID:             cs.Id,
			Handle:         handle,
			Number:         cs.Number,
			Title:          cs.Title,
			Status:         cs.Status,
			Author:         firstNonEmpty(ec.names[cs.Author], cs.Author),
			TargetRef:      cs.TargetRef,
			PatchsetID:     firstNonEmpty(event.PatchsetID, cs.CurrentPatchsetId),
			PatchsetNumber: cs.CurrentPatchsetNumber,
			CommitID:       cs.CommitId,
		}
		for _, ps := range cs.Patchsets {
			if ps.Id == pc.PatchsetID {
				pc.PatchsetNumber = ps.Number
			}
		}
		if d.opts.WebBaseURL != "" {
			pc.URL = d.opts.WebBaseURL + "/cs/" + handle
		}
		p.Changeset = pc
	}
	if event.Kind == storage.WebhookEventChangesetSubmitted || event.Kind == storage.WebhookEventPush {
		p.Commit = &PayloadCommit{ID: event.CommitID, TargetRef: event.TargetRef, ChangedPaths: nonNilStrings(event.ChangedPaths)}
	}
	if event.Kind == storage.WebhookEventCheckRunCompleted {
		if run := ec.checkRun; run != nil {
			p.CheckRun = &PayloadCheckRun{ID: run.Id, Name: run.CheckName, Status: run.Status, Summary: run.Summary, ExitCode: run.ExitCode, PatchsetID: run.PatchsetId}
		} else {
			p.CheckRun = &PayloadCheckRun{Name: extra["name"], Status: extra["status"], PatchsetID: event.PatchsetID}
		}
	}
	return p
}

func (d *Dispatcher) payloadSlice(slice *corev1.Slice) *PayloadSlice {
	ps := &PayloadSlice{ID: slice.Id}
	if slice.Ref != nil {
		ps.Account = slice.Ref.Account
		ps.Name = slice.Ref.Slice
		ps.FullName = slice.Ref.Account + "/" + slice.Ref.Slice
		if d.opts.WebBaseURL != "" {
			ps.URL = d.opts.WebBaseURL + "/slices/" + url.PathEscape(slice.Ref.Account) + "/" + url.PathEscape(slice.Ref.Slice)
		}
	}
	return ps
}

// Ping records a ping delivery for hook and sends it now. senderSubjectID is
// whoever asked for it.
func (d *Dispatcher) Ping(ctx context.Context, hook *storage.Webhook, senderSubjectID string) (*storage.WebhookDelivery, error) {
	slice, err := d.stores.Slices.Get(ctx, hook.SliceID)
	if err != nil {
		return nil, err
	}
	eventID, err := objectid.RandomID("evt")
	if err != nil {
		return nil, err
	}
	ec := &eventContext{slice: slice}
	if senderSubjectID != "" && d.stores.Auth != nil {
		ec.names, _ = d.stores.Auth.UsernamesForSubjects(ctx, []string{senderSubjectID})
	}
	p := d.payload(storage.WebhookEvent{ID: eventID, Kind: storage.WebhookEventPing, ActorSubjectID: senderSubjectID}, ec, hook)
	p.Zen = "Gitslice is listening."
	body, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	delivery, err := d.stores.Webhooks.CreateWebhookDelivery(ctx, storage.WebhookDelivery{
		WebhookID: hook.ID, EventID: eventID, Event: storage.WebhookEventPing, RequestBody: body,
		// Not due until the attempt below gives up its lease, so the loop
		// does not send it as well.
		NextAttemptAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		return nil, err
	}
	return d.DeliverNow(ctx, delivery.ID)
}

// Redeliver sends a delivery's body again, now, as a new delivery with the
// same event id.
func (d *Dispatcher) Redeliver(ctx context.Context, delivery *storage.WebhookDelivery) (*storage.WebhookDelivery, error) {
	again, err := d.stores.Webhooks.CreateWebhookDelivery(ctx, storage.WebhookDelivery{
		WebhookID: delivery.WebhookID, EventID: delivery.EventID, Event: delivery.Event, RequestBody: delivery.RequestBody,
		NextAttemptAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		return nil, err
	}
	return d.DeliverNow(ctx, again.ID)
}

// DeliverNow attempts one pending delivery immediately and returns it after
// the attempt.
func (d *Dispatcher) DeliverNow(ctx context.Context, deliveryID string) (*storage.WebhookDelivery, error) {
	delivery, err := d.stores.Webhooks.ClaimWebhookDelivery(ctx, deliveryID, deliveryLease)
	if err != nil {
		return nil, err
	}
	d.attempt(context.WithoutCancel(ctx), *delivery)
	return d.stores.Webhooks.GetWebhookDelivery(context.WithoutCancel(ctx), deliveryID)
}

// attempt sends a claimed delivery once and records the outcome.
func (d *Dispatcher) attempt(ctx context.Context, delivery storage.WebhookDelivery) {
	delivery.Attempts++
	result := d.send(ctx, delivery)
	delivery.ResponseStatus = result.status
	delivery.ResponseBody = result.body
	delivery.DurationMs = result.duration.Milliseconds()
	delivery.Error = result.err
	now := time.Now()
	switch {
	case result.err == "":
		delivery.Status = storage.WebhookDeliverySucceeded
		delivery.DeliveredAt = now
		delivery.NextAttemptAt = now
	case result.final || delivery.Attempts >= MaxAttempts || delivery.Event == storage.WebhookEventPing:
		// Pings report at once and are not retried.
		delivery.Status = storage.WebhookDeliveryFailed
		delivery.NextAttemptAt = now
	default:
		delivery.Status = storage.WebhookDeliveryPending
		delivery.NextAttemptAt = now.Add(retryDelays[min(delivery.Attempts-1, len(retryDelays)-1)])
	}
	if err := d.stores.Webhooks.RecordWebhookAttempt(ctx, delivery); err != nil {
		slog.Warn("record webhook attempt", "delivery", delivery.ID, "error", err)
	}
}

type sendResult struct {
	status   int
	body     string
	duration time.Duration
	err      string
	// final means retrying cannot help.
	final bool
}

func (d *Dispatcher) send(ctx context.Context, delivery storage.WebhookDelivery) sendResult {
	hook, err := d.stores.Webhooks.GetWebhook(ctx, delivery.WebhookID)
	if errors.Is(err, storage.ErrNotFound) {
		return sendResult{err: "the webhook was deleted", final: true}
	}
	if err != nil {
		return sendResult{err: "read the webhook: " + err.Error()}
	}
	if !hook.Active && delivery.Event != storage.WebhookEventPing {
		return sendResult{err: "the webhook is inactive", final: true}
	}
	secret, err := d.openSecret(hook.SealedSecret)
	if err != nil {
		return sendResult{err: err.Error(), final: true}
	}
	if _, err := d.ValidateURL(hook.URL); err != nil {
		return sendResult{err: err.Error(), final: true}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hook.URL, bytes.NewReader(delivery.RequestBody))
	if err != nil {
		return sendResult{err: err.Error(), final: true}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("X-Gitslice-Event", delivery.Event)
	req.Header.Set("X-Gitslice-Event-ID", delivery.EventID)
	req.Header.Set("X-Gitslice-Delivery", delivery.ID)
	req.Header.Set("X-Gitslice-Hook-ID", hook.ID)
	if secret != "" {
		req.Header.Set(SignatureHeader, Sign(secret, delivery.RequestBody))
	}
	start := time.Now()
	resp, err := d.client.Do(req)
	if err != nil {
		result := sendResult{duration: time.Since(start), err: describeSendError(err)}
		result.final = isBlocked(err)
		return result
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	result := sendResult{status: resp.StatusCode, body: string(bytes.ToValidUTF8(body, []byte("?"))), duration: time.Since(start)}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		result.err = fmt.Sprintf("the endpoint answered %s", resp.Status)
	}
	return result
}

func describeSendError(err error) string {
	if isBlocked(err) {
		return errBlockedAddress.Error()
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if urlErr.Timeout() {
			return "the endpoint did not answer within 10 seconds"
		}
		return urlErr.Err.Error()
	}
	return err.Error()
}

func (d *Dispatcher) openSecret(stored string) (string, error) {
	if stored == "" {
		return "", nil
	}
	if d.opts.Secrets == nil {
		if secretbox.IsSealed(stored) {
			return "", errors.New("the server cannot open the webhook's secret: set it again")
		}
		return stored, nil
	}
	secret, err := d.opts.Secrets.Open(stored)
	if err != nil {
		return "", errors.New("the server cannot open the webhook's secret: set it again")
	}
	return secret, nil
}

// SealSecret prepares a secret for storage.
func (d *Dispatcher) SealSecret(secret string) (string, error) {
	if secret == "" || d.opts.Secrets == nil {
		return secret, nil
	}
	return d.opts.Secrets.Seal(secret)
}

// Sign returns the signature header value for body: "sha256=" and the hex
// HMAC-SHA256 of the body keyed by secret.
func Sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// Verify reports whether signature (the header value) matches body.
func Verify(secret string, body []byte, signature string) bool {
	return hmac.Equal([]byte(Sign(secret, body)), []byte(strings.TrimSpace(signature)))
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
