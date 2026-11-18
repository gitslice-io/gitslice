package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/agentd"
	"gitslice.io/gitslice/internal/checkexec"
)

func TestAgentStartCIFlagsAreExclusive(t *testing.T) {
	for _, args := range [][]string{
		{"agent", "start", "--ci", "--lazy"},
		{"agent", "start", "--ci", "--runtime", "codex"},
	} {
		err := (Runner{Home: t.TempDir(), Dir: t.TempDir()}).Run(context.Background(), args)
		if !isUserErrorCode(err, "invalid_args") {
			t.Fatalf("%v: error = %v, want invalid_args", args, err)
		}
	}
}

func TestAgentDoctorInjectedChecksAndFailure(t *testing.T) {
	home := t.TempDir()
	var stdout bytes.Buffer
	r := Runner{
		Home:   home,
		Stdout: &stdout,
		AgentDoctorProbes: &agentd.DoctorProbes{
			DetectRuntime: func(context.Context) checkexec.RuntimeInfo {
				return checkexec.RuntimeInfo{Err: errors.New("docker is stopped")}
			},
			CacheRoot: func() (string, error) { return filepath.Join(home, "cache", "missing"), nil },
			FreeBytes: func(string) (uint64, error) { return 512 * 1024 * 1024, nil },
			LookPath: func(name string) (string, error) {
				if name == "go" {
					return "/tools/go", nil
				}
				return "", os.ErrNotExist
			},
			CommandOutput: func(_ context.Context, name string, _ ...string) ([]byte, error) {
				if name != "/tools/go" {
					t.Fatalf("command = %q", name)
				}
				return []byte("go version go1.24.0 linux/amd64\n"), nil
			},
		},
	}
	err := r.runAgentDoctor(context.Background(), commandOptions{}, false)
	if !isUserErrorCode(err, "agent_doctor_failed") || err.Error() != "3 check(s) failed" {
		t.Fatalf("error = %v, want three doctor failures", err)
	}
	text := stdout.String()
	for _, want := range []string{
		"fail  signed in  not signed in; run gs auth login",
		"warn  container runtime  docker is stopped; checks with image or build will error",
		"fail  disk  temp: 0.5 GiB free; cache: 0.5 GiB free",
		"warn  host toolchains  go version go1.24.0 linux/amd64; missing: node, git; checks without image or build run on this host and need them",
		"fail  agent runtime  codex is not on PATH",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %q:\n%s", want, text)
		}
	}
}

func TestAgentDoctorCIJSONOmitsRuntimeAndUsesInjectedStatuses(t *testing.T) {
	home := t.TempDir()
	r := Runner{
		Home: home,
		AgentDoctorProbes: &agentd.DoctorProbes{
			DetectRuntime: func(context.Context) checkexec.RuntimeInfo {
				return checkexec.RuntimeInfo{Name: "podman", Path: "/bin/podman", Rootless: true}
			},
			CacheRoot: func() (string, error) { return home, nil },
			FreeBytes: func(string) (uint64, error) { return 10 * 1024 * 1024 * 1024, nil },
			LookPath:  func(string) (string, error) { return "/tool", nil },
			CommandOutput: func(_ context.Context, name string, _ ...string) ([]byte, error) {
				return []byte(filepath.Base(name) + " version\n"), nil
			},
		},
	}
	if err := r.writeUserConfig(UserConfig{Token: "gsk_test", SubjectID: "ci"}); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	r.Stdout = &stdout
	if err := r.runAgentDoctor(context.Background(), commandOptions{Format: "json"}, true); err != nil {
		t.Fatal(err)
	}
	var out agentd.DoctorOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("JSON = %q: %v", stdout.String(), err)
	}
	if len(out.Checks) != 4 {
		t.Fatalf("checks = %#v, want ci checks without agent runtime", out.Checks)
	}
	if got := out.Checks[1]; got.Name != "container runtime" || got.Status != "ok" || got.Detail != "podman (rootless; keep-id)" {
		t.Fatalf("runtime check = %#v", got)
	}
}

