package clientcache

import (
	"os"
	"path/filepath"
	"testing"

	"gitslice.io/gitslice/internal/objectid"
)

func TestObjectCacheDeduplicatesAcrossWorkspaceFiles(t *testing.T) {
	cache, err := New(filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	firstWorkspace := t.TempDir()
	secondWorkspace := t.TempDir()
	content := []byte("package payment\nconst Shared = true\n")
	firstPath := filepath.Join(firstWorkspace, "shared.go")
	secondPath := filepath.Join(secondWorkspace, "copy.go")
	if err := os.WriteFile(firstPath, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	first, err := cache.PutFile(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	second, err := cache.PutFile(secondPath)
	if err != nil {
		t.Fatal(err)
	}
	if first.ContentHash != second.ContentHash {
		t.Fatalf("content hashes differ: %s vs %s", first.ContentHash, second.ContentHash)
	}
	if first.ContentHash != objectid.RawContentHash(content) {
		t.Fatalf("unexpected content hash %s", first.ContentHash)
	}

	got, err := cache.Read(first.ContentHash)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Fatalf("unexpected cached bytes: %q", string(got))
	}
}

func TestObjectPathRejectsUnsafeHashes(t *testing.T) {
	if _, err := ObjectPath("sha256:../bad"); err == nil {
		t.Fatal("expected invalid hash to be rejected")
	}
	if _, err := ObjectPath("md5:abcdef"); err == nil {
		t.Fatal("expected unsupported algorithm to be rejected")
	}
}

func TestObjectCacheTightensPermissionsAndSkipsTempOnPutBytesHit(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cache")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	cache, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{root, filepath.Join(root, "objects"), filepath.Join(root, "tmp")} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o700 {
			t.Fatalf("%s perm = %#o, want 0700", dir, got)
		}
	}

	data := []byte("cached payload\n")
	obj, err := cache.PutBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	// Make tmp read-only to prove a PutBytes hit never creates a temp file.
	tmpDir := filepath.Join(root, "tmp")
	if err := os.Chmod(tmpDir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(tmpDir, 0o700)

	hit, err := cache.PutBytes(data)
	if err != nil {
		t.Fatalf("PutBytes cache hit created temp file instead of returning immediately: %v", err)
	}
	if hit != obj {
		t.Fatalf("PutBytes hit = %+v, want %+v", hit, obj)
	}
}
