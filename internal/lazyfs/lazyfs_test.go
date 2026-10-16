package lazyfs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManifestFillsInDirectoriesAndRejectsBadListings(t *testing.T) {
	content := newMemContent()
	m := newBase(t, content, "c1", map[string]baseFile{"/a/b/c.txt": {content: "x"}, "/a/d.txt": {content: "y"}})
	for _, dir := range []string{"/", "/a", "/a/b"} {
		if entry, ok := m.Lookup(dir); !ok || entry.Kind != KindDir {
			t.Fatalf("%s should be a directory: %+v", dir, entry)
		}
	}
	if got := strings.Join(m.Children("/a"), ","); got != "b,d.txt" {
		t.Fatalf("children of /a = %s", got)
	}
	if _, err := NewManifest("c", []BaseEntry{{Path: "/a", Kind: KindFile}, {Path: "/a/b", Kind: KindFile}}); err == nil {
		t.Fatal("a file under a file must be refused")
	}
	if _, err := NewManifest("c", []BaseEntry{{Path: "/a", Kind: KindFile}, {Path: "/a", Kind: KindFile}}); err == nil {
		t.Fatal("a path listed twice must be refused")
	}
	if _, err := NewManifest("c", []BaseEntry{{Path: "a", Kind: KindFile}}); err == nil {
		t.Fatal("a relative path must be refused")
	}
}

// TestRenamesAndModeChangesFetchNothing: moving a whole base directory, or
// making a base file executable, records references; no content is read, and
// the edits name the content the server already has.
func TestRenamesAndModeChangesFetchNothing(t *testing.T) {
	content := newMemContent()
	files := map[string]baseFile{}
	for i := 0; i < 100; i++ {
		files[fmt.Sprintf("/big/dir%d/file%d.bin", i%10, i)] = baseFile{content: fmt.Sprintf("payload %d", i)}
	}
	files["/tool.sh"] = baseFile{content: "#!/bin/sh"}
	base := newBase(t, content, "c1", files)
	f := openFS(t, t.TempDir(), base, content)

	if err := f.Rename("/big", "/moved"); err != nil {
		t.Fatal(err)
	}
	if err := f.Chmod("/tool.sh", ModeExecutable); err != nil {
		t.Fatal(err)
	}
	if err := f.Rename("/moved/dir3", "/dir3"); err != nil {
		t.Fatal(err)
	}
	edits, err := f.Edits()
	if err != nil {
		t.Fatal(err)
	}
	if opens := content.openCount(); opens != 0 {
		t.Fatalf("renames and chmod read %d contents, want none", opens)
	}
	upserts, deletes := 0, 0
	for _, edit := range edits {
		switch edit.Op {
		case "delete":
			deletes++
		case "upsert":
			upserts++
			if edit.LocalPath != "" {
				t.Fatalf("%s should be a reference, not a local copy", edit.Path)
			}
			if _, known := content.blobs[edit.ContentHash]; !known {
				t.Fatalf("%s names content the base does not have", edit.Path)
			}
		}
	}
	if deletes != 100 || upserts != 101 {
		t.Fatalf("edits: %d deletes, %d upserts; want 100 and 101 (the moved files and tool.sh)", deletes, upserts)
	}
	if attr, err := f.Stat("/dir3/file3.bin"); err != nil || attr.Kind != KindFile || attr.Local {
		t.Fatalf("a moved file is still served from the base: %+v, %v", attr, err)
	}
}

func TestCopyUpReadsOnceAndTruncateSkipsTheRead(t *testing.T) {
	content := newMemContent()
	base := newBase(t, content, "c1", map[string]baseFile{"/a.txt": {content: "hello"}, "/b.txt": {content: "world"}})
	f := openFS(t, t.TempDir(), base, content)
	ctx := context.Background()

	local, err := f.OpenWritable(ctx, "/a.txt", false)
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(local); string(data) != "hello" {
		t.Fatalf("copied-up content = %q", data)
	}
	if again, err := f.OpenWritable(ctx, "/a.txt", false); err != nil || again != local {
		t.Fatalf("a second open must reuse the copy: %q, %v", again, err)
	}
	if _, err := f.OpenWritable(ctx, "/b.txt", true); err != nil {
		t.Fatal(err)
	}
	if opens := content.openCount(); opens != 1 {
		t.Fatalf("reads = %d, want 1 (the copy-up of a.txt; truncating b.txt reads nothing)", opens)
	}
	// A copy-up that changes nothing is no edit.
	edits, err := f.Edits()
	if err != nil || len(edits) != 1 || edits[0].Path != "/b.txt" {
		t.Fatalf("edits = %+v, %v; want only the truncated b.txt", edits, err)
	}
}

