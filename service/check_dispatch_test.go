package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gitslice-io/gitslice/internal/authctx"
	"github.com/gitslice-io/gitslice/internal/objectid"
	"github.com/gitslice-io/gitslice/internal/objectstore/filesystem"
	"github.com/gitslice-io/gitslice/internal/storage"
	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
	"google.golang.org/protobuf/proto"
)

func TestCheckDispatchSweepRepushesOnlyStaleQueuedRuns(t *testing.T) {
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
	mem.PutObject(filesystem.BlobKey(checksHash), checksFile)
	mem.PutCommitWithFiles("commit_with_root_checks", []storage.FileEntry{{
		Path:        "/acme/.gitslice/checks.yaml",
		BlobID:      objectid.BlobID(checksFile),
		ContentHash: checksHash,
		Mode:        0o100644,
		Size:        int64(len(checksFile)),
	}}, []string{"/acme/.gitslice/checks.yaml"})

	sliceRef := &corev1.SliceRef{Account: "acme", Slice: "sweep"}
	mem.PutSliceWithSubmitSettings(sliceRef, []string{"/acme/payment/sweep"}, "private", 0, []string{"root-required"})

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

	conn := handlers.Agent.hub.registerDaemon("daemon-1")
	defer handlers.Agent.hub.unregisterDaemon(conn)
	handlers.Agent.runCheckDispatchSweepOnce(ctx, now)

	select {
	case msg := <-conn.send:
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
	case extra := <-conn.send:
		t.Fatalf("unexpected extra sweep message: %#v", extra)
	default:
	}
	if len(checks.listCalls) != 1 || checks.listCalls[0] != "daemon-1:queued" {
		t.Fatalf("ListRunsByDaemonStatus calls = %#v, want daemon-1:queued", checks.listCalls)
	}
}

type sweepFakeCheckStore struct {
	runs      []*corev1.CheckRun
	listCalls []string
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

func (s *sweepFakeCheckStore) UpdateCheckRunStatus(ctx context.Context, runID, status string, exitCode int32, summary string) (*corev1.CheckRun, error) {
	return nil, storage.ErrNotFound
}

func (s *sweepFakeCheckStore) AppendCheckRunLog(ctx context.Context, runID string, seq int64, stream, chunk string) (bool, error) {
	return false, storage.ErrNotFound
}

func (s *sweepFakeCheckStore) ListCheckRunLogs(ctx context.Context, runID string, afterSeq int64) ([]*corev1.CheckRunLog, error) {
	return nil, storage.ErrNotFound
}
