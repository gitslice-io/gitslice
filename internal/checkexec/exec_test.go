package checkexec

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/gitslice-io/gitslice/internal/checks"
)

func TestRunHostPass(t *testing.T) {
	result, err := Run(context.Background(), t.TempDir(), checks.CheckSpec{Run: "printf ok"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Status != "passed" || result.ExitCode != 0 {
		t.Fatalf("result = %#v, want passed exit 0", result)
	}
	if result.Log != "ok" {
		t.Fatalf("log = %q, want ok", result.Log)
	}
	if result.SetupMs != 0 || result.Cached {
		t.Fatalf("host result setup fields = setup_ms %d cached %v, want zero/false", result.SetupMs, result.Cached)
	}
}

func TestRunHostFail(t *testing.T) {
	result, err := Run(context.Background(), t.TempDir(), checks.CheckSpec{Run: "exit 3"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Status != "failed" || result.ExitCode != 3 {
		t.Fatalf("result = %#v, want failed exit 3", result)
	}
}

func TestRunHostTimeout(t *testing.T) {
	result, err := Run(context.Background(), t.TempDir(), checks.CheckSpec{
		Run:     "sleep 2",
		Timeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Status != "errored" {
		t.Fatalf("status = %q, want errored", result.Status)
	}
	if !strings.Contains(result.Summary, "timed out") {
		t.Fatalf("summary = %q, want timeout", result.Summary)
	}
}

func TestRunHostEnvPassthrough(t *testing.T) {
	result, err := Run(context.Background(), t.TempDir(), checks.CheckSpec{
		Run: "printf %s \"$CHECKEXEC_TEST_VALUE\"",
		Env: map[string]string{"CHECKEXEC_TEST_VALUE": "from-env"},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Status != "passed" {
		t.Fatalf("status = %q, want passed", result.Status)
	}
	if result.Log != "from-env" {
		t.Fatalf("log = %q, want env value", result.Log)
	}
}

func TestRunContainerPass(t *testing.T) {
	runtime, ok := testContainerRuntime()
	if !ok {
		t.Skip("docker/podman not on PATH")
	}
	image := os.Getenv("CHECKEXEC_TEST_IMAGE")
	if image == "" {
		image = "alpine:3.20"
	}
	if err := exec.Command(runtime, "image", "inspect", image).Run(); err != nil {
		t.Skipf("container image %s unavailable locally: %v", image, err)
	}

	root := t.TempDir()
	if err := os.WriteFile(root+"/marker", []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := Run(context.Background(), root, checks.CheckSpec{
		Image: image,
		Run:   "test -f marker",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Status != "passed" || result.ExitCode != 0 {
		t.Fatalf("result = %#v, want passed exit 0", result)
	}
}

func TestContainerRunArgsHardeningAndCacheVolume(t *testing.T) {
	args, err := containerRunArgs("/tmp/workspace", "example:latest", checks.CheckSpec{
		Name:       "backend/test",
		Run:        "go test ./...",
		WorkingDir: "backend",
		Cache:      []string{"/go/pkg/mod", "/root/.cache/go-build"},
		Env:        map[string]string{"CGO_ENABLED": "0"},
	})
	if err != nil {
		t.Fatalf("containerRunArgs() error = %v", err)
	}
	wantArgs := []string{
		"--security-opt=no-new-privileges",
		"--cap-drop=ALL",
		"--pids-limit=" + defaultContainerPIDsLimit,
		"--memory=" + DefaultContainerMemory,
		"--cpus=" + DefaultContainerCPUs,
		"--network=none",
	}
	for _, want := range wantArgs {
		if !containsArg(args, want) {
			t.Fatalf("containerRunArgs() = %#v, want arg %q", args, want)
		}
	}
	wantCacheMounts := []string{
		checkCacheVolumeName("backend/test", "/go/pkg/mod") + ":/go/pkg/mod",
		checkCacheVolumeName("backend/test", "/root/.cache/go-build") + ":/root/.cache/go-build",
	}
	for _, want := range wantCacheMounts {
		if !containsArgPair(args, "-v", want) {
			t.Fatalf("containerRunArgs() = %#v, want cache mount %q", args, want)
		}
	}
	if !containsArgPair(args, "-w", "/workspace/backend") {
		t.Fatalf("containerRunArgs() = %#v, want working dir /workspace/backend", args)
	}
}

func TestContainerRunArgsResourceOverrides(t *testing.T) {
	args, err := containerRunArgs("/tmp/workspace", "example:latest", checks.CheckSpec{
		Run:     "true",
		Network: true,
		Memory:  "8g",
		CPUs:    "4",
	})
	if err != nil {
		t.Fatalf("containerRunArgs() error = %v", err)
	}
	if !containsArg(args, "--memory=8g") {
		t.Fatalf("containerRunArgs() = %#v, want memory override", args)
	}
	if !containsArg(args, "--cpus=4") {
		t.Fatalf("containerRunArgs() = %#v, want cpus override", args)
	}
	if containsArg(args, "--network=none") {
		t.Fatalf("containerRunArgs() = %#v, want network enabled", args)
	}
}

func TestRunContainerCacheVolumePersists(t *testing.T) {
	runtime, ok := testContainerRuntime()
	if !ok {
		t.Skip("docker/podman not on PATH")
	}
	image := os.Getenv("CHECKEXEC_TEST_IMAGE")
	if image == "" {
		image = "alpine:3.20"
	}
	if err := exec.Command(runtime, "image", "inspect", image).Run(); err != nil {
		t.Skipf("container image %s unavailable locally: %v", image, err)
	}

	cachePath := "/gitslice-cache"
	spec := checks.CheckSpec{
		Name:    "cache-persistence",
		Image:   image,
		Cache:   []string{cachePath},
		Run:     "mkdir -p /gitslice-cache && printf persisted > /gitslice-cache/marker",
		Timeout: 30 * time.Second,
	}
	volume := checkCacheVolumeName(spec.Name, cachePath)
	removeTestVolume(t, runtime, volume)
	t.Cleanup(func() {
		removeTestVolume(t, runtime, volume)
	})

	first, err := Run(context.Background(), t.TempDir(), spec)
	if err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	if first.Status != "passed" || first.ExitCode != 0 {
		t.Fatalf("first result = %#v, want passed exit 0", first)
	}

	spec.Run = `test "$(cat /gitslice-cache/marker)" = persisted`
	second, err := Run(context.Background(), t.TempDir(), spec)
	if err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
	if second.Status != "passed" || second.ExitCode != 0 {
		t.Fatalf("second result = %#v, want passed exit 0", second)
	}
}

func TestRunContainerSetupBuildsAndReusesPreparedImage(t *testing.T) {
	runtime, ok := testContainerRuntime()
	if !ok {
		t.Skip("docker/podman not on PATH")
	}
	image := "busybox"
	tag := preparedImageTag(image, []string{"true"})
	removeTestImage(t, runtime, tag)
	t.Cleanup(func() {
		removeTestImage(t, runtime, tag)
	})

	spec := checks.CheckSpec{
		Image: image,
		Setup: []string{"true"},
		Run:   "true",
	}
	first, err := Run(context.Background(), t.TempDir(), spec)
	if err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	if first.Status != "passed" || first.ExitCode != 0 {
		t.Fatalf("first result = %#v, want passed exit 0", first)
	}
	if first.Cached {
		t.Fatalf("first Cached = true, want false after forced image removal")
	}
	if first.SetupMs <= 0 {
		t.Fatalf("first SetupMs = %d, want > 0", first.SetupMs)
	}
	if err := exec.Command(runtime, "image", "inspect", tag).Run(); err != nil {
		t.Fatalf("prepared image %s was not built: %v", tag, err)
	}

	second, err := Run(context.Background(), t.TempDir(), spec)
	if err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
	if second.Status != "passed" || second.ExitCode != 0 {
		t.Fatalf("second result = %#v, want passed exit 0", second)
	}
	if !second.Cached {
		t.Fatalf("second Cached = false, want true")
	}
	if second.SetupMs != 0 {
		t.Fatalf("second SetupMs = %d, want 0 for cached image", second.SetupMs)
	}
}

func testContainerRuntime() (string, bool) {
	if path, err := exec.LookPath("docker"); err == nil {
		return path, true
	}
	if path, err := exec.LookPath("podman"); err == nil {
		return path, true
	}
	return "", false
}

func removeTestImage(t *testing.T, runtime, tag string) {
	t.Helper()
	_ = exec.Command(runtime, "rmi", "-f", tag).Run()
}

func removeTestVolume(t *testing.T, runtime, name string) {
	t.Helper()
	_ = exec.Command(runtime, "volume", "rm", "-f", name).Run()
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func containsArgPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}
