//go:build load

package load_test

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/gitcompat"
	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/objectstore/filesystem"
	"gitslice.io/gitslice/internal/postgres"
	"gitslice.io/gitslice/internal/treestore"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestLoadHotFilesCreateSubmitProjectionLatency(t *testing.T) {
	ts := startLoadServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	conn := dialLoadGRPC(t, ts.addr)
	defer conn.Close()
	clients := newLoadCoreClients(t, ctx, ts, conn)

	db, err := postgres.Open(ctx, databaseURLWithSearchPath(t, ts.databaseURL, ts.schema))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	objectStore, err := filesystem.New(ts.objectRoot)
	if err != nil {
		t.Fatal(err)
	}
	db.SetTreeStore(treestore.New(objectStore))
	projector, err := gitcompat.NewProjector(gitcompat.ProjectorStores{
		Auth:       db.Auth(),
		Repository: db.Repository(),
		Slices:     db.Slices(),
	}, objectStore, filepath.Join(ts.objectRoot, "projection-cache"))
	if err != nil {
		t.Fatal(err)
	}

	hotFiles := []hotFile{
		{name: "X", path: "/acme/payment/shared/x.go", gitPath: "acme/payment/shared/x.go"},
		{name: "Y", path: "/acme/payment/shared/y.go", gitPath: "acme/payment/shared/y.go"},
		{name: "Z", path: "/acme/payment/shared/z.go", gitPath: "acme/payment/shared/z.go"},
	}
	for _, file := range hotFiles {
		if _, err := submitHotFileOnce(clients, file, "seed", 0, 1); err != nil {
			t.Fatalf("seed %s: %v", file.name, err)
		}
	}

	workers := envInt("GITSLICE_LOAD_HOT_WORKERS", 300)
	operations := envInt("GITSLICE_LOAD_HOT_OPERATIONS", workers)
	maxAttempts := envInt("GITSLICE_LOAD_HOT_MAX_ATTEMPTS", workers)
	projectionWorkers := envInt("GITSLICE_LOAD_PROJECTION_WORKERS", minInt(32, workers))
	if projectionWorkers > operations {
		projectionWorkers = operations
	}

	jobs := make(chan int, operations)
	submitDurations := make(chan time.Duration, operations)
	errs := make(chan error, operations)
	projectionJobs := make(chan projectionJob, operations)
	projectionResults := make(chan projectionResult, operations)
	projectionErrs := make(chan error, operations)

	var projectionWG sync.WaitGroup
	for i := 0; i < projectionWorkers; i++ {
		projectionWG.Add(1)
		go func(worker int) {
			defer projectionWG.Done()
			for job := range projectionJobs {
				result, err := measureProjectionLatency(ctx, db.Changesets(), db.Repository(), projector, clients.subjectID, job)
				if err != nil {
					projectionErrs <- fmt.Errorf("projection worker %d job %d: %w", worker, job.operation, err)
					continue
				}
				projectionResults <- result
			}
		}(i)
	}

	var wg sync.WaitGroup
	start := time.Now()
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for operation := range jobs {
				file := hotFiles[operation%len(hotFiles)]
				begin := time.Now()
				submit, attempts, conflicts, err := submitHotFileWithRetry(clients, file, worker, operation, maxAttempts)
				if err != nil {
					errs <- fmt.Errorf("worker %d operation %d: %w", worker, operation, err)
					continue
				}
				submittedAt := time.Now()
				submitDurations <- submittedAt.Sub(begin)
				projectionJobs <- projectionJob{
					operation:   operation,
					file:        file,
					changesetID: submit.ChangesetID,
					submittedAt: submittedAt,
					attempts:    attempts,
					conflicts:   conflicts,
				}
			}
		}(worker)
	}
	for operation := 0; operation < operations; operation++ {
		jobs <- operation
	}
	close(jobs)
	wg.Wait()
	submitWall := time.Since(start)
	close(projectionJobs)
	projectionWG.Wait()
	close(submitDurations)
	close(errs)
	close(projectionResults)
	close(projectionErrs)

	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for err := range projectionErrs {
		if err != nil {
			t.Fatal(err)
		}
	}

	submitSamples := drainDurations(submitDurations)
	reportDurations(t, "hot_files_create_update_submit_accept", len(submitSamples), submitWall, submitSamples)

	var publishLatency, homeRefresh, otherRefresh, homeVisible, otherVisible []time.Duration
	totalAttempts := 0
	totalConflicts := 0
	for result := range projectionResults {
		publishLatency = append(publishLatency, result.publishLatency)
		homeRefresh = append(homeRefresh, result.homeRefresh)
		otherRefresh = append(otherRefresh, result.otherRefresh)
		homeVisible = append(homeVisible, result.homeVisible)
		otherVisible = append(otherVisible, result.otherVisible)
		totalAttempts += result.attempts
		totalConflicts += result.conflicts
	}
	sortDurations(publishLatency)
	sortDurations(homeRefresh)
	sortDurations(otherRefresh)
	sortDurations(homeVisible)
	sortDurations(otherVisible)
	if len(homeRefresh) != len(submitSamples) {
		t.Fatalf("expected %d projection samples, got %d", len(submitSamples), len(homeRefresh))
	}
	t.Logf("hot_files_contention successes=%d attempts=%d conflicts=%d conflict_rate=%.2f%% projection_workers=%d",
		len(submitSamples),
		totalAttempts,
		totalConflicts,
		100*float64(totalConflicts)/float64(totalAttempts),
		projectionWorkers,
	)
	reportDurations(t, "hot_files_accepted_to_published", len(publishLatency), submitWall, publishLatency)
	reportDurations(t, "hot_files_home_projection_refresh", len(homeRefresh), submitWall, homeRefresh)
	reportDurations(t, "hot_files_other_projection_refresh", len(otherRefresh), submitWall, otherRefresh)
	reportDurations(t, "hot_files_home_submit_to_visible", len(homeVisible), submitWall, homeVisible)
	reportDurations(t, "hot_files_other_submit_to_visible", len(otherVisible), submitWall, otherVisible)

	assertFinalProjectionMatchesNative(t, ctx, db.Repository(), objectStore, projector, clients.subjectID, "payment", hotFiles)
	assertFinalProjectionMatchesNative(t, ctx, db.Repository(), objectStore, projector, clients.subjectID, "backend", hotFiles)
	assertLoadIntegrity(t, ctx, db, objectStore)
}

