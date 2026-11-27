package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/agenthub"
	"gitslice.io/gitslice/internal/authctx"
	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/objectstore"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/storage/memory"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/protobuf/proto"
)

func TestDispatchOutOfSliceChecksWithoutRunnerRecordsErroredRun(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")

	putRootCheckFileForDispatchTest(t, mem, "root-required", "echo root")
	sliceRef := &corev1.SliceRef{Account: "acme", Slice: "no-runner"}
	mem.PutSliceWithSubmitSettings(sliceRef, []string{"/acme/payment/no-runner"}, "private", 0, []string{"root-required"})

	ref, err := handlers.Repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	cs, patchset := createDispatchTestPatchset(t, ctx, handlers, sliceRef, ref.CommitId, "/acme/payment/no-runner/change.go", "package norunner\nconst V = 1\n", "no runner")

	runs, err := handlers.Check.ListCheckRuns(ctx, &corev1.ListCheckRunsRequest{ChangesetId: cs.Id, PatchsetId: patchset.Id})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs.Runs) != 1 {
		t.Fatalf("ListCheckRuns returned %d runs, want 1: %#v", len(runs.Runs), runs.Runs)
	}
	run := runs.Runs[0]
	if run.CheckName != "root-required" || run.Status != "errored" || run.ExitCode != -1 {
		t.Fatalf("run = %#v, want errored root-required with exit -1", run)
	}
	const wantSummary = "no CI runner configured for this account or slice; set an account CI runner or slice CI daemon"
	if run.Summary != wantSummary {
		t.Fatalf("summary = %q, want %q", run.Summary, wantSummary)
	}
	if _, err := handlers.Changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{
		ChangesetId:               cs.Id,
		ExpectedCurrentPatchsetId: patchset.Id,
	}); err == nil || !strings.Contains(err.Error(), `required check "root-required" is failing`) {
		t.Fatalf("SubmitChangeset error = %v, want failing required check", err)
	}
}

