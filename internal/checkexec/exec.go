package checkexec

import (
	"bytes"
	"context"
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
}

// Run executes one resolved check against workspaceRoot.
func Run(ctx context.Context, workspaceRoot string, spec checks.CheckSpec) (Result, error) {
	timeout := spec.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	if strings.TrimSpace(spec.Image) != "" {
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
	args = append(args, spec.Image, "sh", "-c", spec.Run)

	cmd := exec.CommandContext(ctx, runtime, args...)
	return runCommand(ctx, cmd, timeout)
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

	err := cmdCtx.Run()
	log := output.String()
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		_ = killProcessGroup(cmdCtx)
		return Result{
			Status:   "errored",
			ExitCode: -1,
			Summary:  fmt.Sprintf("timed out after %s", timeout),
			Log:      log,
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
			}, nil
		}
		if runCtx.Err() != nil {
			return Result{
				Status:   "errored",
				ExitCode: -1,
				Summary:  runCtx.Err().Error(),
				Log:      log,
			}, nil
		}
		return Result{
			Status:   "errored",
			ExitCode: -1,
			Summary:  fmt.Sprintf("failed to start check: %v", err),
			Log:      log,
		}, nil
	}
	return Result{
		Status:   "passed",
		ExitCode: 0,
		Summary:  "exit 0",
		Log:      log,
	}, nil
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
