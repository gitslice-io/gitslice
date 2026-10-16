package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitslice.io/gitslice/internal/lazyfs"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// TestOverlayAndWalkFindTheSameEdits makes two workspaces from one base, an
// eager directory and a lazy layer, applies the same changes to both, and
// expects the two edit sources to find the same edits: against the base, and
// against the base of a changeset higher in a dependency tree. This is what
// guarantees that a lazy workspace creates exactly the changeset an eager one
// would.
func TestOverlayAndWalkFindTheSameEdits(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	r := Runner{Home: t.TempDir(), Dir: workspace}
	ws := WorkspaceConfig{Account: "acme", Slice: "app", IncludedPaths: []string{"/acme/app"}}
	if err := r.writeWorkspaceConfig(ws); err != nil {
		t.Fatal(err)
	}
	cache, err := r.objectCache()
	if err != nil {
		t.Fatal(err)
	}

	type file struct {
		content    string
		executable bool
		symlink    string
	}
	baseFiles := map[string]file{
		"/acme/app/main.go":        {content: "package main\n"},
		"/acme/app/lib/a.go":       {content: "package lib // a\n"},
		"/acme/app/lib/b.go":       {content: "package lib // b\n"},
		"/acme/app/docs/readme.md": {content: "# docs\n"},
		"/acme/app/docs/guide.md":  {content: "# guide\n"},
		"/acme/app/run.sh":         {content: "#!/bin/sh\n", executable: true},
		"/acme/app/link":           {symlink: "main.go"},
		"/acme/app/.gitignore":     {content: "build/\n*.log\n"},
	}
	base := BaseSnapshot{CommitID: "commit-1", Files: map[string]BaseSnapshotFile{}}
	var entries []lazyfs.BaseEntry
	for p, f := range baseFiles {
		local := filepath.Join(workspace, strings.TrimPrefix(p, "/"))
		if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
			t.Fatal(err)
		}
		mode, data := uint32(0o100644), []byte(f.content)
		switch {
		case f.symlink != "":
			mode, data = 0o120000, []byte(f.symlink)
			if err := os.Symlink(f.symlink, local); err != nil {
				t.Fatal(err)
			}
		default:
			if f.executable {
				mode = 0o100755
			}
			if err := os.WriteFile(local, data, os.FileMode(mode&0o777)); err != nil {
				t.Fatal(err)
			}
		}
		object, err := cache.PutBytes(data)
		if err != nil {
			t.Fatal(err)
		}
		base.Files[p] = BaseSnapshotFile{Path: p, RelPath: strings.TrimPrefix(p, "/"), ContentHash: object.ContentHash, Mode: mode, Size: int64(len(data))}
		kind := lazyfs.KindFile
		if f.symlink != "" {
			kind = lazyfs.KindSymlink
		}
		entries = append(entries, lazyfs.BaseEntry{Path: p, Kind: kind, Mode: mode, Size: int64(len(data)), ContentHash: object.ContentHash, Target: f.symlink})
	}
	if err := r.writeBaseSnapshot(base); err != nil {
		t.Fatal(err)
	}
	manifest, err := lazyfs.NewManifest("commit-1", entries)
	if err != nil {
		t.Fatal(err)
	}
	layer, err := lazyfs.Open(t.TempDir(), manifest, cacheContent{cache: cache}, lazyfs.Options{NoSync: true})
	if err != nil {
		t.Fatal(err)
	}
	defer layer.Close()

	// The same changes, to the directory and to the layer.
	disk := func(p string) string { return filepath.Join(workspace, strings.TrimPrefix(p, "/")) }
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	write := func(p, content string) {
		t.Helper()
		must(os.MkdirAll(filepath.Dir(disk(p)), 0o755))
		must(os.WriteFile(disk(p), []byte(content), 0o644))
		for dir := filepath.Dir(p); dir != "/acme/app"; dir = filepath.Dir(dir) {
			if _, err := layer.Stat(dir); err != nil {
				must(mkdirAll(layer, dir))
			}
		}
		must(layer.WriteFile(p, []byte(content), 0))
	}
	write("/acme/app/main.go", "package main // edited\n")
	write("/acme/app/new/deep/x.txt", "new file\n")
	must(os.Remove(disk("/acme/app/lib/b.go")))
	must(layer.Remove("/acme/app/lib/b.go"))
	must(os.Rename(disk("/acme/app/docs"), disk("/acme/app/guide")))
	must(layer.Rename("/acme/app/docs", "/acme/app/guide"))
	must(os.Chmod(disk("/acme/app/lib/a.go"), 0o755))
	must(layer.Chmod("/acme/app/lib/a.go", 0o100755))
	must(os.Symlink("lib", disk("/acme/app/link2")))
	must(layer.Symlink("lib", "/acme/app/link2"))
	must(os.Remove(disk("/acme/app/link")))
	must(layer.Remove("/acme/app/link"))
	write("/acme/app/link", "now a file\n")
	write("/acme/app/build/out.bin", "ignored build output\n")
	write("/acme/app/debug.log", "ignored log\n")
	// Opened for writing in the layer and left as it was: no edit.
	_, err = layer.OpenWritable(ctx, "/acme/app/run.sh", false)
	must(err)

	compare := func(name string, against BaseSnapshot) {
		t.Helper()
		walked, err := walkEditSource{r: r, ws: ws}.edits(ctx, against, 2)
		must(err)
		overlaid, err := overlayEditSource{fs: layer, ws: ws}.edits(ctx, against, 2)
		must(err)
		if want, got := describeEdits(walked.edits), describeEdits(overlaid.edits); want != got {
			t.Fatalf("%s: edits differ\nwalk:\n%s\noverlay:\n%s", name, want, got)
		}
		for _, edit := range overlaid.edits {
			if edit.Op != "upsert" {
				continue
			}
			got, err := readWorkingFile(overlaid.files[edit.Path], cache)
			must(err)
			want, err := readWorkingFile(walked.files[edit.Path], cache)
			if isSymlinkMode(edit.Mode) {
				want, err = []byte(must2(os.Readlink(disk(edit.Path)))), nil
			}
			must(err)
			if string(got) != string(want) {
				t.Fatalf("%s: content of %s: walk %q, overlay %q", name, edit.Path, want, got)
			}
		}
		if strings.Contains(describeEdits(overlaid.edits), "build/out.bin") || strings.Contains(describeEdits(overlaid.edits), "debug.log") {
			t.Fatalf("%s: ignored files became edits:\n%s", name, describeEdits(overlaid.edits))
		}
	}
	compare("against the base", base)
	found, err := overlayEditSource{fs: layer, ws: ws}.edits(ctx, base, 2)
	must(err)
	described := describeEdits(found.edits)
	for _, want := range []string{
		"upsert /acme/app/main.go 100644",
		"delete /acme/app/lib/b.go",
		"delete /acme/app/docs/readme.md",
		"upsert /acme/app/guide/readme.md 100644",
		"upsert /acme/app/new/deep/x.txt 100644",
		"upsert /acme/app/lib/a.go 100755",
		"upsert /acme/app/link2 120000",
		"upsert /acme/app/link 100644",
	} {
		if !strings.Contains(described, want) {
			t.Fatalf("edits are missing %q:\n%s", want, described)
		}
	}
	if strings.Contains(described, "run.sh") {
		t.Fatalf("a file opened for writing and left alone is no edit:\n%s", described)
	}

	// A changeset lower in a dependency tree changed main.go and run.sh,
	// deleted lib/a.go and added extra.txt; edits are now counted from there.
	stacked := BaseSnapshot{CommitID: base.CommitID, Files: map[string]BaseSnapshotFile{}}
	for p, f := range base.Files {
		stacked.Files[p] = f
	}
	for p, content := range map[string]string{"/acme/app/main.go": "package main // lower changeset\n", "/acme/app/run.sh": "#!/bin/sh\necho lower\n", "/acme/app/extra.txt": "extra\n"} {
		object, err := cache.PutBytes([]byte(content))
		must(err)
		stacked.Files[p] = BaseSnapshotFile{Path: p, RelPath: strings.TrimPrefix(p, "/"), ContentHash: object.ContentHash, Mode: 0o100644}
	}
	delete(stacked.Files, "/acme/app/lib/a.go")
	compare("against a dependency tree's base", stacked)
}

func mkdirAll(layer *lazyfs.FS, dir string) error {
	if dir == "/" {
		return nil
	}
	if _, err := layer.Stat(dir); err == nil {
		return nil
	}
	if err := mkdirAll(layer, filepath.Dir(dir)); err != nil {
		return err
	}
	return layer.Mkdir(dir)
}

func must2(s string, err error) string {
	if err != nil {
		panic(err)
	}
	return s
}

func describeEdits(edits []*corev1.FileEdit) string {
	var b strings.Builder
	for _, edit := range edits {
		if edit.Op == "delete" {
			fmt.Fprintf(&b, "delete %s\n", edit.Path)
			continue
		}
		fmt.Fprintf(&b, "%s %s %o %s\n", edit.Op, edit.Path, edit.Mode, edit.ContentHash)
	}
	return b.String()
}