type countingContent struct {
	*memContent
	hashed int
}

func (c *countingContent) HashFile(p string) (string, error) {
	c.hashed++
	return c.memContent.HashFile(p)
}

func TestEditsRememberHashesUntilAFileChanges(t *testing.T) {
	content := &countingContent{memContent: newMemContent()}
	base := newBase(t, content.memContent, "c1", map[string]baseFile{"/a.txt": {content: "a"}})
	f := openFS(t, t.TempDir(), base, content)
	if err := f.WriteFile("/a.txt", []byte("changed"), 0); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := f.Edits(); err != nil {
			t.Fatal(err)
		}
	}
	if content.hashed != 1 {
		t.Fatalf("hashed %d times, want 1", content.hashed)
	}
	local, err := f.OpenWritable(context.Background(), "/a.txt", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, []byte("changed again, longer"), 0o600); err != nil {
		t.Fatal(err)
	}
	edits, err := f.Edits()
	if err != nil || content.hashed != 2 || edits[0].ContentHash != hashOf([]byte("changed again, longer")) {
		t.Fatalf("after a change: hashed %d, edits %+v, %v", content.hashed, edits, err)
	}
}

func TestRestore(t *testing.T) {
	content := newMemContent()
	base := newBase(t, content, "c1", map[string]baseFile{"/app/a.txt": {content: "a"}, "/app/sub/b.txt": {content: "b"}, "/other.txt": {content: "o"}})
	dir := t.TempDir()
	f := openFS(t, dir, base, content)
	before := tree(t, f)

	if err := f.WriteFile("/app/a.txt", []byte("edited"), 0); err != nil {
		t.Fatal(err)
	}
	if err := f.WriteFile("/app/sub/new.txt", []byte("new"), 0); err != nil {
		t.Fatal(err)
	}
	if err := f.Remove("/other.txt"); err != nil {
		t.Fatal(err)
	}
	if err := f.Restore("/app/a.txt"); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.ReadFile(context.Background(), "/app/a.txt"); string(got) != "a" {
		t.Fatalf("restored a.txt = %q", got)
	}
	if err := f.Restore("/app"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Stat("/app/sub/new.txt"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("restoring /app should drop new.txt: %v", err)
	}

	// A path inside a deleted directory is restored with the directory.
	if err := f.Remove("/app/sub/b.txt"); err != nil {
		t.Fatal(err)
	}
	if err := f.Remove("/app/sub"); err != nil {
		t.Fatal(err)
	}
	if err := f.Restore("/app/sub/b.txt"); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("restoring inside a deleted directory: err = %v, want ErrInvalid", err)
	}
	if err := f.RestoreAll(); err != nil {
		t.Fatal(err)
	}
	if d := diffTrees(before, tree(t, f)); d != "" {
		t.Fatalf("RestoreAll should bring back the base:\n%s", d)
	}
	if edits, _ := f.Edits(); len(edits) != 0 {
		t.Fatalf("edits after RestoreAll: %+v", edits)
	}
	if files, _ := os.ReadDir(filepath.Join(dir, filesDir)); len(files) != 0 {
		t.Fatalf("%d local files left after RestoreAll", len(files))
	}
}

