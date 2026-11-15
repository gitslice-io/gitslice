// Package lazyfs is the two-layer model of a lazy workspace
// (design/30_lazy_workspaces.md): a read-only base layer, the slice at a
// commit, under a local layer that holds every change the user made, on local
// disk only. It has no FUSE in it: a file system adapter calls it, and the
// CLI asks it for the workspace's edits.
//
// Paths are absolute repository paths ("/acme/app/main.go"). Symbolic links
// are never followed: a caller resolves them, as the kernel does before it
// asks a FUSE file system about a path.
package lazyfs

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

// Kind is what a path is.
type Kind uint8

const (
	KindFile Kind = iota + 1
	KindDir
	KindSymlink
)

func (k Kind) String() string {
	switch k {
	case KindFile:
		return "file"
	case KindDir:
		return "directory"
	case KindSymlink:
		return "symlink"
	}
	return "unknown"
}

// File modes, as Git and the native model record them.
const (
	ModeFile       uint32 = 0o100644
	ModeExecutable uint32 = 0o100755
	ModeSymlink    uint32 = 0o120000
	ModeDir        uint32 = 0o040000
)

// BaseEntry is one path of the base layer.
type BaseEntry struct {
	Path        string
	Kind        Kind
	Mode        uint32
	Size        int64
	ContentHash string // files and symlinks
	Target      string // symlinks
}

// Manifest is the base layer: the listing of the slice at one commit. It is
// immutable.
type Manifest struct {
	commitID string
	entries  map[string]BaseEntry
	children map[string][]string // directory -> sorted child names
}

// NewManifest builds a manifest from a listing. Directories that a path needs
// but the listing leaves out are added.
func NewManifest(commitID string, entries []BaseEntry) (*Manifest, error) {
	m := &Manifest{commitID: commitID, entries: map[string]BaseEntry{}, children: map[string][]string{}}
	m.entries["/"] = BaseEntry{Path: "/", Kind: KindDir, Mode: ModeDir}
	for _, entry := range entries {
		p, err := cleanPath(entry.Path)
		if err != nil {
			return nil, err
		}
		if p == "/" {
			continue
		}
		entry.Path = p
		switch entry.Kind {
		case KindFile:
			entry.Mode = normalizeFileMode(entry.Mode)
		case KindSymlink:
			entry.Mode = ModeSymlink
		case KindDir:
			entry.Mode = ModeDir
		default:
			return nil, fmt.Errorf("lazyfs: %s has no kind", p)
		}
		if existing, ok := m.entries[p]; ok && !(existing.Kind == KindDir && entry.Kind == KindDir) {
			return nil, fmt.Errorf("lazyfs: %s is listed twice", p)
		}
		m.entries[p] = entry
		for dir := parentPath(p); dir != "/"; dir = parentPath(dir) {
			existing, ok := m.entries[dir]
			if ok && existing.Kind != KindDir {
				return nil, fmt.Errorf("lazyfs: %s is under %s, which is not a directory", p, dir)
			}
			if ok {
				break
			}
			m.entries[dir] = BaseEntry{Path: dir, Kind: KindDir, Mode: ModeDir}
		}
	}
	for p := range m.entries {
		if p == "/" {
			continue
		}
		dir := parentPath(p)
		m.children[dir] = append(m.children[dir], path.Base(p))
	}
	for dir := range m.children {
		sort.Strings(m.children[dir])
	}
	return m, nil
}

// CommitID is the commit the manifest lists.
func (m *Manifest) CommitID() string { return m.commitID }

// Lookup returns the base entry at p.
func (m *Manifest) Lookup(p string) (BaseEntry, bool) {
	entry, ok := m.entries[p]
	return entry, ok
}

// Children returns the names directly under the directory p, sorted.
func (m *Manifest) Children(p string) []string { return m.children[p] }

// Files calls fn for every file and symlink of the base, in no order.
func (m *Manifest) Files(fn func(BaseEntry)) {
	for _, entry := range m.entries {
		if entry.Kind != KindDir {
			fn(entry)
		}
	}
}

// filesUnder returns every file and symlink under the directory p.
func (m *Manifest) filesUnder(p string) []BaseEntry {
	var out []BaseEntry
	var walk func(dir string)
	walk = func(dir string) {
		for _, name := range m.children[dir] {
			child := joinPath(dir, name)
			entry := m.entries[child]
			if entry.Kind == KindDir {
				walk(child)
				continue
			}
			out = append(out, entry)
		}
	}
	walk(p)
	return out
}

// cleanPath checks an absolute repository path and returns its clean form.
func cleanPath(p string) (string, error) {
	if !strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("lazyfs: path %q is not absolute", p)
	}
	clean := path.Clean(p)
	if clean != p && clean+"/" != p {
		return "", fmt.Errorf("lazyfs: path %q is not clean", p)
	}
	return clean, nil
}

func parentPath(p string) string {
	if p == "/" {
		return "/"
	}
	return path.Dir(p)
}

func joinPath(dir, name string) string {
	if dir == "/" {
		return "/" + name
	}
	return dir + "/" + name
}

// isUnder reports whether p is strictly below dir.
func isUnder(p, dir string) bool {
	if dir == "/" {
		return p != "/"
	}
	return strings.HasPrefix(p, dir+"/")
}

func normalizeFileMode(mode uint32) uint32 {
	if mode&0o111 != 0 {
		return ModeExecutable
	}
	return ModeFile
}
