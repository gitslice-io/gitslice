package cli

import (
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

var agentServiceNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type agentServiceProbes struct {
	osName       string
	executable   func() (string, error)
	evalSymlinks func(string) (string, error)
	lookPath     func(string) (string, error)
	command      func(context.Context, string, ...string) ([]byte, error)
	environ      func() []string
	uid          func() int
	userName     func() string
	// userHome is the account's own home directory, where systemd and
	// launchd look for user services. It can differ from HOME: an operator
	// runs a daemon with HOME pointing at a separate gs identity.
	userHome func() (string, error)
}

func defaultAgentServiceProbes() agentServiceProbes {
	return agentServiceProbes{
		osName:       runtime.GOOS,
		executable:   os.Executable,
		evalSymlinks: filepath.EvalSymlinks,
		lookPath:     exec.LookPath,
		command: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).CombinedOutput()
		},
		environ: os.Environ,
		uid:     os.Getuid,
		userName: func() string {
			if name := strings.TrimSpace(os.Getenv("USER")); name != "" {
				return name
			}
			current, err := user.Current()
			if err == nil {
				return current.Username
			}
			return "<user>"
		},
		userHome: func() (string, error) {
			current, err := user.Current()
			if err != nil {
				return "", err
			}
			return current.HomeDir, nil
		},
	}
}

func (r Runner) agentServiceProbes() agentServiceProbes {
	p := defaultAgentServiceProbes()
	if r.AgentService == nil {
		return p
	}
	if r.AgentService.osName != "" {
		p.osName = r.AgentService.osName
	}
	if r.AgentService.executable != nil {
		p.executable = r.AgentService.executable
	}
	if r.AgentService.evalSymlinks != nil {
		p.evalSymlinks = r.AgentService.evalSymlinks
	}
	if r.AgentService.lookPath != nil {
		p.lookPath = r.AgentService.lookPath
	}
	if r.AgentService.command != nil {
		p.command = r.AgentService.command
	}
	if r.AgentService.environ != nil {
		p.environ = r.AgentService.environ
	}
	if r.AgentService.uid != nil {
		p.uid = r.AgentService.uid
	}
	if r.AgentService.userName != nil {
		p.userName = r.AgentService.userName
	}
	if r.AgentService.userHome != nil {
		p.userHome = r.AgentService.userHome
	}
	return p
}

type agentInstallOptions struct {
	Name  string
	CI    bool
	Print bool
}

func (r Runner) agentInstallCommand(opts *commandOptions) *cobra.Command {
	in := agentInstallOptions{}
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install an agent daemon as a user service",
		Args:  noArgs("gs agent install [--ci] [--name NAME] [--print]"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return r.runAgentInstall(cmd.Context(), *opts, in)
		},
	}
	cmd.Flags().BoolVar(&in.CI, "ci", false, "install a checks-only daemon")
	cmd.Flags().StringVar(&in.Name, "name", "", "daemon name (defaults to this machine name)")
	cmd.Flags().BoolVar(&in.Print, "print", false, "print the service definition without installing it")
	return cmd
}

func (r Runner) agentUninstallCommand(opts *commandOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall [NAME]",
		Short: "Stop and remove an installed agent daemon",
		Args:  maxArgs(1, "gs agent uninstall [NAME]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			return r.runAgentUninstall(cmd.Context(), *opts, name)
		},
	}
}

func normalizedAgentServiceName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = defaultAgentName()
	}
	if !agentServiceNamePattern.MatchString(name) {
		return "", userError("invalid_args", "invalid agent daemon name: "+name, "Use 1-64 letters, digits, dots, underscores, or hyphens, starting with a letter or digit.")
	}
	return name, nil
}

