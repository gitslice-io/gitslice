package service

import (
	"context"
	"strings"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// captureActorAttribution builds a server-verified storage.ActorAttribution from
// ctx (and authStore fallback when ctx carries only SubjectID) for subjectID.
func captureActorAttribution(ctx context.Context, auth storage.AuthStore, subjectID string) storage.ActorAttribution {
	subjectID = strings.TrimSpace(subjectID)
	if subjectID == "" {
		return storage.ActorAttribution{}
	}

	var attr storage.ActorAttribution
	attr.ActingSubjectID = subjectID

	if p, ok := authctx.PrincipalFrom(ctx); ok && strings.TrimSpace(p.SubjectID) == subjectID {
		attr.ControllerSubjectID = strings.TrimSpace(p.ControllerSubjectID)
		attr.KeyID = strings.TrimSpace(p.KeyID)
		attr.DaemonID = strings.TrimSpace(p.DaemonID)
		attr.ConversationID = strings.TrimSpace(p.ConversationID)
	}

	if attr.ControllerSubjectID == "" && auth != nil {
		if subjects, err := auth.SubjectsByIDs(ctx, []string{subjectID}); err == nil {
			if subj, ok := subjects[subjectID]; ok {
				attr.ControllerSubjectID = strings.TrimSpace(subj.ControllerSubjectID)
			}
		}
	}
	return attr.Normalize()
}

// withCapturedAttribution attaches captureActorAttribution(ctx, auth, subjectID)
// onto ctx so downstream store writes persist accurate audit metadata.
func withCapturedAttribution(ctx context.Context, auth storage.AuthStore, subjectID string) (context.Context, storage.ActorAttribution) {
	attr := captureActorAttribution(ctx, auth, subjectID)
	return storage.WithActorAttribution(ctx, attr), attr
}

// enrichActorAttributions resolves acting_username, controller_username, and
// daemon_name across one or more proto ActorAttribution messages in batches.
func enrichActorAttributions(ctx context.Context, auth storage.AuthStore, agents storage.AgentStore, attrs ...*corev1.ActorAttribution) error {
	if len(attrs) == 0 {
		return nil
	}

	needControllerLookup := make([]string, 0)
	seenControllerLookup := make(map[string]struct{})
	for _, attr := range attrs {
		if attr == nil {
			continue
		}
		acting := strings.TrimSpace(attr.GetActingSubjectId())
		if acting == "" {
			continue
		}
		if strings.TrimSpace(attr.GetControllerSubjectId()) == acting {
			attr.ControllerSubjectId = ""
		}
		if strings.TrimSpace(attr.GetControllerSubjectId()) == "" {
			if _, seen := seenControllerLookup[acting]; !seen {
				seenControllerLookup[acting] = struct{}{}
				needControllerLookup = append(needControllerLookup, acting)
			}
		}
	}

	if auth != nil && len(needControllerLookup) > 0 {
		subjects, err := auth.SubjectsByIDs(ctx, needControllerLookup)
		if err != nil {
			return err
		}
		for _, attr := range attrs {
			if attr == nil || strings.TrimSpace(attr.GetControllerSubjectId()) != "" {
				continue
			}
			acting := strings.TrimSpace(attr.GetActingSubjectId())
			if subj, ok := subjects[acting]; ok {
				ctrl := strings.TrimSpace(subj.ControllerSubjectID)
				if ctrl != "" && ctrl != acting {
					attr.ControllerSubjectId = ctrl
				}
			}
		}
	}

	seenSubjects := make(map[string]struct{})
	var subjectIDs []string
	addSubj := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, ok := seenSubjects[id]; ok {
			return
		}
		seenSubjects[id] = struct{}{}
		subjectIDs = append(subjectIDs, id)
	}

	seenDaemons := make(map[string]struct{})
	var daemonIDs []string
	addDaemon := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, ok := seenDaemons[id]; ok {
			return
		}
		seenDaemons[id] = struct{}{}
		daemonIDs = append(daemonIDs, id)
	}

	for _, attr := range attrs {
		if attr == nil {
			continue
		}
		addSubj(attr.GetActingSubjectId())
		addSubj(attr.GetControllerSubjectId())
		if strings.TrimSpace(attr.GetDaemonName()) == "" {
			addDaemon(attr.GetDaemonId())
		}
	}

	usernames, err := newSubjectDirectory(auth).usernames(ctx, subjectIDs...)
	if err != nil {
		return err
	}

	daemonNames := make(map[string]string, len(daemonIDs))
	if agents != nil && len(daemonIDs) > 0 {
		if daemons, err := agents.GetDaemonsByIDs(ctx, daemonIDs); err == nil {
			for dID, d := range daemons {
				if d == nil {
					continue
				}
				label := strings.TrimSpace(d.GetDisplayName())
				if label == "" {
					label = strings.TrimSpace(d.GetName())
				}
				if label != "" {
					daemonNames[dID] = label
				}
			}
		}
	}

	for _, attr := range attrs {
		if attr == nil {
			continue
		}
		acting := strings.TrimSpace(attr.GetActingSubjectId())
		if acting != "" && strings.TrimSpace(attr.GetActingUsername()) == "" {
			if u := usernames[acting]; u != "" {
				attr.ActingUsername = u
			}
		}
		ctrl := strings.TrimSpace(attr.GetControllerSubjectId())
		if ctrl != "" && ctrl != acting && strings.TrimSpace(attr.GetControllerUsername()) == "" {
			if u := usernames[ctrl]; u != "" {
				attr.ControllerUsername = u
			}
		}
		dID := strings.TrimSpace(attr.GetDaemonId())
		if dID != "" && strings.TrimSpace(attr.GetDaemonName()) == "" {
			if dName := daemonNames[dID]; dName != "" {
				attr.DaemonName = dName
			}
		}
	}
	return nil
}

func collectChangesetAttributions(changesets ...*corev1.Changeset) []*corev1.ActorAttribution {
	var attrs []*corev1.ActorAttribution
	for _, cs := range changesets {
		if cs == nil {
			continue
		}
		if cs.Attribution != nil {
			attrs = append(attrs, cs.Attribution)
		}
		if cs.SubmittedAttribution != nil {
			attrs = append(attrs, cs.SubmittedAttribution)
		}
		for _, ps := range cs.Patchsets {
			if ps != nil && ps.Attribution != nil {
				attrs = append(attrs, ps.Attribution)
			}
		}
		for _, app := range cs.Approvals {
			if app != nil && app.Attribution != nil {
				attrs = append(attrs, app.Attribution)
			}
		}
	}
	return attrs
}
