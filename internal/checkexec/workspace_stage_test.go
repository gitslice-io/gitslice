package checkexec

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/checks"
)

func TestWorkspaceStageTmpfsSelection(t *testing.T) {
	t.Setenv("GITSLICE_CHECK_STAGE_WORKSPACE", "1")
	got := workspaceStageTmpfs(RuntimeInfo{Name: "docker", Path: "/usr/bin/docker", Rootless: false}, []string{"/cache/go-build"})
	if !strings.HasPrefix(got, "/workspace:exec,") {
		t.Fatalf("workspaceStageTmpfs(forced on) = %q, want /workspace:exec,... spec", got)
	}

	rootless := workspaceStageTmpfs(RuntimeInfo{Name: "podman", Path: "/usr/bin/podman", Rootless: true}, nil)
	if rootless != "/workspace:exec,size=4g,mode=1777" {
		t.Fatalf("workspaceStageTmpfs(rootless) = %q, want /workspace:exec,size=4g,mode=1777", rootless)
	}

	t.Setenv("GITSLICE_CHECK_STAGE_SIZE", "8G")
	if got := workspaceStageTmpfs(RuntimeInfo{Name: "podman", Rootless: true}, nil); got != "/workspace:exec,size=8g,mode=1777" {
		t.Fatalf("workspaceStageTmpfs(GITSLICE_CHECK_STAGE_SIZE=8G) = %q", got)
	}
	t.Setenv("GITSLICE_CHECK_STAGE_SIZE", "8g,uid=0")
	if got := workspaceStageTmpfs(RuntimeInfo{Name: "podman", Rootless: true}, nil); got != "/workspace:exec,size=4g,mode=1777" {
		t.Fatalf("workspaceStageTmpfs(bad size) = %q, want the 4g default", got)
	}
	t.Setenv("GITSLICE_CHECK_STAGE_SIZE", "")

	// Cache mounts inside /workspace must disable tmpfs staging so they are not shadowed.
	if nested := workspaceStageTmpfs(RuntimeInfo{Name: "docker", Path: "/usr/bin/docker"}, []string{"/workspace/.cache"}); nested != "" {
		t.Fatalf("workspaceStageTmpfs(nested cache) = %q, want empty", nested)
	}

	t.Setenv("GITSLICE_CHECK_STAGE_WORKSPACE", "0")
	if disabled := workspaceStageTmpfs(RuntimeInfo{Name: "docker", Path: "/usr/bin/docker"}, nil); disabled != "" {
		t.Fatalf("workspaceStageTmpfs(disabled) = %q, want empty", disabled)
	}
}

func TestContainerRunArgsStagedWorkspace(t *testing.T) {
	spec := checks.CheckSpec{
		Name:       "web/build",
		WorkingDir: "gitslice/web",
		Run:        `[ -e node_modules ] || ln -s /deps/node_modules node_modules; npm run build`,
	}
	args, err := containerRunArgs("/host/ws", "node:22-bookworm", testContainerName, spec, containerRun{
		user:           []string{"--user", "501:20"},
		workspaceTmpfs: "/workspace:exec,uid=501,gid=20,mode=0700",
	})
	if err != nil {
		t.Fatalf("containerRunArgs() error = %v", err)
	}
	if !containsArgPair(args, "-v", "/host/ws:/.gitslice-workspace-src:ro") {
		t.Fatalf("args = %#v, want read-only workspace source mount", args)
	}
	if containsArgPair(args, "-v", "/host/ws:/workspace") {
		t.Fatalf("args = %#v, did not want read-write /workspace host bind mount", args)
	}
	if !containsArgPair(args, "--tmpfs", "/workspace:exec,uid=501,gid=20,mode=0700") {
		t.Fatalf("args = %#v, want --tmpfs /workspace spec", args)
	}
	if !containsArgPair(args, "-w", "/workspace") {
		t.Fatalf("args = %#v, want -w /workspace before staging", args)
	}
	sepIdx := slices.Index(args, "--")
	if sepIdx < 0 || len(args)-sepIdx != 8 {
		t.Fatalf("args tail = %#v, want [-- image sh -c script gitslice-stage workdir run]", args)
	}
	if args[sepIdx+1] != "node:22-bookworm" || args[sepIdx+6] != "/workspace/gitslice/web" || args[sepIdx+7] != spec.Run {
		t.Fatalf("args tail = %#v, unexpected positional arguments", args[sepIdx:])
	}
}

