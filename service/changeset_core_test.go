package service

import (
	"context"
	"sync"
	"testing"

	"gitslice.io/gitslice/internal/analytics"
	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/storage/memory"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

type recordingAnalyticsClient struct {
	mu     sync.Mutex
	events []analytics.Event
}

func (r *recordingAnalyticsClient) Capture(_ context.Context, e analytics.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recordingAnalyticsClient) Identify(context.Context, string, map[string]any) {}
func (r *recordingAnalyticsClient) Close() error                                     { return nil }

func (r *recordingAnalyticsClient) hasEvent(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ev := range r.events {
		if ev.Name == name {
			return true
		}
	}
	return false
}

type countingAgentStore struct {
	storage.AgentStore
	getDaemonCalls      int
	getDaemonsByIDsCall int
}

func (c *countingAgentStore) GetDaemon(ctx context.Context, daemonID string) (*corev1.AgentDaemon, error) {
	c.getDaemonCalls++
	return c.AgentStore.GetDaemon(ctx, daemonID)
}

func (c *countingAgentStore) GetDaemonsByIDs(ctx context.Context, daemonIDs []string) (map[string]*corev1.AgentDaemon, error) {
	c.getDaemonsByIDsCall++
	return c.AgentStore.GetDaemonsByIDs(ctx, daemonIDs)
}

func TestEnrichActorAttributionsBatchesDaemonLookups(t *testing.T) {
	mem := memory.New()
	ctx := context.Background()
	subjectID, err := mem.Auth.EnsureExternalSubject(ctx, storage.ProviderService, "alice_ext", "alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Auth.ChooseUsername(ctx, subjectID, "alice"); err != nil {
		t.Fatal(err)
	}

	d1, err := mem.Agents.RegisterDaemon(ctx, storage.AgentDaemonInput{SubjectID: subjectID, Account: "alice", Name: "d1", Runtime: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	d2, err := mem.Agents.RegisterDaemon(ctx, storage.AgentDaemonInput{SubjectID: subjectID, Account: "alice", Name: "d2", Runtime: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Agents.RenameDaemon(ctx, d2.GetId(), "build-box"); err != nil {
		t.Fatal(err)
	}

	spy := &countingAgentStore{AgentStore: mem.Agents}
	attrs := []*corev1.ActorAttribution{
		{ActingSubjectId: subjectID, DaemonId: d1.GetId()},
		{ActingSubjectId: subjectID, DaemonId: d2.GetId()},
		{ActingSubjectId: subjectID, DaemonId: d1.GetId()},
		{ActingSubjectId: subjectID, DaemonId: "daemon_missing"},
	}
	if err := enrichActorAttributions(ctx, mem.Auth, spy, attrs...); err != nil {
		t.Fatalf("enrichActorAttributions: %v", err)
	}
	if spy.getDaemonCalls != 0 || spy.getDaemonsByIDsCall != 1 {
		t.Fatalf("daemon lookups: GetDaemon=%d GetDaemonsByIDs=%d, want 0 and 1", spy.getDaemonCalls, spy.getDaemonsByIDsCall)
	}
	if attrs[0].GetDaemonName() != "d1" || attrs[1].GetDaemonName() != "build-box" || attrs[2].GetDaemonName() != "d1" || attrs[3].GetDaemonName() != "" {
		t.Fatalf("enriched daemon names = [%q, %q, %q, %q]", attrs[0].GetDaemonName(), attrs[1].GetDaemonName(), attrs[2].GetDaemonName(), attrs[3].GetDaemonName())
	}
}

func TestSharedChangesetCorePropagatesAnalyticsOnStackSubmit(t *testing.T) {
	mem := memory.New()
	tracker := &recordingAnalyticsClient{}
	handlers := New(Stores{
		Auth:       mem.Auth,
		Blobs:      mem.Blobs,
		Changesets: mem.Changesets,
		Repository: mem.Repository,
		Slices:     mem.Slices,
		Agents:     mem.Agents,
		Checks:     mem.Checks,
	}, mem.Objects, tracker)

	alice := asUser(t, handlers, "alice_stack_ext", "alice")
	seedPathInMemoryRepo(t, mem, "cmt_stack_base", "/alice/app/main.go", "package main\n")
	slice, err := handlers.Slice.CreateSlice(alice, &corev1.CreateSliceRequest{
		Ref:           &corev1.SliceRef{Account: "alice", Slice: "app"},
		Visibility:    "private",
		IncludedPaths: []string{"/alice/app"},
	})
	if err != nil {
		t.Fatal(err)
	}
	stack, err := handlers.Stack.CreateStack(alice, &corev1.CreateStackRequest{
		AuthoringSlice: slice.GetRef(),
		BaseCommitId:   "cmt_stack_base",
		Title:          "stack root",
	})
	if err != nil {
		t.Fatal(err)
	}
	entry, err := handlers.Stack.AddStackEntry(alice, &corev1.AddStackEntryRequest{
		StackId: stack.GetId(),
		Title:   "add README",
	})
	if err != nil {
		t.Fatal(err)
	}
	readme := []byte("# app\n")
	blobResp, err := handlers.Blob.UploadBlob(alice, &corev1.UploadBlobRequest{
		Slice:       slice.GetRef(),
		ContentHash: objectid.RawContentHash(readme),
		Data:        readme,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handlers.Changeset.UpdateChangeset(alice, &corev1.UpdateChangesetRequest{
		ChangesetId:               entry.GetId(),
		ExpectedCurrentPatchsetId: entry.GetCurrentPatchsetId(),
		BaseCommitId:              "cmt_stack_base",
		FileEdits: []*corev1.FileEdit{{
			Path:        "/alice/app/README.md",
			Op:          "upsert",
			BlobId:      blobResp.GetBlobId(),
			ContentHash: objectid.RawContentHash(readme),
			Mode:        0o100644,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := handlers.Stack.SubmitStack(alice, &corev1.SubmitStackRequest{StackId: stack.GetId()}); err != nil {
		t.Fatalf("SubmitStack: %v", err)
	}
	if !tracker.hasEvent(analytics.EventChangesetSubmitted) {
		t.Fatalf("SubmitStack did not emit %q via shared changesetCore (events: %+v)", analytics.EventChangesetSubmitted, tracker.events)
	}

	aliceID, _ := authctx.SubjectID(alice)
	if got, err := newSubjectDirectory(mem.Auth).username(context.Background(), aliceID); err != nil || got != "alice" {
		t.Fatalf("subjectDirectory.username = %q, %v", got, err)
	}
}
