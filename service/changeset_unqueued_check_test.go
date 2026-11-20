package service

import (
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestExplainUnqueuedCheck(t *testing.T) {
	mem, h := newMemoryHandlers()
	alice := asUser(t, h, "owner_alice", "alice")
	if _, err := h.Auth.CreateOrganization(alice, &corev1.CreateOrganizationRequest{Slug: "chkorg"}); err != nil {
		t.Fatal(err)
	}
	home := &corev1.SliceRef{Account: "chkorg", Slice: "home"}
	base := commitSliceFile(t, alice, h, mem, home, "/chkorg/a.txt", "a\n")
	cs, ps := proposeEdits(t, alice, h, home, base, "edit", uploadEdit(t, alice, h, home, "/chkorg/a.txt", "b\n"))
	cs.CurrentPatchsetId = ps.Id
	svc := h.Changeset

	refusal := status.Error(codes.FailedPrecondition, `required check "web/build" has no result for current patchset`)
	got := svc.explainUnqueuedCheck(alice, cs, refusal)
	if status.Code(got) != codes.FailedPrecondition || !strings.Contains(status.Convert(got).Message(), "nothing has queued it") {
		t.Fatalf("no run of the check: %v, want the explanation", got)
	}
	if !strings.HasPrefix(status.Convert(got).Message(), `required check "web/build" has no result`) {
		t.Fatalf("explanation must keep the original message first (gs parses it): %v", got)
	}

	// Another refusal passes through unchanged.
	other := status.Error(codes.FailedPrecondition, `required check "web/build" is failing for current patchset`)
	if got := svc.explainUnqueuedCheck(alice, cs, other); got != other {
		t.Fatalf("failing check: %v, want unchanged", got)
	}

	// Once a run exists, the plain message stays: it is on its way.
	if _, err := mem.Checks.CreateCheckRun(alice, storage.CheckRunInput{ChangesetID: cs.Id, PatchsetID: ps.Id, CheckName: "web/build", Provenance: "ci", Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	if got := svc.explainUnqueuedCheck(alice, cs, refusal); got != refusal {
		t.Fatalf("queued check: %v, want unchanged", got)
	}
}
