package checkexec

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/checks"
)

const (
	// DefaultTimeout bounds checks that do not declare an explicit timeout.
	DefaultTimeout = 10 * time.Minute
	MaxLogBytes    = 256 * 1024

	containerWorkspacePath = "/workspace"

	defaultContainerPIDsLimit = "2048"
	DefaultContainerMemory    = "4g"
	DefaultContainerCPUs      = "2"

	containerNamePrefix     = "gitslice-run-"
	containerCleanupTimeout = 15 * time.Second

	setupImageLabelKey = "io.gitslice.ci.setup"
	keptSetupImages    = 8
)

// outputWaitDelay bounds how long a check waits for its output to close
// once its command has exited or been stopped. A process the check left
// behind (a server started in the background, a process in its own
// session) keeps the output pipe open, and without this bound the check
// never finished: its daemon held the slot, so it never counted as idle
// and never detached.
var outputWaitDelay = 30 * time.Second

type Result struct {
	Status   string
	ExitCode int32
	Summary  string
	Log      string
	SetupMs  int64
	Cached   bool
	RunMs    int64
	// Unrunnable is set when this machine cannot run the check at all (it
	// needs a container runtime and has none), as opposed to the check
	// failing. Such a result says nothing about the change.
	Unrunnable bool
}

// Run executes one resolved check against workspaceRoot.
func Run(ctx context.Context, workspaceRoot string, spec checks.CheckSpec) (Result, error) {
	timeout := spec.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	if containerized(spec) {
		return runContainer(ctx, workspaceRoot, spec, timeout)
	}
	if len(spec.Services) > 0 {
		return Result{
			Status:   "errored",
			ExitCode: -1,
			Summary:  "services need a containerized check (image or build)",
		}, nil
	}
	if RequireContainerChecks() {
		return Result{}, fmt.Errorf("check %q must declare an image or setup: host-mode execution is disabled on this daemon", spec.Name)
	}
	return runHost(ctx, workspaceRoot, spec, timeout)
}

func containerized(spec checks.CheckSpec) bool {
	return spec.Image != "" || spec.Build != nil || len(spec.Setup) > 0
}

