package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runBrowseForTest(t *testing.T, r Runner, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	r.Stdout, r.Stderr = &stdout, &stderr
	err := r.Run(context.Background(), append([]string{"browse", "--print", "--web-url", "https://gitslice.test"}, args...))
	return strings.TrimSpace(stdout.String()), stderr.String(), err
}

func TestBrowseMapsTargetsToRealRoutes(t *testing.T) {
	r := Runner{Home: t.TempDir(), Dir: t.TempDir()}
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"slice ref", []string{"heibot:home"}, "https://gitslice.test/slices/heibot/home"},
		{"slice ref with path", []string{"heibot:home", "--path", "/heibot/jev-pricing"}, "https://gitslice.test/slices/heibot/home?path=%2Fheibot%2Fjev-pricing"},
		// The agent's case: a source path must not be glued onto the host.
		{"absolute source path", []string{"/heibot/jev-pricing"}, "https://gitslice.test/slices/heibot/home?path=%2Fheibot%2Fjev-pricing"},
		{"path flag only", []string{"--path", "/heibot/jev-pricing/README.md"}, "https://gitslice.test/slices/heibot/home?path=%2Fheibot%2Fjev-pricing%2FREADME.md"},
		{"web page", []string{"cs/abc123"}, "https://gitslice.test/cs/abc123"},
		{"slice page route", []string{"slices/heibot/home?path=/heibot/x"}, "https://gitslice.test/slices/heibot/home?path=%2Fheibot%2Fx"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, stderr, err := runBrowseForTest(t, r, tc.args...)
			if err != nil {
				t.Fatalf("browse %v: %v", tc.args, err)
			}
			if got != tc.want {
				t.Fatalf("browse %v = %q, want %q", tc.args, got, tc.want)
			}
			// Not signed in: every slice link says it was not checked.
			if strings.Contains(tc.want, "/slices/") && !strings.Contains(stderr, "not signed in") {
				t.Fatalf("stderr = %q; want a not-signed-in note", stderr)
			}
		})
	}

	if _, _, err := runBrowseForTest(t, r, "jev-pricing"); err == nil || !strings.Contains(err.Error(), "not a slice") {
		t.Fatalf("bare word outside a workspace err = %v; want a clear error instead of a bogus URL", err)
	}
	if _, _, err := runBrowseForTest(t, r, "https://gitslice.io/x"); err == nil {
		t.Fatal("a full URL should be rejected")
	}
}

func TestBrowseUsesWorkspaceForRelativePaths(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".gs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(root, ".gs", "slice.json"), WorkspaceConfig{Account: "heibot", Slice: "pricing", IncludedPaths: []string{"/heibot/jev-pricing"}}, 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "heibot", "jev-pricing")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	r := Runner{Home: t.TempDir(), Dir: sub}

	got, _, err := runBrowseForTest(t, r)
	if err != nil || got != "https://gitslice.test/slices/heibot/pricing" {
		t.Fatalf("browse in workspace = %q, %v; want the workspace slice", got, err)
	}
	got, _, err = runBrowseForTest(t, r, "model.py")
	if err != nil || got != "https://gitslice.test/slices/heibot/pricing?path=%2Fheibot%2Fjev-pricing%2Fmodel.py" {
		t.Fatalf("browse relative file = %q, %v", got, err)
	}
	// A path outside the workspace slice falls back to the account's home.
	got, _, err = runBrowseForTest(t, r, "/heibot/other")
	if err != nil || got != "https://gitslice.test/slices/heibot/home?path=%2Fheibot%2Fother" {
		t.Fatalf("browse path outside workspace slice = %q, %v", got, err)
	}
}

func TestBrowseWarnsAboutMissingSlicesAndPaths(t *testing.T) {
	serverAddr := startAgentSignupServer(t, true)
	r := Runner{Home: t.TempDir(), Dir: t.TempDir()}
	var out bytes.Buffer
	r.Stdout, r.Stderr = &out, &out
	if err := r.Run(context.Background(), []string{"auth", "register-agent", "--server", serverAddr, "--username", "warn-bot", "--email", "owner@example.com", "--quiet"}); err != nil {
		t.Fatalf("register-agent: %v\n%s", err, out.String())
	}

	got, stderr, err := runBrowseForTest(t, r, "/warn-bot/does-not-exist")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://gitslice.test/slices/warn-bot/home?path=%2Fwarn-bot%2Fdoes-not-exist" {
		t.Fatalf("url = %q", got)
	}
	if !strings.Contains(stderr, "/warn-bot/does-not-exist does not exist in warn-bot/home") || !strings.Contains(stderr, "warn-bot/home is private") {
		t.Fatalf("stderr = %q; want missing-path and private-slice warnings", stderr)
	}

	_, stderr, err = runBrowseForTest(t, r, "warn-bot:nope")
	if err != nil || !strings.Contains(stderr, "slice warn-bot/nope does not exist or is not visible to you") {
		t.Fatalf("missing slice: err = %v, stderr = %q", err, stderr)
	}
}