func TestRebaseMergesOnlyWhatBothSidesChanged(t *testing.T) {
	content := newMemContent()
	base1 := newBase(t, content, "commit-1", map[string]baseFile{
		"/app/keep.txt": {content: "k1"},
		"/app/edit.txt": {content: "e1"},
		"/app/both.txt": {content: "b1"},
		"/app/same.txt": {content: "s1"},
		"/app/del.txt":  {content: "d1"},
		"/old/x.txt":    {content: "x"},
		"/old/y.txt":    {content: "y"},
		"/gone/z.txt":   {content: "z"},
	})
	dir := t.TempDir()
	f := openFS(t, dir, base1, content)
	write := func(p, data string) {
		t.Helper()
		if err := f.WriteFile(p, []byte(data), 0); err != nil {
			t.Fatal(err)
		}
	}
	write("/app/edit.txt", "mine")
	write("/app/both.txt", "ours")
	write("/app/same.txt", "s2")
	write("/app/new.txt", "new")
	for _, p := range []string{"/app/del.txt", "/old/x.txt", "/old/y.txt", "/old", "/gone/z.txt", "/gone"} {
		if err := f.Remove(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Mkdir("/scratch"); err != nil {
		t.Fatal(err)
	}

	base2 := newBase(t, content, "commit-2", map[string]baseFile{
		"/app/keep.txt":   {content: "k2"},     // upstream only: taken as is
		"/app/edit.txt":   {content: "e1"},     // untouched upstream: ours stays
		"/app/both.txt":   {content: "theirs"}, // both: merged
		"/app/same.txt":   {content: "s2"},     // same change: no conflict
		"/app/del.txt":    {content: "d2"},     // we deleted, they changed: merged
		"/old/x.txt":      {content: "x"},      // deleted dir, unchanged upstream
		"/old/y.txt":      {content: "y"},
		"/gone/z.txt":     {content: "z"},
		"/gone/added.txt": {content: "added"}, // upstream added to a dir we deleted
		"/up/new.txt":     {content: "upstream"},
	})
	opensBefore := content.openCount()
	var cases []string
	merge := func(ctx context.Context, c MergeCase) (MergeResult, error) {
		cases = append(cases, c.Path)
		switch c.Path {
		case "/app/both.txt":
			return MergeResult{Content: []byte("merged"), Mode: ModeFile, Conflict: true}, nil
		case "/app/del.txt":
			return MergeResult{Delete: true}, nil
		}
		return MergeResult{}, fmt.Errorf("unexpected merge of %s", c.Path)
	}
	conflicts, err := f.Rebase(context.Background(), base2, merge)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cases, ","); got != "/app/both.txt,/app/del.txt" {
		t.Fatalf("merged %s; want only both.txt and del.txt", got)
	}
	if strings.Join(conflicts, ",") != "/app/both.txt" {
		t.Fatalf("conflicts = %v", conflicts)
	}
	if opens := content.openCount() - opensBefore; opens != 0 {
		t.Fatalf("rebase read %d contents; untouched paths must cost nothing", opens)
	}

	want := map[string]string{
		"/app":            "dir",
		"/app/keep.txt":   "file 100644 k2",
		"/app/edit.txt":   "file 100644 mine",
		"/app/both.txt":   "file 100644 merged",
		"/app/same.txt":   "file 100644 s2",
		"/app/new.txt":    "file 100644 new",
		"/gone":           "dir",
		"/gone/added.txt": "file 100644 added",
		"/up":             "dir",
		"/up/new.txt":     "file 100644 upstream",
		"/scratch":        "dir",
	}
	if d := diffTrees(want, tree(t, f)); d != "" {
		t.Fatalf("tree after rebase:\n%s", d)
	}
	edits, err := f.Edits()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, edit := range edits {
		got = append(got, edit.Op+" "+edit.Path)
	}
	wantEdits := "upsert /app/both.txt,delete /app/del.txt,upsert /app/edit.txt,upsert /app/new.txt,delete /gone/z.txt,delete /old/x.txt,delete /old/y.txt"
	if strings.Join(got, ",") != wantEdits {
		t.Fatalf("edits after rebase:\n  %s\nwant\n  %s", strings.Join(got, ","), wantEdits)
	}

	// The layer now belongs to the new base, on disk too.
	_ = f.Close()
	if recorded, err := RecordedBase(dir); err != nil || recorded != "commit-2" {
		t.Fatalf("RecordedBase = %q, %v", recorded, err)
	}
	if _, err := Open(dir, base1, content, Options{NoSync: true}); !errors.Is(err, ErrBaseMismatch) {
		t.Fatalf("opening over the old base: err = %v, want ErrBaseMismatch", err)
	}
	reopened := openFS(t, dir, base2, content)
	if d := diffTrees(want, tree(t, reopened)); d != "" {
		t.Fatalf("reopened tree:\n%s", d)
	}
	files, _ := os.ReadDir(filepath.Join(dir, filesDir))
	if len(files) != 3 {
		t.Fatalf("%d local files; want 3 (edit.txt, new.txt and the merged both.txt)", len(files))
	}
}

func TestOpenRejectsAPathOutsideTheRepository(t *testing.T) {
	content := newMemContent()
	f := openFS(t, t.TempDir(), newBase(t, content, "c1", nil), content)
	for _, p := range []string{"relative", "/a/../b", "/a//b"} {
		if _, err := f.Stat(p); err == nil {
			t.Fatalf("Stat(%q) should fail", p)
		}
	}
}
