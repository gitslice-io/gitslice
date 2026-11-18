package agentd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/checkexec"
)

const DoctorStepTimeout = 3 * time.Second

type DoctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type DoctorOutput struct {
	Checks []DoctorCheck `json:"checks"`
}

// DoctorProbes keeps host inspection behind a narrow seam for deterministic tests.
type DoctorProbes struct {
	DetectRuntime func(context.Context) checkexec.RuntimeInfo
	CacheRoot     func() (string, error)
	FreeBytes     func(string) (uint64, error)
	LookPath      func(string) (string, error)
	CommandOutput func(context.Context, string, ...string) ([]byte, error)
}

func DefaultDoctorProbes() DoctorProbes {
	return DoctorProbes{
		DetectRuntime: checkexec.DetectRuntime,
		CacheRoot:     checkexec.CacheRoot,
		FreeBytes:     DoctorFreeBytes,
		LookPath:      exec.LookPath,
		CommandOutput: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).CombinedOutput()
		},
	}
}

func MergeDoctorProbes(override *DoctorProbes) DoctorProbes {
	p := DefaultDoctorProbes()
	if override == nil {
		return p
	}
	if override.DetectRuntime != nil {
		p.DetectRuntime = override.DetectRuntime
	}
	if override.CacheRoot != nil {
		p.CacheRoot = override.CacheRoot
	}
	if override.FreeBytes != nil {
		p.FreeBytes = override.FreeBytes
	}
	if override.LookPath != nil {
		p.LookPath = override.LookPath
	}
	if override.CommandOutput != nil {
		p.CommandOutput = override.CommandOutput
	}
	return p
}

// DoctorConfig carries the caller's parsed auth config for doctor checks.
type DoctorConfig struct {
	Token         string
	SubjectID     string
	ConfigPath    string
	ConfigErr     error
	DescribeToken func(string) (string, time.Time)
}

func DoctorFailure(checks []DoctorCheck) error {
	failed := 0
	for _, check := range checks {
		if check.Status == "fail" {
			failed++
		}
	}
	if failed == 0 {
		return nil
	}
	return NewError("agent_doctor_failed", fmt.Sprintf("%d check(s) failed", failed), "")
}

func RunDoctorChecks(ctx context.Context, ci bool, cfg DoctorConfig, probes DoctorProbes) []DoctorCheck {
	checks := make([]DoctorCheck, 0, 6)
	checks = append(checks, doctorSignedIn(cfg))
	checks = append(checks, doctorRuntime(ctx, probes))
	checks = append(checks, doctorDisk(probes))
	checks = append(checks, doctorToolchains(ctx, probes))
	if !ci {
		checks = append(checks, doctorRuntimeBinary(probes))
		checks = append(checks, doctorLoginExposure(cfg))
	}
	return checks
}

func doctorLoginExposure(cfg DoctorConfig) DoctorCheck {
	const name = "agent isolation"
	if cfg.ConfigErr != nil || strings.TrimSpace(cfg.Token) == "" {
		return DoctorCheck{Name: name, Status: "warn", Detail: "not signed in; see signed in"}
	}
	if strings.HasPrefix(strings.TrimSpace(cfg.Token), "gsk_") {
		return DoctorCheck{Name: name, Status: "ok", Detail: fmt.Sprintf("agents run as this OS user and can read the API key in %s; keep it limited to the daemon's slices (gs auth keys list shows its scope)", cfg.ConfigPath)}
	}
	return DoctorCheck{Name: name, Status: "warn", Detail: fmt.Sprintf("agents get their own short-lived key, but they run as this OS user and can read your full sign-in in %s; run the daemon as a separate OS user signed in with a limited key (gs auth keys create --slice <account/slice> --role writer, then gs auth use-key)", cfg.ConfigPath)}
}

func doctorSignedIn(cfg DoctorConfig) DoctorCheck {
	if cfg.ConfigErr != nil {
		return DoctorCheck{Name: "signed in", Status: "fail", Detail: "cannot read config: " + cfg.ConfigErr.Error() + "; run gs auth login"}
	}
	if strings.TrimSpace(cfg.Token) == "" {
		return DoctorCheck{Name: "signed in", Status: "fail", Detail: "not signed in; run gs auth login"}
	}
	if cfg.DescribeToken != nil {
		_, expires := cfg.DescribeToken(cfg.Token)
		if !expires.IsZero() && time.Now().After(expires) {
			return DoctorCheck{Name: "signed in", Status: "fail", Detail: "saved sign-in has expired; run gs auth login"}
		}
	}
	detail := "signed in"
	if subject := strings.TrimSpace(cfg.SubjectID); subject != "" {
		detail += " as " + subject
	}
	return DoctorCheck{Name: "signed in", Status: "ok", Detail: detail}
}

