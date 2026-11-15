package checkexec

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/checks"
)

func TestRequireContainerChecksExportedHelper(t *testing.T) {
	t.Setenv("GITSLICE_CHECKS_REQUIRE_CONTAINER", "")
	if RequireContainerChecks() {
		t.Fatal("RequireContainerChecks() = true when unset, want false")
	}
	t.Setenv("GITSLICE_CHECKS_REQUIRE_CONTAINER", "1")
	if !RequireContainerChecks() {
		t.Fatal("RequireContainerChecks() = false for 1, want true")
	}
	t.Setenv("GITSLICE_CHECKS_REQUIRE_CONTAINER", "TRUE")
	if !RequireContainerChecks() {
		t.Fatal("RequireContainerChecks() = false for TRUE, want true")
	}
}

func TestImageAndSetupDockerfileInjectionRejected(t *testing.T) {
	badImages := []string{
		"-v",
		"--privileged",
		"busybox --privileged",
		"busybox\nRUN id",
		"busybox\rFROM scratch",
		"../busybox",
	}
	for _, img := range badImages {
		if _, err := setupDockerfile(img, []string{"true"}); err == nil {
			t.Fatalf("setupDockerfile(%q) = nil, want error", img)
		}
		if _, err := containerRunArgs("/tmp/ws", img, testContainerName, checks.CheckSpec{Run: "true"}, containerRun{}); err == nil {
			t.Fatalf("containerRunArgs(%q) = nil, want error", img)
		}
		if _, err := serviceRunArgs("gitslice-svc-1", "net1", checks.ServiceSpec{Name: "db", Image: img}, ""); err == nil {
			t.Fatalf("serviceRunArgs(%q) = nil, want error", img)
		}
		if _, err := Run(context.Background(), t.TempDir(), checks.CheckSpec{Image: img, Run: "true"}); err == nil {
			t.Fatalf("Run(%q) = nil, want error", img)
		}
	}

	badSetups := [][]string{
		{"echo ok\nCOPY /etc/passwd /leaked"},
		{"echo ok\rRUN id"},
		{"echo ok\x00id"},
		{"   "},
	}
	for _, setup := range badSetups {
		if _, err := setupDockerfile("busybox", setup); err == nil {
			t.Fatalf("setupDockerfile(setup=%q) = nil, want error", setup)
		}
		if _, err := Run(context.Background(), t.TempDir(), checks.CheckSpec{Image: "busybox", Setup: setup, Run: "true"}); err == nil {
			t.Fatalf("Run(setup=%q) = nil, want error", setup)
		}
	}

	// Verify "--" precedes positional image argument in containerRunArgs and serviceRunArgs.
	runArgs, err := containerRunArgs("/tmp/ws", "busybox:latest", testContainerName, checks.CheckSpec{Run: "echo hi"}, containerRun{})
	if err != nil {
		t.Fatalf("containerRunArgs() error = %v", err)
	}
	sepIdx := slices.Index(runArgs, "--")
	imgIdx := slices.Index(runArgs, "busybox:latest")
	if sepIdx < 0 || imgIdx != sepIdx+1 {
		t.Fatalf("containerRunArgs() = %#v, want '--' immediately before image", runArgs)
	}

	svcArgs, err := serviceRunArgs("gitslice-svc-1", "net1", checks.ServiceSpec{Name: "db", Image: "postgres:16"}, "")
	if err != nil {
		t.Fatalf("serviceRunArgs() error = %v", err)
	}
	svcSepIdx := slices.Index(svcArgs, "--")
	svcImgIdx := slices.Index(svcArgs, "postgres:16")
	if svcSepIdx < 0 || svcImgIdx != svcSepIdx+1 {
		t.Fatalf("serviceRunArgs() = %#v, want '--' immediately before service image", svcArgs)
	}
}