func runHost(ctx context.Context, workspaceRoot string, spec checks.CheckSpec, timeout time.Duration) (Result, error) {
	// Host execution intentionally ignores container-only cache and resource
	// settings; there is no isolation boundary to mount persistent volumes into.
	if strings.ContainsRune(spec.Run, '\x00') {
		return Result{}, fmt.Errorf("check %q run command contains NUL", spec.Name)
	}
	dir, err := workspaceDir(workspaceRoot, spec.WorkingDir)
	if err != nil {
		return Result{}, err
	}
	env, err := hostEnv(spec.Env)
	if err != nil {
		return Result{}, err
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", spec.Run)
	cmd.Dir = dir
	cmd.Env = env
	return runCommand(ctx, cmd, timeout, nil)
}

// containerRun is what a check's container needs beyond its spec.
type containerRun struct {
	user           []string          // --user / --userns arguments
	home           bool              // give the check a HOME it can write
	envFile        string            // 0600 temporary --env-file path, if any
	network        string            // the run's services network, if any
	hosts          []string          // names that resolve to 127.0.0.1 (services, on the host network)
	cacheDirs      map[string]string // container cache path -> host folder
	workspaceTmpfs string            // --tmpfs spec when staging /workspace inside the container
}

func runContainer(ctx context.Context, workspaceRoot string, spec checks.CheckSpec, timeout time.Duration) (Result, error) {
	if spec.Image != "" {
		if err := checks.ValidateImageRef(spec.Image); err != nil {
			return Result{}, fmt.Errorf("invalid check image: %w", err)
		}
	}
	if err := checks.ValidateSetupCommands(spec.Setup); err != nil {
		return Result{}, fmt.Errorf("invalid check setup: %w", err)
	}
	if err := checks.ValidateEnvMap(spec.Env); err != nil {
		return Result{}, fmt.Errorf("invalid check env: %w", err)
	}
	if strings.ContainsRune(spec.Run, '\x00') {
		return Result{}, fmt.Errorf("check %q run command contains NUL", spec.Name)
	}

	rt := DetectRuntime(ctx)
	if !rt.OK() {
		return Result{
			Status:     "errored",
			ExitCode:   -1,
			Summary:    oneLine(rt.Err.Error()),
			Unrunnable: true,
		}, nil
	}
	root, err := workspaceDir(workspaceRoot, ".")
	if err != nil {
		return Result{}, err
	}
	if _, err := workspaceDir(root, spec.WorkingDir); err != nil {
		return Result{}, err
	}

	var notes []string
	var setupMs int64
	cached := false
	image := spec.Image
	if spec.Build != nil {
		built, err := resolveBuiltImage(root, spec.Scope, spec.Build)
		if err != nil {
			return Result{
				Status:   "errored",
				ExitCode: -1,
				Summary:  oneLine(err.Error()),
				Log:      err.Error(),
			}, nil
		}
		buildMs, buildCached, note, result, err := ensureBuiltImage(ctx, rt, built, spec.Build.Timeout)
		if err != nil {
			return Result{}, err
		}
		if result != nil {
			return *result, nil
		}
		image, setupMs, cached = built.Tag, buildMs, buildCached
		notes = append(notes, note)
	}
	if image == "" {
		return Result{
			Status:   "errored",
			ExitCode: -1,
			Summary:  "container image is required when setup is configured",
		}, nil
	}
	if len(spec.Setup) > 0 {
		preparedImage, preparedSetupMs, preparedCached, result, err := prepareContainerImage(ctx, rt.Path, image, spec.Setup, timeout)
		if err != nil {
			return Result{}, err
		}
		if result != nil {
			result.SetupMs += setupMs
			return *result, nil
		}
		image = preparedImage
		setupMs += preparedSetupMs
		// Cached means every image this check needed was reused.
		cached = preparedCached && (cached || spec.Build == nil)
	}

	run := containerRun{user: userArgs(rt)}
	run.home = needsHome(ctx, rt, image)
	cachePaths, err := normalizedContainerCachePaths(spec.Cache)
	if err != nil {
		return Result{}, err
	}
	if run.cacheDirs, err = checkCacheDirs(spec); err != nil {
		return Result{}, err
	}
	run.workspaceTmpfs = workspaceStageTmpfs(rt, cachePaths)
	if len(spec.Services) > 0 {
		services, note, result, err := startServices(ctx, rt, spec)
		if err != nil {
			return Result{}, err
		}
		if result != nil {
			result.SetupMs = setupMs
			result.Log = withNotes(notes, result.Log)
			return *result, nil
		}
		defer services.stop()
		run.network = services.network
		run.hosts = services.hosts
		notes = append(notes, note)
	}

	defaultHome := ""
	if run.home {
		defaultHome = containerHome
	}
	envFile, cleanupEnv, err := writeContainerEnvFile(spec.Env, defaultHome)
	if err != nil {
		return Result{}, err
	}
	defer cleanupEnv()
	run.envFile = envFile

	containerName, err := newContainerName()
	if err != nil {
		return Result{}, err
	}
	args, err := containerRunArgs(root, image, containerName, spec, run)
	if err != nil {
		return Result{}, err
	}

	cmd := exec.CommandContext(ctx, rt.Path, args...)
	cmd.Env = containerClientEnv()
	cleanup := func() {
		cleanupEnv()
		removeContainer(rt.Path, containerName)
	}
	result, err := runCommand(ctx, cmd, timeout, cleanup)
	result.SetupMs = setupMs
	result.Cached = cached
	result.Log = withNotes(notes, result.Log)
	return result, err
}

// withNotes puts what the daemon did before the check ran (built or reused
// an image, started services) at the top of its log.
func withNotes(notes []string, log string) string {
	var b strings.Builder
	for _, note := range notes {
		if strings.TrimSpace(note) != "" {
			b.WriteString("[gitslice] " + note + "\n")
		}
	}
	if b.Len() == 0 {
		return log
	}
	return b.String() + log
}

func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func runCommand(ctx context.Context, cmd *exec.Cmd, timeout time.Duration, cleanup func()) (Result, error) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmdCtx := exec.CommandContext(runCtx, cmd.Path, cmd.Args[1:]...)
	cmdCtx.Dir = cmd.Dir
	cmdCtx.Env = cmd.Env
	configureProcessGroup(cmdCtx)

	var output cappedBuffer
	cmdCtx.Stdout = &output
	cmdCtx.Stderr = &output

	cleanupRan := false
	runCleanup := func() {
		if cleanup == nil || cleanupRan {
			return
		}
		cleanupRan = true
		cleanup()
	}

	cmdCtx.WaitDelay = outputWaitDelay

	start := time.Now()
	err := cmdCtx.Run()
	runMs := time.Since(start).Milliseconds()
	if errors.Is(err, exec.ErrWaitDelay) {
		// The command passed, but something it started still held its
		// output open; stop what is left of its process group.
		_ = killProcessGroup(cmdCtx)
		output.Write([]byte(fmt.Sprintf("\ngitslice: the check exited but processes it started kept its output open for %s; stopped them\n", outputWaitDelay)))
		err = nil
	}
	log := output.String()
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		_ = killProcessGroup(cmdCtx)
		runCleanup()
		return Result{
			Status:   "errored",
			ExitCode: -1,
			Summary:  fmt.Sprintf("timed out after %s", timeout),
			Log:      log,
			RunMs:    runMs,
		}, nil
	}
	if runCtx.Err() != nil {
		runCleanup()
	}
	if err != nil {
		runCleanup()
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code := int32(exitErr.ExitCode())
			return Result{
				Status:   "failed",
				ExitCode: code,
				Summary:  fmt.Sprintf("exit %d", code),
				Log:      log,
				RunMs:    runMs,
			}, nil
		}
		if runCtx.Err() != nil {
			return Result{
				Status:   "errored",
				ExitCode: -1,
				Summary:  runCtx.Err().Error(),
				Log:      log,
				RunMs:    runMs,
			}, nil
		}
		return Result{
			Status:   "errored",
			ExitCode: -1,
			Summary:  fmt.Sprintf("failed to start check: %v", err),
			Log:      log,
			RunMs:    runMs,
		}, nil
	}
	return Result{
		Status:   "passed",
		ExitCode: 0,
		Summary:  "exit 0",
		Log:      log,
		RunMs:    runMs,
	}, nil
}

