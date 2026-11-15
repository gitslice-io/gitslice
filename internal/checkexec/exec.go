package checkexec

import (
	"bytes"
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
	user      []string          // --user / --userns arguments
	home      bool              // give the check a HOME it can write
	envFile   string            // 0600 temporary --env-file path, if any
	network   string            // the run's services network, if any
	hosts     []string          // names that resolve to 127.0.0.1 (services, on the host network)
	cacheDirs map[string]string // container cache path -> host folder
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
	if run.cacheDirs, err = checkCacheDirs(spec); err != nil {
		return Result{}, err
	}
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

	start := time.Now()
	err := cmdCtx.Run()
	runMs := time.Since(start).Milliseconds()
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

func prepareContainerImage(ctx context.Context, runtime, image string, setup []string, timeout time.Duration) (string, int64, bool, *Result, error) {
	dockerfile, err := setupDockerfile(image, setup)
	if err != nil {
		return "", 0, false, nil, err
	}
	tag := preparedImageTag(image, setup)
	if err := checks.ValidateImageRef(tag); err != nil {
		return "", 0, false, nil, err
	}

	unlock, err := lockBuild(ctx, tag)
	if err != nil {
		return "", 0, false, nil, err
	}
	defer unlock()

	inspect := exec.CommandContext(ctx, runtime, "image", "inspect", "--", tag)
	inspect.Env = containerClientEnv()
	if err := inspect.Run(); err == nil {
		return tag, 0, true, nil, nil
	}

	// Setup is image-level, changeset-independent preparation. Build with an
	// empty context and without mounting the workspace so the derived image can
	// be reused across patchsets that declare the same image/setup pair.
	contextDir, err := os.MkdirTemp("", "gitslice-ci-context-*")
	if err != nil {
		return "", 0, false, nil, fmt.Errorf("create setup build context: %w", err)
	}
	defer os.RemoveAll(contextDir)

	buildCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	buildArgs := []string{
		"build",
		"--label", setupImageLabelKey + "=1",
		"-t", tag,
		"-f", "-",
	}
	buildArgs = append(buildArgs, containerBuildNetworkArgs()...)
	buildArgs = append(buildArgs, contextDir)

	cmd := exec.CommandContext(buildCtx, runtime, buildArgs...)
	cmd.Env = containerClientEnv()
	cmd.Stdin = strings.NewReader(dockerfile)
	configureProcessGroup(cmd)
	var output cappedBuffer
	cmd.Stdout = &output
	cmd.Stderr = &output

	start := time.Now()
	err = cmd.Run()
	setupMs := time.Since(start).Milliseconds()
	log := output.String()
	if errors.Is(buildCtx.Err(), context.DeadlineExceeded) {
		_ = killProcessGroup(cmd)
		return tag, setupMs, false, &Result{
			Status:   "errored",
			ExitCode: -1,
			Summary:  fmt.Sprintf("setup timed out after %s", timeout),
			Log:      log,
			SetupMs:  setupMs,
		}, nil
	}
	if err != nil {
		summary := "setup failed"
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			summary = fmt.Sprintf("setup failed: exit %d", exitErr.ExitCode())
		} else if buildCtx.Err() != nil {
			summary = "setup failed: " + buildCtx.Err().Error()
		} else {
			summary = fmt.Sprintf("setup failed: %v", err)
		}
		return tag, setupMs, false, &Result{
			Status:   "errored",
			ExitCode: -1,
			Summary:  summary,
			Log:      log,
			SetupMs:  setupMs,
		}, nil
	}
	pruneSetupImages(runtime)
	return tag, setupMs, false, nil, nil
}

func pruneSetupImages(runtime string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	lsCmd := exec.CommandContext(ctx, runtime, "image", "ls",
		"--filter", "label="+setupImageLabelKey+"=1",
		"--format", "{{.CreatedAt}}\t{{.Repository}}:{{.Tag}}")
	lsCmd.Env = containerClientEnv()
	out, err := lsCmd.Output()
	if err != nil {
		return
	}
	pruneImageListing(ctx, runtime, string(out), keptSetupImages)
}

func preparedImageTag(image string, setup []string) string {
	sum := sha256.Sum256([]byte(image + "\n" + strings.Join(setup, "\n")))
	return "gitslice-ci/" + hex.EncodeToString(sum[:])[:24]
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

func setupDockerfile(image string, setup []string) (string, error) {
	if err := checks.ValidateImageRef(image); err != nil {
		return "", fmt.Errorf("setup base image: %w", err)
	}
	if err := checks.ValidateSetupCommands(setup); err != nil {
		return "", fmt.Errorf("setup commands: %w", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "FROM %s\n", image)
	for _, command := range setup {
		fmt.Fprintf(&b, "RUN %s\n", command)
	}
	return b.String(), nil
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
	args = append(args, "-v", root+":"+containerWorkspacePath)
	for _, cachePath := range cachePaths {
		hostDir, ok := run.cacheDirs[cachePath]
		if !ok {
			return nil, fmt.Errorf("no cache folder for %s", cachePath)
		}
		args = append(args, "-v", hostDir+":"+cachePath)
	}
	args = append(args, "-w", containerWorkingDir(spec.WorkingDir))
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
	args = append(args, "--", image, "sh", "-c", spec.Run)
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

// logHeadBytes is how much of a check's output is kept from the start; the
// rest of MaxLogBytes holds the most recent output.
const logHeadBytes = 32 * 1024

type cappedBuffer struct {
	head        bytes.Buffer
	tail        []byte // ring holding the latest output once head is full
	next        int    // ring write position
	tailWritten int64  // bytes ever written past head
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if room := logHeadBytes - b.head.Len(); room > 0 {
		take := min(room, len(p))
		_, _ = b.head.Write(p[:take])
		p = p[take:]
	}
	if len(p) == 0 {
		return n, nil
	}
	if b.tail == nil {
		b.tail = make([]byte, MaxLogBytes-logHeadBytes)
	}
	b.tailWritten += int64(len(p))
	if len(p) > len(b.tail) {
		p = p[len(p)-len(b.tail):]
	}
	for len(p) > 0 {
		c := copy(b.tail[b.next:], p)
		b.next = (b.next + c) % len(b.tail)
		p = p[c:]
	}
	return n, nil
}

func (b *cappedBuffer) String() string {
	if b.tailWritten == 0 {
		return b.head.String()
	}
	var out strings.Builder
	out.WriteString(b.head.String())
	size := int64(len(b.tail))
	if b.tailWritten <= size {
		out.Write(b.tail[:b.tailWritten])
		return out.String()
	}
	fmt.Fprintf(&out, "\n[check log truncated: %d bytes omitted]\n", b.tailWritten-size)
	out.Write(b.tail[b.next:])
	out.Write(b.tail[:b.next])
	return out.String()
}
