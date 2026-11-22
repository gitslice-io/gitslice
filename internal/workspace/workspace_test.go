package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFindRootAndDamagedDetection(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := FindRoot(nested); !errors.Is(err, ErrNotInWorkspace) {
		t.Fatalf("FindRoot outside workspace = %v, want ErrNotInWorkspace", err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".gs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := FindRoot(nested); !errors.Is(err, ErrWorkspaceDamaged) {
		t.Fatalf("FindRoot damaged = %v, want ErrWorkspaceDamaged", err)
	}
	if err := WriteJSONFile(filepath.Join(root, ".gs", "slice.json"), Config{Account: "acme", Slice: "core"}, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := FindRoot(nested)
	if err != nil {
		t.Fatalf("FindRoot valid = %v", err)
	}
	if got != root {
		t.Fatalf("FindRoot = %q, want %q", got, root)
	}
}

func TestConfinementRejectsGsAndTraversal(t *testing.T) {
	root := t.TempDir()
	if _, _, err := WriteConfinedFile(root, ".gs/slice.json", 0o100644, strings.NewReader("{}")); !errors.Is(err, ErrReservedPath) {
		t.Fatalf("WriteConfinedFile(.gs/slice.json) = %v, want ErrReservedPath", err)
	}
	if _, _, err := WriteConfinedFile(root, "../escape.txt", 0o100644, strings.NewReader("nope")); err == nil {
		t.Fatal("WriteConfinedFile(../escape.txt) succeeded, want error")
	}
	if _, _, err := WriteConfinedFile(root, "acme/hello.txt", 0o100644, strings.NewReader("hi")); err != nil {
		t.Fatalf("WriteConfinedFile valid: %v", err)
	}
	data, err := ReadConfinedFile(root, "acme/hello.txt")
	if err != nil || string(data) != "hi" {
		t.Fatalf("ReadConfinedFile = (%q, %v), want (\"hi\", nil)", string(data), err)
	}
}

func TestStatCacheRoundTrip(t *testing.T) {
	root := t.TempDir()
	past := time.Now().Add(-time.Minute).Truncate(time.Second)
	WriteStatCache(root, map[string]StatCacheEntry{
		"acme/a.txt": {Size: 3, ModTimeNs: past.UnixNano(), Mode: 0o100644, ContentHash: "sha256:abc"},
	})
	cache := ReadStatCache(root)
	got, ok := cache.Hash("acme/a.txt", 3, past, 0o100644)
	if !ok || got != "sha256:abc" {
		t.Fatalf("Hash = (%q, %v), want (sha256:abc, true)", got, ok)
	}
	if _, ok := cache.Hash("acme/a.txt", 4, past, 0o100644); ok {
		t.Fatal("Hash matched wrong size")
	}
}
