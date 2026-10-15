package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestRunCIPassingHostCheck(t *testing.T) {
	workspace := t.TempDir()
	writeCIWorkspaceForTest(t, workspace, "true")

	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Dir: workspace, Stdout: &stdout, Stderr: &stderr}
	if err := r.Run(context.Background(), []string{"ci"}); err != nil {
		t.Fatalf("gs ci failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, "acme/payment/check: passed") || !strings.Contains(got, "run ") {
		t.Fatalf("gs ci stdout missing passing check/timing:\n%s", got)
	}
}

func TestRunCIFailingHostCheckExitsNonZero(t *testing.T) {
	workspace := t.TempDir()
	writeCIWorkspaceForTest(t, workspace, "false")

	var stdout, stderr bytes.Buffer
	r := Runner{Home: t.TempDir(), Dir: workspace, Stdout: &stdout, Stderr: &stderr}
	err := r.Run(context.Background(), []string{"ci"})
	if err == nil {
		t.Fatalf("gs ci unexpectedly succeeded\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, "acme/payment/check: failed") || !strings.Contains(got, "exit 1") {
		t.Fatalf("gs ci stdout missing failing check summary:\n%s", got)
	}
}

func writeCIWorkspaceForTest(t *testing.T, workspace, checkRun string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(workspace, ".gs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(workspace, ".gs", "slice.json"), WorkspaceConfig{
		Account:       "acme",
		Slice:         "payment",
		IncludedPaths: []string{"/acme/payment"},
	}, 0o644); err != nil {
		t.Fatal(err)
	}
	checksDir := filepath.Join(workspace, "acme", "payment", ".gitslice")
	if err := os.MkdirAll(checksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	checksYAML := "version: 1\nchecks:\n  check:\n    run: " + strconv.Quote(checkRun) + "\n"
	if err := os.WriteFile(filepath.Join(checksDir, "checks.yaml"), []byte(checksYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "acme", "payment", "app.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}