var newContainerName = randomContainerName

func randomContainerName() (string, error) {
	suffix, err := randomSuffix()
	if err != nil {
		return "", err
	}
	return containerNamePrefix + suffix, nil
}

func randomSuffix() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate container name: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

func containerRunArgs(root, image, containerName string, spec checks.CheckSpec, run containerRun) ([]string, error) {
	if err := checks.ValidateImageRef(image); err != nil {
		return nil, fmt.Errorf("invalid container image: %w", err)
	}
	if containerName == "" || strings.HasPrefix(containerName, "-") || strings.ContainsAny(containerName, " \t\r\n\x00") {
		return nil, fmt.Errorf("invalid container name %q", containerName)
	}
	if err := checks.ValidateEnvMap(spec.Env); err != nil {
		return nil, err
	}
	cachePaths, err := normalizedContainerCachePaths(spec.Cache)
	if err != nil {
		return nil, err
	}
	memLimit, err := cappedMemoryLimit(spec.Memory, DefaultContainerMemory)
	if err != nil {
		return nil, err
	}
	cpuLimit, err := cappedCPULimit(spec.CPUs, DefaultContainerCPUs)
	if err != nil {
		return nil, err
	}

	args := []string{
		"run",
		"--rm",
		"--name", containerName,
		"--security-opt=no-new-privileges",
		"--cap-drop=ALL",
		"--pids-limit=" + defaultContainerPIDsLimit,
		"--memory=" + memLimit,
		"--cpus=" + cpuLimit,
	}
	args = append(args, run.user...)
	workdir := containerWorkingDir(spec.WorkingDir)
	if run.workspaceTmpfs != "" {
		args = append(args, "-v", root+":"+containerWorkspaceSrcPath+":ro")
		args = append(args, "--tmpfs", run.workspaceTmpfs)
	} else {
		args = append(args, "-v", root+":"+containerWorkspacePath)
	}
	for _, cachePath := range cachePaths {
		hostDir, ok := run.cacheDirs[cachePath]
		if !ok {
			return nil, fmt.Errorf("no cache folder for %s", cachePath)
		}
		args = append(args, "-v", hostDir+":"+cachePath)
	}
	if run.workspaceTmpfs != "" {
		// Start at /workspace so the OCI runtime never pre-creates nested
		// subdirectories as root before the non-root check user stages files.
		args = append(args, "-w", containerWorkspacePath)
	} else {
		args = append(args, "-w", workdir)
	}
	switch {
	case run.network != "":
		args = append(args, "--network="+run.network)
	case !spec.Network:
		args = append(args, "--network=none")
	case HostNetwork():
		args = append(args, "--network="+hostNetwork)
	}
	for _, host := range run.hosts {
		args = append(args, "--add-host", host+":127.0.0.1")
	}
	if run.envFile != "" {
		args = append(args, "--env-file", run.envFile)
	} else if _, ok := spec.Env["HOME"]; run.home && !ok {
		args = append(args, "-e", "HOME="+containerHome)
	}
	if run.workspaceTmpfs != "" {
		args = append(args, "--", image, "sh", "-c", stageWorkspaceScript(), "gitslice-stage", workdir, spec.Run)
	} else {
		args = append(args, "--", image, "sh", "-c", spec.Run)
	}
	return args, nil
}

