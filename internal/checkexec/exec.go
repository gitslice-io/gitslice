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

	"github.com/gitslice-io/gitslice/internal/checks"
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
)

type Result struct {
	Status   string
	ExitCode int32
	Summary  string
	Log      string
	SetupMs  int64
	Cached   bool
	RunMs    int64
}

// Run executes one resolved check against workspaceRoot.
func Run(ctx context.Context, workspaceRoot string, spec checks.CheckSpec) (Result, error) {
	timeout := spec.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	if strings.TrimSpace(spec.Image) != "" || len(spec.Setup) > 0 {
		return runContainer(ctx, workspaceRoot, spec, timeout)
	}
	return runHost(ctx, workspaceRoot, spec, timeout)
}

func runHost(ctx context.Context, workspaceRoot string, spec checks.CheckSpec, timeout time.Duration) (Result, error) {
	// Host execution intentionally ignores container-only cache and resource
	// settings; there is no isolation boundary to mount persistent volumes into.
	dir, err := workspaceDir(workspaceRoot, spec.WorkingDir)
	if err != nil {
		return Result{}, err
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", spec.Run)
	cmd.Dir = dir
	cmd.Env = hostEnv(spec.Env)
	return runCommand(ctx, cmd, timeout, nil)
}

func runContainer(ctx context.Context, workspaceRoot string, spec checks.CheckSpec, timeout time.Duration) (Result, error) {
	runtime, ok := containerRuntime()
	if !ok {
		return Result{
			Status:   "errored",
			ExitCode: -1,
			Summary:  "container runtime not found (install docker or podman)",
		}, nil
	}
	image := strings.TrimSpace(spec.Image)
	if image == "" {
		return Result{
			Status:   "errored",
			ExitCode: -1,
			Summary:  "container image is required when setup is configured",
		}, nil
	}
	var setupMs int64
	cached := false
	if len(spec.Setup) > 0 {
		preparedImage, preparedSetupMs, preparedCached, result, err := prepareContainerImage(ctx, runtime, image, spec.Setup, timeout)
		if err != nil {
			return Result{}, err
		}
		if result != nil {
			return *result, nil
		}
		image = preparedImage
		setupMs = preparedSetupMs
		cached = preparedCached
	}
	root, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return Result{}, fmt.Errorf("resolve workspace root: %w", err)
	}

	containerName, err := newContainerName()
	if err != nil {
		return Result{}, err
	}
	args, err := containerRunArgs(root, image, containerName, spec)
	if err != nil {
		return Result{}, err
	}

	cmd := exec.CommandContext(ctx, runtime, args...)
	cmd.Env = containerClientEnv(spec.Env)
	cleanup := func() {
		removeContainer(runtime, containerName)
	}
	result, err := runCommand(ctx, cmd, timeout, cleanup)
	result.SetupMs = setupMs
	result.Cached = cached
	return result, err
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
	tag := preparedImageTag(image, setup)
	inspect := exec.CommandContext(ctx, runtime, "image", "inspect", tag)
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
	cmd := exec.CommandContext(buildCtx, runtime, "build", "-t", tag, "-f", "-", contextDir)
	cmd.Stdin = strings.NewReader(setupDockerfile(image, setup))
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
	return tag, setupMs, false, nil, nil
}

func preparedImageTag(image string, setup []string) string {
	sum := sha256.Sum256([]byte(image + "\n" + strings.Join(setup, "\n")))
	return "gitslice-ci/" + hex.EncodeToString(sum[:])[:24]
}

var newContainerName = randomContainerName

func randomContainerName() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate container name: %w", err)
	}
	return containerNamePrefix + hex.EncodeToString(b[:]), nil
}

func setupDockerfile(image string, setup []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "FROM %s\n", image)
	for _, command := range setup {
		fmt.Fprintf(&b, "RUN %s\n", command)
	}
	return b.String()
}

func containerRunArgs(root, image, containerName string, spec checks.CheckSpec) ([]string, error) {
	cachePaths, err := normalizedContainerCachePaths(spec.Cache)
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
		"--memory=" + containerLimitValue(spec.Memory, DefaultContainerMemory),
		"--cpus=" + containerLimitValue(spec.CPUs, DefaultContainerCPUs),
		"-v", root + ":" + containerWorkspacePath,
	}
	for _, cachePath := range cachePaths {
		args = append(args, "-v", checkCacheVolumeName(spec.Name, cachePath)+":"+cachePath)
	}
	args = append(args, "-w", containerWorkingDir(spec.WorkingDir))
	if !spec.Network {
		args = append(args, "--network=none")
	}
	for _, key := range sortedEnvKeys(spec.Env) {
		args = append(args, "-e", key)
	}
	args = append(args, image, "sh", "-c", spec.Run)
	return args, nil
}

func removeContainer(runtime, name string) {
	ctx, cancel := context.WithTimeout(context.Background(), containerCleanupTimeout)
	defer cancel()
	_ = exec.CommandContext(ctx, runtime, "rm", "-f", name).Run()
}

func containerLimitValue(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func normalizedContainerCachePaths(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
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

func checkCacheVolumeName(checkName, cachePath string) string {
	sum := sha256.Sum256([]byte(checkName + "\x00" + cachePath))
	return "gitslice-cache-" + hex.EncodeToString(sum[:])[:24]
}

func workspaceDir(workspaceRoot, logicalDir string) (string, error) {
	if strings.TrimSpace(workspaceRoot) == "" {
		return "", fmt.Errorf("workspace root is required")
	}
	root, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return "", fmt.Errorf("resolve workspace root: %w", err)
	}
	rel := logicalPathRel(logicalDir)
	dir := filepath.Join(root, filepath.FromSlash(rel))
	if err := ensureInside(root, dir); err != nil {
		return "", err
	}
	return dir, nil
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

func ensureInside(root, target string) error {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return fmt.Errorf("resolve workspace-relative path: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("working directory escapes workspace: %s", target)
	}
	return nil
}

func hostEnv(overrides map[string]string) []string {
	env := os.Environ()
	return append(env, envPairs(overrides)...)
}

func containerClientEnv(overrides map[string]string) []string {
	env := os.Environ()
	return append(env, envPairs(overrides)...)
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

func containerRuntime() (string, bool) {
	if path, err := exec.LookPath("docker"); err == nil {
		return path, true
	}
	if path, err := exec.LookPath("podman"); err == nil {
		return path, true
	}
	return "", false
}

type cappedBuffer struct {
	buf       bytes.Buffer
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.buf.Len() < MaxLogBytes {
		remaining := MaxLogBytes - b.buf.Len()
		if len(p) <= remaining {
			_, _ = b.buf.Write(p)
		} else {
			_, _ = b.buf.Write(p[:remaining])
			b.truncated = true
		}
	} else if len(p) > 0 {
		b.truncated = true
	}
	return len(p), nil
}

func (b *cappedBuffer) String() string {
	if !b.truncated {
		return b.buf.String()
	}
	return b.buf.String() + "\n[check log truncated]\n"
}