func TestLoadTwoSliceConcurrentSameFileAppendIntegrity(t *testing.T) {
	ts := startLoadServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	paymentConn := dialLoadGRPC(t, ts.addr)
	defer paymentConn.Close()
	backendConn := dialLoadGRPC(t, ts.addr)
	defer backendConn.Close()
	paymentClient := newLoadCoreClients(t, ctx, ts, paymentConn)
	backendClient := newLoadCoreClients(t, ctx, ts, backendConn)

	db, err := postgres.Open(ctx, databaseURLWithSearchPath(t, ts.databaseURL, ts.schema))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	objectStore, err := filesystem.New(ts.objectRoot)
	if err != nil {
		t.Fatal(err)
	}
	db.SetTreeStore(treestore.New(objectStore))
	projector, err := gitcompat.NewProjector(gitcompat.ProjectorStores{
		Auth:       db.Auth(),
		Repository: db.Repository(),
		Slices:     db.Slices(),
	}, objectStore, filepath.Join(ts.objectRoot, "projection-cache"))
	if err != nil {
		t.Fatal(err)
	}

	const filePath = "/acme/payment/shared/two_slice_append.txt"
	const gitPath = "acme/payment/shared/two_slice_append.txt"
	seedEdit, err := loadUploadFileEdit(paymentClient, filePath, []byte("seed\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, duration, err := submitLoadEdits(paymentClient, &corev1.SliceRef{Account: "acme", Slice: "payment"}, "seed two-slice append file", []*corev1.FileEdit{
		{Op: "mkdir", Path: "/acme/payment/shared"},
		seedEdit,
	}); err != nil {
		t.Fatal(err)
	} else {
		t.Logf("two_slice_append_seed duration=%s", duration)
	}

	operations := envInt("GITSLICE_LOAD_TWO_SLICE_EDITS", 100)
	maxAttempts := envInt("GITSLICE_LOAD_TWO_SLICE_MAX_ATTEMPTS", operations*4)
	writers := []twoSliceAppendWriter{
		{name: "payment-client", slice: &corev1.SliceRef{Account: "acme", Slice: "payment"}, clients: paymentClient},
		{name: "backend-client", slice: &corev1.SliceRef{Account: "acme", Slice: "backend"}, clients: backendClient},
	}

	var wg sync.WaitGroup
	durations := make(chan time.Duration, operations)
	errs := make(chan error, len(writers))
	attemptCh := make(chan int, operations)
	start := time.Now()
	for writerIndex, writer := range writers {
		wg.Add(1)
		go func(writerIndex int, writer twoSliceAppendWriter) {
			defer wg.Done()
			for operation := writerIndex; operation < operations; operation += len(writers) {
				label := twoSliceAppendLine(operation, writer.name, writer.slice.Slice)
				duration, attempts, err := submitTwoSliceAppendWithRetry(writer.clients, writer.slice, filePath, label, maxAttempts)
				if err != nil {
					errs <- fmt.Errorf("%s operation %d: %w", writer.name, operation, err)
					return
				}
				durations <- duration
				attemptCh <- attempts
			}
		}(writerIndex, writer)
	}
	wg.Wait()
	close(durations)
	close(errs)
	close(attemptCh)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	samples := drainDurations(durations)
	reportDurations(t, "two_slice_same_file_append_submit", operations, time.Since(start), samples)
	totalAttempts := 0
	for attempts := range attemptCh {
		totalAttempts += attempts
	}
	t.Logf("two_slice_same_file_append operations=%d attempts=%d conflicts=%d", operations, totalAttempts, totalAttempts-operations)

	ref, err := paymentClient.repo.GetRef(paymentClient.ctx, &corev1.GetRefRequest{RefName: postgres.DefaultTargetRef})
	if err != nil {
		t.Fatal(err)
	}
	read, err := paymentClient.repo.ReadFile(paymentClient.ctx, &corev1.ReadFileRequest{CommitId: ref.CommitId, Path: filePath})
	if err != nil {
		t.Fatal(err)
	}
	assertTwoSliceAppendContent(t, string(read.Data), operations)
	hotFiles := []hotFile{{name: "two-slice-append", path: filePath, gitPath: gitPath}}
	assertFinalProjectionMatchesNative(t, ctx, db.Repository(), objectStore, projector, paymentClient.subjectID, "payment", hotFiles)
	assertFinalProjectionMatchesNative(t, ctx, db.Repository(), objectStore, projector, paymentClient.subjectID, "backend", hotFiles)
	assertLoadIntegrity(t, ctx, db, objectStore)
}

