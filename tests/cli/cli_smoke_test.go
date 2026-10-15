package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestMinimalCLIJourney(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	_, subjectID := loginTestCLI(t, ts, home, workspace)
	authStatus := runCLI(t, home, workspace, "auth", "status")
	if !strings.Contains(authStatus, "signed in as "+subjectID) || !strings.Contains(authStatus, "server: "+ts.addr) {
		t.Fatalf("unexpected auth status:\n%s", authStatus)
	}
	authStatusJSON := runCLI(t, home, workspace, "auth", "status", "--json")
	if strings.Contains(authStatusJSON, "token") {
		t.Fatalf("auth status leaked token data:\n%s", authStatusJSON)
	}
	runCLI(t, home, workspace, "init", "acme/payment")
	status := runCLI(t, home, workspace, "status")
	if !strings.Contains(status, "status: clean") {
		t.Fatalf("expected clean status, got:\n%s", status)
	}
	writeWorkspaceFile(t, workspace, "app.go", "package payment\n")
	status = runCLI(t, home, workspace, "status")
	if !strings.Contains(status, "/acme/payment/app.go") {
		t.Fatalf("expected app.go to be dirty, got:\n%s", status)
	}
	largeContent := bytes.Repeat([]byte("large streaming workspace file\n"), 180000)
	writeWorkspaceFileBytes(t, workspace, "large.txt", largeContent)
	runCLI(t, home, workspace, "cs", "create")
	runCLI(t, home, workspace, "cs", "submit")
	csStatus := runCLI(t, home, workspace, "cs", "status")
	if !strings.Contains(csStatus, "status: submitted") {
		t.Fatalf("expected submitted changeset, got:\n%s", csStatus)
	}
	metricsText := httpGet(t, ts.httpAddr, "/metrics")
	assertMetricPositive(t, metricsText, `gitslice_submit_total{result="accepted",reason="none"}`)
	assertMetricPositive(t, metricsText, `gitslice_publish_batches_total{result="success"}`)
	assertMetricPositive(t, metricsText, "gitslice_published_changesets_total")
	status = runCLI(t, home, workspace, "status")
	if !strings.Contains(status, "status: clean") {
		t.Fatalf("expected clean status after submit, got:\n%s", status)
	}
	hydratedWorkspace := t.TempDir()
	runCLI(t, home, hydratedWorkspace, "init", "acme/payment")
	gotLarge, err := os.ReadFile(filepath.Join(hydratedWorkspace, "acme", "payment", "large.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotLarge, largeContent) {
		t.Fatalf("hydrated large file length = %d, want %d", len(gotLarge), len(largeContent))
	}
}
