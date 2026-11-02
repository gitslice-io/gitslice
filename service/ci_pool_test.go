package service

import (
	"context"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/objectstore/filesystem"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/storage/memory"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// Pools of CI daemons and claims on check runs
// (design/32_ci_dispatch_on_cloudflare.md, layer 3).

func putChecksForPoolTest(t *testing.T, mem *memory.Stores, names ...string) {
	t.Helper()
	content := "version: 1\nchecks:\n"
	for _, name := range names {
		content += "  " + name + ":\n    run: \"echo " + name + "\"\n"
	}
	data := []byte(content)
	hash := objectid.RawContentHash(data)
	mem.PutObject(filesystem.BlobKey(hash), data)
	mem.PutCommitWithFiles("commit_with_pool_checks", []storage.FileEntry{{
		Path:        "/acme/.gitslice/checks.yaml",
		BlobID:      objectid.BlobID(data),
		ContentHash: hash,
		Mode:        0o100644,
		Size:        int64(len(data)),
	}}, []string{"/acme/.gitslice/checks.yaml"})
}

// poolSlice makes a slice whose pool is daemons, each exposed to it, and
// whose required checks are checks.
func poolSlice(t *testing.T, ctx context.Context, mem *memory.Stores, name string, daemons []string, checks ...string) (*corev1.SliceRef, *corev1.Slice) {
	t.Helper()
	ref := &corev1.SliceRef{Account: "acme", Slice: name}
	slice := mem.PutSliceWithSubmitSettings(ref, []string{"/acme/payment/" + name}, "private", 0, checks)
	if _, err := mem.Slices.SetCIDaemons(ctx, slice.Id, daemons); err != nil {
		t.Fatal(err)
	}
	for _, daemonID := range daemons {
		if err := mem.Agents.ExposeDaemon(ctx, daemonID, slice.Id, "test"); err != nil {
			t.Fatal(err)
		}
	}
	return ref, slice
}

func noMessage(t *testing.T, conn *daemonConn) {
	t.Helper()
	select {
	case msg := <-conn.send:
		t.Fatalf("unexpected message: %v", msg)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestProcessesOfADaemonShareItsRunsByTheirSlots(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	putChecksForPoolTest(t, mem, "c1", "c2", "c3")
	ref, _ := poolSlice(t, ctx, mem, "slots", []string{"daemon-1"}, "c1", "c2", "c3")
	hub := handlers.Agent.hub
	p1 := hub.registerDaemon("daemon-1", storage.InstanceKey("daemon-1", "p1"), 1)
	p2 := hub.registerDaemon("daemon-1", storage.InstanceKey("daemon-1", "p2"), 1)
	defer hub.unregisterDaemon(p1)
	defer hub.unregisterDaemon(p2)

	base, err := handlers.Repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	createDispatchTestPatchset(t, ctx, handlers, ref, base.CommitId, "/acme/payment/slots/a.go", "package slots\n", "slots")

	first := waitForRunChecksMessage(t, p1)
	second := waitForRunChecksMessage(t, p2)
	if len(first.Checks) != 1 || len(second.Checks) != 1 || first.Checks[0].RunId == second.Checks[0].RunId {
		t.Fatalf("each process should get one run: p1 %v, p2 %v", first.Checks, second.Checks)
	}
	noMessage(t, p1)
	noMessage(t, p2)

	// p1 finishes: its slot takes the third run.
	handlers.Agent.handleCheckRunUpdate(ctx, "daemon-1", p1, &corev1.CheckRunUpdate{RunId: first.Checks[0].RunId, Status: "passed", ClientSeq: 1})
	third := waitForRunChecksMessage(t, p1)
	if len(third.Checks) != 1 || third.Checks[0].RunId == first.Checks[0].RunId || third.Checks[0].RunId == second.Checks[0].RunId {
		t.Fatalf("p1's next run = %v", third.Checks)
	}
	noMessage(t, p2)
}

func TestRunsGoToTheFirstReachableDaemonOfThePool(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	putChecksForPoolTest(t, mem, "c1")
	ref, _ := poolSlice(t, ctx, mem, "failover", []string{"daemon-a", "daemon-b"}, "c1")
	b := handlers.Agent.hub.registerDaemon("daemon-b", storage.InstanceKey("daemon-b", ""), 0)
	defer handlers.Agent.hub.unregisterDaemon(b)

	base, err := handlers.Repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	cs, patchset := createDispatchTestPatchset(t, ctx, handlers, ref, base.CommitId, "/acme/payment/failover/a.go", "package failover\n", "failover")
	sent := waitForRunChecksMessage(t, b)
	runs, err := handlers.Check.ListCheckRuns(ctx, &corev1.ListCheckRunsRequest{ChangesetId: cs.Id, PatchsetId: patchset.Id})
	if err != nil || len(runs.Runs) != 1 || runs.Runs[0].DaemonId != "daemon-b" || sent.Checks[0].RunId != runs.Runs[0].Id {
		t.Fatalf("runs = %v, %v; want one run on daemon-b", runs, err)
	}
}

func TestARunWhoseProcessStopsRespondingIsTriedAgain(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	putChecksForPoolTest(t, mem, "c1")
	ref, _ := poolSlice(t, ctx, mem, "lapse", []string{"daemon-1"}, "c1")
	conn := handlers.Agent.hub.registerDaemon("daemon-1", storage.InstanceKey("daemon-1", "p1"), 0)
	defer handlers.Agent.hub.unregisterDaemon(conn)

	base, err := handlers.Repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	cs, patchset := createDispatchTestPatchset(t, ctx, handlers, ref, base.CommitId, "/acme/payment/lapse/a.go", "package lapse\n", "lapse")
	runID := waitForRunChecksMessage(t, conn).Checks[0].RunId
	for attempt := int32(1); attempt <= storage.MaxCheckRunAttempts; attempt++ {
		handlers.Agent.handleCheckRunUpdate(ctx, "daemon-1", conn, &corev1.CheckRunUpdate{RunId: runID, Status: "running", ClientSeq: 1})
		// The process goes quiet and its claim lapses.
		if err := mem.Checks.RenewCheckRunClaim(ctx, runID, conn.instance, time.Nanosecond); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
		handlers.Agent.dispatcher.recoverCheckRuns(ctx)

		runs, err := handlers.Check.ListCheckRuns(ctx, &corev1.ListCheckRunsRequest{ChangesetId: cs.Id, PatchsetId: patchset.Id})
		if err != nil || len(runs.Runs) != 1 {
			t.Fatalf("runs = %v, %v", runs, err)
		}
		latest := runs.Runs[0]
		if attempt == storage.MaxCheckRunAttempts {
			if latest.Id != runID || latest.Status != "errored" {
				t.Fatalf("after the last attempt: %v, want run %s errored", latest, runID)
			}
			return
		}
		if latest.Attempt != attempt+1 || latest.Status != "queued" {
			t.Fatalf("after attempt %d: %v, want attempt %d queued", attempt, latest, attempt+1)
		}
		cancel := waitForCancelCheckMessage(t, conn)
		if cancel.RunId != runID {
			t.Fatalf("canceled %s, want %s", cancel.RunId, runID)
		}
		next := waitForRunChecksMessage(t, conn)
		if next.Checks[0].RunId != latest.Id {
			t.Fatalf("sent %s, want the new attempt %s", next.Checks[0].RunId, latest.Id)
		}
		runID = latest.Id
	}
}

func TestAQueuedRunMovesOffADaemonThatIsGone(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	putChecksForPoolTest(t, mem, "c1")
	ref, _ := poolSlice(t, ctx, mem, "stranded", []string{"daemon-a", "daemon-b"}, "c1")
	saved := strandedRunAfter
	strandedRunAfter = 0
	defer func() { strandedRunAfter = saved }()

	base, err := handlers.Repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	// Nobody is reachable: the run waits on the first daemon.
	cs, patchset := createDispatchTestPatchset(t, ctx, handlers, ref, base.CommitId, "/acme/payment/stranded/a.go", "package stranded\n", "stranded")
	runs, err := handlers.Check.ListCheckRuns(ctx, &corev1.ListCheckRunsRequest{ChangesetId: cs.Id, PatchsetId: patchset.Id})
	if err != nil || len(runs.Runs) != 1 || runs.Runs[0].DaemonId != "daemon-a" {
		t.Fatalf("runs = %v, %v; want one run waiting on daemon-a", runs, err)
	}

	b := handlers.Agent.hub.registerDaemon("daemon-b", storage.InstanceKey("daemon-b", ""), 0)
	defer handlers.Agent.hub.unregisterDaemon(b)
	handlers.Agent.dispatcher.recoverCheckRuns(ctx)
	sent := waitForRunChecksMessage(t, b)
	if sent.Checks[0].RunId != runs.Runs[0].Id {
		t.Fatalf("daemon-b got %v, want %s", sent.Checks, runs.Runs[0].Id)
	}
	moved, err := mem.Checks.GetCheckRun(ctx, runs.Runs[0].Id)
	if err != nil || moved.DaemonId != "daemon-b" {
		t.Fatalf("run = %v, %v; want it on daemon-b", moved, err)
	}
}

func TestSetSliceCIDaemonAddsAndRemovesPoolMembers(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	alice := authctx.WithSubjectID(context.Background(), "user_alice")
	sliceRef := &corev1.SliceRef{Account: "acme", Slice: "pool"}
	mem.PutSlice(sliceRef, []string{"/acme/pool"}, "private")
	var ids []string
	for _, name := range []string{"ci-1", "ci-2", "ci-3"} {
		daemon, err := mem.Agents.RegisterDaemon(alice, storage.AgentDaemonInput{SubjectID: "user_alice", Account: "acme", Name: name, Runtime: "ci"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, daemon.Id)
	}
	set := func(req *corev1.SetSliceCIDaemonRequest) []string {
		t.Helper()
		req.Slice = sliceRef
		slice, err := handlers.Slice.SetSliceCIDaemon(alice, req)
		if err != nil {
			t.Fatal(err)
		}
		return slice.CiDaemonIds
	}
	equal := func(got, want []string) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}
	if got := set(&corev1.SetSliceCIDaemonRequest{DaemonId: ids[0]}); !equal(got, ids[:1]) {
		t.Fatalf("set = %v", got)
	}
	if got := set(&corev1.SetSliceCIDaemonRequest{DaemonId: ids[1], Add: true}); !equal(got, ids[:2]) {
		t.Fatalf("add = %v", got)
	}
	if got := set(&corev1.SetSliceCIDaemonRequest{DaemonId: ids[2], Add: true}); !equal(got, ids) {
		t.Fatalf("add = %v", got)
	}
	if got := set(&corev1.SetSliceCIDaemonRequest{DaemonId: ids[0], Remove: true}); !equal(got, ids[1:]) {
		t.Fatalf("remove = %v", got)
	}
	slice, err := mem.Slices.Resolve(alice, sliceRef)
	if err != nil || slice.CiDaemonId != ids[1] || len(slice.CiDaemonNames) != 2 || slice.CiDaemonNames[0] != "ci-2" {
		t.Fatalf("slice = %v, %v; want ci-2 first", slice, err)
	}
	if got := set(&corev1.SetSliceCIDaemonRequest{}); len(got) != 0 {
		t.Fatalf("clear = %v", got)
	}
}
