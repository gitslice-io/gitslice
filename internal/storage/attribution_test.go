package storage

import (
	"context"
	"testing"

	"gitslice.io/gitslice/internal/authctx"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestActorAttributionNormalizeContextAndFormat(t *testing.T) {
	// Human controller_subject_id == acting_subject_id normalizes controller to "".
	selfAttr := (ActorAttribution{
		ActingSubjectID:     "user_nic",
		ControllerSubjectID: "user_nic",
		KeyID:               "key_1",
	}).Normalize()
	if selfAttr.ControllerSubjectID != "" {
		t.Fatalf("expected self controller cleared, got %+v", selfAttr)
	}

	// Principal fallback extracts key, controller, daemon, and conversation.
	pCtx := authctx.WithPrincipal(context.Background(), authctx.Principal{
		SubjectID:           "agent_web",
		SubjectKind:         "agent",
		ControllerSubjectID: "user_nic",
		KeyID:               "key_child_1",
		DaemonID:            "agent_daemon_1",
		ConversationID:      "cv_42",
	})
	got := ActorAttributionFrom(pCtx, "agent_web")
	if got.ActingSubjectID != "agent_web" || got.ControllerSubjectID != "user_nic" || got.KeyID != "key_child_1" || got.DaemonID != "agent_daemon_1" || got.ConversationID != "cv_42" {
		t.Fatalf("unexpected Principal attribution: %+v", got)
	}

	// Mismatched fallbackSubjectID must not inherit another subject's Principal metadata.
	other := ActorAttributionFrom(pCtx, "user_other")
	if other.ActingSubjectID != "user_other" || other.KeyID != "" || other.DaemonID != "" || other.ConversationID != "" {
		t.Fatalf("mismatched subject leaked Principal metadata: %+v", other)
	}

	// FormatActorAttribution variants.
	full := FormatActorAttribution(&corev1.ActorAttribution{
		ActingSubjectId:     "agent_web",
		ActingUsername:      "web-agent",
		ControllerSubjectId: "user_nic",
		ControllerUsername:  "nic",
		DaemonId:            "agent_daemon_1",
		DaemonName:          "my-ci",
		ConversationId:      "cv_42",
	}, "web-agent")
	if full != "web-agent (for nic) via my-ci, conversation cv_42" {
		t.Fatalf("full = %q", full)
	}

	plainUser := FormatActorAttribution(&corev1.ActorAttribution{
		ActingSubjectId:     "user_nic",
		ActingUsername:      "nic",
		ControllerSubjectId: "user_nic",
		ControllerUsername:  "nic",
	}, "nic")
	if plainUser != "nic" {
		t.Fatalf("plainUser = %q, want nic", plainUser)
	}

	daemonIDFallback := FormatActorAttribution(&corev1.ActorAttribution{
		ActingSubjectId: "agent_unclaimed",
		ActingUsername:  "solo-bot",
		DaemonId:        "agent_d1",
	}, "")
	if daemonIDFallback != "solo-bot via agent_d1" {
		t.Fatalf("daemonIDFallback = %q, want 'solo-bot via agent_d1'", daemonIDFallback)
	}
}
