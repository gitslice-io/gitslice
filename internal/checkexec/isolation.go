package checkexec

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gitslice.io/gitslice/internal/checks"
)

const (
	// MaxContainerMemory and MaxContainerCPUs cap check-supplied resource
	// overrides unless the runner operator configures GITSLICE_CHECK_MAX_MEMORY
	// or GITSLICE_CHECK_MAX_CPUS.
	MaxContainerMemory = "16g"
	MaxContainerCPUs   = "8"

	defaultMaxContainerMemoryBytes int64   = 16 << 30
	defaultMaxContainerCPUs        float64 = 8
)

// RequireContainerChecks reports whether host-mode check execution is disabled
// on this daemon (GITSLICE_CHECKS_REQUIRE_CONTAINER=1 or true).
func RequireContainerChecks() bool {
	v := strings.TrimSpace(os.Getenv("GITSLICE_CHECKS_REQUIRE_CONTAINER"))
	return v == "1" || strings.EqualFold(v, "true")
}

func requireContainerChecks() bool {
	return RequireContainerChecks()
}

func isBlockedHostEnvKey(key string) bool {
	upper := strings.ToUpper(key)
	if strings.HasPrefix(upper, "LD_") || strings.HasPrefix(upper, "DYLD_") {
		return true
	}
	switch upper {
	case "BASH_ENV", "ENV", "CDPATH", "IFS", "GCONV_PATH", "HOSTALIASES", "LOCALDOMAIN", "RES_OPTIONS", "LOCPATH", "NLSPATH":
		return true
	default:
		return false
	}
}

// containerClientEnv returns a sanitized host environment for invoking the
// docker/podman CLI. Untrusted check/service env vars MUST NEVER be merged here.
func containerClientEnv() []string {
	raw := os.Environ()
	out := make([]string, 0, len(raw))
	for _, entry := range raw {
		if strings.ContainsAny(entry, "\r\n\x00") {
			continue
		}
		key, _, ok := strings.Cut(entry, "=")
		if !ok || key == "" || isBlockedHostEnvKey(key) {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func hostEnv(overrides map[string]string) ([]string, error) {
	if err := checks.ValidateEnvMap(overrides); err != nil {
		return nil, err
	}
	for key := range overrides {
		if isBlockedHostEnvKey(key) {
			return nil, fmt.Errorf("env variable %q is not permitted in check environment", key)
		}
	}
	base := containerClientEnv()
	return append(base, envPairs(overrides)...), nil
}

// writeContainerEnvFile writes validated container environment variables to a
// 0600 temporary file suitable for docker/podman --env-file.
func writeContainerEnvFile(env map[string]string, defaultHome string) (string, func(), error) {
	if err := checks.ValidateEnvMap(env); err != nil {
		return "", nil, err
	}
	if strings.ContainsAny(defaultHome, "\r\n\x00") {
		return "", nil, fmt.Errorf("invalid container HOME value")
	}
	needHome := defaultHome != ""
	if _, hasHome := env["HOME"]; hasHome {
		needHome = false
	}
	if len(env) == 0 && !needHome {
		return "", func() {}, nil
	}

	f, err := os.CreateTemp("", "gitslice-ci-env-*")
	if err != nil {
		return "", nil, fmt.Errorf("create container env file: %w", err)
	}
	name := f.Name()
	cleanup := sync.OnceFunc(func() {
		_ = os.Remove(name)
	})
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		cleanup()
		return "", nil, fmt.Errorf("chmod container env file: %w", err)
	}

	var b strings.Builder
	if needHome {
		b.WriteString("HOME=")
		b.WriteString(defaultHome)
		b.WriteByte('\n')
	}
	for _, key := range sortedEnvKeys(env) {
		b.WriteString(key)
		b.WriteByte('=')
		b.WriteString(env[key])
		b.WriteByte('\n')
	}
	if _, err := f.WriteString(b.String()); err != nil {
		_ = f.Close()
		cleanup()
		return "", nil, fmt.Errorf("write container env file: %w", err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("close container env file: %w", err)
	}
	return name, cleanup, nil
}

func runnerMaxMemoryBytes() int64 {
	if raw := strings.TrimSpace(os.Getenv("GITSLICE_CHECK_MAX_MEMORY")); raw != "" {
		if parsed, err := checks.ParseMemoryBytes(raw); err == nil {
			return parsed
		}
	}
	return defaultMaxContainerMemoryBytes
}

func runnerMaxCPUs() float64 {
	if raw := strings.TrimSpace(os.Getenv("GITSLICE_CHECK_MAX_CPUS")); raw != "" {
		if parsed, err := checks.ParseCPUs(raw); err == nil {
			return parsed
		}
	}
	return defaultMaxContainerCPUs
}

func cappedMemoryLimit(value, fallback string) (string, error) {
	chosen := value
	if chosen == "" {
		chosen = fallback
	}
	bytes, err := checks.ParseMemoryBytes(chosen)
	if err != nil {
		return "", err
	}
	if maxBytes := runnerMaxMemoryBytes(); bytes > maxBytes {
		bytes = maxBytes
	}
	return checks.FormatMemoryBytes(bytes), nil
}

func cappedCPULimit(value, fallback string) (string, error) {
	chosen := value
	if chosen == "" {
		chosen = fallback
	}
	cpus, err := checks.ParseCPUs(chosen)
	if err != nil {
		return "", err
	}
	if maxCPUs := runnerMaxCPUs(); cpus > maxCPUs {
		cpus = maxCPUs
	}
	return checks.FormatCPUs(cpus), nil
}

func containerBuildNetworkArgs(network bool) []string {
	if !network {
		return []string{"--network=none"}
	}
	if HostNetwork() {
		return []string{"--network=" + hostNetwork}
	}
	return nil
}

func workspaceDir(workspaceRoot, logicalDir string) (string, error) {
	if strings.TrimSpace(workspaceRoot) == "" {
		return "", fmt.Errorf("workspace root is required")
	}
	if strings.ContainsAny(logicalDir, "\r\n\x00") {
		return "", fmt.Errorf("working directory contains control characters")
	}
	root, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return "", fmt.Errorf("resolve workspace root: %w", err)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("resolve workspace root symlinks: %w", err)
		}
		realRoot = filepath.Clean(root)
	}
	rel := logicalPathRel(logicalDir)
	dir := filepath.Join(realRoot, filepath.FromSlash(rel))
	if err := ensureInside(realRoot, dir); err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		if err := lexicalInside(realRoot, resolved); err != nil {
			return "", err
		}
		return resolved, nil
	}
	return dir, nil
}

