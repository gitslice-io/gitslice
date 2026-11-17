package events

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/activity"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

const (
	inlineWait    = 2 * time.Second
	inlineDrains  = 4
	eventLease    = time.Minute
	batchSize     = 50
	maxBatches    = 10
	pollInterval  = 30 * time.Second
	pruneInterval = time.Hour
	historyKeep   = 30 * 24 * time.Hour
	drainWindow   = 30 * time.Second
)

// Stores are what the dispatcher reads to fan out and describe events.
type Stores struct {
	Events     storage.EventStore
	Slices     storage.SliceStore
	Changesets storage.ChangesetStore
	Checks     storage.CheckStore
	Auth       storage.AuthStore
}

// Options configure a Dispatcher.
type Options struct {
	// WebBaseURL is the web app's origin, for links in trigger payloads.
	WebBaseURL string
	// Triggers, when set, turns events into trigger runs too.
	Triggers Triggers
	// Mirrors, when set, queues and pushes slice Git mirrors.
	Mirrors Mirrors
}

// Mirrors queues Git mirror pushes for recorded events and runs due ones.
type Mirrors interface {
	FanOut(ctx context.Context, event storage.Event)
	Drain(ctx context.Context)
}

// Triggers turns recorded events into trigger runs and runs them.
type Triggers interface {
	// MayHaveTriggers says whether events are worth resolving at once.
	MayHaveTriggers(ctx context.Context) bool
	// FanOut creates the event's trigger runs. envelope is the event's payload.
	// It must be safe to repeat.
	FanOut(ctx context.Context, event storage.Event, envelope []byte)
	// Drain sends and starts due runs and settles expired ones.
	Drain(ctx context.Context)
}

// Dispatcher fans recorded events out to triggers and Git mirrors.
type Dispatcher struct {
	stores Stores
	opts   Options
	nudge  chan struct{}
	inline chan struct{}
}

// NewDispatcher returns a dispatcher; call Run to fan out in the background.
func NewDispatcher(stores Stores, opts Options) *Dispatcher {
	opts.WebBaseURL = strings.TrimRight(opts.WebBaseURL, "/")
	return &Dispatcher{
		stores: stores,
		opts:   opts,
		nudge:  make(chan struct{}, 1),
		inline: make(chan struct{}, inlineDrains),
	}
}

// Emit records an event and fans it out (see Flush).
func (d *Dispatcher) Emit(ctx context.Context, event storage.Event) {
	if _, err := d.stores.Events.AppendEvent(context.WithoutCancel(ctx), event); err != nil {
		slog.WarnContext(ctx, "event not recorded", "event", event.Kind, "changeset", event.ChangesetID, "error", err)
		return
	}
	d.Flush(ctx)
}

// Flush fans out recorded events now, waiting up to two seconds so
// request-driven hosts make progress before their CPU is throttled, when a
// trigger is likely to run. Otherwise it only nudges the background loop, so a
// request that records an event doesn't wait for mirrors.
func (d *Dispatcher) Flush(ctx context.Context) {
	if d.opts.Triggers == nil && d.opts.Mirrors == nil {
		return
	}
	if d.opts.Triggers == nil || !d.opts.Triggers.MayHaveTriggers(ctx) {
		d.Nudge()
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
		drainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), drainWindow)
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

// Run drains until ctx ends: on a nudge, every 30 seconds for trigger and
// mirror retries, and prunes completed events every hour.
func (d *Dispatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	prune := time.NewTicker(pruneInterval)
	defer prune.Stop()
	if !d.waitActiveOrNudge(ctx) {
		return
	}
	d.drainLogged(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-prune.C:
			if activity.Default.Wait(ctx) != nil {
				return
			}
			if err := d.stores.Events.PruneEvents(ctx, time.Now().Add(-historyKeep)); err != nil && ctx.Err() == nil {
				slog.Warn("prune events", "error", err)
			}
			continue
		case <-ticker.C:
			// Retries wait for the server to be in use (internal/activity): an
			// idle server leaves the database alone.
			if activity.Default.Wait(ctx) != nil {
				return
			}
		case <-d.nudge:
		}
		d.drainLogged(ctx)
	}
}

func (d *Dispatcher) waitActiveOrNudge(ctx context.Context) bool {
	waitCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	active := make(chan error, 1)
	go func() {
		active <- activity.Default.Wait(waitCtx)
	}()
	select {
	case <-ctx.Done():
		return false
	case <-d.nudge:
		return true
	case err := <-active:
		return err == nil
	}
}

func (d *Dispatcher) drainLogged(ctx context.Context) {
	if err := d.DrainOnce(ctx); err != nil && ctx.Err() == nil {
		slog.Warn("event drain", "error", err)
	}
}

// DrainOnce fans out recorded events and drains their consumers.
func (d *Dispatcher) DrainOnce(ctx context.Context) error {
	for range maxBatches {
		events, err := d.stores.Events.ClaimEvents(ctx, batchSize, eventLease)
		if err != nil {
			return fmt.Errorf("claim events: %w", err)
		}
		if len(events) == 0 {
			break
		}
		for _, event := range events {
			if d.opts.Mirrors != nil {
				d.opts.Mirrors.FanOut(ctx, event)
			}
			if d.opts.Triggers != nil {
				ec, err := d.loadContext(ctx, event)
				if err != nil {
					slog.Warn("event context", "event", event.ID, "kind", event.Kind, "error", err)
				} else if envelope, err := json.Marshal(d.payload(event, ec)); err != nil {
					slog.Warn("marshal event payload", "event", event.ID, "error", err)
				} else {
					d.opts.Triggers.FanOut(ctx, event, envelope)
				}
			}
			if err := d.stores.Events.CompleteEvent(ctx, event.ID); err != nil {
				slog.Warn("complete event", "event", event.ID, "error", err)
			}
		}
		if len(events) < batchSize {
			break
		}
	}
	if d.opts.Triggers != nil {
		d.opts.Triggers.Drain(ctx)
	}
	if d.opts.Mirrors != nil {
		d.opts.Mirrors.Drain(ctx)
	}
	return nil
}

// eventContext is what an event refers to, read once per fan-out.
type eventContext struct {
	slice     *corev1.Slice
	changeset *corev1.Changeset
	checkRun  *corev1.CheckRun
	names     map[string]string
}

func (d *Dispatcher) loadContext(ctx context.Context, event storage.Event) (*eventContext, error) {
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

// Payload is the JSON body delivered to a trigger.
type Payload struct {
	ID        string            `json:"id"`
	Event     string            `json:"event"`
	CreatedAt string            `json:"created_at"`
	Slice     *PayloadSlice     `json:"slice,omitempty"`
	Sender    *PayloadUser      `json:"sender,omitempty"`
	Tag       *PayloadTag       `json:"tag,omitempty"`
	Changeset *PayloadChangeset `json:"changeset,omitempty"`
	Commit    *PayloadCommit    `json:"commit,omitempty"`
	CheckRun  *PayloadCheckRun  `json:"check_run,omitempty"`
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

func (d *Dispatcher) payload(event storage.Event, ec *eventContext) Payload {
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
	if event.Kind == storage.EventChangesetSubmitted || event.Kind == storage.EventPush {
		p.Commit = &PayloadCommit{ID: event.CommitID, TargetRef: event.TargetRef, ChangedPaths: nonNilStrings(event.ChangedPaths)}
	}
	if event.Kind == storage.EventCheckRunCompleted {
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
