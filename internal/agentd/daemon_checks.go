package agentd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"gitslice.io/gitslice/internal/checkexec"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func (d *Daemon) HandleRunChecks(req *corev1.RunChecks) {
	if req == nil {
		return
	}
	baseCtx := d.checkBaseContext()
	serverAddr, err := ResolveRunChecksServerAddr(d.Host.ServerAddr, req.GetServerAddr())
	if err != nil {
		d.emitSetupFailureForRunChecks(req, err)
		return
	}
	conn, err := d.Host.dial(baseCtx, serverAddr)
	if err != nil {
		d.emitSetupFailureForRunChecks(req, fmt.Errorf("dial repository service: %w", err))
		return
	}
	defer conn.Close()

	repo := RepoClient(corev1.NewRepositoryServiceClient(conn))
	if account := strings.TrimSpace(req.GetSlice().GetAccount()); account != "" {
		repo = CheckTreeRepoClient{Inner: repo, Account: account}
	}
	specs := req.GetChecks()
	workers := len(specs)
	if d.CheckSlots != nil && cap(d.CheckSlots) < workers {
		workers = cap(d.CheckSlots)
	}
	next := make(chan *corev1.CheckRunSpec)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for spec := range next {
				d.handleCheckRunSpec(req, repo, spec)
			}
		}()
	}
	for _, spec := range specs {
		next <- spec
	}
	close(next)
	wg.Wait()
}

func (d *Daemon) handleCheckRunSpec(req *corev1.RunChecks, repo RepoClient, spec *corev1.CheckRunSpec) {
	if spec == nil || strings.TrimSpace(spec.GetRunId()) == "" {
		return
	}
	baseCtx := d.checkBaseContext()
	runCtx, cancel := context.WithCancel(baseCtx)
	if !d.startCheckRun(spec.GetRunId(), cancel) {
		cancel()
		return
	}
	defer d.clearCheckRun(spec.GetRunId(), cancel)
	defer cancel()

	masker := CheckSecretMasker(spec)
	emitLog := func(seq *int64, log string) { d.emitCheckRunLog(spec.GetRunId(), seq, masker.Mask(log)) }
	emitTerminal := func(seq *int64, status string, exitCode int32, summary string) {
		d.emitCheckRunTerminal(spec.GetRunId(), seq, status, exitCode, masker.Mask(summary))
	}

	seq := int64(0)
	if d.CheckSlots != nil {
		select {
		case d.CheckSlots <- struct{}{}:
			defer func() { <-d.CheckSlots }()
		case <-runCtx.Done():
			emitTerminal(&seq, "canceled", -1, "canceled")
			return
		}
	}
	d.emitCheckRunRunning(spec.GetRunId(), &seq)

	tempDir, err := os.MkdirTemp("", "gitslice-check-*")
	if err != nil {
		emitLog(&seq, err.Error())
		emitTerminal(&seq, "errored", -1, OneLineSummary(err.Error()))
		return
	}
	_ = os.Chmod(tempDir, 0o700)
	defer func() {
		if err := os.RemoveAll(tempDir); err != nil {
			fmt.Fprintf(d.Host.stderr(), "check workspace remove failed for %s: %v\n", tempDir, err)
		}
	}()

	if err := d.materializeCheckTree(d.Host.authContext(runCtx), repo, req.GetResultTreeId(), spec.GetMaterializePaths(), tempDir); err != nil {
		if errors.Is(err, context.Canceled) && baseCtx.Err() == nil {
			emitTerminal(&seq, "canceled", -1, "canceled")
			return
		}
		emitLog(&seq, err.Error())
		emitTerminal(&seq, "errored", -1, OneLineSummary(err.Error()))
		return
	}

	checkSpec := CheckSpecFromProto(spec)
	checkSpec.Scope = req.GetSliceId()
	if checkSpec.Image == "" && checkSpec.Build == nil && len(checkSpec.Setup) == 0 {
		checkHome, err := os.MkdirTemp("", "gs-check-home-*")
		if err != nil {
			emitLog(&seq, err.Error())
			emitTerminal(&seq, "errored", -1, OneLineSummary(err.Error()))
			return
		}
		_ = os.Chmod(checkHome, 0o700)
		defer func() {
			if rmErr := os.RemoveAll(checkHome); rmErr != nil {
				fmt.Fprintf(d.Host.stderr(), "check home remove failed for %s: %v\n", checkHome, rmErr)
			}
		}()
		if checkSpec.Env == nil {
			checkSpec.Env = map[string]string{}
		}
		checkSpec.Env["HOME"] = checkHome
	}
	result, err := checkexec.Run(runCtx, tempDir, checkSpec)
	if err != nil {
		result = checkexec.Result{
			Status:   "errored",
			ExitCode: -1,
			Summary:  OneLineSummary(err.Error()),
			Log:      err.Error(),
		}
	}
	if runCtx.Err() != nil && baseCtx.Err() == nil {
		result.Status = "canceled"
		result.ExitCode = -1
		result.Summary = "canceled"
	}
	if result.Log != "" {
		emitLog(&seq, result.Log)
	}
	emitTerminal(&seq, result.Status, result.ExitCode, OneLineSummary(result.Summary))
}

