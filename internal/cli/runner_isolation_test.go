package cli

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// A Runner with its own Home keeps the mount sockets under that home, even
// when the host user has XDG_RUNTIME_DIR and a HOME of their own.
func TestMountRuntimeDirFollowsRunnerHome(t *testing.T) {
	hostRun := shortTempDir(t, "gshostrun-")
	hostHome := shortTempDir(t, "gshosthome-")
	t.Setenv("XDG_RUNTIME_DIR", hostRun)
	t.Setenv("HOME", hostHome)

	if got := (Runner{}).mountRuntimeDir(); got != hostRun {
		t.Fatalf("host runner mountRuntimeDir = %q, want XDG_RUNTIME_DIR %q", got, hostRun)
	}

	isolated := shortTempDir(t, "gsiso-")
	r := Runner{Home: isolated}
	want := filepath.Join(isolated, ".gitslice", "run")
	if got := r.mountRuntimeDir(); got != want {
		t.Fatalf("isolated mountRuntimeDir = %q, want %q (not the host's %q)", got, want, hostRun)
	}
	if got := r.mountControlPath("0123456789abcdef"); filepath.Dir(got) != want {
		t.Fatalf("control socket %q is outside the runner's run dir %q", got, want)
	}
	if got := r.mountSupervisorSocket(); filepath.Dir(got) != want {
		t.Fatalf("supervisor socket %q is outside the runner's run dir %q", got, want)
	}

	// Naming XDG_RUNTIME_DIR in Env is explicit: it is honored.
	named := Runner{Home: isolated, Env: map[string]string{"XDG_RUNTIME_DIR": hostRun}}
	if got := named.mountRuntimeDir(); got != hostRun {
		t.Fatalf("mountRuntimeDir with Env XDG_RUNTIME_DIR = %q, want %q", got, hostRun)
	}
}

// Two homes too long for a Unix socket path must not share the /tmp fallback.
func TestMountRuntimeDirFallbackIsPrivatePerHome(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	long := strings.Repeat("d", maxMountRuntimeDirLen)
	a := Runner{Home: filepath.Join(t.TempDir(), long)}.mountRuntimeDir()
	b := Runner{Home: filepath.Join(t.TempDir(), long)}.mountRuntimeDir()
	host := Runner{Home: ""}.mountRuntimeDir()
	if a == b {
		t.Fatalf("two isolated homes share the fallback runtime dir %q", a)
	}
	if a == host || b == host {
		t.Fatalf("isolated runtime dirs %q, %q collide with the host's %q", a, b, host)
	}
	if len(a) > maxMountRuntimeDirLen {
		t.Fatalf("fallback runtime dir %q is too long for a socket path", a)
	}
}

func TestRunnerGetenvOverridesAndHidesHostCredentials(t *testing.T) {
	t.Setenv("GS_TOKEN", "host-token")
	t.Setenv("SOME_PLAIN_SETTING", "host-value")

	if got := (Runner{}).Getenv("GS_TOKEN"); got != "host-token" {
		t.Fatalf("plain Runner Getenv(GS_TOKEN) = %q, want the host's", got)
	}
	r := Runner{Env: map[string]string{"GS_TOKEN": "child-token"}}
	if got := r.Getenv("GS_TOKEN"); got != "child-token" {
		t.Fatalf("Getenv(GS_TOKEN) = %q, want the override", got)
	}
	hidden := Runner{Env: map[string]string{"UNRELATED": "x"}}
	if got := hidden.Getenv("GS_TOKEN"); got != "" {
		t.Fatalf("isolated Runner sees host GS_TOKEN %q", got)
	}
	if got := hidden.Getenv("SOME_PLAIN_SETTING"); got != "host-value" {
		t.Fatalf("isolated Runner Getenv(plain setting) = %q, want pass-through", got)
	}
}

