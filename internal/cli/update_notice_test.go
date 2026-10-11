package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

// latestReleaseServer serves /releases/latest leading to tag, and counts the
// lookups.
func latestReleaseServer(t *testing.T, tag string) (string, *atomic.Int64) {
	t.Helper()
	var lookups atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		lookups.Add(1)
		http.Redirect(w, r, "/releases/tag/"+tag, http.StatusFound)
	})
	mux.HandleFunc("/releases/tag/"+tag, func(w http.ResponseWriter, r *http.Request) {})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL + "/releases", &lookups
}

func withVersion(t *testing.T, version string) {
	t.Helper()
	old := Version
	Version = version
	t.Cleanup(func() { Version = old })
}

func TestUpdateNoticeOncePerDay(t *testing.T) {
	base, lookups := latestReleaseServer(t, "v9.9.9")
	t.Setenv("GS_DOWNLOAD_BASE", base)
	t.Setenv("GS_NO_UPDATE_CHECK", "")
	withVersion(t, "v0.1.0")
	home := t.TempDir()
	run := func(args ...string) string {
		var stdout, stderr bytes.Buffer
		r := Runner{Home: home, Stdout: &stdout, Stderr: &stderr}
		_ = r.Run(context.Background(), args)
		return stderr.String()
	}

	stderr := run("config", "list")
	if !strings.Contains(stderr, "gs v9.9.9 is available (this is v0.1.0). Run gs upgrade to install it.") {
		t.Fatalf("missing the notice:\n%s", stderr)
	}
	if _, err := os.Stat(home + "/.gitslice/update-check.json"); err != nil {
		t.Fatalf("the lookup was not recorded: %v", err)
	}
	// Within the day, no lookup and no notice.
	if stderr := run("config", "list"); strings.Contains(stderr, "available") || lookups.Load() != 1 {
		t.Fatalf("second run looked up %d times, stderr:\n%s", lookups.Load(), stderr)
	}

	// Machine output, --quiet, upgrade itself, and the opt-out skip it.
	if err := os.Remove(home + "/.gitslice/update-check.json"); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"config", "list", "--json"},
		{"config", "list", "--quiet"},
		{"--format", "json", "config", "list"},
		{"upgrade", "--check"},
	} {
		if stderr := run(args...); strings.Contains(stderr, "is available (this is") {
			t.Fatalf("%v printed the notice:\n%s", args, stderr)
		}
	}
	if lookups.Load() != 2 { // gs upgrade --check looks up for itself
		t.Fatalf("lookups = %d, want 2", lookups.Load())
	}
	t.Setenv("GS_NO_UPDATE_CHECK", "1")
	if stderr := run("config", "list"); strings.Contains(stderr, "available") || lookups.Load() != 2 {
		t.Fatalf("GS_NO_UPDATE_CHECK=1 still looked up (%d):\n%s", lookups.Load(), stderr)
	}
}

func TestUpdateNoticeQuietWhenCurrent(t *testing.T) {
	base, lookups := latestReleaseServer(t, "v0.4.3")
	t.Setenv("GS_DOWNLOAD_BASE", base)
	t.Setenv("GS_NO_UPDATE_CHECK", "")
	for _, version := range []string{"v0.4.3", "v0.4.4-0.20261011000000-abcdef", "dev"} {
		withVersion(t, version)
		var stdout, stderr bytes.Buffer
		r := Runner{Home: t.TempDir(), Stdout: &stdout, Stderr: &stderr}
		_ = r.Run(context.Background(), []string{"config", "list"})
		if strings.Contains(stderr.String(), "available") {
			t.Fatalf("%s printed a notice:\n%s", version, stderr.String())
		}
	}
	if lookups.Load() != 2 { // a source build ("dev") does not look
		t.Fatalf("lookups = %d, want 2", lookups.Load())
	}
}

func TestVersionSaysWhenAnUpdateIsOut(t *testing.T) {
	base, lookups := latestReleaseServer(t, "v9.9.9")
	t.Setenv("GS_DOWNLOAD_BASE", base)
	t.Setenv("GS_NO_UPDATE_CHECK", "")
	withVersion(t, "v0.1.0")
	home := t.TempDir()
	run := func(args ...string) (string, string) {
		var stdout, stderr bytes.Buffer
		r := Runner{Home: home, Stdout: &stdout, Stderr: &stderr}
		if err := r.Run(context.Background(), args); err != nil {
			t.Fatalf("gs %v: %v", args, err)
		}
		return stdout.String(), stderr.String()
	}

	stdout, stderr := run("version")
	if !strings.HasPrefix(stdout, "gs version v0.1.0\n") || !strings.HasSuffix(stdout, "update: v9.9.9 is available; run gs upgrade\n") || stderr != "" {
		t.Fatalf("version:\n%s\nstderr: %s", stdout, stderr)
	}
	// The lookup is recorded, so the next gs version (or command) does not
	// look again that day.
	if stdout, _ := run("version"); !strings.Contains(stdout, "update: v9.9.9") || lookups.Load() != 1 {
		t.Fatalf("second version looked up %d times:\n%s", lookups.Load(), stdout)
	}
	// JSON stays offline and unchanged; the opt-out and current releases say
	// nothing.
	if stdout, _ := run("version", "--json"); strings.Contains(stdout, "update") {
		t.Fatalf("version --json:\n%s", stdout)
	}
	t.Setenv("GS_NO_UPDATE_CHECK", "1")
	if stdout, _ := run("version"); strings.Contains(stdout, "update:") {
		t.Fatalf("GS_NO_UPDATE_CHECK=1:\n%s", stdout)
	}
	t.Setenv("GS_NO_UPDATE_CHECK", "")
	withVersion(t, "v9.9.9")
	if stdout, _ := run("version"); strings.Contains(stdout, "update:") {
		t.Fatalf("the latest release:\n%s", stdout)
	}
	if lookups.Load() != 1 {
		t.Fatalf("lookups = %d, want 1", lookups.Load())
	}
}