type hotFile struct {
	name    string
	path    string
	gitPath string
}

type hotSubmitResult struct {
	ChangesetID      string
	PendingPublishID string
}

type twoSliceAppendWriter struct {
	name    string
	slice   *corev1.SliceRef
	clients loadCoreClients
}

type projectionJob struct {
	operation   int
	file        hotFile
	changesetID string
	submittedAt time.Time
	attempts    int
	conflicts   int
}

type projectionResult struct {
	publishLatency time.Duration
	homeRefresh    time.Duration
	otherRefresh   time.Duration
	homeVisible    time.Duration
	otherVisible   time.Duration
	attempts       int
	conflicts      int
}

func submitTwoSliceAppendWithRetry(clients loadCoreClients, sliceRef *corev1.SliceRef, filePath, line string, maxAttempts int) (time.Duration, int, error) {
	begin := time.Now()
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		err := submitTwoSliceAppendOnce(clients, sliceRef, filePath, line, attempt)
		if err == nil {
			return time.Since(begin), attempt, nil
		}
		if !isConflictError(err) {
			return 0, attempt, err
		}
		time.Sleep(time.Duration(attempt%11+1) * time.Millisecond)
	}
	return 0, maxAttempts, fmt.Errorf("exhausted %d attempts appending %q through %s/%s", maxAttempts, line, sliceRef.Account, sliceRef.Slice)
}

func submitTwoSliceAppendOnce(clients loadCoreClients, sliceRef *corev1.SliceRef, filePath, line string, attempt int) error {
	ref, err := clients.repo.GetRef(clients.ctx, &corev1.GetRefRequest{RefName: postgres.DefaultTargetRef})
	if err != nil {
		return err
	}
	read, err := clients.repo.ReadFile(clients.ctx, &corev1.ReadFileRequest{CommitId: ref.CommitId, Path: filePath})
	if err != nil {
		return err
	}
	current := string(read.Data)
	if !strings.HasSuffix(current, "\n") {
		current += "\n"
	}
	if strings.Contains(current, line+"\n") {
		return nil
	}
	edit, err := loadUploadFileEdit(clients, filePath, []byte(current+line+"\n"))
	if err != nil {
		return err
	}
	cs, err := clients.changeset.CreateChangeset(clients.ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: sliceRef,
		TargetRef:      postgres.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          fmt.Sprintf("two-slice append %s attempt %d", line, attempt),
	})
	if err != nil {
		return err
	}
	patchset, err := clients.changeset.UpdateChangeset(clients.ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		FileEdits:    []*corev1.FileEdit{edit},
	})
	if err != nil {
		return err
	}
	if _, err := clients.changeset.SubmitChangeset(clients.ctx, &corev1.SubmitChangesetRequest{
		ChangesetId:               cs.Id,
		ExpectedCurrentPatchsetId: patchset.Id,
	}); err != nil {
		if isConflictError(err) {
			_, _ = clients.changeset.AbandonChangeset(clients.ctx, &corev1.AbandonChangesetRequest{ChangesetId: cs.Id})
		}
		return err
	}
	_, err = waitForRPCSubmittedWithTimeout(clients.ctx, clients.changeset, cs.Id, loadPublishTimeout(1))
	return err
}

