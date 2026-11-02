package postgres

import (
	"testing"
	"time"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// Claims on check runs (design/32 layer 3) against the real database: a
// queued run goes to one process, a lapsed claim frees it, and a run whose
// daemon is gone can move to another daemon.
func TestCheckRunClaims(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	base := getTestRef(t, ctx, store)
	blobID, hash := upsertTestBlob(t, ctx, store, "package payment\nconst Claims = true\n")
	ps := createDraftPatchset(t, ctx, store, base.CommitId, "/acme/payment/claims.go", blobID, hash)
	daemon, err := store.Agents().RegisterDaemon(ctx, storage.AgentDaemonInput{SubjectID: "user_alice", Account: "acme", Name: "ci", Runtime: "ci"})
	if err != nil {
		t.Fatal(err)
	}
	checks := store.Checks()
	var runIDs []string
	for _, name := range []string{"a", "b", "c"} {
		run, err := checks.CreateCheckRun(ctx, storage.CheckRunInput{ChangesetID: ps.ChangesetId, PatchsetID: ps.Id, CheckName: name, DaemonID: daemon.Id, Provenance: "ci", Status: "queued"})
		if err != nil {
			t.Fatal(err)
		}
		runIDs = append(runIDs, run.Id)
	}
	p1, p2 := storage.InstanceKey(daemon.Id, "p1"), storage.InstanceKey(daemon.Id, "p2")

	got, err := checks.ClaimCheckRuns(ctx, daemon.Id, p1, 2, time.Minute)
	if err != nil || len(got) != 2 || got[0].Id != runIDs[0] || got[1].Id != runIDs[1] {
		t.Fatalf("p1 claims = %v, %v; want the two oldest", got, err)
	}
	got, err = checks.ClaimCheckRuns(ctx, daemon.Id, p2, 0, time.Minute)
	if err != nil || len(got) != 1 || got[0].Id != runIDs[2] {
		t.Fatalf("p2 claims = %v, %v; want the last one", got, err)
	}
	if claimed, err := checks.ListClaimedCheckRuns(ctx, p1); err != nil || len(claimed) != 2 {
		t.Fatalf("p1 holds %v, %v", claimed, err)
	}
	if pending, err := checks.HasPendingCheckRuns(ctx, daemon.Id, storage.InstanceKey(daemon.Id, "p3")); err != nil || pending {
		t.Fatalf("p3 pending = %v, %v; want nothing it could claim", pending, err)
	}

	// p2 disconnects: its queued run is free again.
	if err := checks.ReleaseCheckRunClaims(ctx, p2); err != nil {
		t.Fatal(err)
	}
	if pending, err := checks.HasPendingCheckRuns(ctx, daemon.Id, storage.InstanceKey(daemon.Id, "p3")); err != nil || !pending {
		t.Fatalf("p3 pending after release = %v, %v", pending, err)
	}

	// p1 starts one run and goes quiet: both claims lapse.
	if _, err := checks.UpdateCheckRunStatus(ctx, runIDs[0], "running", 0, ""); err != nil {
		t.Fatal(err)
	}
	if err := checks.RenewCheckRunClaims(ctx, p1, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	lapsed, err := checks.TakeLapsedCheckRuns(ctx, 10)
	if err != nil || len(lapsed) != 2 {
		t.Fatalf("lapsed = %v, %v; want p1's two runs", lapsed, err)
	}
	if lapsed, err := checks.TakeLapsedCheckRuns(ctx, 10); err != nil || len(lapsed) != 0 {
		t.Fatalf("lapsed again = %v, %v; want them taken once", lapsed, err)
	}

	// The daemon goes away: its unclaimed queued runs are stranded and can move.
	if _, err := store.db.ExecContext(ctx, `update agent_daemons set last_seen_at = now() - interval '1 hour' where id = $1`, daemon.Id); err != nil {
		t.Fatal(err)
	}
	stranded, err := checks.ListStrandedCheckRuns(ctx, 0)
	if err != nil || len(stranded) != 2 {
		t.Fatalf("stranded = %v, %v; want runs b and c", stranded, err)
	}
	moved, err := checks.ReassignCheckRun(ctx, runIDs[2], daemon.Id, "agent_other")
	if err != nil || !moved {
		t.Fatalf("reassign = %v, %v", moved, err)
	}
	run, err := checks.GetCheckRun(ctx, runIDs[2])
	if err != nil || run.DaemonId != "agent_other" {
		t.Fatalf("moved run = %v, %v", run, err)
	}
}

// A slice's pool of CI daemons keeps its order, and ci_daemon_id is its
// first daemon.
func TestSliceCIDaemonPool(t *testing.T) {
	ctx, store := newPostgresTestStore(t)
	slice, err := store.Slices().Resolve(ctx, &corev1.SliceRef{Account: "acme", Slice: "payment"})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := store.Slices().SetCIDaemons(ctx, slice.Id, []string{"agent_b", "agent_a", "agent_b", " "})
	if err != nil {
		t.Fatal(err)
	}
	if updated.CiDaemonId != "agent_b" || len(updated.CiDaemonIds) != 2 || updated.CiDaemonIds[1] != "agent_a" || updated.CiDaemonNames[1] != "agent_a" {
		t.Fatalf("pool = %v / %v / %v", updated.CiDaemonId, updated.CiDaemonIds, updated.CiDaemonNames)
	}
	listed, err := store.Slices().ListByCIDaemon(ctx, []string{"agent_a"})
	if err != nil || len(listed) != 1 || listed[0].Id != slice.Id {
		t.Fatalf("ListByCIDaemon = %v, %v", listed, err)
	}
	cleared, err := store.Slices().SetCIDaemons(ctx, slice.Id, nil)
	if err != nil || cleared.CiDaemonId != "" || len(cleared.CiDaemonIds) != 0 {
		t.Fatalf("cleared = %v, %v", cleared, err)
	}
}