func TestAgentServiceRenderGolden(t *testing.T) {
	env := map[string]string{
		"HOME":             `/tmp/a%"b\\c`,
		"PATH":             "/bin:/usr/bin",
		"GITSLICE_FEATURE": "a&b",
	}
	if got, want := agentd.RenderSystemdService("ci.1", "/Applications/gs tool", `/work/a%"b\\c`, true, env), `[Unit]
Description=Gitslice agent daemon ci.1
Wants=network-online.target
After=network-online.target

[Service]
ExecStart="/Applications/gs tool" agent start --name "ci.1" --ci
WorkingDirectory=/work/a%%"b\\c
Environment="GITSLICE_FEATURE=a&b"
Environment="HOME=/tmp/a%%\"b\\\\c"
Environment="PATH=/bin:/usr/bin"
Restart=always
RestartSec=5

[Install]
WantedBy=default.target
`; got != want {
		t.Fatalf("systemd unit =\n%s\nwant:\n%s", got, want)
	}

	if got, want := agentd.RenderLaunchdService("ci.1", "/Applications/gs&tool", "/work/a", true, env, "/logs/a&b.log"), `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>io.gitslice.agent.ci.1</string>
  <key>ProgramArguments</key>
  <array>
    <string>/Applications/gs&amp;tool</string>
    <string>agent</string>
    <string>start</string>
    <string>--name</string>
    <string>ci.1</string>
    <string>--ci</string>
  </array>
  <key>WorkingDirectory</key>
  <string>/work/a</string>
  <key>EnvironmentVariables</key>
  <dict>
    <key>GITSLICE_FEATURE</key>
    <string>a&amp;b</string>
    <key>HOME</key>
    <string>/tmp/a%&#34;b\\c</string>
    <key>PATH</key>
    <string>/bin:/usr/bin</string>
  </dict>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>/logs/a&amp;b.log</string>
  <key>StandardErrorPath</key>
  <string>/logs/a&amp;b.log</string>
</dict>
</plist>
`; got != want {
		t.Fatalf("launchd plist =\n%s\nwant:\n%s", got, want)
	}
}

func TestAgentInstallAndUninstallLinux(t *testing.T) {
	home := t.TempDir()
	accountHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "config")
	var calls [][]string
	p := &agentd.ServiceProbes{
		OSName:     "linux",
		Executable: func() (string, error) { return "/opt/gs-real", nil },
		EvalSymlinks: func(path string) (string, error) {
			return map[string]string{"/opt/gs-real": "/opt/gs-real", "/usr/local/bin/gs": "/opt/gs-real"}[path], nil
		},
		LookPath: func(string) (string, error) { return "/usr/local/bin/gs", nil },
		Environ:  func() []string { return []string{"PATH=/tools", "GITSLICE_TRACE=1", "GITSLICE_API_TOKEN=hide"} },
		UserName: func() string { return "ci-user" },
		UserHome: func() (string, error) { return accountHome, nil },
		Command: func(_ context.Context, name string, args ...string) ([]byte, error) {
			calls = append(calls, append([]string{name}, args...))
			if name == "loginctl" {
				return []byte("no\n"), nil
			}
			return nil, nil
		},
	}
	var stdout bytes.Buffer
	r := Runner{Home: home, Dir: t.TempDir(), Stdout: &stdout, AgentService: p}
	if err := r.runAgentInstall(context.Background(), commandOptions{}, agentd.InstallOptions{Name: "ci-1", CI: true}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(accountHome, ".config", "systemd", "user", "gs-agent-ci-1.service")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, `ExecStart="/usr/local/bin/gs" agent start --name "ci-1" --ci`) || !strings.Contains(text, "Environment=\"GITSLICE_TRACE=1\"") || strings.Contains(text, "API_TOKEN") || !strings.Contains(text, "Environment=\"HOME="+home+"\"") || !strings.Contains(text, "WorkingDirectory="+home+"\n") {
		t.Fatalf("unit =\n%s", text)
	}
	if !reflect.DeepEqual(calls, [][]string{{"systemctl", "--user", "daemon-reload"}, {"systemctl", "--user", "enable", "--now", "gs-agent-ci-1.service"}, {"loginctl", "show-user", "ci-user", "-p", "Linger", "--value"}}) {
		t.Fatalf("install commands = %#v", calls)
	}
	if !strings.Contains(stdout.String(), `sudo loginctl enable-linger ci-user`) || !strings.Contains(stdout.String(), "journalctl --user -u gs-agent-ci-1.service -f") {
		t.Fatalf("install output = %q", stdout.String())
	}

	calls = nil
	if err := r.runAgentUninstall(context.Background(), commandOptions{}, "ci-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("unit remains after uninstall: %v", err)
	}
	if !reflect.DeepEqual(calls, [][]string{{"systemctl", "--user", "disable", "--now", "gs-agent-ci-1.service"}, {"systemctl", "--user", "daemon-reload"}}) {
		t.Fatalf("uninstall commands = %#v", calls)
	}
}

