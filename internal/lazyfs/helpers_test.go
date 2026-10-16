package lazyfs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"sync"
	"testing"
)

// memContent is Content in memory, with sha256 hashes. It counts the opens,
// which are the fetches a real one would make.
type memContent struct {
	mu    sync.Mutex
	blobs map[string][]byte
	opens int
}

func newMemContent() *memContent { return &memContent{blobs: map[string][]byte{}} }

func hashOf(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
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
		return nil, fmt.Errorf("no content %s: %w", hash, fs.ErrNotExist)
	}
	c.opens++
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

func (c *memContent) openCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.opens
}

// baseFile describes a file of a test base: content, or a symlink target.
type baseFile struct {
	content    string
	executable bool
	symlink    string
}

// newBase builds a manifest from files keyed by path, storing their content.
func newBase(t *testing.T, content *memContent, commit string, files map[string]baseFile) *Manifest {
	t.Helper()
	var entries []BaseEntry
	for p, file := range files {
		if file.symlink != "" {
			entries = append(entries, BaseEntry{Path: p, Kind: KindSymlink, Size: int64(len(file.symlink)), ContentHash: content.put([]byte(file.symlink)), Target: file.symlink})
			continue
		}
		mode := ModeFile
		if file.executable {
			mode = ModeExecutable
		}
		entries = append(entries, BaseEntry{Path: p, Kind: KindFile, Mode: mode, Size: int64(len(file.content)), ContentHash: content.put([]byte(file.content))})
	}
	m, err := NewManifest(commit, entries)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func openFS(t *testing.T, dir string, base *Manifest, content Content) *FS {
	t.Helper()
	f, err := Open(dir, base, content, Options{NoSync: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// tree describes the merged view: path -> "file 100644 <content>",
// "symlink <target>" or "dir".
func tree(t *testing.T, f *FS) map[string]string {
	t.Helper()
	out := map[string]string{}
	var walk func(dir string)
	walk = func(dir string) {
		entries, err := f.ReadDir(dir)
		if err != nil {
			t.Fatalf("ReadDir(%s): %v", dir, err)
		}
		for _, entry := range entries {
			p := joinPath(dir, entry.Name)
			switch entry.Kind {
			case KindDir:
				out[p] = "dir"
				walk(p)
			case KindSymlink:
				out[p] = "symlink " + entry.Target
			case KindFile:
				data, err := f.ReadFile(context.Background(), p)
				if err != nil {
					t.Fatalf("ReadFile(%s): %v", p, err)
				}
				if int64(len(data)) != entry.Size {
					t.Fatalf("%s: listed size %d, content %d bytes", p, entry.Size, len(data))
				}
				out[p] = fmt.Sprintf("file %o %s", entry.Mode, data)
			}
		}
	}
	walk("/")
	return out
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func diffTrees(want, got map[string]string) string {
	var b bytes.Buffer
	seen := map[string]bool{}
	for _, p := range sortedKeys(want) {
		seen[p] = true
		if got[p] != want[p] {
			fmt.Fprintf(&b, "  %s: want %q, got %q\n", p, want[p], got[p])
		}
	}
	for _, p := range sortedKeys(got) {
		if !seen[p] {
			fmt.Fprintf(&b, "  %s: unexpected %q\n", p, got[p])
		}
	}
	return b.String()
}

func editStrings(edits []Edit) []string {
	out := make([]string, 0, len(edits))
	for _, edit := range edits {
		if edit.Op == "delete" {
			out = append(out, "delete "+edit.Path)
			continue
		}
		out = append(out, fmt.Sprintf("upsert %s %o %s", edit.Path, edit.Mode, edit.ContentHash))
	}
	return out
}
