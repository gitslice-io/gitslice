package agentd

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

var serviceNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type ServiceProbes struct {
	OSName       string
	Executable   func() (string, error)
	EvalSymlinks func(string) (string, error)
	LookPath     func(string) (string, error)
	Command      func(context.Context, string, ...string) ([]byte, error)
	Environ      func() []string
	UID          func() int
	UserName     func() string
	UserHome     func() (string, error)
}

func DefaultServiceProbes() ServiceProbes {
	return ServiceProbes{
		OSName:       runtime.GOOS,
		Executable:   os.Executable,
		EvalSymlinks: filepath.EvalSymlinks,
		LookPath:     exec.LookPath,
		Command: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).CombinedOutput()
		},
		Environ: os.Environ,
		UID:     os.Getuid,
		UserName: func() string {
			if name := strings.TrimSpace(os.Getenv("USER")); name != "" {
				return name
			}
			current, err := user.Current()
			if err == nil {
				return current.Username
			}
			return "<user>"
		},
		UserHome: func() (string, error) {
			current, err := user.Current()
			if err != nil {
				return "", err
			}
			return current.HomeDir, nil
		},
	}
}

func MergeServiceProbes(override *ServiceProbes) ServiceProbes {
	p := DefaultServiceProbes()
	if override == nil {
		return p
	}
	if override.OSName != "" {
		p.OSName = override.OSName
	}
	if override.Executable != nil {
		p.Executable = override.Executable
	}
	if override.EvalSymlinks != nil {
		p.EvalSymlinks = override.EvalSymlinks
	}
	if override.LookPath != nil {
		p.LookPath = override.LookPath
	}
	if override.Command != nil {
		p.Command = override.Command
	}
	if override.Environ != nil {
		p.Environ = override.Environ
	}
	if override.UID != nil {
		p.UID = override.UID
	}
	if override.UserName != nil {
		p.UserName = override.UserName
	}
	p.UserHome = override.UserHome
	return p
}

type InstallOptions struct {
	Name   string
	CI     bool
	Print  bool
	Slices []string
}

func DefaultAgentName() string {
	hostname, err := os.Hostname()
	if err == nil && strings.TrimSpace(hostname) != "" {
		return hostname
	}
	return "gs-agent"
}

func ResolvedAgentName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = DefaultAgentName()
	}
	return name
}

func CanonicalSliceKey(account, slice string) string {
	a := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(account), "_", "-"))
	s := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(slice), "_", "-"))
	if a == "" || s == "" {
		return ""
	}
	return a + "/" + s
}

func NormalizedServiceName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = DefaultAgentName()
	}
	if !serviceNamePattern.MatchString(name) {
		return "", NewError("invalid_args", "invalid agent daemon name: "+name, "Use 1-64 letters, digits, dots, underscores, or hyphens, starting with a letter or digit.")
	}
	return name, nil
}

func InstallService(ctx context.Context, p ServiceProbes, home, workingDir string, in InstallOptions, allowedSlices []string, quiet bool, stdout io.Writer) error {
	name, err := NormalizedServiceName(in.Name)
	if err != nil {
		return err
	}
	if p.OSName != "linux" && p.OSName != "darwin" {
		return NewError("unsupported_platform", "gs agent install supports Linux (systemd) and macOS (launchd)", "")
	}
	gs, err := ServiceExecutable(p)
	if err != nil {
		return err
	}
	serviceHome, err := ServiceHome(p, home)
	if err != nil {
		return err
	}
	env := ServiceEnvironment(home, p.Environ())
	definition, path, logPath := ServiceDefinitionWithSlices(p.OSName, serviceHome, name, gs, workingDir, in.CI, allowedSlices, env)
	if in.Print {
		_, err := fmt.Fprint(stdout, definition)
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if p.OSName == "darwin" {
		if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
			return err
		}
	}
	if err := os.WriteFile(path, []byte(definition), 0o644); err != nil {
		return err
	}

	if p.OSName == "linux" {
		if out, err := p.Command(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
			return serviceCommandError("systemctl --user daemon-reload", out, err)
		}
		unit := SystemdUnitName(name)
		if out, err := p.Command(ctx, "systemctl", "--user", "enable", "--now", unit); err != nil {
			return serviceCommandError("systemctl --user enable --now "+unit, out, err)
		}
		linger, err := p.Command(ctx, "loginctl", "show-user", p.UserName(), "-p", "Linger", "--value")
		if err == nil && strings.TrimSpace(string(linger)) == "no" && !quiet {
			fmt.Fprintf(stdout, "run \"sudo loginctl enable-linger %s\" so it also runs while you are logged out\n", p.UserName())
		}
		if !quiet {
			fmt.Fprintf(stdout, "logs: journalctl --user -u %s -f\n", unit)
		}
		return nil
	}

	label := LaunchdLabel(name)
	_, _ = p.Command(ctx, "launchctl", "bootout", fmt.Sprintf("gui/%d/%s", p.UID(), label))
	if out, err := p.Command(ctx, "launchctl", "bootstrap", fmt.Sprintf("gui/%d", p.UID()), path); err != nil {
		return serviceCommandError("launchctl bootstrap", out, err)
	}
	if !quiet {
		fmt.Fprintf(stdout, "logs: %s\n", logPath)
	}
	return nil
}