func TestServiceHardeningAndHostNetworkWithoutServices(t *testing.T) {
	args, err := serviceRunArgs("gitslice-svc-test", "gitslice-net-test", checks.ServiceSpec{
		Name:   "postgres",
		Image:  "postgres:16",
		Memory: "64g",
	}, "/tmp/test.env")
	if err != nil {
		t.Fatalf("serviceRunArgs() error = %v", err)
	}
	for _, want := range []string{
		"--security-opt=no-new-privileges",
		"--pids-limit=" + defaultContainerPIDsLimit,
		"--memory=" + MaxContainerMemory,
		"--cpus=" + defaultServiceCPUs,
	} {
		if !containsArg(args, want) {
			t.Fatalf("serviceRunArgs() = %#v, missing %q", args, want)
		}
	}
	if !containsArgPair(args, "--env-file", "/tmp/test.env") {
		t.Fatalf("serviceRunArgs() = %#v, missing --env-file", args)
	}

	// HostNetwork must never force host networking on a network:false check without services.
	t.Setenv("GITSLICE_CHECK_HOST_NETWORK", "1")
	svcs, note, res, err := startServices(context.Background(), RuntimeInfo{Path: "/bin/false", Name: "docker"}, checks.CheckSpec{
		Name:    "offline",
		Image:   "busybox",
		Network: false,
		Run:     "true",
	})
	if err != nil || res != nil || svcs != nil || note != "" {
		t.Fatalf("startServices(empty) = (%#v, %q, %#v, %v), want nil", svcs, note, res, err)
	}
	offlineArgs, err := containerRunArgs("/tmp/ws", "busybox", testContainerName, checks.CheckSpec{
		Run:     "true",
		Network: false,
	}, containerRun{})
	if err != nil {
		t.Fatalf("containerRunArgs() error = %v", err)
	}
	if !containsArg(offlineArgs, "--network=none") || containsArg(offlineArgs, "--network=host") {
		t.Fatalf("offline check on HostNetwork got args %#v, want --network=none", offlineArgs)
	}
}

func TestResourceLimitCappingAndValidation(t *testing.T) {
	args, err := containerRunArgs("/tmp/ws", "busybox", testContainerName, checks.CheckSpec{
		Run:    "true",
		Memory: "128g",
		CPUs:   "64",
	}, containerRun{})
	if err != nil {
		t.Fatalf("containerRunArgs() error = %v", err)
	}
	if !containsArg(args, "--memory=16g") {
		t.Fatalf("args = %#v, want --memory=16g cap", args)
	}
	if !containsArg(args, "--cpus=8") {
		t.Fatalf("args = %#v, want --cpus=8 cap", args)
	}

	t.Setenv("GITSLICE_CHECK_MAX_MEMORY", "2g")
	t.Setenv("GITSLICE_CHECK_MAX_CPUS", "1.5")
	cappedArgs, err := containerRunArgs("/tmp/ws", "busybox", testContainerName, checks.CheckSpec{
		Run:    "true",
		Memory: "8g",
		CPUs:   "4",
	}, containerRun{})
	if err != nil {
		t.Fatalf("containerRunArgs() error = %v", err)
	}
	if !containsArg(cappedArgs, "--memory=2g") || !containsArg(cappedArgs, "--cpus=1.5") {
		t.Fatalf("cappedArgs = %#v, want --memory=2g and --cpus=1.5", cappedArgs)
	}
}

