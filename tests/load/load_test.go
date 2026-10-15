//go:build load

package load_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"gitslice.io/gitslice/internal/postgres"
	corev1 "gitslice.io/gitslice/proto/core/v1"
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
	loginLoadCLI(t, ts, home, firstWorkspace)

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

func TestLoadDisjointPublishThroughput(t *testing.T) {
	ts := startLoadServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	conn := dialLoadGRPC(t, ts.addr)
	defer conn.Close()
	clients := newLoadCoreClients(t, ctx, ts, conn)
	if _, _, err := submitLoadEdits(clients, &corev1.SliceRef{Account: "acme", Slice: "payment"}, "seed disjoint publish benchmark", []*corev1.FileEdit{
		{Op: "mkdir", Path: "/acme/payment/bench"},
	}); err != nil {
		t.Fatal(err)
	}

	workers := envInt("GITSLICE_LOAD_DISJOINT_WORKERS", 64)
	prepared := make([]disjointPublish, workers)
	prepareErrs := make(chan error, workers)
	var prepareWG sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		prepareWG.Add(1)
		go func(worker int) {
			defer prepareWG.Done()
			publish, err := prepareDisjointPublish(clients, worker)
			if err != nil {
				prepareErrs <- fmt.Errorf("prepare worker %d: %w", worker, err)
				return
			}
			prepared[worker] = publish
		}(worker)
	}
	prepareWG.Wait()
	close(prepareErrs)
	for err := range prepareErrs {
		if err != nil {
			t.Fatal(err)
		}
	}

	durations := make(chan time.Duration, workers)
	submitErrs := make(chan error, workers)
	startGate := make(chan struct{})
	var submitWG sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		submitWG.Add(1)
		go func(worker int) {
			defer submitWG.Done()
			<-startGate
			duration, err := submitDisjointPublish(clients, prepared[worker])
			if err != nil {
				submitErrs <- fmt.Errorf("submit worker %d: %w", worker, err)
				return
			}
			durations <- duration
		}(worker)
	}
	start := time.Now()
	close(startGate)
	submitWG.Wait()
	wall := time.Since(start)
	close(durations)
	close(submitErrs)
	for err := range submitErrs {
		if err != nil {
			t.Fatal(err)
		}
	}
	reportDurationsNoBudget(t, "disjoint_publish_throughput", workers, wall, drainDurations(durations), envInt("GITSLICE_LOAD_OBJECT_STORE_LATENCY_MS", 0))
}

func TestLoadSamePathSubmitContention(t *testing.T) {
	ts := startLoadServer(t)
	workers := envInt("GITSLICE_LOAD_WORKERS", 16)
	home := t.TempDir()
	firstWorkspace := t.TempDir()
	loginLoadCLI(t, ts, home, firstWorkspace)

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
	loginLoadCLI(t, ts, home, workspace)
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

type disjointPublish struct {
	changesetID string
	patchsetID  string
}

func prepareDisjointPublish(clients loadCoreClients, worker int) (disjointPublish, error) {
	ref, err := clients.repo.GetRef(clients.ctx, &corev1.GetRefRequest{RefName: postgres.DefaultTargetRef})
	if err != nil {
		return disjointPublish{}, err
	}
	path := fmt.Sprintf("/acme/payment/bench/w%03d.go", worker)
	edit, err := loadUploadFileEdit(clients, path, []byte(fmt.Sprintf("package bench\n\nconst Worker%d = %d\n", worker, worker)))
	if err != nil {
		return disjointPublish{}, err
	}
	cs, err := clients.changeset.CreateChangeset(clients.ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
		TargetRef:      postgres.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          fmt.Sprintf("disjoint publish worker %03d", worker),
	})
	if err != nil {
		return disjointPublish{}, err
	}
	patchset, err := clients.changeset.UpdateChangeset(clients.ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		FileEdits:    []*corev1.FileEdit{edit},
	})
	if err != nil {
		return disjointPublish{}, err
	}
	return disjointPublish{changesetID: cs.Id, patchsetID: patchset.Id}, nil
}

func submitDisjointPublish(clients loadCoreClients, publish disjointPublish) (time.Duration, error) {
	begin := time.Now()
	if _, err := clients.changeset.SubmitChangeset(clients.ctx, &corev1.SubmitChangesetRequest{
		ChangesetId:               publish.changesetID,
		ExpectedCurrentPatchsetId: publish.patchsetID,
	}); err != nil {
		return 0, err
	}
	if _, err := waitForRPCSubmittedWithTimeout(clients.ctx, clients.changeset, publish.changesetID, loadPublishTimeout(1)); err != nil {
		return 0, err
	}
	return time.Since(begin), nil
}
