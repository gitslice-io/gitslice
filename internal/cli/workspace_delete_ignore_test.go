package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"gitslice.io/gitslice/internal/lazyfs"
)

// A lazy workspace's ignored build output is not an edit that would be lost.
func TestDropIgnoredLazyEdits(t *testing.T) {
	r := Runner{Home: t.TempDir()}
	rec := mountRecord{ID: "ws1"}
	gsDir := r.mountGsDir(rec.ID)
	if err := os.MkdirAll(gsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(WorkspaceConfig{Account: "acme", Slice: "app", IncludedPaths: []string{"/acme/app"}})
	if err := os.WriteFile(filepath.Join(gsDir, "slice.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"acme/app/.gitignore":     "node_modules/\n*.log\n",
		"acme/app/web/.gitignore": "dist\n",
	}
	read := func(rel string) []byte {
		if s, ok := files[rel]; ok {
			return []byte(s)
		}
		return nil
	}
	edits := []lazyfs.Edit{
		{Op: "upsert", Path: "/acme/app/main.go"},
		{Op: "upsert", Path: "/acme/app/node_modules/x/index.js"},
		{Op: "upsert", Path: "/acme/app/debug.log"},
		{Op: "upsert", Path: "/acme/app/web/dist/app.js"},
		{Op: "upsert", Path: "/acme/app/web/src/app.ts"},
		{Op: "delete", Path: "/acme/app/old.txt"},
	}
	got := r.dropIgnoredLazyEdits(rec, edits, read)
	var paths []string
	for _, e := range got {
		paths = append(paths, e.Path)
	}
	want := []string{"/acme/app/main.go", "/acme/app/web/src/app.ts", "/acme/app/old.txt"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("kept %v, want %v", paths, want)
	}
}
