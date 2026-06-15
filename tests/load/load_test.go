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
	"unicode"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/gitslice-io/gitslice/internal/cli"
	"github.com/gitslice-io/gitslice/internal/gitcompat"
	"github.com/gitslice-io/gitslice/internal/objectid"
	"github.com/gitslice-io/gitslice/internal/objectstore/filesystem"
	"github.com/gitslice-io/gitslice/internal/postgres"
	"github.com/gitslice-io/gitslice/internal/rpclimits"
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

// Load latency budgets are intentionally lenient MVP regression guards. By
// default each reported scenario must keep p95 <= 5000ms. Override all
// scenarios with GITSLICE_LOAD_BUDGET_P95_MS, or one scenario with
// GITSLICE_LOAD_BUDGET_<SCENARIO>_P95_MS where SCENARIO is uppercased and
// non-alphanumeric characters are replaced with underscores.
const defaultLoadBudgetP95MS = 5000

// The hot-files scenarios intentionally drive hundreds of workers into
// admission and publish contention on overlapping paths, so their baseline
// p95 is queueing-dominated (measured ~20s at 300 workers). Their default
// budgets guard order-of-magnitude regressions rather than absolute latency;
// the per-scenario env overrides above still apply.
var defaultScenarioBudgetP95 = map[string]time.Duration{
	"hot_files_create_update_submit_accept": 60 * time.Second,
	"hot_files_accepted_to_published":       60 * time.Second,
	"hot_files_home_projection_refresh":     60 * time.Second,
	"hot_files_other_projection_refresh":    60 * time.Second,
	"hot_files_home_submit_to_visible":      60 * time.Second,
	"hot_files_other_submit_to_visible":     60 * time.Second,
}

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