func doctorRuntime(ctx context.Context, p DoctorProbes) DoctorCheck {
	info := p.DetectRuntime(ctx)
	if !info.OK() {
		detail := "no usable container runtime"
		if info.Err != nil {
			detail = info.Err.Error()
		}
		return DoctorCheck{Name: "container runtime", Status: "warn", Detail: detail + "; checks with image or build will error"}
	}
	if info.Rootless {
		detail := info.Name + " (rootless)"
		if info.Name == "podman" {
			detail = "podman (rootless; keep-id)"
		}
		return DoctorCheck{Name: "container runtime", Status: "ok", Detail: detail}
	}
	return DoctorCheck{Name: "container runtime", Status: "ok", Detail: fmt.Sprintf("%s (rootful; checks run as uid %d:%d)", info.Name, os.Getuid(), os.Getgid())}
}

func doctorDisk(p DoctorProbes) DoctorCheck {
	temp, tempErr := p.FreeBytes(os.TempDir())
	cacheRoot, cacheErr := p.CacheRoot()
	cache := uint64(0)
	if cacheErr == nil {
		cache, cacheErr = p.FreeBytes(nearestExistingAncestor(cacheRoot))
	}
	if errors.Is(tempErr, ErrDoctorDiskUnknown) || errors.Is(cacheErr, ErrDoctorDiskUnknown) {
		return DoctorCheck{Name: "disk", Status: "warn", Detail: "unknown"}
	}
	if tempErr != nil || cacheErr != nil {
		detail := ""
		if tempErr != nil {
			detail = "temp: " + tempErr.Error()
		}
		if cacheErr != nil {
			if detail != "" {
				detail += "; "
			}
			detail += "cache: " + cacheErr.Error()
		}
		return DoctorCheck{Name: "disk", Status: "warn", Detail: detail}
	}
	detail := fmt.Sprintf("temp: %s free; cache: %s free", doctorBytes(temp), doctorBytes(cache))
	minimum := temp
	if cache < minimum {
		minimum = cache
	}
	const gib = uint64(1024 * 1024 * 1024)
	switch {
	case minimum < gib:
		return DoctorCheck{Name: "disk", Status: "fail", Detail: detail}
	case minimum < 5*gib:
		return DoctorCheck{Name: "disk", Status: "warn", Detail: detail}
	default:
		return DoctorCheck{Name: "disk", Status: "ok", Detail: detail}
	}
}

func nearestExistingAncestor(path string) string {
	path = filepath.Clean(path)
	for {
		if _, err := os.Stat(path); err == nil {
			return path
		}
		parent := filepath.Dir(path)
		if parent == path {
			return path
		}
		path = parent
	}
}

func doctorBytes(value uint64) string {
	return fmt.Sprintf("%.1f GiB", float64(value)/(1024*1024*1024))
}

func doctorToolchains(ctx context.Context, p DoctorProbes) DoctorCheck {
	tools := []struct {
		name string
		args []string
	}{{"go", []string{"version"}}, {"node", []string{"--version"}}, {"git", []string{"--version"}}}
	versions := make([]string, 0, len(tools))
	missing := make([]string, 0, len(tools))
	for _, tool := range tools {
		path, err := p.LookPath(tool.name)
		if err != nil {
			missing = append(missing, tool.name)
			continue
		}
		stepCtx, cancel := context.WithTimeout(ctx, DoctorStepTimeout)
		out, err := p.CommandOutput(stepCtx, path, tool.args...)
		cancel()
		if err != nil || strings.TrimSpace(string(out)) == "" {
			missing = append(missing, tool.name)
			continue
		}
		versions = append(versions, strings.TrimSpace(string(out)))
	}
	detail := strings.Join(versions, "; ")
	if len(missing) > 0 {
		if detail != "" {
			detail += "; "
		}
		detail += "missing: " + strings.Join(missing, ", ") + "; checks without image or build run on this host and need them"
		return DoctorCheck{Name: "host toolchains", Status: "warn", Detail: detail}
	}
	return DoctorCheck{Name: "host toolchains", Status: "ok", Detail: detail}
}

func doctorRuntimeBinary(p DoctorProbes) DoctorCheck {
	if _, err := p.LookPath(DefaultRuntimeName); err != nil {
		return DoctorCheck{Name: "agent runtime", Status: "fail", Detail: "codex is not on PATH"}
	}
	return DoctorCheck{Name: "agent runtime", Status: "ok", Detail: "codex found on PATH"}
}
