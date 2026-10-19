package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/clientcache"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func TestSummarizeChangesLabelsAndCountsFromLocalContent(t *testing.T) {
	cache, err := clientcache.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	put := func(data string) string {
		obj, err := cache.PutBytes([]byte(data))
		if err != nil {
			t.Fatal(err)
		}
		return obj.ContentHash
	}
	dir := t.TempDir()
	write := func(name, data string) workingFile {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		return workingFile{BaseSnapshotFile: BaseSnapshotFile{Path: "/a/" + name}, AbsPath: p}
	}
	base := BaseSnapshot{Files: map[string]BaseSnapshotFile{
		"/a/old.go":      {Path: "/a/old.go", ContentHash: put("x\ny\n")},
		"/a/edit.go":     {Path: "/a/edit.go", ContentHash: put("one\ntwo\n")},
		"/a/gone.txt":    {Path: "/a/gone.txt", ContentHash: put("1\n2\n3\n")},
		"/a/unfetched.c": {Path: "/a/unfetched.c", ContentHash: "sha256:not-on-this-machine"},
		"/a/img.png":     {Path: "/a/img.png", ContentHash: put("\x89PNG\x00a")},
	}}
	current := map[string]workingFile{
		"/a/new.go":      write("new.go", "n\n"),
		"/a/edit.go":     write("edit.go", "one\nTWO\nthree\n"),
		"/a/renamed.go":  write("renamed.go", "x\ny\nz\n"),
		"/a/unfetched.c": write("unfetched.c", "int x;\n"),
		"/a/img.png":     write("img.png", "\x89PNG\x00b"),
	}
	edits := []*corev1.FileEdit{
		{Op: "upsert", Path: "/a/new.go"},
		{Op: "upsert", Path: "/a/edit.go"},
		{Op: "delete", Path: "/a/gone.txt"},
		{Op: "rename", OldPath: "/a/old.go", Path: "/a/renamed.go"},
		{Op: "upsert", Path: "/a/unfetched.c"},
		{Op: "upsert", Path: "/a/img.png"},
		{Op: "mkdir", Path: "/a/empty"},
	}
	changes := summarizeChanges(edits, base, current, cache)
	got := map[string]pathChange{}
	for _, c := range changes {
		got[c.Path] = c
	}
	want := map[string]pathChange{
		"/a/new.go":      {Path: "/a/new.go", Kind: "added", Added: 1, Counted: true},
		"/a/edit.go":     {Path: "/a/edit.go", Kind: "modified", Added: 2, Removed: 1, Counted: true},
		"/a/gone.txt":    {Path: "/a/gone.txt", Kind: "deleted", Removed: 3, Counted: true},
		"/a/renamed.go":  {Path: "/a/renamed.go", Kind: "renamed", OldPath: "/a/old.go", Added: 1, Counted: true},
		"/a/unfetched.c": {Path: "/a/unfetched.c", Kind: "modified"},
		"/a/img.png":     {Path: "/a/img.png", Kind: "modified", Binary: true},
	}
	if len(got) != len(want) {
		t.Fatalf("changes = %+v", changes)
	}
	for p, w := range want {
		if got[p] != w {
			t.Errorf("%s = %+v, want %+v", p, got[p], w)
		}
	}

	var out bytes.Buffer
	printChangeSummary(&out, changes, "  ", false)
	for _, line := range []string{
		"  modified  /a/edit.go                  +2 -1",
		"  renamed   /a/old.go -> /a/renamed.go  +1",
		"  modified  /a/img.png                  binary",
		"  modified  /a/unfetched.c\n",
	} {
		if !strings.Contains(out.String(), line) {
			t.Errorf("summary missing %q:\n%s", line, out.String())
		}
	}
	if head := changeHeadline(changes); head != "6 changed path(s), +4 -4" {
		t.Errorf("headline = %q", head)
	}
}