func twoSliceAppendLine(operation int, clientName, sliceName string) string {
	return fmt.Sprintf("op=%04d client=%s slice=%s", operation, clientName, sliceName)
}

func assertTwoSliceAppendContent(t *testing.T, content string, operations int) {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	if len(lines) != operations+1 {
		t.Fatalf("final file line count = %d, want %d", len(lines), operations+1)
	}
	if lines[0] != "seed" {
		t.Fatalf("final file first line = %q, want seed", lines[0])
	}
	seen := map[string]struct{}{}
	for _, line := range lines[1:] {
		if _, ok := seen[line]; ok {
			t.Fatalf("duplicate final file line %q", line)
		}
		seen[line] = struct{}{}
	}
	for operation := 0; operation < operations; operation++ {
		clientName := "payment-client"
		sliceName := "payment"
		if operation%2 == 1 {
			clientName = "backend-client"
			sliceName = "backend"
		}
		want := twoSliceAppendLine(operation, clientName, sliceName)
		if _, ok := seen[want]; !ok {
			t.Fatalf("missing final file line %q", want)
		}
	}
}

func submitHotFileWithRetry(clients loadCoreClients, file hotFile, worker, operation, maxAttempts int) (*hotSubmitResult, int, int, error) {
	conflicts := 0
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		result, err := submitHotFileOnce(clients, file, fmt.Sprintf("w%03d-op%05d", worker, operation), operation, attempt)
		if err == nil {
			return result, attempt, conflicts, nil
		}
		if !isConflictError(err) {
			return nil, attempt, conflicts, err
		}
		conflicts++
		time.Sleep(time.Duration((worker+operation+attempt)%17+1) * time.Millisecond)
	}
	return nil, maxAttempts, conflicts, fmt.Errorf("exhausted %d attempts on %s", maxAttempts, file.path)
}

func submitHotFileOnce(clients loadCoreClients, file hotFile, label string, operation, attempt int) (*hotSubmitResult, error) {
	ref, err := clients.repo.GetRef(clients.ctx, &corev1.GetRefRequest{RefName: postgres.DefaultTargetRef})
	if err != nil {
		return nil, err
	}
	content := []byte(fmt.Sprintf("package shared\n\nconst %s = %q\nconst Operation = %d\nconst Attempt = %d\n", file.name, label, operation, attempt))
	upload, err := clients.blob.UploadBlob(clients.ctx, &corev1.UploadBlobRequest{
		ContentHash: objectid.RawContentHash(content),
		Data:        content,
		Slice:       &corev1.SliceRef{Account: "acme", Slice: "payment"},
	})
	if err != nil {
		return nil, err
	}
	cs, err := clients.changeset.CreateChangeset(clients.ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
		TargetRef:      postgres.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          fmt.Sprintf("hot file %s %s attempt %d", file.name, label, attempt),
	})
	if err != nil {
		return nil, err
	}
	patchset, err := clients.changeset.UpdateChangeset(clients.ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		FileEdits: []*corev1.FileEdit{{
			Op:          "upsert",
			Path:        file.path,
			BlobId:      upload.BlobId,
			ContentHash: upload.ContentHash,
			Mode:        0o100644,
		}},
	})
	if err != nil {
		return nil, err
	}
	submit, err := clients.changeset.SubmitChangeset(clients.ctx, &corev1.SubmitChangesetRequest{
		ChangesetId:               cs.Id,
		ExpectedCurrentPatchsetId: patchset.Id,
	})
	if err != nil {
		return nil, err
	}
	return &hotSubmitResult{ChangesetID: cs.Id, PendingPublishID: submit.PendingPublishId}, nil
}

