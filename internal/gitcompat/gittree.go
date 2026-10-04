package gitcompat

import (
	"bytes"
	"crypto/sha1" //nolint:gosec // Git object ids are SHA-1.
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// The lazy projection builds a slice's trees and commits itself, from file
// paths, modes and Git blob ids, so it never has to read a file to know what a
// commit's id is. The objects must be byte for byte what git fast-import writes
// for the same history: published commit ids (Go pseudo-versions, for one)
// stay valid only if every instance computes the same ones.

const (
	objCommit = 1
	objTree   = 2
	objBlob   = 3
)

// gitObjectID returns the id Git gives an object: SHA-1 of "<kind> <size>\0"
// and the object's bytes.
func gitObjectID(kind string, data []byte) string {
	h := sha1.New() //nolint:gosec // Git object ids are SHA-1.
	_, _ = h.Write([]byte(kind + " " + strconv.Itoa(len(data)) + "\x00"))
	_, _ = h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

// newObject is a tree or commit that a build produced and still has to write.
type newObject struct {
	kind byte // objCommit or objTree
	id   string
	data []byte
}

type treeEntry struct {
	mode  string // "100644", "100755", "120000" or "40000" for a directory
	sha   string // 40 hex characters
	isDir bool
}

type dirNode struct {
	entries map[string]treeEntry
	sha     string
	dirty   bool
}

// treeBuilder keeps a slice's directory tree in memory and recomputes only the
// directories a commit touches.
type treeBuilder struct {
	dirs    map[string]*dirNode // "" is the root
	objects []newObject
	seen    map[string]bool
}

const emptyTreeID = emptyGitTree

// newTreeBuilder builds the directory tree for files. Every file needs its Git
// blob id.
func newTreeBuilder(files map[string]projectedFile) (*treeBuilder, error) {
	b := &treeBuilder{dirs: map[string]*dirNode{"": {entries: map[string]treeEntry{}, dirty: true}}, seen: map[string]bool{}}
	for path, file := range files {
		if file.Blob == "" {
			return nil, fmt.Errorf("file %s has no git blob id", path)
		}
		b.set(path, treeEntry{mode: file.Mode, sha: file.Blob})
	}
	// The trees of an existing state are already in the repository; computing
	// them again only produces their ids.
	b.root()
	b.objects = nil
	return b, nil
}

func (b *treeBuilder) dir(path string) *dirNode {
	d := b.dirs[path]
	if d == nil {
		d = &dirNode{entries: map[string]treeEntry{}, dirty: true}
		b.dirs[path] = d
	}
	return d
}

func splitPath(path string) (dir, name string) {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[:i], path[i+1:]
	}
	return "", path
}

// set puts a file entry at path and marks every directory above it dirty.
func (b *treeBuilder) set(path string, entry treeEntry) {
	dir, name := splitPath(path)
	b.dir(dir).entries[name] = entry
	b.markDirty(dir)
}

// markDirty makes sure dir and its ancestors exist and will be recomputed.
func (b *treeBuilder) markDirty(dir string) {
	for {
		d := b.dir(dir)
		d.dirty = true
		if dir == "" {
			return
		}
		parent, name := splitPath(dir)
		pd := b.dir(parent)
		if _, ok := pd.entries[name]; !ok {
			pd.entries[name] = treeEntry{mode: "40000", isDir: true}
		}
		dir = parent
	}
}

// remove deletes a file and any directories it leaves empty, as git
// fast-import does.
func (b *treeBuilder) remove(path string) {
	dir, name := splitPath(path)
	d := b.dirs[dir]
	if d == nil {
		return
	}
	delete(d.entries, name)
	for dir != "" && len(b.dirs[dir].entries) == 0 {
		delete(b.dirs, dir)
		parent, name := splitPath(dir)
		delete(b.dirs[parent].entries, name)
		dir = parent
	}
	b.markDirty(dir)
}

// apply records one file change. blob is the file's Git blob id.
func (b *treeBuilder) apply(change fileChange, blob string) {
	if change.Delete {
		b.remove(change.Path)
		return
	}
	b.set(change.Path, treeEntry{mode: change.Mode, sha: blob})
}

// root recomputes the directories that changed since the last call, deepest
// first, and returns the root tree's id. The trees it computed are queued in
// objects.
func (b *treeBuilder) root() string {
	var dirty []string
	for path, d := range b.dirs {
		if d.dirty {
			dirty = append(dirty, path)
		}
	}
	// Children before parents: deeper paths first.
	sort.Slice(dirty, func(i, j int) bool {
		di, dj := strings.Count(dirty[i], "/"), strings.Count(dirty[j], "/")
		if dirty[i] == "" {
			di = -1
		}
		if dirty[j] == "" {
			dj = -1
		}
		if di != dj {
			return di > dj
		}
		return dirty[i] < dirty[j]
	})
	for _, path := range dirty {
		d := b.dirs[path]
		if d == nil {
			continue
		}
		if len(d.entries) == 0 && path == "" {
			d.sha, d.dirty = emptyTreeID, false
			continue
		}
		data := encodeTree(d.entries)
		d.sha = gitObjectID("tree", data)
		d.dirty = false
		if !b.seen[d.sha] {
			b.seen[d.sha] = true
			b.objects = append(b.objects, newObject{kind: objTree, id: d.sha, data: data})
		}
		if path != "" {
			parent, name := splitPath(path)
			b.dirs[parent].entries[name] = treeEntry{mode: "40000", sha: d.sha, isDir: true}
		}
	}
	return b.dirs[""].sha
}

// takeObjects returns the objects queued since the last call.
func (b *treeBuilder) takeObjects() []newObject {
	out := b.objects
	b.objects = nil
	return out
}

// encodeTree writes a tree object's bytes. Git orders entries by name as bytes,
// comparing a directory as if its name ended in "/".
func encodeTree(entries map[string]treeEntry) []byte {
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	key := func(name string) string {
		if entries[name].isDir {
			return name + "/"
		}
		return name
	}
	sort.Slice(names, func(i, j int) bool { return key(names[i]) < key(names[j]) })
	var buf bytes.Buffer
	for _, name := range names {
		e := entries[name]
		buf.WriteString(e.mode)
		buf.WriteByte(' ')
		buf.WriteString(name)
		buf.WriteByte(0)
		raw, _ := hex.DecodeString(e.sha)
		buf.Write(raw)
	}
	return buf.Bytes()
}

// encodeCommit writes a commit object the way git fast-import does.
func encodeCommit(tree, parent, authorName, authorEmail string, authored int64, committerName, committerEmail string, committed int64, message string) []byte {
	var b strings.Builder
	b.WriteString("tree " + tree + "\n")
	if parent != "" {
		b.WriteString("parent " + parent + "\n")
	}
	fmt.Fprintf(&b, "author %s <%s> %d +0000\n", authorName, authorEmail, authored)
	fmt.Fprintf(&b, "committer %s <%s> %d +0000\n", committerName, committerEmail, committed)
	b.WriteString("\n")
	b.WriteString(message)
	return []byte(b.String())
}
