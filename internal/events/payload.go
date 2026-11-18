package events

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// eventContext is what an event refers to, read once per fan-out.
type eventContext struct {
	slice     *corev1.Slice
	changeset *corev1.Changeset
	checkRun  *corev1.CheckRun
	names     map[string]string
}

func (ec *eventContext) entities() storage.EventEntities {
	if ec == nil {
		return storage.EventEntities{}
	}
	return storage.EventEntities{
		Slice:     ec.slice,
		Changeset: ec.changeset,
		CheckRun:  ec.checkRun,
	}
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