func (r Runner) runAgentInstall(ctx context.Context, opts commandOptions, in agentInstallOptions) error {
	name, err := normalizedAgentServiceName(in.Name)
	if err != nil {
		return err
	}
	p := r.agentServiceProbes()
	if p.osName != "linux" && p.osName != "darwin" {
		return userError("unsupported_platform", "gs agent install supports Linux (systemd) and macOS (launchd)", "")
	}

	workingDir, err := r.agentServiceWorkingDir(in.CI)
	if err != nil {
		return err
	}
	gs, err := agentServiceExecutable(p)
	if err != nil {
		return err
	}
	home := r.homeDir()
	serviceHome, err := agentServiceHome(p, home)
	if err != nil {
		return err
	}
	env := agentServiceEnvironment(home, p.environ())
	definition, path, logPath := agentServiceDefinition(p.osName, serviceHome, name, gs, workingDir, in.CI, env)
	if in.Print {
		_, err := fmt.Fprint(r.stdout(), definition)
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if p.osName == "darwin" {
		if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
			return err
		}
	}
	if err := os.WriteFile(path, []byte(definition), 0o644); err != nil {
		return err
	}

	if p.osName == "linux" {
		if out, err := p.command(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
			return agentServiceCommandError("systemctl --user daemon-reload", out, err)
		}
		unit := agentSystemdUnitName(name)
		if out, err := p.command(ctx, "systemctl", "--user", "enable", "--now", unit); err != nil {
			return agentServiceCommandError("systemctl --user enable --now "+unit, out, err)
		}
		linger, err := p.command(ctx, "loginctl", "show-user", p.userName(), "-p", "Linger", "--value")
		if err == nil && strings.TrimSpace(string(linger)) == "no" && !opts.Quiet {
			fmt.Fprintf(r.stdout(), "run \"sudo loginctl enable-linger %s\" so it also runs while you are logged out\n", p.userName())
		}
		if !opts.Quiet {
			fmt.Fprintf(r.stdout(), "logs: journalctl --user -u %s -f\n", unit)
		}
		return nil
	}

	label := agentLaunchdLabel(name)
	_, _ = p.command(ctx, "launchctl", "bootout", fmt.Sprintf("gui/%d/%s", p.uid(), label))
	if out, err := p.command(ctx, "launchctl", "bootstrap", fmt.Sprintf("gui/%d", p.uid()), path); err != nil {
		return agentServiceCommandError("launchctl bootstrap", out, err)
	}
	if !opts.Quiet {
		fmt.Fprintf(r.stdout(), "logs: %s\n", logPath)
	}
	return nil
}

func (r Runner) runAgentUninstall(ctx context.Context, opts commandOptions, requestedName string) error {
	name, err := normalizedAgentServiceName(requestedName)
	if err != nil {
		return err
	}
	p := r.agentServiceProbes()
	if p.osName != "linux" && p.osName != "darwin" {
		return userError("unsupported_platform", "gs agent install supports Linux (systemd) and macOS (launchd)", "")
	}
	serviceHome, err := agentServiceHome(p, r.homeDir())
	if err != nil {
		return err
	}
	path := agentServiceFilePath(p.osName, serviceHome, name)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return userError("agent_not_installed", "no installed agent daemon named "+name, "")
		}
		return err
	}
	if p.osName == "linux" {
		unit := agentSystemdUnitName(name)
		if out, err := p.command(ctx, "systemctl", "--user", "disable", "--now", unit); err != nil {
			return agentServiceCommandError("systemctl --user disable --now "+unit, out, err)
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		if out, err := p.command(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
			return agentServiceCommandError("systemctl --user daemon-reload", out, err)
		}
	} else {
		_, _ = p.command(ctx, "launchctl", "bootout", fmt.Sprintf("gui/%d/%s", p.uid(), agentLaunchdLabel(name)))
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	if !opts.Quiet {
		fmt.Fprintf(r.stdout(), "uninstalled agent daemon %s\n", name)
	}
	return nil
}

func (r Runner) agentServiceWorkingDir(ci bool) (string, error) {
	if ci {
		return filepath.Abs(r.homeDir())
	}
	if err := r.requireAgentWorkspaceDir(); err != nil {
		return "", err
	}
	return filepath.Abs(r.cwd())
}

func agentServiceExecutable(p agentServiceProbes) (string, error) {
	path, err := p.executable()
	if err != nil {
		return "", err
	}
	resolved, err := p.evalSymlinks(path)
	if err == nil {
		path = resolved
	}
	if gs, err := p.lookPath("gs"); err == nil {
		if resolvedGS, resolveErr := p.evalSymlinks(gs); resolveErr == nil && resolvedGS == path {
			return gs, nil
		}
	}
	return path, nil
}

func agentServiceEnvironment(home string, environ []string) map[string]string {
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
		if !ok || !strings.HasPrefix(key, "GITSLICE_") || agentServiceSecretEnv(key) {
			continue
		}
		env[key] = value
	}
	return env
}

func agentServiceSecretEnv(name string) bool {
	name = strings.ToUpper(name)
	return strings.Contains(name, "TOKEN") || strings.Contains(name, "SECRET") || strings.Contains(name, "PASSWORD") || strings.Contains(name, "KEY")
}

