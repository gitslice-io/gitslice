package cli

import (
	"context"
	"path/filepath"
	"testing"
)

func TestSessionCachesServiceClients(t *testing.T) {
	cfg := UserConfig{
		ServerAddr: "localhost:50051",
		Token:      "tok_test_123",
		SubjectID:  "user_alice",
	}
	s := newSession(Runner{}, cfg, nil, nil)
	if s.Config().Token != "tok_test_123" {
		t.Fatalf("Config().Token = %q, want tok_test_123", s.Config().Token)
	}
	if s.Auth() == nil || s.Auth() != s.Auth() {
		t.Fatal("expected cached AuthServiceClient instance")
	}
	if s.Slice() == nil || s.Slice() != s.Slice() {
		t.Fatal("expected cached SliceServiceClient instance")
	}
	if s.Changeset() == nil || s.Changeset() != s.Changeset() {
		t.Fatal("expected cached ChangesetServiceClient instance")
	}
	if s.ChangesetStack() == nil || s.ChangesetStack() != s.ChangesetStack() {
		t.Fatal("expected cached ChangesetStackServiceClient instance")
	}
	if s.Repository() == nil || s.Repository() != s.Repository() {
		t.Fatal("expected cached RepositoryServiceClient instance")
	}
	if s.Blob() == nil || s.Blob() != s.Blob() {
		t.Fatal("expected cached BlobServiceClient instance")
	}
	if s.Agent() == nil || s.Agent() != s.Agent() {
		t.Fatal("expected cached AgentServiceClient instance")
	}
	if s.Check() == nil || s.Check() != s.Check() {
		t.Fatal("expected cached CheckServiceClient instance")
	}
	if s.Secret() == nil || s.Secret() != s.Secret() {
		t.Fatal("expected cached SecretServiceClient instance")
	}
	if s.Trigger() == nil || s.Trigger() != s.Trigger() {
		t.Fatal("expected cached TriggerServiceClient instance")
	}
	if s.Feedback() == nil || s.Feedback() != s.Feedback() {
		t.Fatal("expected cached FeedbackServiceClient instance")
	}
}

func TestSessionResolveSliceRefAndWorkspaceFallback(t *testing.T) {
	home := t.TempDir()
	outsideDir := t.TempDir()
	rOutside := Runner{Home: home, Dir: outsideDir}
	sOutside := newSession(rOutside, UserConfig{SubjectID: "user_alice"}, nil, context.Background())

	ref, err := sOutside.ResolveSliceRef("acme:Payment_API", false)
	if err != nil {
		t.Fatalf("ResolveSliceRef explicit failed: %v", err)
	}
	if ref.GetAccount() != "acme" || ref.GetSlice() != "payment-api" {
		t.Fatalf("ResolveSliceRef explicit = %#v, want acme:payment-api", ref)
	}

	if _, err := sOutside.ResolveSliceRef("", true); !isUserErrorCode(err, "slice_required") {
		t.Fatalf("ResolveSliceRef outside workspace = %v, want slice_required", err)
	}
	if _, err := sOutside.ResolveSliceRefWithOptions("", SliceResolveOptions{
		FallbackToWorkspace: true,
		MissingCode:         "missing_slice",
		MissingMessage:      "changeset list needs a slice outside a workspace",
	}); !isUserErrorCode(err, "missing_slice") {
		t.Fatalf("ResolveSliceRefWithOptions outside workspace = %v, want missing_slice", err)
	}

	wsDir := t.TempDir()
	rWS := Runner{Home: home, Dir: wsDir}
	if err := writeJSONFile(filepath.Join(wsDir, ".gs", "slice.json"), WorkspaceConfig{Account: "acme", Slice: "core"}, 0o644); err != nil {
		t.Fatal(err)
	}
	sWS := newSession(rWS, UserConfig{SubjectID: "user_alice"}, nil, context.Background())
	wsRef, err := sWS.ResolveSliceRef("", true)
	if err != nil {
		t.Fatalf("ResolveSliceRef in workspace failed: %v", err)
	}
	if wsRef.GetAccount() != "acme" || wsRef.GetSlice() != "core" {
		t.Fatalf("ResolveSliceRef in workspace = %#v, want acme:core", wsRef)
	}
}

func TestSessionResolveChangesetTarget(t *testing.T) {
	home := t.TempDir()
	wsDir := t.TempDir()
	r := Runner{Home: home, Dir: wsDir}
	if err := r.writeUserConfig(UserConfig{ServerAddr: "localhost:50051", Token: "tok_abc", SubjectID: "user_alice"}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(wsDir, ".gs", "slice.json"), WorkspaceConfig{Account: "acme", Slice: "core"}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.writeWorkspaceState(WorkspaceState{BaseCommitID: "sha256:1111"}); err != nil {
		t.Fatal(err)
	}

	if _, err := r.resolveChangesetTarget(""); !isUserErrorCode(err, "no_current_changeset") {
		t.Fatalf("resolveChangesetTarget empty state = %v, want no_current_changeset", err)
	}

	if err := r.writeWorkspaceState(WorkspaceState{
		BaseCommitID:       "sha256:1111",
		CurrentChangesetID: "cs_abcdef1234567890",
		CurrentPatchsetID:  "ps_1",
	}); err != nil {
		t.Fatal(err)
	}

	cur, err := r.resolveChangesetTarget("")
	if err != nil {
		t.Fatalf("resolveChangesetTarget current failed: %v", err)
	}
	if !cur.HasWorkspace || !cur.UsingWorkspaceCurrent || cur.ChangesetID != "cs_abcdef1234567890" {
		t.Fatalf("unexpected current target: %#v", cur)
	}

	shortMatch, err := r.resolveChangesetTarget("abcdef1234")
	if err != nil {
		t.Fatalf("resolveChangesetTarget short handle failed: %v", err)
	}
	if !shortMatch.HasWorkspace || !shortMatch.UsingWorkspaceCurrent || shortMatch.ChangesetID != "abcdef1234" {
		t.Fatalf("unexpected short-handle target: %#v", shortMatch)
	}

	other, err := r.resolveChangesetTarget("cs_9999999999999999")
	if err != nil {
		t.Fatalf("resolveChangesetTarget explicit other failed: %v", err)
	}
	if !other.HasWorkspace || other.UsingWorkspaceCurrent || other.ChangesetID != "cs_9999999999999999" {
		t.Fatalf("unexpected explicit other target: %#v", other)
	}
}
