package checkexec

import (
	"bytes"
	"context"
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
	dir, err := workspaceDir(workspaceRoot, spec.WorkingDir)
	if err != nil {
		return Result{}, err
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", spec.Run)
	cmd.Dir = dir
	cmd.Env = hostEnv(spec.Env)
	return runCommand(ctx, cmd, timeout)
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

	args := []string{
		"run",
		"--rm",
		"-v", root + ":" + containerWorkspacePath,
		"-w", containerWorkingDir(spec.WorkingDir),
	}
	if !spec.Network {
		args = append(args, "--network=none")
	}
	for _, key := range sortedEnvKeys(spec.Env) {
		args = append(args, "-e", key+"="+spec.Env[key])
	}
	args = append(args, image, "sh", "-c", spec.Run)

	cmd := exec.CommandContext(ctx, runtime, args...)
	result, err := runCommand(ctx, cmd, timeout)
	result.SetupMs = setupMs
	result.Cached = cached
	return result, err
}

func runCommand(ctx context.Context, cmd *exec.Cmd, timeout time.Duration) (Result, error) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmdCtx := exec.CommandContext(runCtx, cmd.Path, cmd.Args[1:]...)
	cmdCtx.Dir = cmd.Dir
	cmdCtx.Env = cmd.Env
	configureProcessGroup(cmdCtx)

	var output cappedBuffer
	cmdCtx.Stdout = &output
	cmdCtx.Stderr = &output

	start := time.Now()
	err := cmdCtx.Run()
	runMs := time.Since(start).Milliseconds()
	log := output.String()
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		_ = killProcessGroup(cmdCtx)
		return Result{
			Status:   "errored",
			ExitCode: -1,
			Summary:  fmt.Sprintf("timed out after %s", timeout),
			Log:      log,
			RunMs:    runMs,
		}, nil
	}
	if err != nil {
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

func setupDockerfile(image string, setup []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "FROM %s\n", image)
	for _, command := range setup {
		fmt.Fprintf(&b, "RUN %s\n", command)
	}
	return b.String()
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
	for _, key := range sortedEnvKeys(overrides) {
		env = append(env, key+"="+overrides[key])
	}
	return env
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
