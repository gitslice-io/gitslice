package checkexec

import (
	"context"
	"os"
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
	if rootless != "/workspace:exec,mode=1777" {
		t.Fatalf("workspaceStageTmpfs(rootless) = %q, want /workspace:exec,mode=1777", rootless)
	}

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
