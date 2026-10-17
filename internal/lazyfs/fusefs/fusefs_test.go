//go:build linux

package fusefs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"testing"

	"gitslice.io/gitslice/internal/lazyfs"
)

// fakeFetcher serves base content from memory, writing each file once to a
// directory, and counts what it fetched.
type fakeFetcher struct {
	mu      sync.Mutex
	dir     string
	content map[string][]byte // hash -> content
	fetched map[string]int    // path -> fetches
	fail    map[string]error
}

func (f *fakeFetcher) Fetch(ctx context.Context, p string, attr lazyfs.Attr) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail[p]; err != nil {
		return "", false, err
	}
	local := filepath.Join(f.dir, attr.ContentHash[len("sha256:"):])
	if _, err := os.Stat(local); err == nil {
		return local, false, nil
	}
	if err := os.WriteFile(local, f.content[attr.ContentHash], 0o600); err != nil {
		return "", false, err
	}
	f.fetched[p]++
	return local, true, nil
}

type noContent struct{}

func (noContent) Open(ctx context.Context, hash string) (io.ReadCloser, error) {
	return nil, errors.New("not used")
}
func (noContent) HashFile(p string) (string, error)     { return "", errors.New("not used") }
func (noContent) HashBytes(data []byte) (string, error) { return "", errors.New("not used") }

func hashOf(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func mountTest(t *testing.T) (string, *fakeFetcher, *Mounted) {
	t.Helper()
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skip("no /dev/fuse here")
	}
	fetcher := &fakeFetcher{dir: t.TempDir(), content: map[string][]byte{}, fetched: map[string]int{}, fail: map[string]error{}}
	file := func(p, content string, mode uint32) lazyfs.BaseEntry {
		hash := hashOf([]byte(content))
		fetcher.content[hash] = []byte(content)
		return lazyfs.BaseEntry{Path: p, Kind: lazyfs.KindFile, Mode: mode, Size: int64(len(content)), ContentHash: hash}
	}
	manifest, err := lazyfs.NewManifest("commit-1", []lazyfs.BaseEntry{
		file("/acme/app/README.md", "# App\n", lazyfs.ModeFile),
		file("/acme/app/run.sh", "#!/bin/sh\necho hi\n", lazyfs.ModeExecutable),
		file("/acme/app/src/main.go", "package main\n", lazyfs.ModeFile),
		file("/acme/app/broken.txt", "unreachable\n", lazyfs.ModeFile),
		{Path: "/acme/app/link", Kind: lazyfs.KindSymlink, Size: 9, ContentHash: hashOf([]byte("README.md")), Target: "README.md"},
		{Path: "/acme/app/empty", Kind: lazyfs.KindDir},
	})
	if err != nil {
		t.Fatal(err)
	}
	layer, err := lazyfs.Open(t.TempDir(), manifest, noContent{}, lazyfs.Options{NoSync: true})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	mounted, err := Mount(dir, layer, fetcher, Options{FsName: "gitslice:test", UID: uint32(os.Getuid()), GID: uint32(os.Getgid())})
	if err != nil {
		t.Skipf("cannot mount FUSE here: %v", err)
	}
	t.Cleanup(func() {
		if err := mounted.Server.Unmount(); err != nil {
			t.Errorf("unmount: %v", err)
		}
		_ = layer.Close()
	})
	return dir, fetcher, mounted
}

func TestMountServesTheBaseReadOnly(t *testing.T) {
	dir, fetcher, mounted := mountTest(t)
	app := filepath.Join(dir, "acme", "app")

	entries, err := os.ReadDir(app)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	if got := fmt.Sprint(names); got != "[README.md broken.txt empty link run.sh src]" {
		t.Fatalf("listing = %s", got)
	}
	// Listing and stat need no content.
	info, err := os.Stat(filepath.Join(app, "run.sh"))
	if err != nil || info.Size() != 18 || info.Mode().Perm() != 0o755 {
		t.Fatalf("stat run.sh = %v, %v", info, err)
	}
	if len(fetcher.fetched) != 0 {
		t.Fatalf("listing and stat fetched %v", fetcher.fetched)
	}

	for i := 0; i < 3; i++ {
		data, err := os.ReadFile(filepath.Join(app, "README.md"))
		if err != nil || string(data) != "# App\n" {
			t.Fatalf("read README.md = %q, %v", data, err)
		}
	}
	if fetcher.fetched["/acme/app/README.md"] != 1 {
		t.Fatalf("README.md fetched %d times, want once", fetcher.fetched["/acme/app/README.md"])
	}
	if target, err := os.Readlink(filepath.Join(app, "link")); err != nil || target != "README.md" {
		t.Fatalf("readlink = %q, %v", target, err)
	}
	if data, err := os.ReadFile(filepath.Join(app, "link")); err != nil || !bytes.Equal(data, []byte("# App\n")) {
		t.Fatalf("reading through the symlink = %q, %v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(app, "src", "main.go")); err != nil || string(data) != "package main\n" {
		t.Fatalf("read src/main.go = %q, %v", data, err)
	}
	if empty, err := os.ReadDir(filepath.Join(app, "empty")); err != nil || len(empty) != 0 {
		t.Fatalf("empty directory = %v, %v", empty, err)
	}

	// Missing paths, writes and fetch failures fail as a file system does.
	if _, err := os.Stat(filepath.Join(app, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat missing: %v", err)
	}
	if err := os.WriteFile(filepath.Join(app, "README.md"), []byte("x"), 0o644); !errors.Is(err, syscall.EROFS) {
		t.Fatalf("write: err = %v, want EROFS", err)
	}
	if err := os.Mkdir(filepath.Join(app, "new"), 0o755); !errors.Is(err, syscall.EROFS) {
		t.Fatalf("mkdir: err = %v, want EROFS", err)
	}
	fetcher.fail["/acme/app/broken.txt"] = errors.New("server unreachable")
	if _, err := os.ReadFile(filepath.Join(app, "broken.txt")); !errors.Is(err, syscall.EIO) {
		t.Fatalf("a failed fetch: err = %v, want EIO", err)
	}
	stats := mounted.Stats()
	if stats.Fetches != 2 || stats.Errors != 1 || stats.LastError != "/acme/app/broken.txt: server unreachable" {
		t.Fatalf("stats = %+v", stats)
	}
}