func measureProjectionLatency(ctx context.Context, changesets *postgres.ChangesetStore, repository *postgres.RepositoryStore, projector *gitcompat.Projector, subjectID string, job projectionJob) (projectionResult, error) {
	commitID, publishedAt, err := waitForPublishedChangeset(ctx, changesets, job.changesetID)
	if err != nil {
		return projectionResult{}, err
	}
	homeStart := time.Now()
	_, home, err := projector.EnsureProjectedRepo(ctx, subjectID, "acme", "payment")
	if err != nil {
		return projectionResult{}, err
	}
	homeDone := time.Now()
	if err := ensureNativeCommitIncludes(ctx, repository, commitID, home.NativeCommitID); err != nil {
		return projectionResult{}, fmt.Errorf("home projection: %w", err)
	}

	otherStart := time.Now()
	_, other, err := projector.EnsureProjectedRepo(ctx, subjectID, "acme", "backend")
	if err != nil {
		return projectionResult{}, err
	}
	otherDone := time.Now()
	if err := ensureNativeCommitIncludes(ctx, repository, commitID, other.NativeCommitID); err != nil {
		return projectionResult{}, fmt.Errorf("other projection: %w", err)
	}

	return projectionResult{
		publishLatency: publishedAt.Sub(job.submittedAt),
		homeRefresh:    homeDone.Sub(homeStart),
		otherRefresh:   otherDone.Sub(otherStart),
		homeVisible:    homeDone.Sub(job.submittedAt),
		otherVisible:   otherDone.Sub(job.submittedAt),
		attempts:       job.attempts,
		conflicts:      job.conflicts,
	}, nil
}

func waitForPublishedChangeset(ctx context.Context, changesets *postgres.ChangesetStore, changesetID string) (string, time.Time, error) {
	deadline := time.Now().Add(30 * time.Second)
	for {
		cs, err := changesets.Get(ctx, changesetID)
		if err != nil {
			return "", time.Time{}, err
		}
		if cs.Status == "submitted" && cs.CommitId != "" {
			return cs.CommitId, time.Now(), nil
		}
		if time.Now().After(deadline) {
			return "", time.Time{}, fmt.Errorf("changeset %s was not published before timeout, last status %s", changesetID, cs.Status)
		}
		select {
		case <-ctx.Done():
			return "", time.Time{}, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func ensureNativeCommitIncludes(ctx context.Context, repository *postgres.RepositoryStore, ancestorCommitID, projectedCommitID string) error {
	for current := projectedCommitID; current != ""; {
		if current == ancestorCommitID {
			return nil
		}
		commit, err := repository.GetCommit(ctx, current)
		if err != nil {
			return err
		}
		if len(commit.ParentIds) == 0 {
			break
		}
		current = commit.ParentIds[0]
	}
	return fmt.Errorf("projected native commit %s does not include submitted commit %s", projectedCommitID, ancestorCommitID)
}

func assertFinalProjectionMatchesNative(t *testing.T, ctx context.Context, repository *postgres.RepositoryStore, objectStore *filesystem.Store, projector *gitcompat.Projector, subjectID, slice string, files []hotFile) {
	t.Helper()
	ref, err := repository.GetRef(ctx, postgres.DefaultTargetRef)
	if err != nil {
		t.Fatal(err)
	}
	repoPath, projection, err := projector.EnsureProjectedRepo(ctx, subjectID, "acme", slice)
	if err != nil {
		t.Fatal(err)
	}
	if projection.NativeCommitID != ref.CommitId {
		t.Fatalf("%s projection at %s, want latest %s", slice, projection.NativeCommitID, ref.CommitId)
	}
	for _, file := range files {
		entry, err := repository.GetFile(ctx, ref.CommitId, file.path)
		if err != nil {
			t.Fatal(err)
		}
		rc, err := objectStore.Get(ctx, filesystem.BlobKey(entry.ContentHash), 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		want, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		got := gitShowFile(t, repoPath, file.gitPath)
		if string(got) != string(want) {
			t.Fatalf("%s projection mismatch for %s\nwant:\n%s\ngot:\n%s", slice, file.path, string(want), string(got))
		}
	}
}

func assertLoadIntegrity(t *testing.T, ctx context.Context, db *postgres.DB, objectStore *filesystem.Store) {
	t.Helper()
	report, err := db.VerifyIntegrity(ctx, objectStore)
	if err != nil {
		t.Fatalf("integrity verification failed: %v\nreport: %#v", err, report)
	}
	t.Logf("integrity ref_count=%d commit_count=%d blob_count=%d tree_count=%d tree_file_count=%d path_head_count=%d",
		report.RefCount,
		report.CommitCount,
		report.BlobCount,
		report.TreeCount,
		report.TreeFileCount,
		report.PathHeadCount,
	)
}

func gitShowFile(t *testing.T, repoPath, gitPath string) []byte {
	t.Helper()
	cmd := exec.Command("git", "--git-dir", repoPath, "show", "refs/heads/main:"+gitPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git show %s in %s failed: %v\n%s", gitPath, repoPath, err, string(out))
	}
	return out
}

func isConflictError(err error) bool {
	return status.Code(err) == codes.FailedPrecondition || strings.Contains(err.Error(), "conflict")
}
