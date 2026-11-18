package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"gitslice.io/gitslice/internal/agentd"
	"gitslice.io/gitslice/internal/checkexec"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

const (
	agentMaxPendingCheckRunUpdates = agentd.MaxPendingCheckRunUpdates
	agentMaxCheckRunOutboxes       = agentd.MaxCheckRunOutboxes
)

var (
	resolveRunChecksServerAddr = agentd.ResolveRunChecksServerAddr
	checkSpecFromProto         = agentd.CheckSpecFromProto
	detectContainerRuntimes    = agentd.DetectContainerRuntimes
	checkSecretMasker          = agentd.CheckSecretMasker
	runCheckIDs                = agentd.RunCheckIDs
)

func (d *agentDaemon) handleRunChecks(req *corev1.RunChecks) {
	if req == nil {
		return
	}
	baseCtx := d.checkBaseContext()
	serverAddr, err := resolveRunChecksServerAddr(d.cfg.ServerAddr, req.GetServerAddr())
	if err != nil {
		d.emitSetupFailureForRunChecks(req, err)
		return
	}
	conn, err := dial(baseCtx, serverAddr)
	if err != nil {
		d.emitSetupFailureForRunChecks(req, fmt.Errorf("dial repository service: %w", err))
		return
	}
	defer conn.Close()

	repo := RepoClient(corev1.NewRepositoryServiceClient(conn))
	if account := strings.TrimSpace(req.GetSlice().GetAccount()); account != "" {
		repo = checkTreeRepoClient{Inner: repo, Account: account}
	}
	specs := req.GetChecks()
	workers := len(specs)
	if d.checkSlots != nil && cap(d.checkSlots) < workers {
		workers = cap(d.checkSlots)
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

func (d *agentDaemon) handleCheckRunSpec(req *corev1.RunChecks, repo RepoClient, spec *corev1.CheckRunSpec) {
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

	masker := checkSecretMasker(spec)
	emitLog := func(seq *int64, log string) { d.emitCheckRunLog(spec.GetRunId(), seq, masker.Mask(log)) }
	emitTerminal := func(seq *int64, status string, exitCode int32, summary string) {
		d.emitCheckRunTerminal(spec.GetRunId(), seq, status, exitCode, masker.Mask(summary))
	}

	seq := int64(0)
	if d.checkSlots != nil {
		select {
		case d.checkSlots <- struct{}{}:
			defer func() { <-d.checkSlots }()
		case <-runCtx.Done():
			emitTerminal(&seq, "canceled", -1, "canceled")
			return
		}
	}
	d.emitCheckRunRunning(spec.GetRunId(), &seq)

	tempDir, err := os.MkdirTemp("", "gitslice-check-*")
	if err != nil {
		emitLog(&seq, err.Error())
		emitTerminal(&seq, "errored", -1, oneLineSummary(err.Error()))
		return
	}
	_ = os.Chmod(tempDir, 0o700)
	defer func() {
		if err := os.RemoveAll(tempDir); err != nil {
			fmt.Fprintf(d.runner.stderr(), "check workspace remove failed for %s: %v\n", tempDir, err)
		}
	}()

	if err := d.materializeCheckTree(authContext(runCtx, d.cfg), repo, req.GetResultTreeId(), spec.GetMaterializePaths(), tempDir); err != nil {
		if errors.Is(err, context.Canceled) && baseCtx.Err() == nil {
			emitTerminal(&seq, "canceled", -1, "canceled")
			return
		}
		emitLog(&seq, err.Error())
		emitTerminal(&seq, "errored", -1, oneLineSummary(err.Error()))
		return
	}

	checkSpec := checkSpecFromProto(spec)
	checkSpec.Scope = req.GetSliceId()
	if checkSpec.Image == "" && checkSpec.Build == nil && len(checkSpec.Setup) == 0 {
		checkHome, err := os.MkdirTemp("", "gs-check-home-*")
		if err != nil {
			emitLog(&seq, err.Error())
			emitTerminal(&seq, "errored", -1, oneLineSummary(err.Error()))
			return
		}
		_ = os.Chmod(checkHome, 0o700)
		defer func() {
			if rmErr := os.RemoveAll(checkHome); rmErr != nil {
				fmt.Fprintf(d.runner.stderr(), "check home remove failed for %s: %v\n", checkHome, rmErr)
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
			Summary:  oneLineSummary(err.Error()),
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
	emitTerminal(&seq, result.Status, result.ExitCode, oneLineSummary(result.Summary))
}

func (d *agentDaemon) emitSetupFailureForRunChecks(req *corev1.RunChecks, err error) {
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

func (d *agentDaemon) startCheckRun(runID string, cancel context.CancelFunc) bool {
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

func (d *agentDaemon) clearCheckRun(runID string, cancel context.CancelFunc) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.checkRuns, runID)
	d.rememberRecentCheckRunLocked(runID)
}

func (d *agentDaemon) rememberRecentCheckRunLocked(runID string) {
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
	for len(d.recentCheckRunOrder) > agentRecentCheckRunLimit {
		oldest := d.recentCheckRunOrder[0]
		copy(d.recentCheckRunOrder, d.recentCheckRunOrder[1:])
		d.recentCheckRunOrder = d.recentCheckRunOrder[:len(d.recentCheckRunOrder)-1]
		delete(d.recentCheckRuns, oldest)
	}
}

func (d *agentDaemon) handleCancelCheckRun(cancel *corev1.CancelCheckRun) {
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

func (d *agentDaemon) checkBaseContext() context.Context {
	if d.baseCtx != nil {
		return d.baseCtx
	}
	return context.Background()
}
