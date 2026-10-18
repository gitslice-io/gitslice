package lazyfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func containsString(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

func TestSwitchShowsAChangesetWithoutFetching(t *testing.T) {
	content := newMemContent()
	base1 := newBase(t, content, "commit-1", map[string]baseFile{
		"/app/main.go":    {content: "main 1"},
		"/app/util.go":    {content: "util 1"},
		"/app/gone.go":    {content: "gone"},
		"/app/link":       {symlink: "main.go"},
		"/app/conf":       {content: "a file that becomes a directory"},
		"/docs/guide.md":  {content: "guide"},
		"/docs/extra.md":  {content: "extra"},
		"/app/.gitignore": {content: "out/\n"},
	})
	dir := t.TempDir()
	f := openFS(t, dir, base1, content)
	// The changeset being left: its files, as gs modify captured them.
	if err := f.WriteFile("/app/main.go", []byte("main from the old changeset"), 0); err != nil {
		t.Fatal(err)
	}
	if err := f.Remove("/docs/extra.md"); err != nil {
		t.Fatal(err)
	}
	// Build output, which a switch leaves alone.
	if err := f.Mkdir("/app/out"); err != nil {
		t.Fatal(err)
	}
	if err := f.WriteFile("/app/out/bin", []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	expect, err := f.Edits()
	if err != nil {
		t.Fatal(err)
	}

	// The target changeset, over a new base.
	base2 := newBase(t, content, "commit-2", map[string]baseFile{
		"/app/main.go":    {content: "main 2"},
		"/app/util.go":    {content: "util 1"},
		"/app/gone.go":    {content: "gone"},
		"/app/link":       {symlink: "main.go"},
		"/app/conf":       {content: "a file that becomes a directory"},
		"/docs/guide.md":  {content: "guide"},
		"/docs/extra.md":  {content: "extra"},
		"/app/.gitignore": {content: "out/\n"},
	})
	hash := func(data string) string { return content.put([]byte(data)) }
	diff := map[string]*BaseEntry{
		"/app/util.go":       {Kind: KindFile, Mode: ModeExecutable, Size: 6, ContentHash: hash("util 2")},
		"/app/gone.go":       nil,
		"/app/link":          {Kind: KindSymlink, Target: "util.go"},
		"/app/conf":          nil,
		"/app/conf/app.yaml": {Kind: KindFile, Mode: ModeFile, Size: 4, ContentHash: hash("yaml")},
		"/new/pkg/a.go":      {Kind: KindFile, Mode: ModeFile, Size: 1, ContentHash: hash("a")},
		"/docs/guide.md":     {Kind: KindFile, Mode: ModeFile, Size: 5, ContentHash: hash("guide")}, // same as the base
	}
	opensBefore := content.openCount()
	changed, err := f.Switch(base2, diff, []string{"/app/out/bin", "/app/main.go"}, expect)
	if err != nil {
		t.Fatal(err)
	}
	if opens := content.openCount() - opensBefore; opens != 0 {
		t.Fatalf("Switch read %d contents; it must only record references", opens)
	}
	for _, p := range []string{"/app/main.go", "/app/util.go", "/app/gone.go", "/app/link", "/app/conf", "/app/conf/app.yaml", "/new/pkg/a.go", "/docs/extra.md"} {
		if !containsString(changed, p) {
			t.Fatalf("changed %v is missing %s", changed, p)
		}
	}

	want := map[string]string{
		"/app":               "dir",
		"/app/main.go":       "file 100644 main 2", // the old changeset's version is gone: main.go is tracked
		"/app/util.go":       "file 100755 util 2",
		"/app/link":          "symlink util.go",
		"/app/conf":          "dir",
		"/app/conf/app.yaml": "file 100644 yaml",
		"/app/.gitignore":    "file 100644 out/\n",
		"/app/out":           "dir",
		"/app/out/bin":       "file 100755 binary",
		"/docs":              "dir",
		"/docs/guide.md":     "file 100644 guide",
		"/docs/extra.md":     "file 100644 extra",
		"/new":               "dir",
		"/new/pkg":           "dir",
		"/new/pkg/a.go":      "file 100644 a",
	}
	if d := diffTrees(want, tree(t, f)); d != "" {
		t.Fatalf("tree after Switch:\n%s", d)
	}
	edits, err := f.Edits()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, edit := range edits {
		got = append(got, edit.Op+" "+edit.Path)
	}
	wantEdits := "delete /app/conf,upsert /app/conf/app.yaml,delete /app/gone.go,upsert /app/link,upsert /app/out/bin,upsert /app/util.go,upsert /new/pkg/a.go"
	if strings.Join(got, ",") != wantEdits {
		t.Fatalf("edits after Switch:\n  %s\nwant\n  %s", strings.Join(got, ","), wantEdits)
	}
	// The old changeset's local copy of main.go is deleted; the kept build
	// output is not.
	files, _ := os.ReadDir(filepath.Join(dir, filesDir))
	if len(files) != 1 {
		t.Fatalf("%d local files after Switch; want 1 (the build output)", len(files))
	}

	// On disk, over the new base.
	_ = f.Close()
	reopened := openFS(t, dir, base2, content)
	if d := diffTrees(want, tree(t, reopened)); d != "" {
		t.Fatalf("reopened tree:\n%s", d)
	}
}

func TestSwitchRefusesWhenTheWorkspaceChangedMeanwhile(t *testing.T) {
	content := newMemContent()
	base := newBase(t, content, "commit-1", map[string]baseFile{"/a.txt": {content: "a"}})
	f := openFS(t, t.TempDir(), base, content)
	expect, err := f.Edits()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.WriteFile("/a.txt", []byte("written after the caller looked"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Switch(base, map[string]*BaseEntry{"/a.txt": nil}, nil, expect); !errors.Is(err, ErrChanged) {
		t.Fatalf("Switch over a changed workspace: err = %v, want ErrChanged", err)
	}
	if data, _ := f.ReadFile(context.Background(), "/a.txt"); string(data) != "written after the caller looked" {
		t.Fatalf("a refused Switch changed the workspace: a.txt = %q", data)
	}
}

func TestRebaseCanKeepOursOrTakeTheirs(t *testing.T) {
	content := newMemContent()
	base1 := newBase(t, content, "commit-1", map[string]baseFile{
		"/keep.txt":   {content: "k1"},
		"/theirs.txt": {content: "t1"},
	})
	f := openFS(t, t.TempDir(), base1, content)
	for p, data := range map[string]string{"/keep.txt": "ours", "/theirs.txt": "ours"} {
		if err := f.WriteFile(p, []byte(data), 0); err != nil {
			t.Fatal(err)
		}
	}
	base2 := newBase(t, content, "commit-2", map[string]baseFile{
		"/keep.txt":   {content: "k2"},
		"/theirs.txt": {content: "t2"},
	})
	edits, _ := f.Edits()
	for _, edit := range edits {
		if !NeedsMerge(edit, base1, base2) {
			t.Fatalf("NeedsMerge(%s) = false; both sides changed it", edit.Path)
		}
	}
	result, err := f.Rebase(context.Background(), base2, func(ctx context.Context, c MergeCase) (MergeResult, error) {
		if c.Path == "/keep.txt" {
			return MergeResult{Keep: true}, nil
		}
		return MergeResult{Theirs: true}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Conflicts) != 0 {
		t.Fatalf("conflicts = %v", result.Conflicts)
	}
	want := map[string]string{"/keep.txt": "file 100644 ours", "/theirs.txt": "file 100644 t2"}
	if d := diffTrees(want, tree(t, f)); d != "" {
		t.Fatalf("tree after rebase:\n%s", d)
	}
}
