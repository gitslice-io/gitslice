package authctx

import (
	"context"
	"slices"
	"testing"
)

func TestPrincipalRoundTripAndBackwardCompatibility(t *testing.T) {
	ctx := WithPrincipal(context.Background(), Principal{
		SubjectID:           "agent_1",
		SubjectKind:         "agent",
		ControllerSubjectID: "user_alice",
		KeyID:               "key_1",
		HasSliceScope:       true,
		SliceIDs:            []string{"slice_a", "slice_b"},
		MaxRole:             "contributor",
		Purpose:             "test",
		DaemonID:            "daemon_1",
		ConversationID:      "conv_1",
	})

	if sid, ok := SubjectID(ctx); !ok || sid != "agent_1" {
		t.Fatalf("SubjectID = (%q, %v), want (agent_1, true)", sid, ok)
	}
	p, ok := PrincipalFrom(ctx)
	if !ok {
		t.Fatal("PrincipalFrom = false, want true")
	}
	if p.SubjectID != "agent_1" || p.SubjectKind != "agent" || p.ControllerSubjectID != "user_alice" ||
		p.KeyID != "key_1" || !p.HasSliceScope || !slices.Equal(p.SliceIDs, []string{"slice_a", "slice_b"}) ||
		p.MaxRole != "contributor" || p.Purpose != "test" || p.DaemonID != "daemon_1" || p.ConversationID != "conv_1" {
		t.Fatalf("PrincipalFrom = %+v", p)
	}
	// Mutating the returned slice must not mutate the context value.
	p.SliceIDs[0] = "mutated"
	p2, _ := PrincipalFrom(ctx)
	if p2.SliceIDs[0] != "slice_a" {
		t.Fatalf("SliceIDs mutated in context: %v", p2.SliceIDs)
	}

	if !p2.AllowsSlice("slice_a") || !p2.AllowsSlice("slice_b") || p2.AllowsSlice("slice_c") || p2.AllowsSlice("") {
		t.Fatalf("AllowsSlice unexpected result on scoped principal: %+v", p2)
	}
	if !p2.HasKeyGrant() {
		t.Fatal("HasKeyGrant = false, want true")
	}

	// WithSubjectID with the same subject preserves Principal metadata.
	sameCtx := WithSubjectID(ctx, "agent_1")
	if sameP, ok := PrincipalFrom(sameCtx); !ok || sameP.KeyID != "key_1" {
		t.Fatalf("WithSubjectID same subject dropped principal: (%+v, %v)", sameP, ok)
	}

	// WithSubjectID with another subject MUST clear stale key metadata.
	otherCtx := WithSubjectID(ctx, "user_bob")
	if sid, ok := SubjectID(otherCtx); !ok || sid != "user_bob" {
		t.Fatalf("SubjectID(otherCtx) = (%q, %v), want (user_bob, true)", sid, ok)
	}
	if otherP, ok := PrincipalFrom(otherCtx); ok || otherP.KeyID != "" || otherP.HasSliceScope || otherP.MaxRole != "" {
		t.Fatalf("WithSubjectID other subject retained stale principal: (%+v, %v)", otherP, ok)
	}

	// Plain WithSubjectID without Principal works and has no Principal metadata.
	plainCtx := WithSubjectID(context.Background(), "user_carol")
	if sid, ok := SubjectID(plainCtx); !ok || sid != "user_carol" {
		t.Fatalf("SubjectID(plainCtx) = (%q, %v), want (user_carol, true)", sid, ok)
	}
	if plainP, ok := PrincipalFrom(plainCtx); ok || plainP.KeyID != "" {
		t.Fatalf("PrincipalFrom(plainCtx) = (%+v, %v), want empty false", plainP, ok)
	}
}
