package model

import (
	"testing"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestModelHandlesRolesAndAttribution(t *testing.T) {
	if got := ShortChangesetID("cs_0123456789abcdef"); got != "0123456789" {
		t.Fatalf("ShortChangesetID = %q", got)
	}
	if prefix, ok := ChangesetIDLookupPrefix("0123abcd"); !ok || prefix != "cs_0123abcd" {
		t.Fatalf("ChangesetIDLookupPrefix = %q, %v", prefix, ok)
	}
	if prefix, ok := CommitIDLookupPrefix("01234567"); !ok || prefix != "sha256:01234567" {
		t.Fatalf("CommitIDLookupPrefix = %q, %v", prefix, ok)
	}
	if acc, sl, ok := SplitSliceHandle("acme:payment"); !ok || acc != "acme" || sl != "payment" {
		t.Fatalf("SplitSliceHandle = %q, %q, %v", acc, sl, ok)
	}
	if AccountRoleRank("owner") >= AccountRoleRank("reader") || SliceRoleRank("admin") >= SliceRoleRank("reader") {
		t.Fatal("unexpected role rank order")
	}
	if !ValidSliceSecretName("DEPLOY_TOKEN_1") || ValidSliceSecretName("1BAD") || ValidSliceSecretName("bad-name") {
		t.Fatal("ValidSliceSecretName mismatch")
	}
	attr := &corev1.ActorAttribution{
		ActingUsername:     "bot",
		ControllerUsername: "alice",
		DaemonName:         "mac-mini",
		ConversationId:     "conv_1",
	}
	if got := FormatActorAttribution(attr, ""); got != "bot (for alice) via mac-mini, conversation conv_1" {
		t.Fatalf("FormatActorAttribution = %q", got)
	}
}

func TestCheckRunStatusHelpers(t *testing.T) {
	if got, err := NormalizeCheckRunStatus(""); err != nil || got != "queued" {
		t.Fatalf("NormalizeCheckRunStatus(\"\") = %q, %v", got, err)
	}
	if got, err := NormalizeCheckRunStatus(" PASSED "); err != nil || got != "passed" {
		t.Fatalf("NormalizeCheckRunStatus(\" PASSED \") = %q, %v", got, err)
	}
	if _, err := NormalizeCheckRunStatus("bogus"); err == nil {
		t.Fatal("expected error for bogus check run status")
	}
	for _, s := range []string{"passed", "failed", "errored", "skipped", "canceled", "cancelled"} {
		if !IsTerminalCheckRunStatus(s) {
			t.Fatalf("expected %q to be terminal", s)
		}
	}
	for _, s := range []string{"", "queued", "running"} {
		if IsTerminalCheckRunStatus(s) {
			t.Fatalf("expected %q to be non-terminal", s)
		}
	}
	if got, ok := TerminalCheckResultStatus("passed"); !ok || got != CheckStatusPass {
		t.Fatalf("TerminalCheckResultStatus(passed) = %q, %v", got, ok)
	}
	if got, ok := TerminalCheckResultStatus("errored"); !ok || got != CheckStatusFail {
		t.Fatalf("TerminalCheckResultStatus(errored) = %q, %v", got, ok)
	}
	if _, ok := TerminalCheckResultStatus("canceled"); ok {
		t.Fatal("expected canceled to have no binary check result")
	}
}