func TestLoadTwoSliceConcurrentSameFileAppendIntegrity(t *testing.T) {
	ts := startLoadServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	paymentConn := dialLoadGRPC(t, ts.addr)
	defer paymentConn.Close()
	backendConn := dialLoadGRPC(t, ts.addr)
	defer backendConn.Close()
	paymentClient := newLoadCoreClients(t, ctx, paymentConn)
	backendClient := newLoadCoreClients(t, ctx, backendConn)

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

func TestLoadRPCMultiUserPersonalAccounts(t *testing.T) {
	ts := startLoadServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	conn := dialLoadGRPC(t, ts.addr)
	defer conn.Close()

	userCount := envInt("GITSLICE_LOAD_RPC_USERS", 12)
	opsPerUser := envInt("GITSLICE_LOAD_RPC_OPS_PER_USER", 4)

	var signupWG sync.WaitGroup
	signupDurations := make(chan time.Duration, userCount)
	userCh := make(chan rpcLoadUser, userCount)
	errs := make(chan error, userCount)
	signupStart := time.Now()
	for i := range userCount {
		signupWG.Add(1)
		go func(i int) {
			defer signupWG.Done()
			begin := time.Now()
			user, err := signupRPCLoadUser(ctx, conn, i)
			signupDurations <- time.Since(begin)
			if err != nil {
				errs <- fmt.Errorf("signup user %d: %w", i, err)
				return
			}
			userCh <- user
		}(i)
	}
	signupWG.Wait()
	close(signupDurations)
	close(userCh)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	users := make([]rpcLoadUser, 0, userCount)
	for user := range userCh {
		users = append(users, user)
	}
	sort.Slice(users, func(i, j int) bool { return users[i].index < users[j].index })
	if len(users) != userCount {
		t.Fatalf("signed up %d users, want %d", len(users), userCount)
	}
	reportDurations(t, "rpc_multi_user_signup", userCount, time.Since(signupStart), drainDurations(signupDurations))

	var seedWG sync.WaitGroup
	seedDurations := make(chan time.Duration, userCount)
	errs = make(chan error, userCount)
	seedStart := time.Now()
	for _, user := range users {
		seedWG.Add(1)
		go func(user rpcLoadUser) {
			defer seedWG.Done()
			begin := time.Now()
			if err := seedRPCUserHome(user); err != nil {
				errs <- fmt.Errorf("%s seed home: %w", user.account, err)
				return
			}
			seedDurations <- time.Since(begin)
		}(user)
	}
	seedWG.Wait()
	close(seedDurations)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	reportDurations(t, "rpc_multi_user_seed_home_and_slice", userCount, time.Since(seedStart), drainDurations(seedDurations))

	if len(users) > 1 {
		for i, user := range users {
			other := users[(i+1)%len(users)]
			if err := assertRPCUserIsolation(user, other.account); err != nil {
				t.Fatal(err)
			}
		}
	}

	totalOps := userCount * opsPerUser
	var opsWG sync.WaitGroup
	opDurations := make(chan time.Duration, totalOps)
	errs = make(chan error, totalOps)
	opsStart := time.Now()
	for _, user := range users {
		opsWG.Add(1)
		go func(user rpcLoadUser) {
			defer opsWG.Done()
			for op := range opsPerUser {
				duration, err := runRPCUserOperation(user, op)
				if err != nil {
					errs <- fmt.Errorf("%s operation %d: %w", user.account, op, err)
					return
				}
				opDurations <- duration
			}
		}(user)
	}
	opsWG.Wait()
	close(opDurations)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	reportDurations(t, "rpc_multi_user_custom_slice_ops", totalOps, time.Since(opsStart), drainDurations(opDurations))

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
	assertLoadIntegrity(t, ctx, db, objectStore)
}

func TestLoadLargeDirectoryPaginationAndProjection(t *testing.T) {
	ts := startLoadServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	conn := dialLoadGRPC(t, ts.addr)
	defer conn.Close()
	clients := newLoadCoreClients(t, ctx, conn)

	fileCount := envInt("GITSLICE_LOAD_LARGE_DIR_FILES", 1500)
	emptyDirCount := envInt("GITSLICE_LOAD_LARGE_DIR_EMPTY_DIRS", 250)
	pageSize := envInt("GITSLICE_LOAD_LARGE_DIR_PAGE_SIZE", 137)
	root := "/acme/payment/large-dir"
	sharedFile, err := loadUploadFileEdit(clients, root+"/seed.txt", []byte("large directory shared content\n"))
	if err != nil {
		t.Fatal(err)
	}
	edits := make([]*corev1.FileEdit, 0, 1+emptyDirCount+fileCount)
	edits = append(edits, &corev1.FileEdit{Op: "mkdir", Path: root})
	for i := range emptyDirCount {
		edits = append(edits, &corev1.FileEdit{Op: "mkdir", Path: fmt.Sprintf("%s/empty_%05d", root, i)})
	}
	for i := range fileCount {
		edit := *sharedFile
		edit.Path = fmt.Sprintf("%s/file_%05d.txt", root, i)
		edits = append(edits, &edit)
	}
	commitID, duration, err := submitLoadEdits(clients, &corev1.SliceRef{Account: "acme", Slice: "payment"}, "large directory seed", edits)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("large_directory_submit entries=%d files=%d empty_dirs=%d duration=%s", len(edits), fileCount, emptyDirCount, duration)

	expected := fileCount + emptyDirCount
	globalEntries, globalPages, err := collectLoadDirectoryPages(clients.ctx, clients.repo, &corev1.ListDirectoryRequest{
		CommitId: commitID,
		Path:     root,
		PageSize: int32(pageSize),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(globalEntries) != expected {
		t.Fatalf("global directory entries = %d, want %d", len(globalEntries), expected)
	}
	assertLoadDirectoryPages(t, "global_large_directory", globalEntries, globalPages, pageSize)

	sliceEntries, slicePages, err := collectLoadDirectoryPages(clients.ctx, clients.repo, &corev1.ListDirectoryRequest{
		CommitId: commitID,
		Path:     root,
		PageSize: int32(pageSize),
		Slice:    &corev1.SliceRef{Account: "acme", Slice: "payment"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sliceEntries) != expected {
		t.Fatalf("slice directory entries = %d, want %d", len(sliceEntries), expected)
	}
	assertLoadDirectoryPages(t, "slice_large_directory", sliceEntries, slicePages, pageSize)
	if err := requireEntry(sliceEntries, fmt.Sprintf("%s/empty_%05d", root, emptyDirCount-1), corev1.EntryKind_ENTRY_KIND_DIRECTORY); err != nil {
		t.Fatal(err)
	}
	if err := requireEntry(sliceEntries, fmt.Sprintf("%s/file_%05d.txt", root, fileCount-1), corev1.EntryKind_ENTRY_KIND_FILE); err != nil {
		t.Fatal(err)
	}

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
	assertLoadIntegrity(t, ctx, db, objectStore)
}

func TestLoadLargeDirectoryRenameIntegrity(t *testing.T) {
	ts := startLoadServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	conn := dialLoadGRPC(t, ts.addr)
	defer conn.Close()
	clients := newLoadCoreClients(t, ctx, conn)

	fileCount := envInt("GITSLICE_LOAD_RENAME_DIR_FILES", 500)
	emptyDirCount := envInt("GITSLICE_LOAD_RENAME_DIR_EMPTY_DIRS", 50)
	sourceRoot := "/acme/payment/rename-src"
	movedRoot := "/acme/payment/rename-moved"
	sharedFile, err := loadUploadFileEdit(clients, sourceRoot+"/seed.txt", []byte("large rename shared content\n"))
	if err != nil {
		t.Fatal(err)
	}
	edits := make([]*corev1.FileEdit, 0, 2+emptyDirCount+fileCount)
	edits = append(edits,
		&corev1.FileEdit{Op: "mkdir", Path: sourceRoot},
		&corev1.FileEdit{Op: "mkdir", Path: sourceRoot + "/nested"},
	)
	for i := range emptyDirCount {
		edits = append(edits, &corev1.FileEdit{Op: "mkdir", Path: fmt.Sprintf("%s/nested/empty_%05d", sourceRoot, i)})
	}
	for i := range fileCount {
		edit := *sharedFile
		edit.Path = fmt.Sprintf("%s/nested/file_%05d.txt", sourceRoot, i)
		edits = append(edits, &edit)
	}
	if _, duration, err := submitLoadEdits(clients, &corev1.SliceRef{Account: "acme", Slice: "payment"}, "large rename seed", edits); err != nil {
		t.Fatal(err)
	} else {
		t.Logf("large_directory_rename_seed entries=%d files=%d empty_dirs=%d duration=%s", len(edits), fileCount, emptyDirCount, duration)
	}

	commitID, duration, err := submitLoadEdits(clients, &corev1.SliceRef{Account: "acme", Slice: "payment"}, "large directory rename", []*corev1.FileEdit{{
		Op:      "rename",
		OldPath: sourceRoot,
		Path:    movedRoot,
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("large_directory_rename_publish files=%d empty_dirs=%d duration=%s", fileCount, emptyDirCount, duration)

	movedEntries, movedPages, err := collectLoadDirectoryPages(clients.ctx, clients.repo, &corev1.ListDirectoryRequest{
		CommitId: commitID,
		Path:     movedRoot + "/nested",
		PageSize: 101,
		Slice:    &corev1.SliceRef{Account: "acme", Slice: "payment"},
	})
	if err != nil {
		t.Fatal(err)
	}
	expected := fileCount + emptyDirCount
	if len(movedEntries) != expected {
		t.Fatalf("moved directory entries = %d, want %d", len(movedEntries), expected)
	}
	assertLoadDirectoryPages(t, "renamed_large_directory", movedEntries, movedPages, 101)
	if err := requireEntry(movedEntries, fmt.Sprintf("%s/nested/file_%05d.txt", movedRoot, fileCount-1), corev1.EntryKind_ENTRY_KIND_FILE); err != nil {
		t.Fatal(err)
	}

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
	assertLoadIntegrity(t, ctx, db, objectStore)
}

func TestLoadLargeFileUploadCommitAndRead(t *testing.T) {
	ts := startLoadServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	conn := dialLoadGRPC(t, ts.addr)
	defer conn.Close()
	clients := newLoadCoreClients(t, ctx, conn)

	size := envInt("GITSLICE_LOAD_LARGE_FILE_BYTES", 8*1024*1024)
	data := deterministicLoadBytes(size)
	path := "/acme/payment/large-file/blob.bin"
	edit, err := loadUploadFileEdit(clients, path, data)
	if err != nil {
		t.Fatal(err)
	}
	commitID, duration, err := submitLoadEdits(clients, &corev1.SliceRef{Account: "acme", Slice: "payment"}, "large file upload", []*corev1.FileEdit{
		{Op: "mkdir", Path: "/acme/payment/large-file"},
		edit,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("large_file_submit bytes=%d duration=%s", size, duration)

	fullBegin := time.Now()
	full, err := clients.repo.ReadFile(clients.ctx, &corev1.ReadFileRequest{CommitId: commitID, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(full.Data, data) {
		t.Fatalf("full read bytes mismatch: got %d bytes want %d", len(full.Data), len(data))
	}
	t.Logf("large_file_full_read bytes=%d duration=%s", len(full.Data), time.Since(fullBegin))

	offset := int64(size / 2)
	length := int64(minInt(64*1024, size/2))
	partialBegin := time.Now()
	partial, err := clients.repo.ReadFile(clients.ctx, &corev1.ReadFileRequest{CommitId: commitID, Path: path, Offset: offset, Length: length})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(partial.Data, data[int(offset):int(offset+length)]) {
		t.Fatalf("partial read bytes mismatch: got %d bytes want %d", len(partial.Data), length)
	}
	t.Logf("large_file_partial_read bytes=%d offset=%d duration=%s", len(partial.Data), offset, time.Since(partialBegin))
}

func TestLoadManySequentialCommitsAndHistoryPagination(t *testing.T) {
	ts := startLoadServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	conn := dialLoadGRPC(t, ts.addr)
	defer conn.Close()
	clients := newLoadCoreClients(t, ctx, conn)

	commitCount := envInt("GITSLICE_LOAD_MANY_COMMITS", 180)
	pageSize := envInt("GITSLICE_LOAD_MANY_COMMITS_PAGE_SIZE", 37)
	path := "/acme/payment/history/many.txt"
	var commitIDs []string
	durations := make(chan time.Duration, commitCount)
	start := time.Now()
	for i := range commitCount {
		edit, err := loadUploadFileEdit(clients, path, []byte(fmt.Sprintf("version=%05d\n", i)))
		if err != nil {
			t.Fatal(err)
		}
		edits := []*corev1.FileEdit{edit}
		if i == 0 {
			edits = []*corev1.FileEdit{{Op: "mkdir", Path: "/acme/payment/history"}, edit}
		}
		commitID, duration, err := submitLoadEdits(clients, &corev1.SliceRef{Account: "acme", Slice: "payment"}, fmt.Sprintf("history commit %05d", i), edits)
		if err != nil {
			t.Fatalf("commit %d: %v", i, err)
		}
		commitIDs = append(commitIDs, commitID)
		durations <- duration
	}
	close(durations)
	reportDurations(t, "many_sequential_commits_submit", commitCount, time.Since(start), drainDurations(durations))

	historyStart := time.Now()
	commits, pages, err := collectLoadCommitPages(clients.ctx, clients.repo, &corev1.ListCommitsRequest{
		RefName: postgres.DefaultTargetRef,
		Limit:   int32(pageSize),
		Path:    path,
		Slice:   &corev1.SliceRef{Account: "acme", Slice: "payment"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("many_commits_history pages=%d commits=%d duration=%s", pages, len(commits), time.Since(historyStart))
	if len(commits) != commitCount {
		t.Fatalf("history commits = %d, want %d", len(commits), commitCount)
	}
	seen := map[string]struct{}{}
	for i, commit := range commits {
		want := commitIDs[len(commitIDs)-1-i]
		if commit.Id != want {
			t.Fatalf("history commit %d = %s, want %s", i, commit.Id, want)
		}
		if _, ok := seen[commit.Id]; ok {
			t.Fatalf("duplicate commit in history: %s", commit.Id)
		}
		seen[commit.Id] = struct{}{}
	}
	if pages <= 1 {
		t.Fatalf("history pagination returned %d pages, want multiple", pages)
	}
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
			DevMode:         true,
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

type rpcLoadUser struct {
	index     int
	username  string
	account   string
	subjectID string
	ctx       context.Context
	repo      corev1.RepositoryServiceClient
	blob      corev1.BlobServiceClient
	changeset corev1.ChangesetServiceClient
	slices    corev1.SliceServiceClient
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

func dialLoadGRPC(t *testing.T, addr string) *grpc.ClientConn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := grpc.DialContext(ctx, addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(rpclimits.MaxUnaryMessageBytes),
			grpc.MaxCallSendMsgSize(rpclimits.MaxUnaryMessageBytes),
		),
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

func loadUploadFileEdit(clients loadCoreClients, p string, content []byte) (*corev1.FileEdit, error) {
	upload, err := clients.blob.UploadBlob(clients.ctx, &corev1.UploadBlobRequest{
		ContentHash: objectid.RawContentHash(content),
		Data:        content,
		Slice:       &corev1.SliceRef{Account: "acme", Slice: "payment"},
	})
	if err != nil {
		return nil, err
	}
	return &corev1.FileEdit{
		Op:          "upsert",
		Path:        p,
		BlobId:      upload.BlobId,
		ContentHash: upload.ContentHash,
		Mode:        0o100644,
	}, nil
}

func submitLoadEdits(clients loadCoreClients, sliceRef *corev1.SliceRef, title string, edits []*corev1.FileEdit) (string, time.Duration, error) {
	begin := time.Now()
	ref, err := clients.repo.GetRef(clients.ctx, &corev1.GetRefRequest{RefName: postgres.DefaultTargetRef})
	if err != nil {
		return "", 0, err
	}
	cs, err := clients.changeset.CreateChangeset(clients.ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: sliceRef,
		TargetRef:      postgres.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          title,
	})
	if err != nil {
		return "", 0, err
	}
	patchset, err := clients.changeset.UpdateChangeset(clients.ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		FileEdits:    edits,
	})
	if err != nil {
		return "", 0, err
	}
	if _, err := clients.changeset.SubmitChangeset(clients.ctx, &corev1.SubmitChangesetRequest{
		ChangesetId:               cs.Id,
		ExpectedCurrentPatchsetId: patchset.Id,
	}); err != nil {
		return "", 0, err
	}
	commitID, err := waitForRPCSubmittedWithTimeout(clients.ctx, clients.changeset, cs.Id, loadPublishTimeout(len(edits)))
	if err != nil {
		return "", 0, err
	}
	return commitID, time.Since(begin), nil
}

type loadDirectoryPage struct {
	count      int
	cursor     string
	nextCursor string
}

func collectLoadDirectoryPages(ctx context.Context, repo corev1.RepositoryServiceClient, req *corev1.ListDirectoryRequest) ([]*corev1.TreeEntry, []loadDirectoryPage, error) {
	pageReq := *req
	var entries []*corev1.TreeEntry
	var pages []loadDirectoryPage
	seen := map[string]struct{}{}
	for {
		page, err := repo.ListDirectory(ctx, &pageReq)
		if err != nil {
			return nil, nil, err
		}
		pages = append(pages, loadDirectoryPage{count: len(page.Entries), cursor: pageReq.Cursor, nextCursor: page.NextCursor})
		for _, entry := range page.Entries {
			if _, ok := seen[entry.Path]; ok {
				return nil, nil, fmt.Errorf("duplicate directory entry across pages: %s", entry.Path)
			}
			seen[entry.Path] = struct{}{}
			entries = append(entries, entry)
		}
		if page.NextCursor == "" {
			return entries, pages, nil
		}
		if page.NextCursor == pageReq.Cursor {
			return nil, nil, fmt.Errorf("directory cursor did not advance for %s", req.Path)
		}
		pageReq.Cursor = page.NextCursor
	}
}

func assertLoadDirectoryPages(t *testing.T, label string, entries []*corev1.TreeEntry, pages []loadDirectoryPage, pageSize int) {
	t.Helper()
	if len(pages) == 0 {
		t.Fatalf("%s returned no pages", label)
	}
	for i, page := range pages {
		if page.count > pageSize {
			t.Fatalf("%s page %d count = %d, want <= %d", label, i, page.count, pageSize)
		}
		if i < len(pages)-1 && page.nextCursor == "" {
			t.Fatalf("%s page %d had empty next cursor before final page", label, i)
		}
	}
	t.Logf("%s entries=%d pages=%d page_size=%d", label, len(entries), len(pages), pageSize)
}

func deterministicLoadBytes(size int) []byte {
	data := make([]byte, size)
	for i := range data {
		data[i] = byte((i*31 + 17) % 251)
	}
	return data
}

func collectLoadCommitPages(ctx context.Context, repo corev1.RepositoryServiceClient, req *corev1.ListCommitsRequest) ([]*corev1.Commit, int, error) {
	pageReq := *req
	var commits []*corev1.Commit
	seen := map[string]struct{}{}
	pages := 0
	for {
		page, err := repo.ListCommits(ctx, &pageReq)
		if err != nil {
			return nil, 0, err
		}
		pages++
		for _, commit := range page.Commits {
			if _, ok := seen[commit.Id]; ok {
				return nil, 0, fmt.Errorf("duplicate commit across pages: %s", commit.Id)
			}
			seen[commit.Id] = struct{}{}
			commits = append(commits, commit)
		}
		if page.NextPageToken == "" {
			return commits, pages, nil
		}
		if page.NextPageToken == pageReq.PageToken {
			return nil, 0, fmt.Errorf("commit page token did not advance")
		}
		pageReq.PageToken = page.NextPageToken
	}
}

func signupRPCLoadUser(ctx context.Context, conn *grpc.ClientConn, index int) (rpcLoadUser, error) {
	username := fmt.Sprintf("load-rpc-%02d", index)
	signup, err := corev1.NewFakeAccountServiceClient(conn).ApproveSignup(ctx, &corev1.ApproveSignupRequest{
		Username:    username,
		CallbackUrl: "http://127.0.0.1:1/callback",
		State:       fmt.Sprintf("load-state-%02d", index),
	})
	if err != nil {
		return rpcLoadUser{}, err
	}
	authCtx := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+signup.Token)
	return rpcLoadUser{
		index:     index,
		username:  username,
		account:   username,
		subjectID: signup.SubjectId,
		ctx:       authCtx,
		repo:      corev1.NewRepositoryServiceClient(conn),
		blob:      corev1.NewBlobServiceClient(conn),
		changeset: corev1.NewChangesetServiceClient(conn),
		slices:    corev1.NewSliceServiceClient(conn),
	}, nil
}

func seedRPCUserHome(user rpcLoadUser) error {
	home, err := user.slices.ResolveSlice(user.ctx, &corev1.ResolveSliceRequest{
		Ref: &corev1.SliceRef{Account: user.account, Slice: "home"},
	})
	if err != nil {
		return err
	}
	if len(home.Definition.IncludedPaths) != 1 || home.Definition.IncludedPaths[0] != "/"+user.account {
		return fmt.Errorf("home slice includes %v, want /%s", home.Definition.IncludedPaths, user.account)
	}

	projectPath := "/" + user.account + "/project"
	emptyPath := projectPath + "/empty-seed"
	readme, err := rpcUploadFileEdit(user, projectPath+"/readme.txt", []byte("hello from "+user.username+"\n"))
	if err != nil {
		return err
	}
	commitID, _, err := rpcSubmitEdits(user, "home", "seed personal home", []*corev1.FileEdit{
		{Op: "mkdir", Path: projectPath},
		{Op: "mkdir", Path: emptyPath},
		readme,
	})
	if err != nil {
		return err
	}
	accountList, err := user.repo.ListDirectory(user.ctx, &corev1.ListDirectoryRequest{
		CommitId: commitID,
		Path:     "/" + user.account,
		Slice:    &corev1.SliceRef{Account: user.account, Slice: "home"},
	})
	if err != nil {
		return err
	}
	if err := requireEntry(accountList.Entries, projectPath, corev1.EntryKind_ENTRY_KIND_DIRECTORY); err != nil {
		return err
	}
	if _, err := user.slices.CreateSlice(user.ctx, &corev1.CreateSliceRequest{
		Ref:           &corev1.SliceRef{Account: user.account, Slice: "project"},
		IncludedPaths: []string{projectPath},
		Visibility:    "private",
	}); err != nil {
		return err
	}
	ref, err := user.repo.GetRef(user.ctx, &corev1.GetRefRequest{RefName: postgres.DefaultTargetRef})
	if err != nil {
		return err
	}
	projectList, err := user.repo.ListDirectory(user.ctx, &corev1.ListDirectoryRequest{
		CommitId: ref.CommitId,
		Path:     projectPath,
		Slice:    &corev1.SliceRef{Account: user.account, Slice: "project"},
	})
	if err != nil {
		return err
	}
	if err := requireEntry(projectList.Entries, emptyPath, corev1.EntryKind_ENTRY_KIND_DIRECTORY); err != nil {
		return fmt.Errorf("custom slice did not preserve empty directory: %w", err)
	}
	return requireEntry(projectList.Entries, projectPath+"/readme.txt", corev1.EntryKind_ENTRY_KIND_FILE)
}

func assertRPCUserIsolation(user rpcLoadUser, otherAccount string) error {
	_, err := user.slices.ResolveSlice(user.ctx, &corev1.ResolveSliceRequest{
		Ref: &corev1.SliceRef{Account: otherAccount, Slice: "home"},
	})
	if status.Code(err) != codes.PermissionDenied {
		return fmt.Errorf("resolve other home returned %v, want PermissionDenied", err)
	}
	_, _, err = rpcSubmitEdits(user, "home", "outside home should fail", []*corev1.FileEdit{
		{Op: "mkdir", Path: "/" + otherAccount + "/illegal"},
	})
	if status.Code(err) != codes.FailedPrecondition {
		return fmt.Errorf("outside-home mutation returned %v, want FailedPrecondition", err)
	}
	return nil
}

func runRPCUserOperation(user rpcLoadUser, operation int) (time.Duration, error) {
	projectPath := "/" + user.account + "/project"
	srcPath := projectPath + "/src"
	filePath := fmt.Sprintf("%s/user_%02d_op_%02d.txt", srcPath, user.index, operation)
	content := []byte(fmt.Sprintf("user=%s\noperation=%d\n", user.username, operation))
	fileEdit, err := rpcUploadFileEdit(user, filePath, content)
	if err != nil {
		return 0, err
	}
	commitID, duration, err := rpcSubmitEdits(user, "project", fmt.Sprintf("rpc operation %02d", operation), []*corev1.FileEdit{
		{Op: "mkdir", Path: srcPath},
		fileEdit,
	})
	if err != nil {
		return 0, err
	}
	read, err := user.repo.ReadFile(user.ctx, &corev1.ReadFileRequest{CommitId: commitID, Path: filePath})
	if err != nil {
		return 0, err
	}
	if string(read.Data) != string(content) {
		return 0, fmt.Errorf("read %s got %q, want %q", filePath, string(read.Data), string(content))
	}
	ref, err := user.repo.GetRef(user.ctx, &corev1.GetRefRequest{RefName: postgres.DefaultTargetRef})
	if err != nil {
		return 0, err
	}
	list, err := user.repo.ListDirectory(user.ctx, &corev1.ListDirectoryRequest{
		CommitId: ref.CommitId,
		Path:     srcPath,
		Slice:    &corev1.SliceRef{Account: user.account, Slice: "project"},
	})
	if err != nil {
		return 0, err
	}
	if err := requireEntry(list.Entries, filePath, corev1.EntryKind_ENTRY_KIND_FILE); err != nil {
		return 0, err
	}
	// Path-filtered history reads the commit_changed_paths derived index,
	// which the outbox worker fills shortly after publish (design/06).
	// Poll briefly like a real client instead of assuming read-your-writes.
	deadline := time.Now().Add(5 * time.Second)
	lastCount := 0
	for {
		history, err := user.repo.ListCommits(user.ctx, &corev1.ListCommitsRequest{
			RefName: postgres.DefaultTargetRef,
			Limit:   10,
			Path:    filePath,
			Slice:   &corev1.SliceRef{Account: user.account, Slice: "project"},
		})
		if err != nil {
			return 0, err
		}
		for _, commit := range history.Commits {
			if commit.Id == commitID {
				return duration, nil
			}
		}
		lastCount = len(history.Commits)
		if time.Now().After(deadline) {
			break
		}
		select {
		case <-user.ctx.Done():
			return 0, user.ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	return 0, fmt.Errorf("commit %s missing from ListCommits(%s), got %d commits", commitID, filePath, lastCount)
}

func rpcUploadFileEdit(user rpcLoadUser, p string, content []byte) (*corev1.FileEdit, error) {
	upload, err := user.blob.UploadBlob(user.ctx, &corev1.UploadBlobRequest{
		ContentHash: objectid.RawContentHash(content),
		Data:        content,
		// Authorize against the home slice: it covers the whole account
		// root and exists from signup, while the project slice is only
		// created later in the seed flow.
		Slice: &corev1.SliceRef{Account: user.account, Slice: "home"},
	})
	if err != nil {
		return nil, err
	}
	return &corev1.FileEdit{
		Op:          "upsert",
		Path:        p,
		BlobId:      upload.BlobId,
		ContentHash: upload.ContentHash,
		Mode:        0o100644,
	}, nil
}

func rpcSubmitEdits(user rpcLoadUser, sliceSlug, title string, edits []*corev1.FileEdit) (string, time.Duration, error) {
	begin := time.Now()
	ref, err := user.repo.GetRef(user.ctx, &corev1.GetRefRequest{RefName: postgres.DefaultTargetRef})
	if err != nil {
		return "", 0, err
	}
	cs, err := user.changeset.CreateChangeset(user.ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: user.account, Slice: sliceSlug},
		TargetRef:      postgres.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          title,
	})
	if err != nil {
		return "", 0, err
	}
	patchset, err := user.changeset.UpdateChangeset(user.ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		FileEdits:    edits,
	})
	if err != nil {
		return "", 0, err
	}
	if _, err := user.changeset.SubmitChangeset(user.ctx, &corev1.SubmitChangesetRequest{
		ChangesetId:               cs.Id,
		ExpectedCurrentPatchsetId: patchset.Id,
	}); err != nil {
		return "", 0, err
	}
	commitID, err := waitForRPCSubmittedWithTimeout(user.ctx, user.changeset, cs.Id, loadPublishTimeout(len(edits)))
	if err != nil {
		return "", 0, err
	}
	return commitID, time.Since(begin), nil
}

func waitForRPCSubmitted(ctx context.Context, client corev1.ChangesetServiceClient, changesetID string) (string, error) {
	return waitForRPCSubmittedWithTimeout(ctx, client, changesetID, 30*time.Second)
}

func waitForRPCSubmittedWithTimeout(ctx context.Context, client corev1.ChangesetServiceClient, changesetID string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for {
		cs, err := client.GetChangeset(ctx, &corev1.GetChangesetRequest{ChangesetId: changesetID})
		if err != nil {
			return "", err
		}
		if cs.Status == "submitted" && cs.CommitId != "" {
			return cs.CommitId, nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("changeset %s was not published before timeout, last status %s", changesetID, cs.Status)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func loadPublishTimeout(editCount int) time.Duration {
	timeout := 2 * time.Minute
	if editCount > 0 {
		timeout += time.Duration(editCount/100) * time.Second
	}
	if timeout > 15*time.Minute {
		return 15 * time.Minute
	}
	return timeout
}

func requireEntry(entries []*corev1.TreeEntry, wantPath string, wantKind corev1.EntryKind) error {
	for _, entry := range entries {
		if entry.Path == wantPath && entry.Kind == wantKind {
			return nil
		}
	}
	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		got = append(got, fmt.Sprintf("%s:%s", entry.Path, entry.Kind))
	}
	sort.Strings(got)
	return fmt.Errorf("missing %s:%s from [%s]", wantPath, wantKind, strings.Join(got, ", "))
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
	p50 := percentile(durations, 0.50)
	p95 := percentile(durations, 0.95)
	p99 := percentile(durations, 0.99)
	p95Budget := loadBudgetP95(name)
	t.Logf("%s operations=%d wall=%s throughput=%.2f/s p50=%s p95=%s p99=%s",
		name,
		operations,
		wall,
		float64(operations)/wall.Seconds(),
		p50,
		p95,
		p99,
	)
	if p95 > p95Budget {
		t.Fatalf("%s p95=%s exceeds budget %s; override with GITSLICE_LOAD_BUDGET_P95_MS or %s", name, p95, p95Budget, loadBudgetEnvKey(name))
	}
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

func loadBudgetP95(name string) time.Duration {
	if value, ok := envMillis(loadBudgetEnvKey(name)); ok {
		return value
	}
	if value, ok := envMillis("GITSLICE_LOAD_BUDGET_P95_MS"); ok {
		return value
	}
	if value, ok := defaultScenarioBudgetP95[name]; ok {
		return value
	}
	return time.Duration(defaultLoadBudgetP95MS) * time.Millisecond
}

func loadBudgetEnvKey(name string) string {
	var b strings.Builder
	b.WriteString("GITSLICE_LOAD_BUDGET_")
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(unicode.ToUpper(r))
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	b.WriteString("_P95_MS")
	return b.String()
}

func envMillis(key string) (time.Duration, bool) {
	raw := os.Getenv(key)
	if raw == "" {
		return 0, false
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, false
	}
	return time.Duration(value) * time.Millisecond, true
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