func agentServiceDefinition(osName, home, name, gs, workingDir string, ci bool, env map[string]string) (definition, path, logPath string) {
	switch osName {
	case "linux":
		return renderSystemdAgentService(name, gs, workingDir, ci, env), agentServiceFilePath(osName, home, name), ""
	case "darwin":
		logPath = filepath.Join(home, "Library", "Logs", "gitslice", "agent-"+name+".log")
		return renderLaunchdAgentService(name, gs, workingDir, ci, env, logPath), agentServiceFilePath(osName, home, name), logPath
	default:
		return "", "", ""
	}
}

// agentServiceHome is the home directory the service manager reads user
// services from: the account's own, falling back to HOME.
func agentServiceHome(p agentServiceProbes, home string) (string, error) {
	if p.userHome != nil {
		if dir, err := p.userHome(); err == nil && strings.TrimSpace(dir) != "" {
			return dir, nil
		}
	}
	if strings.TrimSpace(home) == "" {
		return "", userError("no_home", "cannot find your home directory", "Set HOME.")
	}
	return home, nil
}

// agentServiceFilePath is where the unit or plist goes, under the account's
// home (agentServiceHome). XDG_CONFIG_HOME is honored only when it is
// absolute, as the systemd user manager reads it.
func agentServiceFilePath(osName, serviceHome, name string) string {
	if osName == "darwin" {
		return filepath.Join(serviceHome, "Library", "LaunchAgents", agentLaunchdLabel(name)+".plist")
	}
	config := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
	if !filepath.IsAbs(config) {
		config = filepath.Join(serviceHome, ".config")
	}
	return filepath.Join(config, "systemd", "user", agentSystemdUnitName(name))
}

func agentSystemdUnitName(name string) string { return "gs-agent-" + name + ".service" }

func agentLaunchdLabel(name string) string { return "io.gitslice.agent." + name }

func systemdQuote(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\"", "\\\"")
	value = strings.ReplaceAll(value, "%", "%%")
	return "\"" + value + "\""
}

func renderSystemdAgentService(name, gs, workingDir string, ci bool, env map[string]string) string {
	args := []string{systemdQuote(gs), "agent", "start", "--name", systemdQuote(name)}
	if ci {
		args = append(args, "--ci")
	}
	keys := sortedAgentServiceEnv(env)
	var b strings.Builder
	fmt.Fprintf(&b, "[Unit]\nDescription=Gitslice agent daemon %s\nWants=network-online.target\nAfter=network-online.target\n\n", name)
	// WorkingDirectory takes a bare path: systemd does not unquote it.
	fmt.Fprintf(&b, "[Service]\nExecStart=%s\nWorkingDirectory=%s\n", strings.Join(args, " "), strings.ReplaceAll(workingDir, "%", "%%"))
	for _, key := range keys {
		fmt.Fprintf(&b, "Environment=%s\n", systemdQuote(key+"="+env[key]))
	}
	b.WriteString("Restart=always\nRestartSec=5\n\n[Install]\nWantedBy=default.target\n")
	return b.String()
}

func renderLaunchdAgentService(name, gs, workingDir string, ci bool, env map[string]string, logPath string) string {
	args := []string{gs, "agent", "start", "--name", name}
	if ci {
		args = append(args, "--ci")
	}
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	b.WriteString("<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n")
	b.WriteString("<plist version=\"1.0\">\n<dict>\n")
	launchdString(&b, "Label", agentLaunchdLabel(name))
	b.WriteString("  <key>ProgramArguments</key>\n  <array>\n")
	for _, arg := range args {
		fmt.Fprintf(&b, "    <string>%s</string>\n", xmlEscape(arg))
	}
	b.WriteString("  </array>\n")
	launchdString(&b, "WorkingDirectory", workingDir)
	b.WriteString("  <key>EnvironmentVariables</key>\n  <dict>\n")
	for _, key := range sortedAgentServiceEnv(env) {
		fmt.Fprintf(&b, "    <key>%s</key>\n    <string>%s</string>\n", xmlEscape(key), xmlEscape(env[key]))
	}
	b.WriteString("  </dict>\n  <key>RunAtLoad</key>\n  <true/>\n  <key>KeepAlive</key>\n  <true/>\n")
	launchdString(&b, "StandardOutPath", logPath)
	launchdString(&b, "StandardErrorPath", logPath)
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

func sortedAgentServiceEnv(env map[string]string) []string {
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

func agentServiceCommandError(command string, out []byte, err error) error {
	if detail := strings.TrimSpace(string(out)); detail != "" {
		return fmt.Errorf("%s: %w: %s", command, err, detail)
	}
	return fmt.Errorf("%s: %w", command, err)
}
