//go:build load

package load_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/gitslice-io/gitslice/internal/cli"
	"github.com/gitslice-io/gitslice/internal/gitcompat"
	"github.com/gitslice-io/gitslice/internal/objectid"
	"github.com/gitslice-io/gitslice/internal/objectstore/filesystem"
	"github.com/gitslice-io/gitslice/internal/postgres"
	"github.com/gitslice-io/gitslice/internal/treestore"
	"github.com/gitslice-io/gitslice/proto/core/v1"
	"github.com/gitslice-io/gitslice/server"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthv1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestLoadConcurrentDisjointSubmit(t *testing.T) {
	ts := startLoadServer(t)
	workers := envInt("GITSLICE_LOAD_WORKERS", 16)
	home := t.TempDir()
	firstWorkspace := t.TempDir()
	runCLI(t, home, firstWorkspace, "auth", "login", "--server", ts.addr, "--dev-user", "alice")

	var wg sync.WaitGroup
	results := make(chan time.Duration, workers)
	errs := make(chan error, workers)
	start := time.Now()
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			workspace := t.TempDir()
			begin := time.Now()
			if _, err := runCLIResult(home, workspace, "workspace", "init", "acme/payment"); err != nil {
				errs <- err
				return
			}
			writeWorkspaceFile(t, workspace, fmt.Sprintf("load_%03d.go", i), fmt.Sprintf("package payment\nconst Load%d = %d\n", i, i))
			if _, err := runCLIResult(home, workspace, "cs", "create", "--title", fmt.Sprintf("load %03d", i)); err != nil {
				errs <- err
				return
			}
			if _, err := runCLIResult(home, workspace, "cs", "submit"); err != nil {
				errs <- err
				return
			}
			results <- time.Since(begin)
		}(i)
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	durations := drainDurations(results)
	reportDurations(t, "concurrent_disjoint_submit", workers, time.Since(start), durations)
}

func TestLoadSamePathSubmitContention(t *testing.T) {
	ts := startLoadServer(t)
	workers := envInt("GITSLICE_LOAD_WORKERS", 16)
	home := t.TempDir()
	firstWorkspace := t.TempDir()
	runCLI(t, home, firstWorkspace, "auth", "login", "--server", ts.addr, "--dev-user", "alice")

	workspaces := make([]string, workers)
	for i := range workers {
		workspace := t.TempDir()
		workspaces[i] = workspace
		runCLI(t, home, workspace, "workspace", "init", "acme/payment")
		writeWorkspaceFile(t, workspace, "hotspot.go", fmt.Sprintf("package payment\nconst Hotspot = %d\n", i))
		runCLI(t, home, workspace, "cs", "create", "--title", fmt.Sprintf("hotspot %03d", i))
	}

	var wg sync.WaitGroup
	results := make(chan time.Duration, workers)
	errs := make(chan error, workers)
	start := time.Now()
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			begin := time.Now()
			_, err := runCLIResult(home, workspaces[i], "cs", "submit")
			results <- time.Since(begin)
			errs <- err
		}(i)
	}
	wg.Wait()
	close(results)
	close(errs)

	successes := 0
	conflicts := 0
	for err := range errs {
		if err == nil {
			successes++
			continue
		}
		if strings.Contains(err.Error(), "FailedPrecondition") || strings.Contains(err.Error(), "conflict") {
			conflicts++
			continue
		}
		t.Fatalf("unexpected contention error: %v", err)
	}
	if successes != 1 || conflicts != workers-1 {
		t.Fatalf("expected one success and %d conflicts, got successes=%d conflicts=%d", workers-1, successes, conflicts)
	}
	durations := drainDurations(results)
	reportDurations(t, "same_path_submit_contention", workers, time.Since(start), durations)
}

func TestLoadRepeatedStatus(t *testing.T) {
	ts := startLoadServer(t)
	workers := envInt("GITSLICE_LOAD_WORKERS", 16)
	iterations := envInt("GITSLICE_LOAD_STATUS_ITERATIONS", 8)
	home := t.TempDir()
	workspace := t.TempDir()
	runCLI(t, home, workspace, "auth", "login", "--server", ts.addr, "--dev-user", "alice")
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	for i := range 20 {
		writeWorkspaceFile(t, workspace, fmt.Sprintf("status_%03d.go", i), fmt.Sprintf("package payment\nconst Status%d = %d\n", i, i))
	}

	total := workers * iterations
	var wg sync.WaitGroup
	results := make(chan time.Duration, total)
	errs := make(chan error, total)
	start := time.Now()
	for i := range workers {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for j := range iterations {
				begin := time.Now()
				if _, err := runCLIResult(home, workspace, "status", "--format", "json"); err != nil {
					errs <- fmt.Errorf("worker %d iteration %d: %w", worker, j, err)
					return
				}
				results <- time.Since(begin)
			}
		}(i)
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	durations := drainDurations(results)
	reportDurations(t, "repeated_status", total, time.Since(start), durations)
}