// The daemon's child Runner carries the conversation's identity and nothing of
// the host's.
func TestAgentDaemonChildRunnerIsScopedToConversation(t *testing.T) {
	t.Setenv("GS_TOKEN", "host-daemon-token")
	hostHome := t.TempDir()
	runHome := t.TempDir()
	workdir := filepath.Join(t.TempDir(), "conversations", "conv-1")
	r := Runner{Home: hostHome, Insecure: true}
	cfg := UserConfig{ServerAddr: "127.0.0.1:9090", Token: "host-daemon-token"}
	child := r.agentChildRunner(cfg, workdir, map[string]string{"HOME": runHome, "GS_TOKEN": "child-token"}, nil)
	if child.Dir != workdir || child.Home != runHome {
		t.Fatalf("child Dir/Home = %q/%q, want %q/%q", child.Dir, child.Home, workdir, runHome)
	}
	if !child.Insecure {
		t.Fatal("child Runner lost the daemon's Insecure setting")
	}
	if got := child.Getenv("GS_TOKEN"); got != "child-token" {
		t.Fatalf("child GS_TOKEN = %q, want child-token", got)
	}
	if got := child.envServerAddrOverride(); got != "127.0.0.1:9090" {
		t.Fatalf("child server address = %q, want the daemon's", got)
	}
	gotCfg, ok, err := child.readEnvUserConfig()
	if err != nil || !ok || gotCfg.Token != "child-token" || gotCfg.ServerAddr != "127.0.0.1:9090" {
		t.Fatalf("child readEnvUserConfig = %+v ok=%v err=%v", gotCfg, ok, err)
	}
	if r.Home != hostHome || r.Env != nil {
		t.Fatalf("daemon Runner was modified: %+v", r)
	}
	plain := r.agentChildRunner(cfg, workdir, nil, nil)
	if got := plain.Getenv("GS_TOKEN"); got != "host-daemon-token" {
		t.Fatalf("child without overrides GS_TOKEN = %q, want the daemon's", got)
	}
	if plain.Home != hostHome {
		t.Fatalf("child without overrides Home = %q, want %q", plain.Home, hostHome)
	}
}

// --insecure belongs to one run: concurrent Runners never see each other's.
func TestInsecureIsPerRunNotProcessGlobal(t *testing.T) {
	t.Setenv("GS_INSECURE", "")
	t.Setenv("GS_TLS", "")
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		insecure := i%2 == 0
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := withInsecure(context.Background(), insecure)
			for j := 0; j < 200; j++ {
				if got := insecureFromContext(ctx); got != insecure {
					t.Errorf("insecureFromContext = %v, want %v", got, insecure)
					return
				}
				_, useTLS, _ := resolveDialTargetPolicy("api.gitslice.io:50051", insecureFromContext(ctx))
				if useTLS == insecure {
					t.Errorf("insecure=%v gave useTLS=%v for a remote address", insecure, useTLS)
					return
				}
			}
		}()
	}
	wg.Wait()

	// Running a command with --insecure leaves nothing behind for the next.
	var out, errOut strings.Builder
	r := Runner{Stdout: &out, Stderr: &errOut, Insecure: true}
	if err := r.Run(context.Background(), []string{"version"}); err != nil {
		t.Fatalf("version failed: %v", err)
	}
	if insecureTransportRequested(false) {
		t.Fatal("an earlier --insecure run left plaintext transport enabled")
	}
	if _, useTLS, _ := resolveDialTargetPolicy("api.gitslice.io:50051", false); !useTLS {
		t.Fatal("remote address no longer defaults to TLS after an --insecure run")
	}
}

func TestCaptureResultSummary(t *testing.T) {
	got := captureResult{Outcome: captureCaptured, ChangesetID: "cs_abcdef1234567890", PatchsetNumber: 3}.Summary()
	if got != "captured changeset abcdef1234 patchset 3" {
		t.Fatalf("Summary = %q", got)
	}
}