func TestNewPatchsetCancelsOpenOlderCheckRuns(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")

	putRootCheckFileForDispatchTest(t, mem, "root-required", "echo root")
	sliceRef := &corev1.SliceRef{Account: "acme", Slice: "cancel-open"}
	slice := mem.PutSliceWithSubmitSettings(sliceRef, []string{"/acme/payment/cancel-open"}, "private", 0, []string{"root-required"})
	if _, err := mem.Slices.SetCIDaemons(ctx, slice.Id, []string{"daemon-1"}); err != nil {
		t.Fatal(err)
	}
	if err := mem.Agents.ExposeDaemon(ctx, "daemon-1", slice.Id, "test"); err != nil {
		t.Fatal(err)
	}
	conn := handlers.Agent.hub.RegisterDaemon("daemon-1", "daemon-1/legacy", 0)
	defer handlers.Agent.hub.UnregisterDaemon(conn)

	ref, err := handlers.Repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	cs, first := createDispatchTestPatchset(t, ctx, handlers, sliceRef, ref.CommitId, "/acme/payment/cancel-open/first.go", "package cancelopen\nconst First = 1\n", "first")
	firstDispatch := waitForRunChecksMessage(t, conn)
	if firstDispatch.GetPatchsetId() != first.Id || len(firstDispatch.GetChecks()) != 1 {
		t.Fatalf("first RunChecks = %#v, want one check for patchset %s", firstDispatch, first.Id)
	}
	firstRunID := firstDispatch.Checks[0].RunId

	upload, err := handlers.Blob.UploadBlob(ctx, &corev1.UploadBlobRequest{
		Data:  []byte("package cancelopen\nconst Second = 2\n"),
		Slice: sliceRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := handlers.Changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:               cs.Id,
		ExpectedCurrentPatchsetId: first.Id,
		BaseCommitId:              ref.CommitId,
		FileEdits: []*corev1.FileEdit{{
			Op:          "add",
			Path:        "/acme/payment/cancel-open/second.go",
			BlobId:      upload.BlobId,
			ContentHash: upload.ContentHash,
			Mode:        0o100644,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	cancel := waitForCancelCheckMessage(t, conn)
	if cancel.GetRunId() != firstRunID {
		t.Fatalf("CancelCheck run_id = %q, want %q", cancel.GetRunId(), firstRunID)
	}
	secondDispatch := waitForRunChecksMessage(t, conn)
	if secondDispatch.GetPatchsetId() != second.Id || len(secondDispatch.GetChecks()) != 1 {
		t.Fatalf("second RunChecks = %#v, want one check for patchset %s", secondDispatch, second.Id)
	}

	runs, err := handlers.Check.ListCheckRuns(ctx, &corev1.ListCheckRunsRequest{ChangesetId: cs.Id})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]*corev1.CheckRun{}
	for _, run := range runs.Runs {
		byID[run.Id] = run
	}
	if got := byID[firstRunID]; got == nil || got.Status != "canceled" || got.Summary != "superseded by newer patchset" {
		t.Fatalf("first run after second patchset = %#v, want canceled superseded run", got)
	}
}

func TestCheckDispatchSweepRepushesOnlyStaleClaimedRuns(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	ctx := authctx.WithSubjectID(context.Background(), "user_alice")
	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)

	checksFile := []byte(`
version: 1
checks:
  root-required:
    run: "echo root"
`)
	checksHash := objectid.RawContentHash(checksFile)
	mem.PutObject(objectstore.BlobKey(checksHash), checksFile)
	mem.PutCommitWithFiles("commit_with_root_checks", []storage.FileEntry{{
		Path:        "/acme/.gitslice/checks.yaml",
		BlobID:      objectid.BlobID(checksFile),
		ContentHash: checksHash,
		Mode:        0o100644,
		Size:        int64(len(checksFile)),
	}}, []string{"/acme/.gitslice/checks.yaml"})

	sliceRef := &corev1.SliceRef{Account: "acme", Slice: "sweep"}
	sweepSlice := mem.PutSliceWithSubmitSettings(sliceRef, []string{"/acme/payment/sweep"}, "private", 0, []string{"root-required"})
	if err := mem.Agents.ExposeDaemon(ctx, "daemon-1", sweepSlice.Id, "test"); err != nil {
		t.Fatal(err)
	}

	ref, err := handlers.Repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	upload, err := handlers.Blob.UploadBlob(ctx, &corev1.UploadBlobRequest{
		Data:  []byte("package sweep\nconst V = 1\n"),
		Slice: sliceRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := handlers.Changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: sliceRef,
		TargetRef:      storage.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "sweep dispatch",
	})
	if err != nil {
		t.Fatal(err)
	}
	patchset, err := handlers.Changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		FileEdits: []*corev1.FileEdit{{
			Op:          "add",
			Path:        "/acme/payment/sweep/change.go",
			BlobId:      upload.BlobId,
			ContentHash: upload.ContentHash,
			Mode:        0o100644,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	checks := &sweepFakeCheckStore{runs: []*corev1.CheckRun{
		{
			Id:          "run-old",
			ChangesetId: cs.Id,
			PatchsetId:  patchset.Id,
			CheckName:   "root-required",
			DaemonId:    "daemon-1",
			Provenance:  "ci",
			Status:      "queued",
			CreatedAt:   now.Add(-checkDispatchSweepMinAge - time.Second).Format(time.RFC3339),
		},
		{
			Id:          "run-young",
			ChangesetId: cs.Id,
			PatchsetId:  patchset.Id,
			CheckName:   "root-required",
			DaemonId:    "daemon-1",
			Provenance:  "ci",
			Status:      "queued",
			CreatedAt:   now.Add(-checkDispatchSweepMinAge + time.Second).Format(time.RFC3339),
		},
		{
			Id:          "run-running",
			ChangesetId: cs.Id,
			PatchsetId:  patchset.Id,
			CheckName:   "root-required",
			DaemonId:    "daemon-1",
			Provenance:  "ci",
			Status:      "running",
			CreatedAt:   now.Add(-time.Minute).Format(time.RFC3339),
		},
	}}
	handlers.Agent.Checks = checks

	conn := handlers.Agent.hub.RegisterDaemon("daemon-1", "daemon-1/legacy", 0)
	defer handlers.Agent.hub.UnregisterDaemon(conn)
	handlers.Agent.runCheckDispatchSweepOnce(ctx, now)

	select {
	case msg := <-conn.Send:
		runChecks := msg.GetRunChecks()
		if runChecks == nil {
			t.Fatalf("sweep sent %#v, want RunChecks", msg)
		}
		if runChecks.ChangesetId != cs.Id || runChecks.PatchsetId != patchset.Id {
			t.Fatalf("RunChecks target = %s/%s, want %s/%s", runChecks.ChangesetId, runChecks.PatchsetId, cs.Id, patchset.Id)
		}
		if len(runChecks.Checks) != 1 || runChecks.Checks[0].RunId != "run-old" {
			t.Fatalf("RunChecks checks = %#v, want only run-old", runChecks.Checks)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for sweep RunChecks")
	}
	select {
	case extra := <-conn.Send:
		t.Fatalf("unexpected extra sweep message: %#v", extra)
	default:
	}
	if len(checks.listCalls) != 1 || checks.listCalls[0] != "claimed:daemon-1/legacy" {
		t.Fatalf("ListClaimedCheckRuns calls = %#v, want claimed:daemon-1/legacy", checks.listCalls)
	}
}

func TestHandleCheckRunUpdateAcksPermanentConflict(t *testing.T) {
	conn := &agenthub.DaemonConn{DaemonID: "daemon-1", Send: make(chan *corev1.ServerMessage, 1), Done: make(chan struct{})}
	checks := &checkUpdateFakeStore{
		run:       &corev1.CheckRun{Id: "run-1", DaemonId: "daemon-1", Status: "running"},
		updateErr: storage.ErrConflict,
	}
	agent := &AgentService{Checks: checks}

	agent.handleCheckRunUpdate(context.Background(), "daemon-1", conn, &corev1.CheckRunUpdate{
		RunId:     "run-1",
		Status:    "passed",
		ClientSeq: 42,
	})

	msg := <-conn.Send
	ack := msg.GetCheckAck()
	if ack == nil || ack.RunId != "run-1" || ack.AckedClientSeq != 42 {
		t.Fatalf("ack = %#v, want run-1 seq 42", ack)
	}
}

func TestHandleCheckRunUpdateDoesNotAckTransientStatusError(t *testing.T) {
	conn := &agenthub.DaemonConn{DaemonID: "daemon-1", Send: make(chan *corev1.ServerMessage, 1), Done: make(chan struct{})}
	checks := &checkUpdateFakeStore{
		run:       &corev1.CheckRun{Id: "run-1", DaemonId: "daemon-1", Status: "running"},
		updateErr: errors.New("database temporarily unavailable"),
	}
	agent := &AgentService{Checks: checks}

	agent.handleCheckRunUpdate(context.Background(), "daemon-1", conn, &corev1.CheckRunUpdate{
		RunId:     "run-1",
		Status:    "passed",
		ClientSeq: 42,
	})

	select {
	case msg := <-conn.Send:
		t.Fatalf("unexpected ack for transient error: %#v", msg)
	default:
	}
}

type sweepFakeCheckStore struct {
	storage.CheckStore
	runs      []*corev1.CheckRun
	listCalls []string
}

// The process holds every run here (design/32 layer 3); none is left to claim.
func (s *sweepFakeCheckStore) ListClaimedCheckRuns(ctx context.Context, instance string) ([]*corev1.CheckRun, error) {
	s.listCalls = append(s.listCalls, "claimed:"+instance)
	var out []*corev1.CheckRun
	for _, run := range s.runs {
		out = append(out, proto.Clone(run).(*corev1.CheckRun))
	}
	return out, nil
}

func (s *sweepFakeCheckStore) HasPendingCheckRuns(ctx context.Context, daemonID, instance string) (bool, error) {
	for _, run := range s.runs {
		if run.GetDaemonId() == daemonID && run.GetStatus() == "queued" {
			return true, nil
		}
	}
	return false, nil
}

func (s *sweepFakeCheckStore) ClaimCheckRuns(ctx context.Context, daemonID, instance string, limit int, lease time.Duration) ([]*corev1.CheckRun, error) {
	return nil, nil
}

func (s *sweepFakeCheckStore) CreateCheckRun(ctx context.Context, in storage.CheckRunInput) (*corev1.CheckRun, error) {
	return nil, storage.ErrInvalid
}

func (s *sweepFakeCheckStore) GetCheckRun(ctx context.Context, runID string) (*corev1.CheckRun, error) {
	for _, run := range s.runs {
		if run.GetId() == runID {
			return proto.Clone(run).(*corev1.CheckRun), nil
		}
	}
	return nil, storage.ErrNotFound
}

func (s *sweepFakeCheckStore) ListCheckRuns(ctx context.Context, changesetID, patchsetID string) ([]*corev1.CheckRun, error) {
	return nil, storage.ErrInvalid
}

func (s *sweepFakeCheckStore) ListRunsByDaemonStatus(ctx context.Context, daemonID, status string) ([]*corev1.CheckRun, error) {
	s.listCalls = append(s.listCalls, daemonID+":"+status)
	var out []*corev1.CheckRun
	for _, run := range s.runs {
		if run.GetDaemonId() != daemonID || !strings.EqualFold(run.GetStatus(), status) {
			continue
		}
		out = append(out, proto.Clone(run).(*corev1.CheckRun))
	}
	return out, nil
}

func (s *sweepFakeCheckStore) CancelOpenCheckRunsBeforePatchset(ctx context.Context, changesetID, currentPatchsetID string) ([]*corev1.CheckRun, error) {
	return nil, storage.ErrInvalid
}

func (s *sweepFakeCheckStore) UpdateCheckRunStatus(ctx context.Context, runID, status string, exitCode int32, summary string) (*corev1.CheckRun, error) {
	return nil, storage.ErrNotFound
}

func (s *sweepFakeCheckStore) AppendCheckRunLog(ctx context.Context, runID string, seq int64, stream, chunk string) (bool, error) {
	return false, storage.ErrNotFound
}

func (s *sweepFakeCheckStore) ListCheckRunLogs(ctx context.Context, runID string, afterSeq int64) ([]*corev1.CheckRunLog, error) {
	return nil, storage.ErrNotFound
}

func (s *sweepFakeCheckStore) SetCheckRunDetails(ctx context.Context, runID string, details storage.CheckRunDetails) (*corev1.CheckRun, error) {
	return nil, storage.ErrNotFound
}

type checkUpdateFakeStore struct {
	storage.CheckStore
	run       *corev1.CheckRun
	updateErr error
}

func (s *checkUpdateFakeStore) RenewCheckRunClaim(ctx context.Context, runID, instance string, lease time.Duration) error {
	return nil
}

func (s *checkUpdateFakeStore) CreateCheckRun(ctx context.Context, in storage.CheckRunInput) (*corev1.CheckRun, error) {
	return nil, storage.ErrInvalid
}

func (s *checkUpdateFakeStore) GetCheckRun(ctx context.Context, runID string) (*corev1.CheckRun, error) {
	if s.run == nil || s.run.GetId() != runID {
		return nil, storage.ErrNotFound
	}
	return proto.Clone(s.run).(*corev1.CheckRun), nil
}

func (s *checkUpdateFakeStore) ListCheckRuns(ctx context.Context, changesetID, patchsetID string) ([]*corev1.CheckRun, error) {
	return nil, storage.ErrInvalid
}

func (s *checkUpdateFakeStore) ListRunsByDaemonStatus(ctx context.Context, daemonID, status string) ([]*corev1.CheckRun, error) {
	return nil, storage.ErrInvalid
}

func (s *checkUpdateFakeStore) CancelOpenCheckRunsBeforePatchset(ctx context.Context, changesetID, currentPatchsetID string) ([]*corev1.CheckRun, error) {
	return nil, storage.ErrInvalid
}

func (s *checkUpdateFakeStore) UpdateCheckRunStatus(ctx context.Context, runID, status string, exitCode int32, summary string) (*corev1.CheckRun, error) {
	if s.updateErr != nil {
		return nil, s.updateErr
	}
	out := proto.Clone(s.run).(*corev1.CheckRun)
	out.Status = status
	return out, nil
}

func (s *checkUpdateFakeStore) AppendCheckRunLog(ctx context.Context, runID string, seq int64, stream, chunk string) (bool, error) {
	return false, nil
}

func (s *checkUpdateFakeStore) ListCheckRunLogs(ctx context.Context, runID string, afterSeq int64) ([]*corev1.CheckRunLog, error) {
	return nil, nil
}

func putRootCheckFileForDispatchTest(t *testing.T, mem *memory.Stores, checkName, command string) {
	t.Helper()
	checksFile := []byte("version: 1\nchecks:\n  " + checkName + ":\n    run: \"" + command + "\"\n")
	checksHash := objectid.RawContentHash(checksFile)
	mem.PutObject(objectstore.BlobKey(checksHash), checksFile)
	mem.PutCommitWithFiles("commit_with_"+strings.ReplaceAll(checkName, "-", "_")+"_checks", []storage.FileEntry{{
		Path:        "/acme/.gitslice/checks.yaml",
		BlobID:      objectid.BlobID(checksFile),
		ContentHash: checksHash,
		Mode:        0o100644,
		Size:        int64(len(checksFile)),
	}}, []string{"/acme/.gitslice/checks.yaml"})
}

func createDispatchTestPatchset(t *testing.T, ctx context.Context, handlers *Handlers, sliceRef *corev1.SliceRef, baseCommitID, filePath, content, title string) (*corev1.Changeset, *corev1.Patchset) {
	t.Helper()
	upload, err := handlers.Blob.UploadBlob(ctx, &corev1.UploadBlobRequest{
		Data:  []byte(content),
		Slice: sliceRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := handlers.Changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: sliceRef,
		TargetRef:      storage.DefaultTargetRef,
		BaseCommitId:   baseCommitID,
		Title:          title,
	})
	if err != nil {
		t.Fatal(err)
	}
	patchset, err := handlers.Changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: baseCommitID,
		FileEdits: []*corev1.FileEdit{{
			Op:          "add",
			Path:        filePath,
			BlobId:      upload.BlobId,
			ContentHash: upload.ContentHash,
			Mode:        0o100644,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return cs, patchset
}

func waitForRunChecksMessage(t *testing.T, conn *agenthub.DaemonConn) *corev1.RunChecks {
	t.Helper()
	for {
		select {
		case msg := <-conn.Send:
			if run := msg.GetRunChecks(); run != nil {
				return run
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for RunChecks")
		}
	}
}

func waitForCancelCheckMessage(t *testing.T, conn *agenthub.DaemonConn) *corev1.CancelCheckRun {
	t.Helper()
	for {
		select {
		case msg := <-conn.Send:
			if cancel := msg.GetCancelCheck(); cancel != nil {
				return cancel
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for CancelCheck")
		}
	}
}

func (s *checkUpdateFakeStore) SetCheckRunDetails(ctx context.Context, runID string, details storage.CheckRunDetails) (*corev1.CheckRun, error) {
	return nil, storage.ErrNotFound
}

func TestDispatchWithholdsSecretsOnUnapprovedGitsliceEditsAndIgnoresBundledRequiredBypass(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	mem.AddAccountRole("user_bob", "acme", "writer")
	alice := authctx.WithSubjectID(context.Background(), "user_alice")
	bob := authctx.WithSubjectID(context.Background(), "user_bob")

	sliceRef := &corev1.SliceRef{Account: "acme", Slice: "payment"}
	slice := mem.PutSliceWithSubmitSettings(sliceRef, []string{"/acme/payment"}, "private", 0, []string{"payment/unit"})
	if _, err := mem.Slices.CreateSecret(alice, "acme", "API_TOKEN", "super-secret-value", []string{"/acme"}, "user_alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Slices.SetCIDaemons(alice, slice.Id, []string{"daemon-sec"}); err != nil {
		t.Fatal(err)
	}
	if err := mem.Agents.ExposeDaemon(alice, "daemon-sec", slice.Id, "user_alice"); err != nil {
		t.Fatal(err)
	}
	conn := handlers.Agent.hub.RegisterDaemon("daemon-sec", "daemon-sec/1", 0)
	defer handlers.Agent.hub.UnregisterDaemon(conn)

	ref, err := handlers.Repository.GetRef(alice, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	checksYAML := []byte("version: 1\nchecks:\n  unit:\n    run: echo unit\n")
	upload, err := handlers.Blob.UploadBlob(bob, &corev1.UploadBlobRequest{Data: checksYAML, Slice: sliceRef})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := handlers.Changeset.CreateChangeset(bob, &corev1.CreateChangesetRequest{
		AuthoringSlice: sliceRef,
		TargetRef:      storage.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "unapproved checks edit",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Bob also attempts to bundle a fake passing result for required check "payment/unit".
	ps, err := handlers.Changeset.UpdateChangeset(bob, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		FileEdits: []*corev1.FileEdit{{
			Op:          "add",
			Path:        "/acme/payment/.gitslice/checks.yaml",
			BlobId:      upload.BlobId,
			ContentHash: upload.ContentHash,
			Mode:        0o100644,
		}},
		BundledCheckRuns: []*corev1.BundledCheckRun{{
			Name:   "payment/unit",
			Status: "passed",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Required check MUST still be dispatched to the CI daemon despite BundledCheckRuns,
	// AND folder/account secrets MUST be withheld on bob's unapproved .gitslice/ edit!
	dispatched := waitForRunChecksMessage(t, conn)
	if dispatched.GetPatchsetId() != ps.Id || len(dispatched.GetChecks()) != 1 {
		t.Fatalf("RunChecks = %#v, want 1 check for patchset %s", dispatched, ps.Id)
	}
	if _, leaked := dispatched.Checks[0].GetEnv()["API_TOKEN"]; leaked || len(dispatched.Checks[0].GetSecretEnv()) != 0 {
		t.Fatalf("unapproved .gitslice edit received secrets: env=%v secret_env=%v", dispatched.Checks[0].GetEnv(), dispatched.Checks[0].GetSecretEnv())
	}

	// Bundled check must not have satisfied submit requirements while CI run is still open.
	if _, err := handlers.Changeset.ApproveChangeset(alice, &corev1.ApproveChangesetRequest{ChangesetId: cs.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := handlers.Changeset.SubmitChangeset(bob, &corev1.SubmitChangesetRequest{ChangesetId: cs.Id, ExpectedCurrentPatchsetId: ps.Id}); err == nil {
		t.Fatal("SubmitChangeset succeeded before CI daemon reported required check")
	}

	// After admin approval on ps, rerunning the check exposes the secret.
	rerun, err := handlers.Check.RerunCheck(alice, &corev1.RerunCheckRequest{RunId: dispatched.Checks[0].RunId})
	if err != nil {
		t.Fatal(err)
	}
	redispatched := waitForRunChecksMessage(t, conn)
	if len(redispatched.GetChecks()) != 1 || redispatched.Checks[0].RunId != rerun.Id {
		t.Fatalf("rerun RunChecks = %#v, want run %s", redispatched, rerun.Id)
	}
	if got := redispatched.Checks[0].GetEnv()["API_TOKEN"]; got != "super-secret-value" {
		t.Fatalf("approved .gitslice rerun API_TOKEN = %q, want super-secret-value", got)
	}
}
