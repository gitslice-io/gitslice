package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type statusChange struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"`
	Added   int    `json:"lines_added"`
	Removed int    `json:"lines_removed"`
	Counted bool   `json:"lines_counted"`
}

type statusJSON struct {
	Changes      []statusChange `json:"changes"`
	LinesAdded   int            `json:"lines_added"`
	LinesRemoved int            `json:"lines_removed"`
	DependencyID string         `json:"dependency_id"`
	Offline      bool           `json:"offline"`
}

func readStatus(t *testing.T, home, workspace string) statusJSON {
	t.Helper()
	var out statusJSON
	raw := runCLI(t, home, workspace, "status", "--json")
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("gs status --json: %v\n%s", err, raw)
	}
	return out
}

func changesByPath(changes []statusChange) map[string]statusChange {
	out := map[string]statusChange{}
	for _, c := range changes {
		out[c.Path] = c
	}
	return out
}

// setServerAddr points the CLI's config at addr, as when the network is gone.
func setServerAddr(t *testing.T, home, addr string) {
	t.Helper()
	path := filepath.Join(home, ".gitslice", "config.json")
	var cfg map[string]any
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &cfg) != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	cfg["server_addr"] = addr
	data, _ = json.Marshal(cfg)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestStatusLabelsChangesAndWorksOffline checks that gs status says how each
// path changed and by how many lines, and that it answers from local state
// when the server is unreachable, including for a changeset that builds on
// another.
func TestStatusLabelsChangesAndWorksOffline(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspace, "acme/payment/keep.go", "package payment\n\nconst A = 1\nconst B = 2\n")
	writeWorkspaceFile(t, workspace, "acme/payment/gone.txt", "one\ntwo\nthree\n")
	runCLI(t, home, workspace, "create", "--message", "seed", "--all", "--submit")

	// A changeset, and a second one built on it.
	writeWorkspaceFile(t, workspace, "acme/payment/first.go", "package payment\n")
	runCLI(t, home, workspace, "create", "--message", "first", "--all")
	writeWorkspaceFile(t, workspace, "acme/payment/keep.go", "package payment\n\nconst A = 10\nconst B = 2\n")
	writeWorkspaceFile(t, workspace, "acme/payment/new.md", "# new\n\nline\n")
	if err := os.Remove(filepath.Join(workspace, "acme/payment/gone.txt")); err != nil {
		t.Fatal(err)
	}
	runCLI(t, home, workspace, "create", "--message", "second", "--all")

	online := readStatus(t, home, workspace)
	want := map[string]statusChange{
		"/acme/payment/keep.go":  {Kind: "modified", Added: 1, Removed: 1, Counted: true},
		"/acme/payment/new.md":   {Kind: "added", Added: 3, Counted: true},
		"/acme/payment/gone.txt": {Kind: "deleted", Removed: 3, Counted: true},
	}
	checkChanges := func(label string, got statusJSON) {
		t.Helper()
		byPath := changesByPath(got.Changes)
		if len(byPath) != len(want) {
			t.Fatalf("%s: changes = %+v, want %v (first.go belongs to the changeset below)", label, got.Changes, want)
		}
		for path, w := range want {
			c := byPath[path]
			if c.Kind != w.Kind || c.Added != w.Added || c.Removed != w.Removed || c.Counted != w.Counted {
				t.Fatalf("%s: %s = %+v, want %+v", label, path, c, w)
			}
		}
		if got.LinesAdded != 4 || got.LinesRemoved != 4 {
			t.Fatalf("%s: totals +%d -%d, want +4 -4", label, got.LinesAdded, got.LinesRemoved)
		}
	}
	checkChanges("online", online)
	if online.Offline || online.DependencyID == "" {
		t.Fatalf("online status = %+v", online)
	}
	text := runCLI(t, home, workspace, "status")
	for _, line := range []string{"status: 3 changed path(s), +4 -4", "modified  /acme/payment/keep.go", "added     /acme/payment/new.md", "deleted   /acme/payment/gone.txt"} {
		if !strings.Contains(text, line) {
			t.Fatalf("status text missing %q:\n%s", line, text)
		}
	}

	// The server goes away.
	setServerAddr(t, home, "127.0.0.1:1")
	started := time.Now()
	offline := readStatus(t, home, workspace)
	if took := time.Since(started); took > 15*time.Second {
		t.Fatalf("offline gs status took %s", took)
	}
	checkChanges("offline", offline)
	if !offline.Offline || offline.DependencyID != online.DependencyID {
		t.Fatalf("offline status = %+v, want offline with the kept dependency tree", offline)
	}
	if text := runCLI(t, home, workspace, "status"); !strings.Contains(text, "offline: 127.0.0.1:1 is unreachable") {
		t.Fatalf("offline status text:\n%s", text)
	}
	if stat := runCLI(t, home, workspace, "diff", "--stat"); !strings.Contains(stat, "3 changed path(s), +4 -4") || !strings.Contains(stat, "modified  /acme/payment/keep.go") {
		t.Fatalf("offline gs diff --stat:\n%s", stat)
	}

	// Without the kept tree, it says what it needs instead of guessing.
	if err := os.Remove(filepath.Join(workspace, ".gs", "dependency-tree.json")); err != nil {
		t.Fatal(err)
	}
	_, stderr := runCLIFails(t, home, workspace, "status")
	if !strings.Contains(stderr, "server is unreachable") || !strings.Contains(stderr, "once while online") {
		t.Fatalf("offline status without the kept tree: %s", stderr)
	}
}