func (d *Daemon) materializeCheckTree(ctx context.Context, repo RepoClient, rootTreeID string, paths []string, destRoot string) error {
	cache, err := d.Host.objectCache()
	if err != nil {
		return err
	}
	return MaterializeCheckTree(ctx, repo, cache, d.Host.FetchCheckBatch, d.Host.CheckTreeWriter, rootTreeID, paths, destRoot)
}

func (d *Daemon) emitSetupFailureForRunChecks(req *corev1.RunChecks, err error) {
	for _, spec := range req.GetChecks() {
		if spec == nil || strings.TrimSpace(spec.GetRunId()) == "" {
			continue
		}
		if !d.startCheckRun(spec.GetRunId(), func() {}) {
			continue
		}
		d.emitCheckRunError(spec.GetRunId(), 0, err)
		d.clearCheckRun(spec.GetRunId(), nil)
	}
}

func (d *Daemon) startCheckRun(runID string, cancel context.CancelFunc) bool {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.checkRuns == nil {
		d.checkRuns = map[string]context.CancelFunc{}
	}
	if d.checkRuns[runID] != nil {
		return false
	}
	if _, ok := d.recentCheckRuns[runID]; ok {
		return false
	}
	d.checkRuns[runID] = cancel
	return true
}

func (d *Daemon) clearCheckRun(runID string, cancel context.CancelFunc) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.checkRuns, runID)
	d.rememberRecentCheckRunLocked(runID)
}

func (d *Daemon) rememberRecentCheckRunLocked(runID string) {
	if d.recentCheckRuns == nil {
		d.recentCheckRuns = map[string]struct{}{}
	}
	if _, ok := d.recentCheckRuns[runID]; ok {
		for i, existing := range d.recentCheckRunOrder {
			if existing == runID {
				copy(d.recentCheckRunOrder[i:], d.recentCheckRunOrder[i+1:])
				d.recentCheckRunOrder = d.recentCheckRunOrder[:len(d.recentCheckRunOrder)-1]
				break
			}
		}
	}
	d.recentCheckRuns[runID] = struct{}{}
	d.recentCheckRunOrder = append(d.recentCheckRunOrder, runID)
	for len(d.recentCheckRunOrder) > RecentCheckRunLimit {
		oldest := d.recentCheckRunOrder[0]
		copy(d.recentCheckRunOrder, d.recentCheckRunOrder[1:])
		d.recentCheckRunOrder = d.recentCheckRunOrder[:len(d.recentCheckRunOrder)-1]
		delete(d.recentCheckRuns, oldest)
	}
}

func (d *Daemon) HandleCancelCheckRun(cancel *corev1.CancelCheckRun) {
	if cancel == nil {
		return
	}
	runID := strings.TrimSpace(cancel.GetRunId())
	if runID == "" {
		return
	}
	d.mu.Lock()
	cancelFn := d.checkRuns[runID]
	d.mu.Unlock()
	if cancelFn != nil {
		cancelFn()
	}
}

func (d *Daemon) checkBaseContext() context.Context {
	if d.BaseCtx != nil {
		return d.BaseCtx
	}
	return context.Background()
}
