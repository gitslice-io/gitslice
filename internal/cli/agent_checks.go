package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"gitslice.io/gitslice/internal/checkexec"
	"gitslice.io/gitslice/internal/checks"
	"gitslice.io/gitslice/internal/secretmask"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func (d *agentDaemon) handleRunChecks(req *corev1.RunChecks) {
	if req == nil {
		return
	}
	baseCtx := d.checkBaseContext()
	serverAddr := strings.TrimSpace(req.GetServerAddr())
	if serverAddr == "" {
		serverAddr = d.cfg.ServerAddr
	}
	conn, err := dial(baseCtx, serverAddr)
	if err != nil {
		d.emitSetupFailureForRunChecks(req, fmt.Errorf("dial repository service: %w", err))
		return
	}
	defer conn.Close()

	repo := RepoClient(corev1.NewRepositoryServiceClient(conn))
	if account := strings.TrimSpace(req.GetSlice().GetAccount()); account != "" {
		repo = checkTreeRepoClient{inner: repo, account: account}
	}
	// The checks of one request start in the order listed, and run side by
	// side up to the daemon's check concurrency (GITSLICE_CHECK_CONCURRENCY);
	// the slots in handleCheckRunSpec bound them across requests too.
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

	// What goes back to the server hides the slice's secrets: a check that
	// prints one must not put it in its log. The server masks too, for older
	// daemons.
	masker := checkSecretMasker(spec)
	emitLog := func(seq *int64, log string) { d.emitCheckRunLog(spec.GetRunId(), seq, masker.Mask(log)) }
	emitTerminal := func(seq *int64, status string, exitCode int32, summary string) {
		d.emitCheckRunTerminal(spec.GetRunId(), seq, status, exitCode, masker.Mask(summary))
	}

	seq := int64(0)
	if d.checkSlots != nil {
		// The run stays queued until a slot frees up.
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

func checkSpecFromProto(spec *corev1.CheckRunSpec) checks.CheckSpec {
	if spec == nil {
		return checks.CheckSpec{}
	}
	timeout := time.Duration(0)
	if spec.GetTimeoutMs() > 0 {
		timeout = time.Duration(spec.GetTimeoutMs()) * time.Millisecond
	}
	return checks.CheckSpec{
		Name:             spec.GetName(),
		Run:              spec.GetCommand(),
		Image:            spec.GetImage(),
		Setup:            append([]string(nil), spec.GetSetup()...),
		Cache:            append([]string(nil), spec.GetCache()...),
		WorkingDir:       spec.GetWorkingDir(),
		MaterializePaths: append([]string(nil), spec.GetMaterializePaths()...),
		Env:              copyCheckEnv(spec.GetEnv()),
		Network:          spec.GetNetwork(),
		Memory:           spec.GetMemory(),
		CPUs:             spec.GetCpus(),
		Timeout:          timeout,
		Build:            checkImageBuildFromProto(spec.GetBuild()),
		Services:         checkServicesFromProto(spec.GetServices()),
	}
}

func checkImageBuildFromProto(build *corev1.CheckImageBuild) *checks.ImageBuild {
	if build == nil || strings.TrimSpace(build.GetDockerfile()) == "" {
		return nil
	}
	return &checks.ImageBuild{
		ContextDir: build.GetContextDir(),
		Dockerfile: build.GetDockerfile(),
		Inputs:     append([]string(nil), build.GetInputs()...),
		Timeout:    time.Duration(build.GetTimeoutMs()) * time.Millisecond,
	}
}

func checkServicesFromProto(services []*corev1.CheckServiceContainer) []checks.ServiceSpec {
	var out []checks.ServiceSpec
	for _, service := range services {
		if service == nil {
			continue
		}
		out = append(out, checks.ServiceSpec{
			Name:         service.GetName(),
			Image:        service.GetImage(),
			Env:          copyCheckEnv(service.GetEnv()),
			Ready:        service.GetReady(),
			ReadyTimeout: time.Duration(service.GetReadyTimeoutMs()) * time.Millisecond,
			Memory:       service.GetMemory(),
		})
	}
	return out
}

func copyCheckEnv(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
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

// detectContainerRuntimes lists the container runtime the daemon can run
// checks in: one whose daemon answers, not merely one on the PATH.
func detectContainerRuntimes() []string {
	rt := checkexec.DetectRuntime(context.Background())
	if !rt.OK() {
		return nil
	}
	return []string{rt.Name}
}

// checkSecretMasker masks the values of the spec's secret env entries.
func checkSecretMasker(spec *corev1.CheckRunSpec) *secretmask.Masker {
	var values []string
	for _, name := range spec.GetSecretEnv() {
		if value, ok := spec.GetEnv()[name]; ok {
			values = append(values, value)
		}
	}
	return secretmask.New(values)
}
