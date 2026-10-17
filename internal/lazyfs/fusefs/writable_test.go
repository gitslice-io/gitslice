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
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"

	"gitslice.io/gitslice/internal/lazyfs"
)

// memContent is lazyfs.Content in memory: base content by hash, and the
// hashes of local files.
type memContent struct {
	mu    sync.Mutex
	blobs map[string][]byte
}

func (c *memContent) put(data []byte) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	hash := hashOf(data)
	c.blobs[hash] = append([]byte(nil), data...)
	return hash
}

func (c *memContent) Open(ctx context.Context, hash string) (io.ReadCloser, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	data, ok := c.blobs[hash]
	if !ok {
		return nil, fmt.Errorf("no content %s", hash)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (c *memContent) HashFile(p string) (string, error) {
	data, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	return c.put(data), nil
}

func (c *memContent) HashBytes(data []byte) (string, error) { return c.put(data), nil }

// contentFetcher serves base content from a memContent.
type contentFetcher struct {
	content *memContent
	dir     string
}

func (f contentFetcher) Fetch(ctx context.Context, p string, attr lazyfs.Attr) (string, bool, error) {
	local := filepath.Join(f.dir, strings.TrimPrefix(attr.ContentHash, "sha256:"))
	if _, err := os.Stat(local); err == nil {
		return local, false, nil
	}
	f.content.mu.Lock()
	data := f.content.blobs[attr.ContentHash]
	f.content.mu.Unlock()
	return local, true, os.WriteFile(local, data, 0o600)
}

// writableMount mounts a lazy workspace over a base made from files, and
// writes the same base into a plain directory for comparison.
func writableMount(t *testing.T, files map[string]string) (mnt, plain string, layer *lazyfs.FS, content *memContent) {
	t.Helper()
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skip("no /dev/fuse here")
	}
	content = &memContent{blobs: map[string][]byte{}}
	plain = t.TempDir()
	var entries []lazyfs.BaseEntry
	for p, data := range files {
		mode := lazyfs.ModeFile
		perm := os.FileMode(0o644)
		if strings.HasSuffix(p, ".sh") {
			mode, perm = lazyfs.ModeExecutable, 0o755
		}
		entries = append(entries, lazyfs.BaseEntry{Path: p, Kind: lazyfs.KindFile, Mode: mode, Size: int64(len(data)), ContentHash: content.put([]byte(data))})
		full := filepath.Join(plain, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(data), perm); err != nil {
			t.Fatal(err)
		}
	}
	manifest, err := lazyfs.NewManifest("commit-1", entries)
	if err != nil {
		t.Fatal(err)
	}
	layer, err = lazyfs.Open(t.TempDir(), manifest, content, lazyfs.Options{NoSync: true})
	if err != nil {
		t.Fatal(err)
	}
	gsDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(gsDir, "slice.json"), []byte(`{"slice":"app"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	mnt = t.TempDir()
	mounted, err := Mount(mnt, layer, contentFetcher{content: content, dir: t.TempDir()}, Options{FsName: "gitslice:test", UID: uint32(os.Getuid()), GID: uint32(os.Getgid()), GsDir: gsDir})
	if err != nil {
		t.Skipf("cannot mount FUSE here: %v", err)
	}
	t.Cleanup(func() {
		if err := mounted.Server.Unmount(); err != nil {
			t.Errorf("unmount: %v", err)
		}
		_ = layer.Close()
	})
	return mnt, plain, layer, content
}

var opNames = []string{"a", "b", "c"}

type fsOp struct {
	kind, path, path2, data, target string
	exec                            bool
	size                            int64
}

func (op fsOp) String() string {
	return strings.TrimSpace(fmt.Sprintf("%s %s %s %q %s", op.kind, op.path, op.path2, op.data, op.target))
}

func randomOpPath(rng *rand.Rand) string {
	parts := make([]string, 1+rng.Intn(3))
	for i := range parts {
		parts[i] = opNames[rng.Intn(len(opNames))]
	}
	return "/" + strings.Join(parts, "/")
}

func randomFsOp(rng *rand.Rand) fsOp {
	op := fsOp{path: randomOpPath(rng)}
	switch n := rng.Intn(100); {
	case n < 20:
		op.kind, op.data = "write", fmt.Sprintf("w%d", rng.Intn(4))
	case n < 30:
		op.kind, op.data = "append", fmt.Sprintf("+%d", rng.Intn(3))
	case n < 45:
		op.kind = "mkdir"
	case n < 55:
		op.kind = "unlink"
	case n < 62:
		op.kind = "rmdir"
	case n < 82:
		op.kind, op.path2 = "rename", randomOpPath(rng)
	case n < 87:
		op.kind, op.target = "symlink", opNames[rng.Intn(len(opNames))]
	case n < 93:
		op.kind, op.exec = "chmod", rng.Intn(2) == 0
	default:
		op.kind, op.size = "truncate", int64(rng.Intn(3))
	}
	return op
}

// apply runs an op with ordinary system calls under root, as a program would.
func apply(root string, op fsOp) error {
	p := filepath.Join(root, op.path)
	switch op.kind {
	case "write":
		return os.WriteFile(p, []byte(op.data), 0o644)
	case "append":
		file, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			return err
		}
		_, err = file.WriteString(op.data)
		if cerr := file.Close(); err == nil {
			err = cerr
		}
		return err
	case "mkdir":
		return os.Mkdir(p, 0o755)
	case "unlink":
		return syscall.Unlink(p)
	case "rmdir":
		return syscall.Rmdir(p)
	case "rename":
		return syscall.Rename(p, filepath.Join(root, op.path2))
	case "symlink":
		return os.Symlink(op.target, p)
	case "chmod":
		perm := os.FileMode(0o644)
		if op.exec {
			perm = 0o755
		}
		return os.Chmod(p, perm)
	case "truncate":
		return os.Truncate(p, op.size)
	}
	return nil
}

func errClass(err error) string {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		if errno == syscall.EEXIST && false {
			return "exist"
		}
		return errno.Error()
	}
	if err == nil {
		return "ok"
	}
	return err.Error()
}

// skipOp leaves out what the comparison cannot hold: paths through a
// symlink, content operations on one, and chmod of a directory (the plain
// directory would lose its execute bit; the native model has no directory
// modes).
func skipOp(plain string, op fsOp) bool {
	for _, p := range []string{op.path, op.path2} {
		if p == "" {
			continue
		}
		for dir := filepath.Dir(p); dir != "/"; dir = filepath.Dir(dir) {
			if info, err := os.Lstat(filepath.Join(plain, dir)); err == nil && info.Mode()&fs.ModeSymlink != 0 {
				return true
			}
		}
	}
	if info, err := os.Lstat(filepath.Join(plain, op.path)); err == nil {
		switch op.kind {
		case "write", "append", "truncate", "chmod":
			if info.Mode()&fs.ModeSymlink != 0 || (op.kind == "chmod" && info.IsDir()) {
				return true
			}
		}
	}
	return false
}

func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		rel := strings.TrimPrefix(p, root)
		if rel == "/.gs" {
			return filepath.SkipDir
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, _ := os.Readlink(p)
			out[rel] = "symlink " + target
		case info.IsDir():
			out[rel] = "dir"
		default:
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			out[rel] = fmt.Sprintf("file %v %s", info.Mode().Perm()&0o111 != 0, data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func describe(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "  %s: %s\n", k, m[k])
	}
	return b.String()
}

// walkEdits is what a walk of the plain directory would send for it.
func walkEdits(t *testing.T, base map[string]string, plain map[string]string) string {
	t.Helper()
	var lines []string
	for p, desc := range plain {
		var hash string
		mode := lazyfs.ModeFile
		switch {
		case strings.HasPrefix(desc, "symlink "):
			hash, mode = hashOf([]byte(strings.TrimPrefix(desc, "symlink "))), lazyfs.ModeSymlink
		case strings.HasPrefix(desc, "file "):
			fields := strings.SplitN(desc, " ", 3)
			if fields[1] == "true" {
				mode = lazyfs.ModeExecutable
			}
			hash = hashOf([]byte(fields[2]))
		default:
			continue
		}
		if was, ok := base[p]; ok {
			wasMode := lazyfs.ModeFile
			if strings.HasSuffix(p, ".sh") {
				wasMode = lazyfs.ModeExecutable
			}
			if hashOf([]byte(was)) == hash && wasMode == mode {
				continue
			}
		}
		lines = append(lines, fmt.Sprintf("upsert %s %o %s", p, mode, hash))
	}
	for p := range base {
		if desc := plain[p]; !strings.HasPrefix(desc, "file ") && !strings.HasPrefix(desc, "symlink ") {
			lines = append(lines, "delete "+p)
		}
	}
	sort.Slice(lines, func(i, j int) bool { return strings.Fields(lines[i])[1] < strings.Fields(lines[j])[1] })
	return strings.Join(lines, "\n")
}

func layerEdits(t *testing.T, layer *lazyfs.FS) string {
	t.Helper()
	edits, err := layer.Edits()
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, edit := range edits {
		if edit.Op == "delete" {
			lines = append(lines, "delete "+edit.Path)
			continue
		}
		lines = append(lines, fmt.Sprintf("upsert %s %o %s", edit.Path, edit.Mode, edit.ContentHash))
	}
	return strings.Join(lines, "\n")
}

// TestWritableMountMatchesARealDirectory sends random operations through the
// kernel to the mount and to a plain directory with the same base: the errors,
// the trees and the edits must agree after every step.
func TestWritableMountMatchesARealDirectory(t *testing.T) {
	base := map[string]string{"/a/a": "base-aa", "/a/b/c": "base-abc", "/b": "base-b", "/c/run.sh": "#!/bin/sh"}
	trials, steps := 25, 40
	if v, err := strconv.Atoi(os.Getenv("LAZYFS_TRIALS")); err == nil && v > 0 {
		trials = v
	}
	if testing.Short() {
		trials = 5
	}
	for trial := 0; trial < trials; trial++ {
		func() {
			mnt, plain, layer, _ := writableMount(t, base)
			rng := rand.New(rand.NewSource(int64(trial)))
			var log []string
			for step := 0; step < steps; step++ {
				op := randomFsOp(rng)
				if skipOp(plain, op) {
					continue
				}
				log = append(log, op.String())
				want, got := errClass(apply(plain, op)), errClass(apply(mnt, op))
				if want != got {
					t.Fatalf("trial %d: %s: plain %s, mount %s\nops:\n  %s", trial, op, want, got, strings.Join(log, "\n  "))
				}
				wantTree, gotTree := snapshot(t, plain), snapshot(t, mnt)
				if describe(wantTree) != describe(gotTree) {
					t.Fatalf("trial %d: after %s:\nplain:\n%smount:\n%sops:\n  %s", trial, op, describe(wantTree), describe(gotTree), strings.Join(log, "\n  "))
				}
				if want, got := walkEdits(t, base, wantTree), layerEdits(t, layer); want != got {
					t.Fatalf("trial %d: after %s the edits differ:\nwalk:\n%s\nlayer:\n%s", trial, op, want, got)
				}
			}
		}()
	}
}

// TestEditorSaveAndWorkspaceStateThroughTheMount: the write-temp-then-rename
// save most editors do, a file growing and shrinking through one handle, a
// hard link refused, and /.gs served from disk.
func TestEditorSaveAndWorkspaceStateThroughTheMount(t *testing.T) {
	mnt, _, layer, content := writableMount(t, map[string]string{"/app/main.go": "package main\n"})
	target := filepath.Join(mnt, "app", "main.go")

	tmp := filepath.Join(mnt, "app", ".main.go.swp")
	if err := os.WriteFile(tmp, []byte("package main // saved\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, target); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(target); string(data) != "package main // saved\n" {
		t.Fatalf("after an editor save: %q", data)
	}

	file, err := os.OpenFile(target, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte("PACKAGE"), 0); err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(7); err != nil {
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
	file.Close()
	if data, _ := os.ReadFile(target); string(data) != "PACKAGE" {
		t.Fatalf("after write and truncate through one handle: %q", data)
	}
	edits := layerEdits(t, layer)
	if edits != fmt.Sprintf("upsert /app/main.go 100644 %s", hashOf([]byte("PACKAGE"))) {
		t.Fatalf("edits = %s", edits)
	}
	_ = content

	if err := os.Link(target, filepath.Join(mnt, "app", "hard")); !errors.Is(err, syscall.EPERM) {
		t.Fatalf("hard link: err = %v, want EPERM", err)
	}
	data, err := os.ReadFile(filepath.Join(mnt, ".gs", "slice.json"))
	if err != nil || string(data) != `{"slice":"app"}` {
		t.Fatalf("/.gs/slice.json = %q, %v", data, err)
	}
	if err := os.WriteFile(filepath.Join(mnt, ".gs", "state.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("writing /.gs/state.json: %v", err)
	}
	if err := os.Rename(filepath.Join(mnt, ".gs", "state.json"), filepath.Join(mnt, "app", "state.json")); !errors.Is(err, syscall.EXDEV) {
		t.Fatalf("moving a file out of /.gs: err = %v, want EXDEV", err)
	}
	if strings.Contains(layerEdits(t, layer), ".gs") {
		t.Fatal("/.gs must never become an edit")
	}
}

func init() {
	_ = sha256.New
	_ = hex.EncodeToString
}