func TestRunContainerStagedWorkspacePreservesHostAndDotfiles(t *testing.T) {
	image := requireLocalImage(t, "busybox")
	t.Setenv("GITSLICE_CHECK_STAGE_WORKSPACE", "1")

	root := t.TempDir()
	writeTestFile(t, root, ".hidden", "dot-ok")
	writeTestFile(t, root, "sub/app/run.sh", "#!/bin/sh\nprintf staged-ok\n")
	if err := os.Chmod(filepath.Join(root, "sub/app/run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("run.sh", filepath.Join(root, "sub/app/link.sh")); err != nil {
		t.Fatal(err)
	}

	res, err := Run(context.Background(), root, checks.CheckSpec{
		Image:      image,
		WorkingDir: "sub/app",
		Run:        `test "$(cat /workspace/.hidden)" = dot-ok && test -L link.sh && ./run.sh && touch build-artifact`,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Status != "passed" || res.Log != "staged-ok" {
		t.Fatalf("result = %#v, want passed with log staged-ok", res)
	}
	// Host workspace must remain clean (no build-artifact written back to root).
	if _, err := os.Stat(filepath.Join(root, "sub/app/build-artifact")); !os.IsNotExist(err) {
		t.Fatalf("host workspace was mutated by staged container run: stat err = %v", err)
	}
}

// runStageScript runs the staging script on the host's sh, copying src into
// dst and then running command in workdir (relative to dst).
func runStageScript(t *testing.T, src, dst, workdir, command string) (string, int) {
	t.Helper()
	cmd := exec.Command("sh", "-c", stageScript(src, dst), "gitslice-stage", filepath.Join(dst, workdir), command)
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return string(out), 0
	case errors.As(err, &exitErr):
		return string(out), exitErr.ExitCode()
	default:
		t.Fatalf("run stage script: %v", err)
		return "", -1
	}
}

func TestStageScriptCopiesTreeAndSkipsGsMetadata(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	writeTestFile(t, src, ".hidden", "dot-ok")
	writeTestFile(t, src, "..odd", "odd-ok")
	writeTestFile(t, src, "sub/app/file", "app-ok")
	writeTestFile(t, src, ".gs/workspace.json", "{}")

	out, code := runStageScript(t, src, dst, "sub/app", `cat file ../../.hidden ../../..odd; test ! -e ../../.gs && echo no-gs`)
	if code != 0 || out != "app-okdot-okodd-okno-gs\n" {
		t.Fatalf("stage script = %q, exit %d; want the files copied, .gs skipped and the check run in sub/app", out, code)
	}
}

// A copy that fails must stop the check before it runs: a loop of cp
// commands reports only the last one's status, so a partial tree could
// otherwise pass.
func TestStageScriptFailsWhenACopyFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads unreadable files")
	}
	src, dst := t.TempDir(), t.TempDir()
	writeTestFile(t, src, "a-unreadable", "secret")
	writeTestFile(t, src, "z-last/file", "ok")
	if err := os.Chmod(filepath.Join(src, "a-unreadable"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(src, "a-unreadable"), 0o600) })

	out, code := runStageScript(t, src, dst, ".", `echo check-ran`)
	if code != stageFailedExit {
		t.Fatalf("exit = %d, want %d; output %q", code, stageFailedExit, out)
	}
	if strings.Contains(out, "check-ran") || !strings.Contains(out, "the check did not run") {
		t.Fatalf("output = %q, want the staging error and no check run", out)
	}
}

// A tree larger than GITSLICE_CHECK_STAGE_SIZE fails the check with the
// staging error instead of running it on whatever fit.
func TestRunContainerStagedWorkspaceTooLarge(t *testing.T) {
	image := requireLocalImage(t, "busybox")
	t.Setenv("GITSLICE_CHECK_STAGE_WORKSPACE", "1")
	t.Setenv("GITSLICE_CHECK_STAGE_SIZE", "1m")

	root := t.TempDir()
	writeTestFile(t, root, "big", strings.Repeat("x", 2<<20))
	res, err := Run(context.Background(), root, checks.CheckSpec{Image: image, Run: `echo check-ran`})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Status == "passed" || strings.Contains(res.Log, "check-ran") || !strings.Contains(res.Log, "the check did not run") {
		t.Fatalf("result = %#v, want a failure with the staging error", res)
	}
}
