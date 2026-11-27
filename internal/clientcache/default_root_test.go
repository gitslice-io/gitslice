package clientcache

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultRootResolutionAndNoDoubleObjectsNesting(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }

	// Explicit home uses <home>/.cache/gitslice, and New creates objects/ directly under it (not objects/objects).
	root := DefaultRoot(tmp, getenv)
	want := filepath.Join(tmp, ".cache", "gitslice")
	if root != want {
		t.Fatalf("DefaultRoot(home) = %q, want %q", root, want)
	}
	cache, err := New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cache.Root(), "objects")); err != nil {
		t.Fatalf("expected objects dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cache.Root(), "objects", "objects")); !os.IsNotExist(err) {
		t.Fatalf("unexpected nested objects/objects directory")
	}

	// Env override wins over home.
	override := filepath.Join(tmp, "custom-cache")
	env["GS_CLIENT_CACHE_DIR"] = override
	if got := DefaultRoot(tmp, getenv); got != override {
		t.Fatalf("DefaultRoot(GS_CLIENT_CACHE_DIR) = %q, want %q", got, override)
	}
	delete(env, "GS_CLIENT_CACHE_DIR")
	env["GITSLICE_CLIENT_CACHE_DIR"] = override
	if got := DefaultRoot(tmp, getenv); got != override {
		t.Fatalf("DefaultRoot(GITSLICE_CLIENT_CACHE_DIR) = %q, want %q", got, override)
	}
}
