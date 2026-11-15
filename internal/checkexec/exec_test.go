package checkexec

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/checks"
)

const testContainerName = containerNamePrefix + "unit-test"

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
	cacheDirs := map[string]string{
		"/go/pkg/mod":           "/cache/a",
		"/root/.cache/go-build": "/cache/b",
	}
	args, err := containerRunArgs("/tmp/workspace", "example:latest", testContainerName, checks.CheckSpec{
		Name:       "backend/test",
		Run:        "go test ./...",
		WorkingDir: "backend",
		Cache:      []string{"/go/pkg/mod", "/root/.cache/go-build"},
		Env:        map[string]string{"CGO_ENABLED": "0"},
	}, containerRun{user: []string{"--user", "1000:1000"}, cacheDirs: cacheDirs})
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
	if !containsArgPair(args, "--name", testContainerName) {
		t.Fatalf("containerRunArgs() = %#v, want generated container name", args)
	}
	if !containsArgPair(args, "--user", "1000:1000") {
		t.Fatalf("containerRunArgs() = %#v, want --user 1000:1000", args)
	}
	wantCacheMounts := []string{
		"/cache/a:/go/pkg/mod",
		"/cache/b:/root/.cache/go-build",
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

func TestContainerRunArgsEnvByNameOnly(t *testing.T) {
	args, err := containerRunArgs("/tmp/workspace", "example:latest", testContainerName, checks.CheckSpec{
		Run: "true",
		Env: map[string]string{
			"Z_SECRET": "top-secret-token",
			"A_TOKEN":  "alpha=secret",
		},
	}, containerRun{})
	if err != nil {
		t.Fatalf("containerRunArgs() error = %v", err)
	}

	wantEnvArgs := []string{"A_TOKEN", "Z_SECRET"}
	if got := envFlagValues(args); !slices.Equal(got, wantEnvArgs) {
		t.Fatalf("env args = %#v, want %#v", got, wantEnvArgs)
	}
	for _, arg := range args {
		if strings.Contains(arg, "top-secret-token") || strings.Contains(arg, "alpha=secret") {
			t.Fatalf("containerRunArgs() leaked env value in arg %q", arg)
		}
		if strings.Contains(arg, "A_TOKEN=") || strings.Contains(arg, "Z_SECRET=") {
			t.Fatalf("containerRunArgs() used KEY=VALUE env arg %q", arg)
		}
	}
}

func TestContainerRunArgsResourceOverrides(t *testing.T) {
	args, err := containerRunArgs("/tmp/workspace", "example:latest", testContainerName, checks.CheckSpec{
		Run:     "true",
		Network: true,
		Memory:  "8g",
		CPUs:    "4",
	}, containerRun{})
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

func TestRunContainerTimeoutRemovesContainer(t *testing.T) {
	docker, ok := testDockerRuntime()
	if !ok {
		t.Skip("docker not on PATH")
	}
	image := os.Getenv("CHECKEXEC_TEST_IMAGE")
	if image == "" {
		image = "alpine:3.20"
	}
	if err := exec.Command(docker, "image", "inspect", image).Run(); err != nil {
		if image != "alpine:3.20" {
			t.Skipf("container image %s unavailable locally: %v", image, err)
		}
		image = "busybox"
		if err := exec.Command(docker, "image", "inspect", image).Run(); err != nil {
			t.Skipf("container images alpine:3.20 and busybox unavailable locally: %v", err)
		}
	}

	name := containerNamePrefix + "timeout-test"
	removeTestContainer(t, docker, name)
	t.Cleanup(func() {
		removeTestContainer(t, docker, name)
	})
	originalNameGenerator := newContainerName
	newContainerName = func() (string, error) {
		return name, nil
	}
	t.Cleanup(func() {
		newContainerName = originalNameGenerator
	})

	result, err := Run(context.Background(), t.TempDir(), checks.CheckSpec{
		Image:   image,
		Run:     "sleep 30",
		Timeout: time.Second,
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

	running, err := runningDockerContainerNamesByPrefix(docker, containerNamePrefix)
	if err != nil {
		t.Fatalf("docker ps failed: %v", err)
	}
	if slices.Contains(running, name) {
		t.Fatalf("container %s still running after timeout; docker ps names = %#v", name, running)
	}
}

func TestRunContainerCacheFolderPersists(t *testing.T) {
	image := requireLocalImage(t, "busybox")
	cacheDir := t.TempDir()
	t.Setenv("GITSLICE_CHECK_CACHE_DIR", cacheDir)

	cachePath := "/gitslice-cache"
	spec := checks.CheckSpec{
		Name:    "cache-persistence",
		Scope:   "slice-a",
		Image:   image,
		Cache:   []string{cachePath},
		Run:     "printf persisted > /gitslice-cache/marker",
		Timeout: 30 * time.Second,
	}
	first, err := Run(context.Background(), t.TempDir(), spec)
	if err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	if first.Status != "passed" || first.ExitCode != 0 {
		t.Fatalf("first result = %#v, want passed exit 0", first)
	}
	// The cache is a folder the daemon's user owns, so it can clear it.
	marker := filepath.Join(cacheDir, checkCacheDirName("slice-a", cachePath), "marker")
	if data, err := os.ReadFile(marker); err != nil || string(data) != "persisted" {
		t.Fatalf("cache marker = %q, %v; want persisted", data, err)
	}

	spec.Run = `test "$(cat /gitslice-cache/marker)" = persisted`
	second, err := Run(context.Background(), t.TempDir(), spec)
	if err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
	if second.Status != "passed" || second.ExitCode != 0 {
		t.Fatalf("second result = %#v, want passed exit 0", second)
	}

	// Another check of the slice shares the cache; another slice running
	// the same check starts with an empty one.
	spec.Name = "other-check"
	shared, err := Run(context.Background(), t.TempDir(), spec)
	if err != nil || shared.Status != "passed" {
		t.Fatalf("other-check result = %#v, %v; want the shared cache", shared, err)
	}

	spec.Scope = "slice-b"
	spec.Run = `test ! -e /gitslice-cache/marker`
	other, err := Run(context.Background(), t.TempDir(), spec)
	if err != nil {
		t.Fatalf("other-slice Run() error = %v", err)
	}
	if other.Status != "passed" {
		t.Fatalf("other-slice result = %#v, want an empty cache", other)
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

// requireLocalImage skips unless a container runtime answers and has image
// without pulling it.
func requireLocalImage(t *testing.T, image string) string {
	t.Helper()
	rt := DetectRuntime(context.Background())
	if !rt.OK() {
		t.Skipf("no container runtime: %v", rt.Err)
	}
	if err := exec.Command(rt.Path, "image", "inspect", image).Run(); err != nil {
		t.Skipf("container image %s unavailable locally: %v", image, err)
	}
	return image
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

func testDockerRuntime() (string, bool) {
	path, err := exec.LookPath("docker")
	return path, err == nil
}

func removeTestImage(t *testing.T, runtime, tag string) {
	t.Helper()
	_ = exec.Command(runtime, "rmi", "-f", tag).Run()
}

func removeTestContainer(t *testing.T, runtime, name string) {
	t.Helper()
	_ = exec.Command(runtime, "rm", "-f", name).Run()
}

func removeTestVolume(t *testing.T, runtime, name string) {
	t.Helper()
	_ = exec.Command(runtime, "volume", "rm", "-f", name).Run()
}

func runningDockerContainerNamesByPrefix(docker, prefix string) ([]string, error) {
	out, err := exec.Command(docker, "ps", "--filter", "name="+prefix, "--format", "{{.Names}}").Output()
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(out)), nil
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

func envFlagValues(args []string) []string {
	var values []string
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-e" {
			values = append(values, args[i+1])
		}
	}
	return values
}
