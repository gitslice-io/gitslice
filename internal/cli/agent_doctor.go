package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gitslice.io/gitslice/internal/checkexec"
)

const agentDoctorStepTimeout = 3 * time.Second

type agentDoctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type agentDoctorOutput struct {
	Checks []agentDoctorCheck `json:"checks"`
}

// agentDoctorProbes keeps host inspection behind a narrow seam. It makes the
// command deterministic in tests without teaching the CLI about Docker's
// implementation details.
type agentDoctorProbes struct {
	detectRuntime func(context.Context) checkexec.RuntimeInfo
	cacheRoot     func() (string, error)
	freeBytes     func(string) (uint64, error)
	lookPath      func(string) (string, error)
	commandOutput func(context.Context, string, ...string) ([]byte, error)
}

func defaultAgentDoctorProbes() agentDoctorProbes {
	return agentDoctorProbes{
		detectRuntime: checkexec.DetectRuntime,
		cacheRoot:     checkexec.CacheRoot,
		freeBytes:     agentDoctorFreeBytes,
		lookPath:      exec.LookPath,
		commandOutput: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).CombinedOutput()
		},
	}
}

func (r Runner) agentDoctorProbes() agentDoctorProbes {
	p := defaultAgentDoctorProbes()
	if r.AgentDoctorProbes == nil {
		return p
	}
	if r.AgentDoctorProbes.detectRuntime != nil {
		p.detectRuntime = r.AgentDoctorProbes.detectRuntime
	}
	if r.AgentDoctorProbes.cacheRoot != nil {
		p.cacheRoot = r.AgentDoctorProbes.cacheRoot
	}
	if r.AgentDoctorProbes.freeBytes != nil {
		p.freeBytes = r.AgentDoctorProbes.freeBytes
	}
	if r.AgentDoctorProbes.lookPath != nil {
		p.lookPath = r.AgentDoctorProbes.lookPath
	}
	if r.AgentDoctorProbes.commandOutput != nil {
		p.commandOutput = r.AgentDoctorProbes.commandOutput
	}
	return p
}

func (r Runner) agentDoctorCommand(opts *commandOptions) *cobra.Command {
	ci := false
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check whether this machine can run an agent daemon",
		Args:  noArgs("gs agent doctor [--ci] [--json]"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return r.runAgentDoctor(cmd.Context(), *opts, ci)
		},
	}
	cmd.Flags().BoolVar(&ci, "ci", false, "check a checks-only CI daemon")
	return cmd
}

func (r Runner) runAgentDoctor(ctx context.Context, opts commandOptions, ci bool) error {
	checks := r.agentDoctorChecks(ctx, ci)
	if opts.jsonOutput() {
		if err := r.writeJSONOutput(opts, agentDoctorOutput{Checks: checks}); err != nil {
			return err
		}
	} else if !opts.Quiet {
		for _, check := range checks {
			fmt.Fprintf(r.stdout(), "%s  %s  %s\n", check.Status, check.Name, check.Detail)
		}
	}
	return agentDoctorFailure(checks)
}

func agentDoctorFailure(checks []agentDoctorCheck) error {
	failed := 0
	for _, check := range checks {
		if check.Status == "fail" {
			failed++
		}
	}
	if failed == 0 {
		return nil
	}
	return userError("agent_doctor_failed", fmt.Sprintf("%d check(s) failed", failed), "")
}

func (r Runner) agentDoctorChecks(ctx context.Context, ci bool) []agentDoctorCheck {
	p := r.agentDoctorProbes()
	checks := make([]agentDoctorCheck, 0, 5)
	checks = append(checks, r.agentDoctorSignedIn())
	checks = append(checks, agentDoctorRuntime(ctx, p))
	checks = append(checks, agentDoctorDisk(p))
	checks = append(checks, agentDoctorToolchains(ctx, p))
	if !ci {
		checks = append(checks, agentDoctorRuntimeBinary(p))
	}
	return checks
}

