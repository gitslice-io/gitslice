package gitslicecfg

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"testing"

	"gitslice.io/gitslice/internal/objectstore"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

type fakeObjects map[string][]byte

func (f fakeObjects) Get(_ context.Context, key string, _, _ int64) (io.ReadCloser, error) {
	data, ok := f[key]
	if !ok {
		return nil, errors.New("missing object " + key)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func TestConfigPathsAccountBounded(t *testing.T) {
	got := ConfigPaths("", "/acme/app/api/users.go", "owners.yaml")
	want := []string{
		"/acme/app/api/.gitslice/owners.yaml",
		"/acme/app/.gitslice/owners.yaml",
		"/acme/.gitslice/owners.yaml",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ConfigPaths inferred = %v, want %v", got, want)
	}

	// Explicit account matches paths inside /acme and rejects paths under /other.
	gotExplicit := ConfigPaths("acme", "/acme/app/go.sum", ".gitslice/merge.yaml")
	wantExplicit := []string{
		"/acme/app/.gitslice/merge.yaml",
		"/acme/.gitslice/merge.yaml",
	}
	if !reflect.DeepEqual(gotExplicit, wantExplicit) {
		t.Fatalf("ConfigPaths explicit = %v, want %v", gotExplicit, wantExplicit)
	}
	if cross := ConfigPaths("other", "/acme/app/go.sum", "merge.yaml"); cross != nil {
		t.Fatalf("ConfigPaths cross-account = %v, want nil", cross)
	}
	for _, root := range []string{"", "/", "/acme"} {
		if res := ConfigPaths("acme", root, "owners.yaml"); res != nil {
			t.Fatalf("ConfigPaths(%q) = %v, want nil", root, res)
		}
	}
}

func TestTreeReaderBoundsAndAccountPrefix(t *testing.T) {
	objs := fakeObjects{
		objectstore.BlobKey("hash_small"): []byte("version: 1\n"),
		objectstore.BlobKey("hash_big"):   bytes.Repeat([]byte("x"), 32),
	}
	customNotFound := errors.New("custom not found")
	reader := TreeReader{
		MaxBytes:    16,
		Account:     "acme",
		NotFoundErr: customNotFound,
		Objects:     objs,
		LookupFile: func(_ context.Context, treeID, storagePath string) (FileEntry, bool, error) {
			if treeID != "tree_1" {
				return FileEntry{}, false, nil
			}
			switch storagePath {
			case "/acme/app/.gitslice/checks.yaml":
				return FileEntry{ContentHash: "hash_small", Size: 11}, true, nil
			case "/acme/app/oversized.yaml":
				return FileEntry{ContentHash: "hash_big", Size: 32}, true, nil
			case "/acme/app/lying_size.yaml":
				// Metadata claims 4 bytes, actual blob is 32 bytes.
				return FileEntry{ContentHash: "hash_big", Size: 4}, true, nil
			default:
				return FileEntry{}, false, nil
			}
		},
	}

	data, err := reader.ReadFile(context.Background(), "tree_1", "app/.gitslice/checks.yaml")
	if err != nil || string(data) != "version: 1\n" {
		t.Fatalf("ReadFile small = (%q, %v)", data, err)
	}
	if _, err := reader.ReadFile(context.Background(), "tree_1", "app/missing.yaml"); !errors.Is(err, customNotFound) {
		t.Fatalf("ReadFile missing err = %v, want customNotFound", err)
	}
	for _, big := range []string{"app/oversized.yaml", "app/lying_size.yaml"} {
		if _, err := reader.ReadFile(context.Background(), "tree_1", big); !errors.Is(err, ErrFileTooLarge) {
			t.Fatalf("ReadFile(%s) err = %v, want ErrFileTooLarge", big, err)
		}
	}
}

func TestResolverMemoizesReadsAndParsesPerTreeAndPath(t *testing.T) {
	reads := 0
	parses := 0
	files := map[string]string{
		"/acme/.gitslice/owners.yaml":     "root-cfg",
		"/acme/app/.gitslice/owners.yaml": "app-cfg",
	}
	resolver := NewResolver(
		".gitslice/owners.yaml",
		func(_ context.Context, treeID, cfgPath string) ([]byte, bool, error) {
			reads++
			v, ok := files[cfgPath]
			if treeID != "t1" || !ok {
				return nil, false, nil
			}
			return []byte(v), true, nil
		},
		func(dir string, data []byte) (string, error) {
			parses++
			return dir + ":" + string(data), nil
		},
	)

	// Query 50 files under /acme/app/sub/... -> must read & parse /acme/app/.gitslice/owners.yaml ONCE.
	for i := 0; i < 50; i++ {
		cfgPath, val, ok, err := resolver.Nearest(context.Background(), "t1", "acme", "/acme/app/sub/file.go")
		if err != nil || !ok || cfgPath != "/acme/app/.gitslice/owners.yaml" || val != "/acme/app:app-cfg" {
			t.Fatalf("Nearest #%d = (%q, %q, %v, %v)", i, cfgPath, val, ok, err)
		}
	}
	// Query 20 files under /acme/other/... -> misses /acme/other once, hits /acme once.
	for i := 0; i < 20; i++ {
		cfgPath, val, ok, err := resolver.Nearest(context.Background(), "t1", "acme", "/acme/other/file.go")
		if err != nil || !ok || cfgPath != "/acme/.gitslice/owners.yaml" || val != "/acme:root-cfg" {
			t.Fatalf("Nearest fallback #%d = (%q, %q, %v, %v)", i, cfgPath, val, ok, err)
		}
	}
	// Expected unique config paths read:
	// 1. /acme/app/sub/.gitslice/owners.yaml (miss)
	// 2. /acme/app/.gitslice/owners.yaml (hit + parse)
	// 3. /acme/other/.gitslice/owners.yaml (miss)
	// 4. /acme/.gitslice/owners.yaml (hit + parse)
	if reads != 4 {
		t.Fatalf("reads = %d, want 4", reads)
	}
	if parses != 2 {
		t.Fatalf("parses = %d, want 2", parses)
	}
}

func TestCollectTouchedPathsExpandsCopyMoveAndDeleteGitsliceSubtrees(t *testing.T) {
	tree := map[string][]string{
		"c1:/acme/templates/bundle": {
			"/acme/templates/bundle/.gitslice/checks.yaml",
			"/acme/templates/bundle/README.md",
		},
		"c2:/acme/legacy": {
			"/acme/legacy/sub/file.go",
		},
	}
	listCalls := 0
	listDir := func(_ context.Context, cid, dir string) ([]string, error) {
		listCalls++
		return tree[cid+":"+dir], nil
	}

	psCopy := &corev1.Patchset{
		BaseCommitId: "c1",
		FileEdits: []*corev1.FileEdit{
			{Op: "copy", OldPath: "/acme/templates/bundle", Path: "/acme/app"},
		},
	}
	gotCopy, err := CollectTouchedPaths(context.Background(), psCopy, []string{"c2"}, listDir)
	if err != nil {
		t.Fatalf("CollectTouchedPaths copy: %v", err)
	}
	wantCopy := []string{
		"/acme/app",
		"/acme/app/.gitslice/checks.yaml",
		"/acme/app/README.md",
	}
	if !reflect.DeepEqual(gotCopy, wantCopy) {
		t.Fatalf("gotCopy = %v, want %v", gotCopy, wantCopy)
	}
	if got := FirstGitslicePath(gotCopy); got != "/acme/app/.gitslice/checks.yaml" {
		t.Fatalf("FirstGitslicePath(copy) = %q, want /acme/app/.gitslice/checks.yaml", got)
	}

	psRename := &corev1.Patchset{
		BaseCommitId: "c1",
		FileEdits: []*corev1.FileEdit{
			{Op: "rename", OldPath: "/acme/legacy", Path: "/acme/modern/.gitslice/hooks"},
		},
	}
	gotRename, err := CollectTouchedPaths(context.Background(), psRename, []string{"c2"}, listDir)
	if err != nil {
		t.Fatalf("CollectTouchedPaths rename: %v", err)
	}
	wantRename := []string{
		"/acme/legacy",
		"/acme/legacy/sub/file.go",
		"/acme/modern/.gitslice/hooks",
		"/acme/modern/.gitslice/hooks/sub/file.go",
	}
	if !reflect.DeepEqual(gotRename, wantRename) {
		t.Fatalf("gotRename = %v, want %v", gotRename, wantRename)
	}
	if got := FirstGitslicePath(gotRename); got != "/acme/modern/.gitslice/hooks" {
		t.Fatalf("FirstGitslicePath(rename) = %q, want /acme/modern/.gitslice/hooks", got)
	}
}
