package cli_test

import (
	"bytes"
	"io"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"connectrpc.com/connect"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	stdout, stderr, err := runGitResult(dir, args...)
	if err != nil {
		t.Fatalf("git %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout, stderr)
	}
	return stdout
}

func runGitResult(dir string, args ...string) (string, string, error) {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func connectBaseURL(addr string) string {
	return "http://" + addr
}

func connectRequest[T any](token string, msg *T) *connect.Request[T] {
	req := connect.NewRequest(msg)
	if token != "" {
		req.Header().Set("Authorization", "Bearer "+token)
	}
	return req
}

func httpAPIOptions(t *testing.T, addr, path, origin string) (int, http.Header) {
	t.Helper()
	req, err := http.NewRequest(http.MethodOptions, "http://"+addr+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", origin)
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "Connect-Protocol-Version, Content-Type")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, resp.Header.Clone()
}

func httpGet(t *testing.T, addr, path string) string {
	t.Helper()
	resp, err := http.Get("http://" + addr + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode >= 300 {
		t.Fatalf("GET %s returned %d:\n%s", path, resp.StatusCode, string(data))
	}
	return string(data)
}

func assertMetricPositive(t *testing.T, text, series string) {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != series {
			continue
		}
		value, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			t.Fatalf("metric %s has unparsable value %q", series, fields[1])
		}
		if value > 0 {
			return
		}
		t.Fatalf("metric %s = %s, want > 0", series, fields[1])
	}
	t.Fatalf("metric %s not found in /metrics output:\n%s", series, text)
}

func gitHTTPRaw(t *testing.T, addr, path, authorization string) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://"+addr+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header.Clone(), data
}