func UninstallService(ctx context.Context, p ServiceProbes, home, requestedName string, quiet bool, stdout io.Writer) error {
	name, err := NormalizedServiceName(requestedName)
	if err != nil {
		return err
	}
	if p.OSName != "linux" && p.OSName != "darwin" {
		return NewError("unsupported_platform", "gs agent install supports Linux (systemd) and macOS (launchd)", "")
	}
	serviceHome, err := ServiceHome(p, home)
	if err != nil {
		return err
	}
	path := ServiceFilePath(p.OSName, serviceHome, name)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return NewError("agent_not_installed", "no installed agent daemon named "+name, "")
		}
		return err
	}
	if p.OSName == "linux" {
		unit := SystemdUnitName(name)
		if out, err := p.Command(ctx, "systemctl", "--user", "disable", "--now", unit); err != nil {
			return serviceCommandError("systemctl --user disable --now "+unit, out, err)
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		if out, err := p.Command(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
			return serviceCommandError("systemctl --user daemon-reload", out, err)
		}
	} else {
		_, _ = p.Command(ctx, "launchctl", "bootout", fmt.Sprintf("gui/%d/%s", p.UID(), LaunchdLabel(name)))
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	if !quiet {
		fmt.Fprintf(stdout, "uninstalled agent daemon %s\n", name)
	}
	return nil
}

func ServiceExecutable(p ServiceProbes) (string, error) {
	path, err := p.Executable()
	if err != nil {
		return "", err
	}
	resolved, err := p.EvalSymlinks(path)
	if err == nil {
		path = resolved
	}
	if gs, err := p.LookPath("gs"); err == nil {
		if resolvedGS, resolveErr := p.EvalSymlinks(gs); resolveErr == nil && resolvedGS == path {
			return gs, nil
		}
	}
	return path, nil
}

func ServiceEnvironment(home string, environ []string) map[string]string {
	path := ""
	for _, entry := range environ {
		if key, value, ok := strings.Cut(entry, "="); ok && key == "PATH" {
			path = value
			break
		}
	}
	env := map[string]string{"HOME": home, "PATH": path}
	for _, entry := range environ {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || !strings.HasPrefix(key, "GITSLICE_") || ServiceSecretEnv(key) {
			continue
		}
		env[key] = value
	}
	return env
}

func ServiceSecretEnv(name string) bool {
	name = strings.ToUpper(name)
	return strings.Contains(name, "TOKEN") || strings.Contains(name, "SECRET") || strings.Contains(name, "PASSWORD") || strings.Contains(name, "KEY")
}

func ServiceDefinition(osName, home, name, gs, workingDir string, ci bool, env map[string]string) (definition, path, logPath string) {
	return ServiceDefinitionWithSlices(osName, home, name, gs, workingDir, ci, nil, env)
}

func ServiceDefinitionWithSlices(osName, home, name, gs, workingDir string, ci bool, slices []string, env map[string]string) (definition, path, logPath string) {
	switch osName {
	case "linux":
		return RenderSystemdServiceWithSlices(name, gs, workingDir, ci, slices, env), ServiceFilePath(osName, home, name), ""
	case "darwin":
		logPath = filepath.Join(home, "Library", "Logs", "gitslice", "agent-"+name+".log")
		return RenderLaunchdServiceWithSlices(name, gs, workingDir, ci, slices, env, logPath), ServiceFilePath(osName, home, name), logPath
	default:
		return "", "", ""
	}
}

func ServiceHome(p ServiceProbes, home string) (string, error) {
	if p.UserHome != nil {
		if dir, err := p.UserHome(); err == nil && strings.TrimSpace(dir) != "" {
			return dir, nil
		}
	}
	if strings.TrimSpace(home) == "" {
		return "", NewError("no_home", "cannot find your home directory", "Set HOME.")
	}
	return home, nil
}

func ServiceFilePath(osName, serviceHome, name string) string {
	if osName == "darwin" {
		return filepath.Join(serviceHome, "Library", "LaunchAgents", LaunchdLabel(name)+".plist")
	}
	config := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
	if !filepath.IsAbs(config) {
		config = filepath.Join(serviceHome, ".config")
	}
	return filepath.Join(config, "systemd", "user", SystemdUnitName(name))
}

func SystemdUnitName(name string) string { return "gs-agent-" + name + ".service" }

func LaunchdLabel(name string) string { return "io.gitslice.agent." + name }

func systemdQuote(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\"", "\\\"")
	value = strings.ReplaceAll(value, "%", "%%")
	return "\"" + value + "\""
}

