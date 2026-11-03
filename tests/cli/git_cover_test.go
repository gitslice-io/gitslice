package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGitCoverServesAColdClone (design/23, "Covers"): a full clone asks for
// a cover; the cover (every file, one pack with a bitmap) reaches the object
// store; a new instance restores it and serves a full clone without writing a
// single file from the object store.
func TestGitCoverServesAColdClone(t *testing.T) {
	logs := &logBuffer{}
	t.Cleanup(captureLogs(logs))
	ts := startTestServer(t)

	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	token := readToken(t, home)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	files := map[string]string{
		"one.go":     "package payment\nconst One = 1\n",
		"dir/two.go": "package payment\nconst Two = 2\n",
	}
	for name, data := range files {
		submitWorkspaceFile(t, home, workspace, name, data, "add "+name)
	}
	gitURL := "http://" + ts.gitAddr + "/git/acme/payment.git"
	header := "http.extraHeader=Authorization: Bearer " + token
	runGit(t, "", "-c", header, "clone", gitURL, filepath.Join(t.TempDir(), "first"))

	manifest := filepath.Join(ts.objectRoot, "git-mirror", "acme", "payment", "manifest.json")
	waitFor(t, "a cover in the mirror", func() bool {
		raw, err := os.ReadFile(manifest)
		return err == nil && strings.Contains(string(raw), `"bitmap"`) && strings.Contains(logs.String(), "git cover built")
	})

	// A new instance with nothing cached, and without the files themselves:
	// the cover must hold every one.
	ts.stop(t)
	forgetProjection(t, ts)
	if err := os.RemoveAll(filepath.Join(ts.objectRoot, "blobs")); err != nil {
		t.Fatal(err)
	}
	before := len(logs.String())
	ts.start(t, false)
	cold := filepath.Join(t.TempDir(), "cold")
	runGit(t, "", "-c", header, "clone", gitURL, cold)
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(cold, "acme", "payment", filepath.FromSlash(name)))
		if err != nil || string(got) != want {
			t.Fatalf("%s after a cold start = %q (%v), want %q", name, got, err, want)
		}
	}
	runGit(t, "", "-C", cold, "fsck", "--full")
	after := logs.String()[before:]
	if !strings.Contains(after, "git projection restored from mirror") {
		t.Fatalf("the new instance did not restore the cover:\n%s", after)
	}
	if strings.Contains(after, "git contents hydrated") {
		t.Fatalf("the new instance wrote files although the cover held them all:\n%s", after)
	}
}
