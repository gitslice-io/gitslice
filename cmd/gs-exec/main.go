package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	defaultBridgeURL = "http://gitslice.internal"
	defaultWorkRoot  = "/work"
	defaultRunDir    = "/run/gitslice"
	defaultSecretDir = "/run/secrets"
	cfCAPath         = "/etc/cloudflare/certs/cloudflare-containers-ca.crt"
	systemCAPath     = "/usr/local/share/ca-certificates/cloudflare-containers-ca.crt"
	runnerUID        = 10001
	runnerGID        = 10001
	maxLogChunkLines = 100
	logFlushInterval = 500 * time.Millisecond
	heartbeatEvery   = 30 * time.Second
)

// BootstrapPayload is returned once by GET http://gitslice.internal/bootstrap.
type BootstrapPayload struct {
	Script      string            `json:"script"`
	EventJSON   string            `json:"event_json"`
	Env         map[string]string `json:"env"`
	SecretFiles map[string]string `json:"secret_files"`
	Checkout    string            `json:"checkout"`
	FolderRel   string            `json:"folder_rel"`
	WorkingDir  string            `json:"working_dir"`
	TimeoutMs   int64             `json:"timeout_ms"`
}

type logLine struct {
	Stream string `json:"stream"`
	Text   string `json:"text"`
}

func main() {
	if filepath.Base(os.Args[0]) == "gs-auto" || (len(os.Args) > 1 && os.Args[1] == "gs-auto") {
		args := os.Args[1:]
		if len(args) > 0 && args[0] == "gs-auto" {
			args = args[1:]
		}
		if err := runHelperCLI(args, os.Stdout, os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	if err := runEntrypoint(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "gs-exec:", err)
		os.Exit(1)
	}
}

func bridgeURL() string {
	if u := strings.TrimSpace(os.Getenv("GITSLICE_BRIDGE_URL")); u != "" {
		return strings.TrimRight(u, "/")
	}
	return defaultBridgeURL
}

func runEntrypoint(ctx context.Context) error {
	bridge := bridgeURL()
	nonce := strings.TrimSpace(os.Getenv("BOOTSTRAP_NONCE"))
	// Immediately clear bootstrap credentials from our own process environment.
	_ = os.Unsetenv("BOOTSTRAP_NONCE")
	if nonce == "" {
		return errors.New("BOOTSTRAP_NONCE is required")
	}

	caBundle := installCloudflareCA()

	client := &http.Client{Timeout: 60 * time.Second}
	payload, err := fetchBootstrap(ctx, client, bridge, nonce)
	if err != nil {
		_ = postFinish(context.Background(), client, bridge, "errored", -1, "bootstrap failed: "+err.Error())
		return err
	}

	outcome, exitCode, runErr := executeBootstrap(ctx, client, bridge, payload, execPaths{
		workRoot:  defaultWorkRoot,
		runDir:    defaultRunDir,
		secretDir: defaultSecretDir,
		caBundle:  caBundle,
	})
	errText := ""
	if runErr != nil {
		errText = runErr.Error()
	}
	if ferr := postFinish(context.Background(), client, bridge, outcome, exitCode, errText); ferr != nil {
		return fmt.Errorf("finish report failed: %w (run err: %v)", ferr, runErr)
	}
	return nil
}

type execPaths struct {
	workRoot  string
	runDir    string
	secretDir string
	caBundle  string
}

func installCloudflareCA() string {
	data, err := os.ReadFile(cfCAPath)
	if err != nil || len(data) == 0 {
		return ""
	}
	if err := os.MkdirAll(filepath.Dir(systemCAPath), 0o755); err == nil {
		if err := os.WriteFile(systemCAPath, data, 0o644); err == nil {
			_ = exec.Command("update-ca-certificates").Run()
		}
	}
	for _, candidate := range []string{"/etc/ssl/certs/ca-certificates.crt", systemCAPath, cfCAPath} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return cfCAPath
}

func fetchBootstrap(ctx context.Context, client *http.Client, bridge, nonce string) (*BootstrapPayload, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, bridge+"/bootstrap", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Bootstrap-Nonce", nonce)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var payload BootstrapPayload
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode bootstrap: %w", err)
	}
	return &payload, nil
}

