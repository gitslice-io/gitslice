package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceIgnoreFollowsGitignoreRules(t *testing.T) {
	root := t.TempDir()
	writeIgnoreFile(t, root, ".gitignore", "# build output\nnode_modules/\n/bin/\n*.log\n!keep.log\n*.tmp\nbuild/\n!build/keep.txt\n")
	writeIgnoreFile(t, root, "web/.gitignore", "dist/\n/.clerk/\n!special.tmp\n")

	ws := WorkspaceConfig{Account: "acme", IncludedPaths: []string{"/acme/app"}}
	ig := newWorkspaceIgnore(root, ws, map[string]BaseSnapshotFile{
		"/acme/app/tracked.log":           {RelPath: "acme/app/tracked.log"},
		"/acme/app/vendor/build/kept.txt": {RelPath: "vendor/build/kept.txt"},
	})

	for _, tc := range []struct {
		rel   string
		isDir bool
		want  bool
	}{
		{rel: "web/node_modules", isDir: true, want: true},
		{rel: "node_modules", isDir: false, want: false}, // directory-only rule
		{rel: "bin", isDir: true, want: true},
		{rel: "web/bin", isDir: true, want: false}, // anchored to the root
		{rel: "a/b/server.log", want: true},
		{rel: "a/keep.log", want: false}, // negated
		{rel: "acme/app/tracked.log", want: false},
		{rel: "web/dist", isDir: true, want: true},
		{rel: "dist", isDir: true, want: false}, // web/.gitignore only covers web/
		{rel: "web/.clerk", isDir: true, want: true},
		{rel: "x.tmp", want: true},
		{rel: "web/special.tmp", want: false}, // deeper file overrides
		{rel: "web/other.tmp", want: true},
		{rel: "build", isDir: true, want: true},
		{rel: "build/keep.txt", want: true},             // cannot re-include below an ignored dir
		{rel: "vendor/build", isDir: true, want: false}, // a tracked file lies beneath
		{rel: "vendor/build/kept.txt", want: false},
		{rel: "vendor/build/new.txt", want: true},
		{rel: "src/main.go", want: false},
	} {
		if got := ig.skip(tc.rel, tc.isDir); got != tc.want {
			t.Errorf("skip(%q, dir=%v) = %v, want %v", tc.rel, tc.isDir, got, tc.want)
		}
	}
}

func TestParseGitignoreEscapesAndBlankLines(t *testing.T) {
	rules := parseGitignore([]byte("\n   \n\\#literal\n\\!bang\n{a,b}.txt\ntrailing   \n"))
	want := []string{"**/#literal", "**/!bang", `**/\{a,b\}.txt`, "**/trailing"}
	if len(rules) != len(want) {
		t.Fatalf("rules = %#v, want %d", rules, len(want))
	}
	for i, rule := range rules {
		if rule.pattern != want[i] || rule.negate || rule.dirOnly {
			t.Fatalf("rule %d = %#v, want pattern %q", i, rule, want[i])
		}
	}
}

func writeIgnoreFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
