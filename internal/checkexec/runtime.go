package checkexec

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// RuntimeInfo describes the container runtime checks run under.
type RuntimeInfo struct {
	Path string // the docker or podman binary
	Name string // "docker" or "podman"
	// Rootless is true when the runtime maps container root to the user who
	// runs it, so a container already owns what that user owns.
	Rootless bool
	// Err says why no runtime is usable: none installed, or its daemon does
	// not answer (stopped, or this user may not talk to it).
	Err error
}

// OK reports whether checks can run in containers.
func (r RuntimeInfo) OK() bool { return r.Err == nil && r.Path != "" }

const runtimeProbeTimeout = 15 * time.Second

var (
	runtimeMu     sync.Mutex
	runtimeCached *RuntimeInfo
)

// DetectRuntime finds docker or podman and asks it for its info, so a
// runtime whose daemon is down counts as missing. A usable runtime is
// remembered for the process; a missing one is asked for again next time,
// so starting Docker later is enough.
func DetectRuntime(ctx context.Context) RuntimeInfo {
	runtimeMu.Lock()
	defer runtimeMu.Unlock()
	if runtimeCached != nil {
		return *runtimeCached
	}
	info := probeRuntimes(ctx)
	if info.OK() {
		runtimeCached = &info
	}
	return info
}

func probeRuntimes(ctx context.Context) RuntimeInfo {
	var firstErr error
	for _, name := range []string{"docker", "podman"} {
		path, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		info := probeRuntime(ctx, name, path)
		if info.OK() {
			return info
		}
		if firstErr == nil {
			firstErr = info.Err
		}
	}
	if firstErr == nil {
		firstErr = fmt.Errorf("no container runtime found (install docker or podman)")
	}
	return RuntimeInfo{Err: firstErr}
}

func probeRuntime(ctx context.Context, name, path string) RuntimeInfo {
	ctx, cancel := context.WithTimeout(ctx, runtimeProbeTimeout)
	defer cancel()
	format := "{{json .SecurityOptions}}"
	if name == "podman" {
		format = "{{.Host.Security.Rootless}}"
	}
	out, err := exec.CommandContext(ctx, path, "info", "--format", format).CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(lastLine(string(out)))
		if detail == "" {
			detail = err.Error()
		}
		return RuntimeInfo{Path: path, Name: name, Err: fmt.Errorf("%s is installed but does not answer: %s", name, detail)}
	}
	text := strings.TrimSpace(string(out))
	rootless := strings.Contains(text, "rootless")
	if name == "podman" {
		rootless = text == "true"
	}
	return RuntimeInfo{Path: path, Name: name, Rootless: rootless}
}

// userArgs make a container run as the daemon's user, so it can read and
// write the tree the daemon materialized and the cache folders it keeps.
// Rootless Docker already maps container root to that user; rootless Podman
// needs keep-id; a rootful runtime is told the uid and gid.
func userArgs(rt RuntimeInfo) []string {
	if rt.Name == "podman" && rt.Rootless {
		return []string{"--userns=keep-id"}
	}
	if rt.Rootless || runtime.GOOS == "windows" {
		return nil
	}
	uid, gid := os.Getuid(), os.Getgid()
	if uid <= 0 {
		return nil
	}
	return []string{"--user", fmt.Sprintf("%d:%d", uid, gid)}
}

// containerHome is the HOME a check gets when it runs as a user the image
// has no home for.
const containerHome = "/tmp/gitslice-home"

// needsHome reports whether a check running as the daemon's uid should be
// given a HOME: the image sets none, or one that only root can write.
func needsHome(ctx context.Context, rt RuntimeInfo, image string) bool {
	if len(userArgs(rt)) == 0 || rt.Name == "podman" {
		return false
	}
	out, err := exec.CommandContext(ctx, rt.Path, "image", "inspect", "--format", "{{range .Config.Env}}{{println .}}{{end}}", image).Output()
	if err != nil {
		return true
	}
	for _, line := range strings.Split(string(out), "\n") {
		if home, ok := strings.CutPrefix(strings.TrimSpace(line), "HOME="); ok {
			return home == "" || home == "/" || home == "/root"
		}
	}
	return true
}

// CacheRoot is where the folders behind a check's cache paths live.
func CacheRoot() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("GITSLICE_CHECK_CACHE_DIR")); dir != "" {
		return dir, nil
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("find a cache folder for checks: %w", err)
	}
	return filepath.Join(base, "gitslice", "check-cache"), nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

// forgetRuntime drops the remembered runtime, for tests that change PATH.
func forgetRuntime() {
	runtimeMu.Lock()
	defer runtimeMu.Unlock()
	runtimeCached = nil
}