func lexicalInside(root, target string) error {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return fmt.Errorf("resolve workspace-relative path: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("working directory escapes workspace: %s", target)
	}
	return nil
}

// ensureInside verifies both lexically and through os.OpenRoot / EvalSymlinks
// that target stays confined inside root and never traverses a symlink outside root.
func ensureInside(root, target string) error {
	cleanRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve root path: %w", err)
	}
	cleanTarget, err := filepath.Abs(target)
	if err != nil {
		return fmt.Errorf("resolve target path: %w", err)
	}
	if err := lexicalInside(cleanRoot, cleanTarget); err != nil {
		return err
	}

	realRoot, err := filepath.EvalSymlinks(cleanRoot)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("resolve root symlinks: %w", err)
	}

	rel, err := filepath.Rel(cleanRoot, cleanTarget)
	if err != nil {
		return fmt.Errorf("resolve relative path: %w", err)
	}
	if rel == "." {
		return nil
	}

	rootHandle, err := os.OpenRoot(realRoot)
	if err != nil {
		return fmt.Errorf("open workspace root: %w", err)
	}
	defer rootHandle.Close()

	parts := strings.Split(filepath.ToSlash(rel), "/")
	var cur string
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if cur == "" {
			cur = part
		} else {
			cur = cur + "/" + part
		}
		osRel := filepath.FromSlash(cur)
		linfo, lerr := rootHandle.Lstat(osRel)
		if errors.Is(lerr, fs.ErrNotExist) {
			break
		}
		if lerr != nil {
			return fmt.Errorf("working directory escapes workspace: %s: %w", target, lerr)
		}
		if _, serr := rootHandle.Stat(osRel); serr != nil {
			return fmt.Errorf("working directory escapes workspace: %s: %w", target, serr)
		}
		if linfo.Mode()&fs.ModeSymlink != 0 {
			resolved, evalErr := filepath.EvalSymlinks(filepath.Join(realRoot, osRel))
			if evalErr != nil {
				return fmt.Errorf("working directory escapes workspace: %s: %w", target, evalErr)
			}
			if err := lexicalInside(realRoot, resolved); err != nil {
				return err
			}
		}
	}
	return nil
}