func (r Runner) agentDoctorSignedIn() agentDoctorCheck {
	cfg, err := r.readPartialUserConfig()
	if err != nil {
		return agentDoctorCheck{Name: "signed in", Status: "fail", Detail: "cannot read config: " + err.Error() + "; run gs auth login"}
	}
	if strings.TrimSpace(cfg.Token) == "" {
		return agentDoctorCheck{Name: "signed in", Status: "fail", Detail: "not signed in; run gs auth login"}
	}
	_, expires := describeToken(cfg.Token)
	if !expires.IsZero() && time.Now().After(expires) {
		return agentDoctorCheck{Name: "signed in", Status: "fail", Detail: "saved sign-in has expired; run gs auth login"}
	}
	detail := "signed in"
	if subject := strings.TrimSpace(cfg.SubjectID); subject != "" {
		detail += " as " + subject
	}
	return agentDoctorCheck{Name: "signed in", Status: "ok", Detail: detail}
}

func agentDoctorRuntime(ctx context.Context, p agentDoctorProbes) agentDoctorCheck {
	info := p.detectRuntime(ctx)
	if !info.OK() {
		detail := "no usable container runtime"
		if info.Err != nil {
			detail = info.Err.Error()
		}
		return agentDoctorCheck{Name: "container runtime", Status: "warn", Detail: detail + "; checks with image or build will error"}
	}
	if info.Rootless {
		detail := info.Name + " (rootless)"
		if info.Name == "podman" {
			detail = "podman (rootless; keep-id)"
		}
		return agentDoctorCheck{Name: "container runtime", Status: "ok", Detail: detail}
	}
	return agentDoctorCheck{Name: "container runtime", Status: "ok", Detail: fmt.Sprintf("%s (rootful; checks run as uid %d:%d)", info.Name, os.Getuid(), os.Getgid())}
}

func agentDoctorDisk(p agentDoctorProbes) agentDoctorCheck {
	temp, tempErr := p.freeBytes(os.TempDir())
	cacheRoot, cacheErr := p.cacheRoot()
	cache := uint64(0)
	if cacheErr == nil {
		cache, cacheErr = p.freeBytes(nearestExistingAncestor(cacheRoot))
	}
	if errors.Is(tempErr, errAgentDoctorDiskUnknown) || errors.Is(cacheErr, errAgentDoctorDiskUnknown) {
		return agentDoctorCheck{Name: "disk", Status: "warn", Detail: "unknown"}
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
		return agentDoctorCheck{Name: "disk", Status: "warn", Detail: detail}
	}
	detail := fmt.Sprintf("temp: %s free; cache: %s free", agentDoctorBytes(temp), agentDoctorBytes(cache))
	minimum := temp
	if cache < minimum {
		minimum = cache
	}
	const gib = uint64(1024 * 1024 * 1024)
	switch {
	case minimum < gib:
		return agentDoctorCheck{Name: "disk", Status: "fail", Detail: detail}
	case minimum < 5*gib:
		return agentDoctorCheck{Name: "disk", Status: "warn", Detail: detail}
	default:
		return agentDoctorCheck{Name: "disk", Status: "ok", Detail: detail}
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

func agentDoctorBytes(value uint64) string {
	return fmt.Sprintf("%.1f GiB", float64(value)/(1024*1024*1024))
}

func agentDoctorToolchains(ctx context.Context, p agentDoctorProbes) agentDoctorCheck {
	tools := []struct {
		name string
		args []string
	}{{"go", []string{"version"}}, {"node", []string{"--version"}}, {"git", []string{"--version"}}}
	versions := make([]string, 0, len(tools))
	missing := make([]string, 0, len(tools))
	for _, tool := range tools {
		path, err := p.lookPath(tool.name)
		if err != nil {
			missing = append(missing, tool.name)
			continue
		}
		stepCtx, cancel := context.WithTimeout(ctx, agentDoctorStepTimeout)
		out, err := p.commandOutput(stepCtx, path, tool.args...)
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
		return agentDoctorCheck{Name: "host toolchains", Status: "warn", Detail: detail}
	}
	return agentDoctorCheck{Name: "host toolchains", Status: "ok", Detail: detail}
}

func agentDoctorRuntimeBinary(p agentDoctorProbes) agentDoctorCheck {
	if _, err := p.lookPath(defaultAgentRuntime); err != nil {
		return agentDoctorCheck{Name: "agent runtime", Status: "fail", Detail: "codex is not on PATH"}
	}
	return agentDoctorCheck{Name: "agent runtime", Status: "ok", Detail: "codex found on PATH"}
}