func TestAgentInstallAndUninstallLaunchd(t *testing.T) {
	home := t.TempDir()
	var calls [][]string
	p := &agentd.ServiceProbes{
		OSName:       "darwin",
		Executable:   func() (string, error) { return "/opt/gs", nil },
		EvalSymlinks: func(path string) (string, error) { return path, nil },
		LookPath:     func(string) (string, error) { return "", os.ErrNotExist },
		Environ:      func() []string { return []string{"PATH=/tools"} },
		UID:          func() int { return 501 },
		UserHome:     func() (string, error) { return home, nil },
		Command: func(_ context.Context, name string, args ...string) ([]byte, error) {
			calls = append(calls, append([]string{name}, args...))
			return nil, nil
		},
	}
	r := Runner{Home: home, Dir: t.TempDir(), AgentService: p}
	if err := r.runAgentInstall(context.Background(), commandOptions{Quiet: true}, agentd.InstallOptions{Name: "mac-ci", CI: true}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "Library", "LaunchAgents", "io.gitslice.agent.mac-ci.plist")
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "Library", "Logs", "gitslice")); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, [][]string{{"launchctl", "bootout", "gui/501/io.gitslice.agent.mac-ci"}, {"launchctl", "bootstrap", "gui/501", path}}) {
		t.Fatalf("install commands = %#v", calls)
	}
	calls = nil
	if err := r.runAgentUninstall(context.Background(), commandOptions{}, "mac-ci"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("plist remains after uninstall: %v", err)
	}
	if !reflect.DeepEqual(calls, [][]string{{"launchctl", "bootout", "gui/501/io.gitslice.agent.mac-ci"}}) {
		t.Fatalf("uninstall commands = %#v", calls)
	}
}

func TestAgentInstallPrintAndInvalidName(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "config")
	called := false
	p := &agentd.ServiceProbes{
		OSName:       "linux",
		Executable:   func() (string, error) { return "/opt/gs", nil },
		EvalSymlinks: func(path string) (string, error) { return path, nil },
		LookPath:     func(string) (string, error) { return "", os.ErrNotExist },
		Environ:      func() []string { return []string{"PATH=/tools"} },
		Command:      func(context.Context, string, ...string) ([]byte, error) { called = true; return nil, nil },
	}
	var stdout bytes.Buffer
	r := Runner{Home: home, Dir: t.TempDir(), Stdout: &stdout, AgentService: p}
	if err := r.runAgentInstall(context.Background(), commandOptions{}, agentd.InstallOptions{Name: "ci", CI: true, Print: true}); err != nil {
		t.Fatal(err)
	}
	if called || stdout.Len() == 0 {
		t.Fatalf("--print called=%t output=%q", called, stdout.String())
	}
	path := filepath.Join(home, "config", "systemd", "user", "gs-agent-ci.service")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("--print wrote %s: %v", path, err)
	}
	if err := r.runAgentInstall(context.Background(), commandOptions{}, agentd.InstallOptions{Name: "bad/name", CI: true}); !isUserErrorCode(err, "invalid_args") {
		t.Fatalf("invalid name error = %v", err)
	}
}

func TestAgentInstallWithoutAccountHomeProbeStaysUnderHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	p := &agentd.ServiceProbes{
		OSName:       "linux",
		Executable:   func() (string, error) { return "/opt/gs", nil },
		EvalSymlinks: func(path string) (string, error) { return path, nil },
		LookPath:     func(string) (string, error) { return "", os.ErrNotExist },
		Environ:      func() []string { return []string{"PATH=/tools"} },
		UserName:     func() string { return "ci-user" },
		Command:      func(context.Context, string, ...string) ([]byte, error) { return nil, nil },
	}
	r := Runner{Home: home, Dir: t.TempDir(), AgentService: p}
	if err := r.runAgentInstall(context.Background(), commandOptions{Quiet: true}, agentd.InstallOptions{Name: "guarded", CI: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "systemd", "user", "gs-agent-guarded.service")); err != nil {
		t.Fatalf("unit not under HOME: %v", err)
	}
}