func executeBootstrap(ctx context.Context, client *http.Client, bridge string, payload *BootstrapPayload, p execPaths) (string, int, error) {
	if err := os.MkdirAll(p.runDir, 0o755); err != nil {
		return "errored", -1, err
	}
	if err := os.MkdirAll(p.workRoot, 0o755); err != nil {
		return "errored", -1, err
	}
	if err := writeSecretFiles(p.secretDir, payload.SecretFiles); err != nil {
		return "errored", -1, err
	}
	eventPath := filepath.Join(p.runDir, "event.json")
	eventJSON := payload.EventJSON
	if strings.TrimSpace(eventJSON) == "" {
		eventJSON = "{}"
	}
	if err := os.WriteFile(eventPath, []byte(eventJSON), 0o644); err != nil {
		return "errored", -1, err
	}
	scriptPath := filepath.Join(p.runDir, "script.sh")
	if err := os.WriteFile(scriptPath, []byte(payload.Script), 0o755); err != nil {
		return "errored", -1, err
	}

	if payload.Checkout != "" && payload.Checkout != "none" {
		if err := downloadAndExtractArchive(ctx, client, bridge, p.workRoot); err != nil {
			return "errored", -1, fmt.Errorf("checkout archive: %w", err)
		}
	}

	workDir := resolveWorkDir(p.workRoot, payload.FolderRel, payload.WorkingDir)
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return "errored", -1, err
	}
	chownIfRoot(p.workRoot, runnerUID, runnerGID)

	timeout := 5 * time.Minute
	if payload.TimeoutMs > 0 {
		timeout = time.Duration(payload.TimeoutMs) * time.Millisecond
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.Command("bash", "-euo", "pipefail", scriptPath)
	cmd.Dir = workDir
	cmd.Env = buildChildEnv(payload.Env, eventPath, p.secretDir, bridge, p.caBundle)
	configureChildProcess(cmd)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return "errored", -1, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return "errored", -1, err
	}
	if err := cmd.Start(); err != nil {
		return "errored", -1, err
	}

	streamer := newLogStreamer(client, bridge, cancel)
	var pipeWG sync.WaitGroup
	pipeWG.Add(2)
	go func() {
		defer pipeWG.Done()
		streamer.consume("stdout", stdoutPipe)
	}()
	go func() {
		defer pipeWG.Done()
		streamer.consume("stderr", stderrPipe)
	}()

	waitDone := make(chan error, 1)
	go func() {
		pipeWG.Wait()
		waitDone <- cmd.Wait()
	}()

	var waitErr error
	timedOut := false
	canceled := false
	select {
	case waitErr = <-waitDone:
	case <-runCtx.Done():
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			timedOut = true
		} else {
			canceled = true
		}
		killProcessGroup(cmd)
		select {
		case waitErr = <-waitDone:
		case <-time.After(3 * time.Second):
		}
	}

	streamer.close()

	if streamer.wasCanceled() || canceled {
		return "errored", -1, errors.New("automation run was canceled")
	}
	if timedOut {
		return "failed", 124, fmt.Errorf("bash command timed out after %s", timeout)
	}
	if waitErr != nil {
		code := 1
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			code = exitErr.ExitCode()
		}
		return "failed", code, fmt.Errorf("bash exited with code %d", code)
	}
	return "succeeded", 0, nil
}

func resolveWorkDir(workRoot, folderRel, workingDir string) string {
	cleanFolder := strings.Trim(path.Clean("/"+strings.TrimSpace(folderRel)), "/")
	cleanSub := path.Clean(strings.TrimSpace(workingDir))
	if cleanSub == "." || cleanSub == "" {
		cleanSub = ""
	}
	parts := []string{workRoot}
	if cleanFolder != "" && cleanFolder != "." {
		parts = append(parts, filepath.FromSlash(cleanFolder))
	}
	if cleanSub != "" {
		parts = append(parts, filepath.FromSlash(cleanSub))
	}
	joined := filepath.Join(parts...)
	rel, err := filepath.Rel(workRoot, joined)
	if err != nil || strings.HasPrefix(rel, "..") {
		return workRoot
	}
	return joined
}

func writeSecretFiles(secretDir string, files map[string]string) error {
	if err := os.MkdirAll(secretDir, 0o755); err != nil {
		return err
	}
	chownIfRoot(secretDir, runnerUID, runnerGID)
	for name, val := range files {
		if name == "" || strings.ContainsAny(name, "/\\") || name == "." || name == ".." {
			continue
		}
		dst := filepath.Join(secretDir, name)
		if err := os.WriteFile(dst, []byte(val), 0o400); err != nil {
			return err
		}
		chownIfRoot(dst, runnerUID, runnerGID)
	}
	return nil
}

func buildChildEnv(extra map[string]string, eventPath, secretDir, bridge, caBundle string) []string {
	envMap := map[string]string{
		"PATH":                 "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME":                 "/home/runner",
		"LANG":                 "C.UTF-8",
		"GITSLICE_EVENT_FILE":  eventPath,
		"GITSLICE_SECRETS_DIR": secretDir,
		"GITSLICE_BRIDGE_URL":  bridge,
	}
	if currentPath := os.Getenv("PATH"); currentPath != "" {
		envMap["PATH"] = currentPath
	}
	if caBundle != "" {
		envMap["SSL_CERT_FILE"] = caBundle
		envMap["CURL_CA_BUNDLE"] = caBundle
		envMap["REQUESTS_CA_BUNDLE"] = caBundle
		envMap["NODE_EXTRA_CA_CERTS"] = caBundle
	}
	for k, v := range extra {
		if k == "BOOTSTRAP_NONCE" {
			continue
		}
		envMap[k] = v
	}
	out := make([]string, 0, len(envMap))
	for k, v := range envMap {
		out = append(out, k+"="+v)
	}
	return out
}