func TestLoadHotFilesCreateSubmitProjectionLatency(t *testing.T) {
	ts := startLoadServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	conn := dialLoadGRPC(t, ts.addr)
	defer conn.Close()
	clients := newLoadCoreClients(t, ctx, conn)

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

type loadServer struct {
	addr        string
	cancel      context.CancelFunc
	errCh       chan error
	databaseURL string
	schema      string
	objectRoot  string
}

func startLoadServer(t *testing.T) *loadServer {
	t.Helper()
	databaseURL := os.Getenv("GITSLICE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set GITSLICE_TEST_DATABASE_URL to run load tests")
	}
	schema := "gitslice_load_" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "_")) + "_" + time.Now().Format("150405000000")
	createSchema(t, databaseURL, schema)
	ctx, cancel := context.WithCancel(context.Background())
	ts := &loadServer{
		addr:        freeAddr(t),
		cancel:      cancel,
		errCh:       make(chan error, 1),
		databaseURL: databaseURL,
		schema:      schema,
		objectRoot:  t.TempDir(),
	}
	go func() {
		ts.errCh <- server.Run(ctx, server.Config{
			GRPCAddr:        ts.addr,
			DatabaseURL:     databaseURLWithSearchPath(t, databaseURL, schema),
			ObjectStoreRoot: ts.objectRoot,
			RunMigrations:   true,
		})
	}()
	waitForHealth(t, ts.addr)
	t.Cleanup(func() {
		ts.cancel()
		err := <-ts.errCh
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("server exited with error: %v", err)
		}
		dropSchema(t, databaseURL, schema)
	})
	return ts
}

type loadCoreClients struct {
	ctx       context.Context
	subjectID string
	repo      corev1.RepositoryServiceClient
	blob      corev1.BlobServiceClient
	changeset corev1.ChangesetServiceClient
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

func dialLoadGRPC(t *testing.T, addr string) *grpc.ClientConn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := grpc.DialContext(ctx, addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func newLoadCoreClients(t *testing.T, ctx context.Context, conn *grpc.ClientConn) loadCoreClients {
	t.Helper()
	login, err := corev1.NewFakeAccountServiceClient(conn).Login(ctx, &corev1.LoginRequest{DevUser: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	return loadCoreClients{
		ctx:       metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+login.Token),
		subjectID: login.SubjectId,
		repo:      corev1.NewRepositoryServiceClient(conn),
		blob:      corev1.NewBlobServiceClient(conn),
		changeset: corev1.NewChangesetServiceClient(conn),
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

func runCLI(t *testing.T, home, workspace string, args ...string) string {
	t.Helper()
	out, err := runCLIResult(home, workspace, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func runCLIResult(home, workspace string, args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	r := cli.Runner{Home: home, Dir: workspace, Stdout: &stdout, Stderr: &stderr}
	if err := r.Run(context.Background(), args); err != nil {
		return stdout.String(), fmt.Errorf("gs %s failed: %w\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String(), nil
}

func writeWorkspaceFile(t *testing.T, workspace, rel, content string) {
	t.Helper()
	path := filepath.Join(workspace, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func drainDurations(ch <-chan time.Duration) []time.Duration {
	var out []time.Duration
	for d := range ch {
		out = append(out, d)
	}
	sortDurations(out)
	return out
}

func sortDurations(durations []time.Duration) {
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
}

func reportDurations(t *testing.T, name string, operations int, wall time.Duration, durations []time.Duration) {
	t.Helper()
	if len(durations) == 0 {
		t.Fatalf("%s recorded no durations", name)
	}
	t.Logf("%s operations=%d wall=%s throughput=%.2f/s p50=%s p95=%s p99=%s",
		name,
		operations,
		wall,
		float64(operations)/wall.Seconds(),
		percentile(durations, 0.50),
		percentile(durations, 0.95),
		percentile(durations, 0.99),
	)
}

func percentile(durations []time.Duration, p float64) time.Duration {
	if len(durations) == 0 {
		return 0
	}
	idx := int(float64(len(durations)-1) * p)
	return durations[idx]
}

func envInt(key string, fallback int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func createSchema(t *testing.T, databaseURL, schema string) {
	t.Helper()
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`create schema ` + pqIdentifier(schema)); err != nil {
		t.Fatal(err)
	}
}

func dropSchema(t *testing.T, databaseURL, schema string) {
	t.Helper()
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Logf("open db for cleanup: %v", err)
		return
	}
	defer db.Close()
	if _, err := db.Exec(`drop schema if exists ` + pqIdentifier(schema) + ` cascade`); err != nil {
		t.Logf("drop schema %s: %v", schema, err)
	}
}

func pqIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func databaseURLWithSearchPath(t *testing.T, databaseURL, schema string) string {
	t.Helper()
	parsed, err := url.Parse(databaseURL)
	if err != nil || parsed.Scheme == "" {
		t.Fatalf("GITSLICE_TEST_DATABASE_URL must be a URL connection string")
	}
	q := parsed.Query()
	q.Set("search_path", schema)
	parsed.RawQuery = q.Encode()
	return parsed.String()
}

func freeAddr(t *testing.T) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()
	return lis.Addr().String()
}

func waitForHealth(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		conn, err := grpc.DialContext(ctx, addr, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
		if err == nil {
			client := healthv1.NewHealthClient(conn)
			_, err = client.Check(ctx, &healthv1.HealthCheckRequest{})
			_ = conn.Close()
		}
		cancel()
		if err == nil {
			return
		}
		lastErr = err
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal(fmt.Errorf("server health check did not pass: %w", lastErr))
}
