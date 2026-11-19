package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func makeTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExecuteBootstrapArchiveSecretsAndLogs(t *testing.T) {
	var mu sync.Mutex
	var logs []logLine

	archiveBytes := makeTarGz(t, map[string]string{
		"infra/hello.txt": "hello from repo",
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/archive":
			w.Header().Set("Content-Type", "application/gzip")
			_, _ = w.Write(archiveBytes)
		case "/logs":
			var req struct {
				Lines []logLine `json:"lines"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			mu.Lock()
			logs = append(logs, req.Lines...)
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	tmp := t.TempDir()
	payload := &BootstrapPayload{
		Script: `
echo "FILE=$(cat hello.txt)"
echo "SECRET_ENV=$API_KEY"
echo "SECRET_FILE=$(cat "$GITSLICE_SECRETS_DIR/SSH_KEY")"
echo "INPUT=$INPUT_VERSION"
echo "warn" >&2
`,
		EventJSON:   `{"event":"manual"}`,
		Env:         map[string]string{"API_KEY": "gs-secret:v1:abc", "INPUT_VERSION": "v1.2.3"},
		SecretFiles: map[string]string{"SSH_KEY": "ssh-private-key-line"},
		Checkout:    "folder",
		FolderRel:   "infra",
		WorkingDir:  ".",
		TimeoutMs:   15000,
	}

	outcome, code, err := executeBootstrap(context.Background(), srv.Client(), srv.URL, payload, execPaths{
		workRoot:  filepath.Join(tmp, "work"),
		runDir:    filepath.Join(tmp, "run"),
		secretDir: filepath.Join(tmp, "secrets"),
	})
	if err != nil || outcome != "succeeded" || code != 0 {
		t.Fatalf("executeBootstrap = (%q, %d, %v)", outcome, code, err)
	}

	mu.Lock()
	defer mu.Unlock()
	var stdout, stderr []string
	for _, l := range logs {
		if l.Stream == "stderr" {
			stderr = append(stderr, l.Text)
		} else {
			stdout = append(stdout, l.Text)
		}
	}
	joined := strings.Join(stdout, "\n")
	for _, want := range []string{
		"FILE=hello from repo",
		"SECRET_ENV=gs-secret:v1:abc",
		"SECRET_FILE=ssh-private-key-line",
		"INPUT=v1.2.3",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("stdout missing %q:\n%s", want, joined)
		}
	}
	if strings.Join(stderr, "\n") != "warn" {
		t.Fatalf("stderr = %v, want [warn]", stderr)
	}

	// Verify secret file mode is 0400.
	info, err := os.Stat(filepath.Join(tmp, "secrets", "SSH_KEY"))
	if err != nil || info.Mode().Perm() != 0o400 {
		t.Fatalf("secret file mode = %v (err=%v), want 0400", info.Mode().Perm(), err)
	}
}

func TestExtractTarGzRejectsTraversal(t *testing.T) {
	bad := makeTarGz(t, map[string]string{"../escape.txt": "nope"})
	if err := extractTarGz(bytes.NewReader(bad), t.TempDir()); err == nil {
		t.Fatal("expected traversal error")
	}
}

func TestHelperCLICheckAndState(t *testing.T) {
	var seenPath string
	var seenBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&seenBody)
		_, _ = w.Write([]byte(`{"id":"chk_1"}`))
	}))
	defer srv.Close()
	t.Setenv("GITSLICE_BRIDGE_URL", srv.URL)

	var out, errBuf bytes.Buffer
	if err := runHelperCLI([]string{"check", "create", "--name", "deploy", "--status", "passed", "--summary", "ok"}, &out, &errBuf); err != nil {
		t.Fatalf("runHelperCLI: %v", err)
	}
	if seenPath != "/gs/check/create" || seenBody["name"] != "deploy" || seenBody["status"] != "passed" {
		t.Fatalf("bridge saw path=%q body=%+v", seenPath, seenBody)
	}
	if !strings.Contains(out.String(), `"id":"chk_1"`) {
		t.Fatalf("stdout = %q", out.String())
	}
}
