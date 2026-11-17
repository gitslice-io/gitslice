package storage

import (
	"context"
	"fmt"
	"strings"

	"gitslice.io/gitslice/internal/authctx"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

type actorAttributionKey struct{}

// ActorAttribution captures the server-verified acting subject, controller,
// credential key, daemon, and conversation for an audited mutation
// (design/36 section 7).
type ActorAttribution struct {
	ActingSubjectID     string
	ControllerSubjectID string
	KeyID               string
	DaemonID            string
	ConversationID      string
}

// Normalize trims whitespace and clears self-referential controller IDs (human
// subjects store controller_subject_id == id in the subjects table for SQL
// distinct-controller counting, whereas audit attribution only records a
// controller when another subject controls the actor).
func (a ActorAttribution) Normalize() ActorAttribution {
	a.ActingSubjectID = strings.TrimSpace(a.ActingSubjectID)
	a.ControllerSubjectID = strings.TrimSpace(a.ControllerSubjectID)
	if a.ControllerSubjectID == a.ActingSubjectID {
		a.ControllerSubjectID = ""
	}
	a.KeyID = strings.TrimSpace(a.KeyID)
	a.DaemonID = strings.TrimSpace(a.DaemonID)
	a.ConversationID = strings.TrimSpace(a.ConversationID)
	return a
}

// ToProto converts a stored ActorAttribution into its wire representation,
// returning nil when no acting subject is recorded.
func (a ActorAttribution) ToProto() *corev1.ActorAttribution {
	norm := a.Normalize()
	if norm.ActingSubjectID == "" {
		return nil
	}
	return &corev1.ActorAttribution{
		ActingSubjectId:     norm.ActingSubjectID,
		ControllerSubjectId: norm.ControllerSubjectID,
		KeyId:               norm.KeyID,
		DaemonId:            norm.DaemonID,
		ConversationId:      norm.ConversationID,
	}
}

// ActorAttributionFromProto converts a wire ActorAttribution back into storage
// form.
func ActorAttributionFromProto(p *corev1.ActorAttribution) ActorAttribution {
	if p == nil {
		return ActorAttribution{}
	}
	return ActorAttribution{
		ActingSubjectID:     p.GetActingSubjectId(),
		ControllerSubjectID: p.GetControllerSubjectId(),
		KeyID:               p.GetKeyId(),
		DaemonID:            p.GetDaemonId(),
		ConversationID:      p.GetConversationId(),
	}.Normalize()
}

// WithActorAttribution attaches a server-verified ActorAttribution to ctx for
// downstream store writes.
func WithActorAttribution(ctx context.Context, attr ActorAttribution) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, actorAttributionKey{}, attr.Normalize())
}

// ActorAttributionFrom resolves the ActorAttribution for an operation from ctx,
// falling back to authctx.PrincipalFrom(ctx) / authctx.SubjectID(ctx) and then
// fallbackSubjectID. When fallbackSubjectID is non-empty, context metadata is
// only used if its subject matches fallbackSubjectID.
func ActorAttributionFrom(ctx context.Context, fallbackSubjectID string) ActorAttribution {
	fallback := strings.TrimSpace(fallbackSubjectID)
	if ctx != nil {
		if attr, ok := ctx.Value(actorAttributionKey{}).(ActorAttribution); ok && attr.ActingSubjectID != "" {
			if fallback == "" || attr.ActingSubjectID == fallback {
				return attr.Normalize()
			}
		}
		if p, ok := authctx.PrincipalFrom(ctx); ok && strings.TrimSpace(p.SubjectID) != "" {
			if fallback == "" || strings.TrimSpace(p.SubjectID) == fallback {
				return ActorAttribution{
					ActingSubjectID:     p.SubjectID,
					ControllerSubjectID: p.ControllerSubjectID,
					KeyID:               p.KeyID,
					DaemonID:            p.DaemonID,
					ConversationID:      p.ConversationID,
				}.Normalize()
			}
		}
		if fallback == "" {
			if sid, ok := authctx.SubjectID(ctx); ok && strings.TrimSpace(sid) != "" {
				return ActorAttribution{ActingSubjectID: sid}.Normalize()
			}
		}
	}
	return ActorAttribution{ActingSubjectID: fallback}.Normalize()
}

// FormatActorAttribution renders an ActorAttribution in the canonical human
// format: "<acting> (for <controller>) via <daemon_name || daemon_id>, conversation <conv_id>".
func FormatActorAttribution(attr *corev1.ActorAttribution, fallbackUsername string) string {
	acting := strings.TrimSpace(attr.GetActingUsername())
	if acting == "" {
		acting = strings.TrimSpace(fallbackUsername)
	}
	if acting == "" {
		acting = strings.TrimSpace(attr.GetActingSubjectId())
	}
	if acting == "" {
		return ""
	}

	var b strings.Builder
	b.WriteString(acting)

	ctrl := strings.TrimSpace(attr.GetControllerUsername())
	if ctrl == "" && strings.TrimSpace(attr.GetControllerSubjectId()) != "" && strings.TrimSpace(attr.GetControllerSubjectId()) != strings.TrimSpace(attr.GetActingSubjectId()) {
		ctrl = strings.TrimSpace(attr.GetControllerSubjectId())
	}
	if ctrl != "" && ctrl != acting && (strings.TrimSpace(attr.GetControllerSubjectId()) == "" || strings.TrimSpace(attr.GetControllerSubjectId()) != strings.TrimSpace(attr.GetActingSubjectId())) {
		fmt.Fprintf(&b, " (for %s)", ctrl)
	}

	daemon := strings.TrimSpace(attr.GetDaemonName())
	if daemon == "" {
		daemon = strings.TrimSpace(attr.GetDaemonId())
	}
	if daemon != "" {
		fmt.Fprintf(&b, " via %s", daemon)
	}

	if conv := strings.TrimSpace(attr.GetConversationId()); conv != "" {
		fmt.Fprintf(&b, ", conversation %s", conv)
	}
	return b.String()
}