func TestSymlinkEscapeRejectedInWorkspaceAndBuildContext(t *testing.T) {
	outsideDir := t.TempDir()
	victimFile := filepath.Join(outsideDir, "victim.txt")
	if err := os.WriteFile(victimFile, []byte("original-host-secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	ws := t.TempDir()
	writeTestFile(t, ws, "Dockerfile", "FROM busybox\n")

	// 1. Working directory symlink escaping workspaceRoot must be rejected.
	if err := os.Symlink(outsideDir, filepath.Join(ws, "escape-dir")); err != nil {
		t.Fatal(err)
	}
	if _, err := workspaceDir(ws, "escape-dir"); err == nil {
		t.Fatal("workspaceDir() allowed symlink escaping workspace")
	}
	if _, err := Run(context.Background(), ws, checks.CheckSpec{
		WorkingDir: "escape-dir",
		Run:        "pwd",
	}); err == nil {
		t.Fatal("Run() host mode allowed WorkingDir symlink escaping workspace")
	}

	// 2. Build input file symlink escaping context must be rejected.
	if err := os.Symlink(victimFile, filepath.Join(ws, "file-link")); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveBuiltImage(ws, "scope", &checks.ImageBuild{
		ContextDir: "/",
		Dockerfile: "Dockerfile",
		Inputs:     []string{"file-link"},
	}); err == nil {
		t.Fatal("resolveBuiltImage() allowed absolute file symlink outside context")
	}

	// 3. Relative ../.. symlink escaping context inside a subdirectory must be rejected.
	if err := os.MkdirAll(filepath.Join(ws, "deps"), 0o755); err != nil {
		t.Fatal(err)
	}
	relOutside, err := filepath.Rel(filepath.Join(ws, "deps"), victimFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(relOutside, filepath.Join(ws, "deps", "rel-escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveBuiltImage(ws, "scope", &checks.ImageBuild{
		ContextDir: "/",
		Dockerfile: "Dockerfile",
		Inputs:     []string{"deps"},
	}); err == nil {
		t.Fatal("resolveBuiltImage() allowed relative symlink escaping context")
	}
	_ = os.Remove(filepath.Join(ws, "deps", "rel-escape"))

	// 4. SEC-SRV-07 dirlink + dirlink/victim.txt overwrite attack in copyContext:
	// even if a caller passes ["escape-dir", "escape-dir/victim.txt"] directly to copyContext,
	// it must fail and leave victimFile untouched.
	writeTestFile(t, ws, "sub/victim.txt", "attacker-overwrite")
	dst := t.TempDir()
	if err := copyContext(ws, dst, []string{"escape-dir", "escape-dir/victim.txt"}); err == nil {
		t.Fatal("copyContext() allowed escaping symlink directory")
	}
	// Also test internal symlink directory + child traversal in copyContext:
	if err := os.Symlink("sub", filepath.Join(ws, "internal-link")); err != nil {
		t.Fatal(err)
	}
	if err := copyContext(ws, t.TempDir(), []string{"internal-link", "internal-link/victim.txt"}); err == nil {
		t.Fatal("copyContext() allowed writing child file through symlink directory")
	}
	gotVictim, err := os.ReadFile(victimFile)
	if err != nil || string(gotVictim) != "original-host-secret" {
		t.Fatalf("victimFile content = %q, %v; want untouched original-host-secret", gotVictim, err)
	}
}

func TestFakeDockerHostEnvIsolationBuildNetworkAndSetupPruning(t *testing.T) {
	binDir := t.TempDir()
	logFile := filepath.Join(t.TempDir(), "docker-invocations.log")
	envCaptureFile := filepath.Join(t.TempDir(), "captured-env-files.log")
	dockerScript := filepath.Join(binDir, "docker")

	// Fake docker CLI records every subcommand + checks that untrusted check/service
	// env vars are NEVER present in the host docker CLI process environment, while
	// verifying --env-file is 0600 at invocation time.
	script := `#!/bin/sh
set -eu
printf 'ARGS:%s\n' "$*" >> "$FAKE_DOCKER_LOG"
for forbidden in DOCKER_CONFIG DOCKER_HOST LD_PRELOAD DYLD_INSERT_LIBRARIES SECRET_CHECK_TOKEN SECRET_SVC_TOKEN; do
  eval "val=\${$forbidden-__UNSET__}"
  if [ "$val" != "__UNSET__" ]; then
    printf 'LEAKED_HOST_ENV:%s=%s\n' "$forbidden" "$val" >> "$FAKE_DOCKER_LOG"
    exit 91
  fi
done

prev=""
for arg in "$@"; do
  if [ "$prev" = "--env-file" ]; then
    if [ ! -f "$arg" ]; then
      echo "missing env-file $arg" >&2
      exit 92
    fi
    # Record permissions and contents while docker runs.
    perm="$(stat -c '%a' "$arg" 2>/dev/null || stat -f '%Lp' "$arg")"
    printf 'ENVFILE_PERM:%s PATH:%s\n' "$perm" "$arg" >> "$FAKE_ENV_CAPTURE"
    cat "$arg" >> "$FAKE_ENV_CAPTURE"
    printf -- '---\n' >> "$FAKE_ENV_CAPTURE"
  fi
  prev="$arg"
done

case "${1-}" in
  info)
    echo '["name=seccomp"]'
    exit 0
    ;;
  image)
    case "${2-}" in
      inspect)
        # Force fresh build on build/setup tags unless FAKE_IMAGE_CACHED=1.
        if [ "${FAKE_IMAGE_CACHED-0}" = "1" ]; then
          echo "HOME=/root"
          exit 0
        fi
        for last; do :; done
        case "$last" in
          gitslice-ci/*) exit 1 ;;
          *) echo "HOME=/root"; exit 0 ;;
        esac
        ;;
      ls)
        i=1
        while [ "$i" -le 10 ]; do
          printf '2026-11-14T00:00:%02dZ\tgitslice-ci/%024d:latest\n' "$i" "$i"
          i=$((i + 1))
        done
        exit 0
        ;;
      rm)
        exit 0
        ;;
    esac
    ;;
  build)
    cat >/dev/null || true
    exit 0
    ;;
  network)
    exit 0
    ;;
  run)
    printf 'check-ok'
    exit 0
    ;;
  exec|rm|logs)
    exit 0
    ;;
  inspect)
    echo "true"
    exit 0
    ;;
esac
exit 0
`
	if err := os.WriteFile(dockerScript, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("FAKE_DOCKER_LOG", logFile)
	t.Setenv("FAKE_ENV_CAPTURE", envCaptureFile)
	t.Setenv("LD_PRELOAD", "/tmp/host-ld-preload-must-be-stripped.so")
	t.Setenv("DYLD_INSERT_LIBRARIES", "/tmp/host-dyld-must-be-stripped.dylib")
	t.Setenv("PATH", binDir+":/usr/bin:/bin")
	forgetRuntime()
	t.Cleanup(forgetRuntime)

	ws := t.TempDir()
	writeTestFile(t, ws, "Dockerfile", "FROM busybox\n")

	res, err := Run(context.Background(), ws, checks.CheckSpec{
		Name:    "isolated-check",
		Scope:   "slice-sec",
		Build:   &checks.ImageBuild{ContextDir: "/", Dockerfile: "Dockerfile"},
		Setup:   []string{"echo setup-step"},
		Network: false,
		Services: []checks.ServiceSpec{{
			Name:  "db",
			Image: "postgres:16",
			Env: map[string]string{
				"SECRET_SVC_TOKEN": "svc-secret",
				"DOCKER_HOST":      "tcp://attacker.example:2375",
			},
			Ready: "pg_isready",
		}},
		Env: map[string]string{
			"SECRET_CHECK_TOKEN": "check-secret",
			"DOCKER_CONFIG":      "/workspace/malicious-docker-cfg",
			"LD_PRELOAD":         "/workspace/malicious.so",
		},
		Run: "echo hi",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Status != "passed" {
		t.Fatalf("Run() result = %#v, want passed", res)
	}

	rawLog, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	logText := string(rawLog)
	if strings.Contains(logText, "LEAKED_HOST_ENV:") {
		t.Fatalf("host docker CLI received untrusted env:\n%s", logText)
	}
	if strings.Contains(logText, "check-secret") || strings.Contains(logText, "svc-secret") || strings.Contains(logText, "malicious-docker-cfg") {
		t.Fatalf("docker CLI argv leaked secret values:\n%s", logText)
	}

	// Image builds (Build + Setup) keep network even for an offline check: they
	// fetch dependencies so the check itself can run without network.
	buildCount := 0
	prunedCount := 0
	for _, line := range strings.Split(strings.TrimSpace(logText), "\n") {
		if strings.HasPrefix(line, "ARGS:build ") {
			buildCount++
			if strings.Contains(line, "--network=none") {
				t.Fatalf("docker build ran without network for an offline check: %s", line)
			}
		}
		if strings.HasPrefix(line, "ARGS:image rm -- gitslice-ci/") {
			prunedCount++
		}
	}
	if buildCount != 2 {
		t.Fatalf("docker build count = %d, want 2 (image build + setup build); log:\n%s", buildCount, logText)
	}
	// 10 setup images listed, keptSetupImages (8) kept -> 2 oldest pruned!
	wantPruned := 10 - keptSetupImages
	if prunedCount < wantPruned {
		t.Fatalf("pruned setup images = %d, want at least %d; log:\n%s", prunedCount, wantPruned, logText)
	}

	// Verify --env-file permissions were 0600 and all temporary env files were removed on cleanup.
	rawCapture, err := os.ReadFile(envCaptureFile)
	if err != nil {
		t.Fatal(err)
	}
	captureText := string(rawCapture)
	if !strings.Contains(captureText, "SECRET_CHECK_TOKEN=check-secret") || !strings.Contains(captureText, "SECRET_SVC_TOKEN=svc-secret") {
		t.Fatalf("captured env files missing expected secrets:\n%s", captureText)
	}
	for _, line := range strings.Split(captureText, "\n") {
		if strings.HasPrefix(line, "ENVFILE_PERM:") {
			var perm, envPath string
			if _, err := fmt.Sscanf(line, "ENVFILE_PERM:%s PATH:%s", &perm, &envPath); err != nil {
				t.Fatalf("parse %q: %v", line, err)
			}
			if perm != "600" {
				t.Fatalf("env file %s had mode %s, want 600", envPath, perm)
			}
			if _, statErr := os.Stat(envPath); !os.IsNotExist(statErr) {
				t.Fatalf("env file %s was not removed after check cleanup: %v", envPath, statErr)
			}
		}
	}
}
