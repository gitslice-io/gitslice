package postgres

import (
	"testing"

	"gitslice.io/gitslice/internal/storage"
)

func TestGetDaemonsByIDsPostgres(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	auth := store.Auth()
	agents := store.Agents()

	subjectID, err := auth.EnsureExternalSubject(ctx, storage.ProviderClerk, "clerk_batch_daemon", "batch@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.ChooseUsername(ctx, subjectID, "batchuser"); err != nil {
		t.Fatal(err)
	}

	d1, err := agents.RegisterDaemon(ctx, storage.AgentDaemonInput{
		SubjectID: subjectID,
		Account:   "batchuser",
		Name:      "runner-1",
		Runtime:   "codex",
		Version:   "1.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	d2, err := agents.RegisterDaemon(ctx, storage.AgentDaemonInput{
		SubjectID: subjectID,
		Account:   "batchuser",
		Name:      "runner-2",
		Runtime:   "codex",
		Version:   "1.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agents.RenameDaemon(ctx, d2.GetId(), "friendly-runner"); err != nil {
		t.Fatal(err)
	}

	empty, err := agents.GetDaemonsByIDs(ctx, []string{"", "   "})
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty GetDaemonsByIDs = %v, %v", empty, err)
	}

	got, err := agents.GetDaemonsByIDs(ctx, []string{d1.GetId(), d2.GetId(), d1.GetId(), "daemon_does_not_exist"})
	if err != nil {
		t.Fatalf("GetDaemonsByIDs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2: %+v", len(got), got)
	}
	if got[d1.GetId()].GetName() != "runner-1" || got[d2.GetId()].GetDisplayName() != "friendly-runner" {
		t.Fatalf("unexpected daemons map: %+v", got)
	}
}
