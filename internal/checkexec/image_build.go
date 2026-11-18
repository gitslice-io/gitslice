package checkexec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/checks"
	"gitslice.io/gitslice/internal/keylock"
)

var imageBuildLocks keylock.Locker

func lockBuild(ctx context.Context, tag string) (func(), error) {
	return imageBuildLocks.LockContext(ctx, tag)
}

type cachedImageBuildSpec struct {
	runtime      string
	tag          string
	label        string
	timeout      time.Duration
	tempPattern  string
	tempErrLabel string
	phaseLabel   string
	stdin        io.Reader
	prepareDir   func(contextDir string) (dockerfileArg string, err error)
	onBuilt      func()
}

// buildCachedContainerImage serializes concurrent builds per image tag, returns
// immediately when the tag already exists locally, and runs `build` inside a
// temporary context directory with bounded timeout, process-group cleanup, and
// consistent error/cancellation classification.
func buildCachedContainerImage(ctx context.Context, spec cachedImageBuildSpec) (setupMs int64, cached bool, result *Result, err error) {
	if err := checks.ValidateImageRef(spec.tag); err != nil {
		return 0, false, nil, err
	}
	unlock, err := lockBuild(ctx, spec.tag)
	if err != nil {
		return 0, false, nil, err
	}
	defer unlock()

	inspectCmd := exec.CommandContext(ctx, spec.runtime, "image", "inspect", "--", spec.tag)
	inspectCmd.Env = containerClientEnv()
	if inspectCmd.Run() == nil {
		return 0, true, nil, nil
	}

	contextDir, err := os.MkdirTemp("", spec.tempPattern)
	if err != nil {
		return 0, false, nil, fmt.Errorf("%s: %w", spec.tempErrLabel, err)
	}
	defer os.RemoveAll(contextDir)

	dockerfileArg := "-"
	if spec.prepareDir != nil {
		dockerfileArg, err = spec.prepareDir(contextDir)
		if err != nil {
			return 0, false, nil, err
		}
	}

	timeout := spec.timeout
	if timeout <= 0 {
		timeout = checks.DefaultBuildTimeout
	}
	buildCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	buildArgs := []string{
		"build",
		"--label", spec.label,
		"-t", spec.tag,
		"-f", dockerfileArg,
	}
	buildArgs = append(buildArgs, containerBuildNetworkArgs()...)
	buildArgs = append(buildArgs, contextDir)

	cmd := exec.CommandContext(buildCtx, spec.runtime, buildArgs...)
	cmd.Env = containerClientEnv()
	if spec.stdin != nil {
		cmd.Stdin = spec.stdin
	}
	configureProcessGroup(cmd)
	var output cappedBuffer
	cmd.Stdout = &output
	cmd.Stderr = &output

	start := time.Now()
	err = cmd.Run()
	setupMs = time.Since(start).Milliseconds()
	log := output.String()
	if errors.Is(buildCtx.Err(), context.DeadlineExceeded) {
		_ = killProcessGroup(cmd)
		return setupMs, false, &Result{
			Status:   "errored",
			ExitCode: -1,
			Summary:  fmt.Sprintf("%s timed out after %s", spec.phaseLabel, timeout),
			Log:      log,
			SetupMs:  setupMs,
		}, nil
	}
	if err != nil {
		_ = killProcessGroup(cmd)
		var summary string
		var exitErr *exec.ExitError
		if buildCtx.Err() != nil {
			summary = fmt.Sprintf("%s failed: %v", spec.phaseLabel, buildCtx.Err())
		} else if errors.As(err, &exitErr) {
			summary = fmt.Sprintf("%s failed: exit %d", spec.phaseLabel, exitErr.ExitCode())
		} else {
			summary = fmt.Sprintf("%s failed: %v", spec.phaseLabel, err)
		}
		return setupMs, false, &Result{
			Status:   "errored",
			ExitCode: -1,
			Summary:  summary,
			Log:      log,
			SetupMs:  setupMs,
		}, nil
	}
	if spec.onBuilt != nil {
		spec.onBuilt()
	}
	return setupMs, false, nil, nil
}

func prepareContainerImage(ctx context.Context, runtime, image string, setup []string, timeout time.Duration) (string, int64, bool, *Result, error) {
	dockerfile, err := setupDockerfile(image, setup)
	if err != nil {
		return "", 0, false, nil, err
	}
	tag := preparedImageTag(image, setup)
	setupMs, cached, result, err := buildCachedContainerImage(ctx, cachedImageBuildSpec{
		runtime:      runtime,
		tag:          tag,
		label:        setupImageLabelKey + "=1",
		timeout:      timeout,
		tempPattern:  "gitslice-ci-context-*",
		tempErrLabel: "create setup build context",
		phaseLabel:   "setup",
		stdin:        strings.NewReader(dockerfile),
		onBuilt:      func() { pruneSetupImages(runtime) },
	})
	return tag, setupMs, cached, result, err
}

// ensureBuiltImage returns the image for a build, building it unless an
// image with its tag exists. note says which happened, for the check's log.
func ensureBuiltImage(ctx context.Context, rt RuntimeInfo, img builtImage, timeout time.Duration) (setupMs int64, cached bool, note string, result *Result, err error) {
	setupMs, cached, result, err = buildCachedContainerImage(ctx, cachedImageBuildSpec{
		runtime:      rt.Path,
		tag:          img.Tag,
		label:        builtImageFamilyKey + "=" + img.Family,
		timeout:      timeout,
		tempPattern:  "gitslice-ci-build-*",
		tempErrLabel: "create build context",
		phaseLabel:   "image build",
		prepareDir: func(contextDir string) (string, error) {
			if err := copyContext(img.root, contextDir, img.files); err != nil {
				return "", err
			}
			return filepath.Join(contextDir, filepath.FromSlash(img.dockerfile)), nil
		},
		onBuilt: func() { pruneBuiltImages(rt, img.Family) },
	})
	if err != nil || result != nil {
		return setupMs, false, "", result, err
	}
	if cached {
		return 0, true, "using image " + img.Tag + ", built earlier from the same Dockerfile and inputs", nil, nil
	}
	return setupMs, false, fmt.Sprintf("built image %s in %s; later runs reuse it until the Dockerfile or its inputs change", img.Tag, time.Duration(setupMs)*time.Millisecond), nil, nil
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