// checkCacheDirs creates the host folders behind a check's cache paths.
func checkCacheDirs(spec checks.CheckSpec) (map[string]string, error) {
	cachePaths, err := normalizedContainerCachePaths(spec.Cache)
	if err != nil || len(cachePaths) == 0 {
		return nil, err
	}
	base, err := CacheRoot()
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(cachePaths))
	for _, cachePath := range cachePaths {
		dir := filepath.Join(base, checkCacheDirName(spec.Scope, cachePath))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create cache folder for %s: %w", cachePath, err)
		}
		out[cachePath] = dir
	}
	return out, nil
}

// removeContainer removes a container and its anonymous volumes.
func removeContainer(runtime, name string) {
	if name == "" || strings.HasPrefix(name, "-") || strings.ContainsAny(name, " \t\r\n\x00") {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), containerCleanupTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, runtime, "rm", "-f", "-v", "--", name)
	cmd.Env = containerClientEnv()
	_ = cmd.Run()
}

func normalizedContainerCachePaths(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		if strings.ContainsAny(value, "\r\n\x00") {
			return nil, fmt.Errorf("cache path %q contains control characters", value)
		}
		normalized := strings.ReplaceAll(strings.TrimSpace(value), "\\", "/")
		if normalized == "" {
			return nil, fmt.Errorf("cache path is empty")
		}
		if !strings.HasPrefix(normalized, "/") {
			return nil, fmt.Errorf("cache path %q must be absolute inside the container", value)
		}
		for _, segment := range strings.Split(normalized, "/") {
			if segment == ".." {
				return nil, fmt.Errorf("cache path %q contains .. segment", value)
			}
		}
		normalized = path.Clean(normalized)
		if normalized == "/" {
			return nil, fmt.Errorf("cache path %q must not be the container root", value)
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out, nil
}

func checkCacheDirName(scope, cachePath string) string {
	sum := sha256.Sum256([]byte(scope + "\x00" + cachePath))
	return hex.EncodeToString(sum[:])[:24]
}

func containerWorkingDir(logicalDir string) string {
	rel := logicalPathRel(logicalDir)
	if rel == "." {
		return containerWorkspacePath
	}
	return path.Join(containerWorkspacePath, rel)
}

func logicalPathRel(value string) string {
	value = strings.ReplaceAll(strings.TrimSpace(value), "\\", "/")
	value = strings.TrimPrefix(value, "/")
	cleaned := path.Clean("/" + value)
	if cleaned == "/" {
		return "."
	}
	return strings.TrimPrefix(cleaned, "/")
}

func envPairs(overrides map[string]string) []string {
	pairs := make([]string, 0, len(overrides))
	for _, key := range sortedEnvKeys(overrides) {
		pairs = append(pairs, key+"="+overrides[key])
	}
	return pairs
}

func sortedEnvKeys(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