func RenderSystemdService(name, gs, workingDir string, ci bool, env map[string]string) string {
	return RenderSystemdServiceWithSlices(name, gs, workingDir, ci, nil, env)
}

func RenderSystemdServiceWithSlices(name, gs, workingDir string, ci bool, slices []string, env map[string]string) string {
	args := []string{systemdQuote(gs), "agent", "start", "--name", systemdQuote(name)}
	if ci {
		args = append(args, "--ci")
	}
	for _, slice := range slices {
		args = append(args, "--slice", systemdQuote(slice))
	}
	keys := sortedServiceEnv(env)
	var b strings.Builder
	fmt.Fprintf(&b, "[Unit]\nDescription=Gitslice agent daemon %s\nWants=network-online.target\nAfter=network-online.target\n\n", name)
	fmt.Fprintf(&b, "[Service]\nExecStart=%s\nWorkingDirectory=%s\n", strings.Join(args, " "), strings.ReplaceAll(workingDir, "%", "%%"))
	for _, key := range keys {
		fmt.Fprintf(&b, "Environment=%s\n", systemdQuote(key+"="+env[key]))
	}
	b.WriteString("Restart=always\nRestartSec=5\n\n[Install]\nWantedBy=default.target\n")
	return b.String()
}

func RenderLaunchdService(name, gs, workingDir string, ci bool, env map[string]string, logPath string) string {
	return RenderLaunchdServiceWithSlices(name, gs, workingDir, ci, nil, env, logPath)
}

func RenderLaunchdServiceWithSlices(name, gs, workingDir string, ci bool, slices []string, env map[string]string, logPath string) string {
	args := []string{gs, "agent", "start", "--name", name}
	if ci {
		args = append(args, "--ci")
	}
	for _, slice := range slices {
		args = append(args, "--slice", slice)
	}
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	b.WriteString("<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n")
	b.WriteString("<plist version=\"1.0\">\n<dict>\n")
	launchdString(&b, "Label", LaunchdLabel(name))
	b.WriteString("  <key>ProgramArguments</key>\n  <array>\n")
	for _, arg := range args {
		fmt.Fprintf(&b, "    <string>%s</string>\n", xmlEscape(arg))
	}
	b.WriteString("  </array>\n")
	launchdString(&b, "WorkingDirectory", workingDir)
	b.WriteString("  <key>EnvironmentVariables</key>\n  <dict>\n")
	for _, key := range sortedServiceEnv(env) {
		fmt.Fprintf(&b, "    <key>%s</key>\n    <string>%s</string>\n", xmlEscape(key), xmlEscape(env[key]))
	}
	b.WriteString("  </dict>\n  <key>RunAtLoad</key>\n  <true/>\n  <key>KeepAlive</key>\n  <true/>\n")
	launchdString(&b, "StandardOutPath", logPath)
	launchdString(&b, "StandardErrorPath", logPath)
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

func sortedServiceEnv(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func launchdString(b *strings.Builder, key, value string) {
	fmt.Fprintf(b, "  <key>%s</key>\n  <string>%s</string>\n", xmlEscape(key), xmlEscape(value))
}

func xmlEscape(value string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(value))
	return b.String()
}

func serviceCommandError(command string, out []byte, err error) error {
	if detail := strings.TrimSpace(string(out)); detail != "" {
		return fmt.Errorf("%s: %w: %s", command, err, detail)
	}
	return fmt.Errorf("%s: %w", command, err)
}
