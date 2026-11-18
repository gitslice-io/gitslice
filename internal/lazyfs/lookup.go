package lazyfs

import (
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"
)

// node is a path in the merged view.
type node struct {
	kind    Kind
	mode    uint32
	size    int64
	modTime time.Time // local files
	hash    string    // base files and symlinks, references
	local   string    // a local file's name under files/
	target  string    // symlinks
	base    bool      // the base layer shows through
	opaque  bool
}

// baseVisibleLocked reports whether the base layer shows through at p: no
// proper ancestor has a local entry other than a plain (not opaque) directory.
func (f *FS) baseVisibleLocked(p string) bool {
	for dir := parentPath(p); dir != "/"; dir = parentPath(dir) {
		entry := f.entries[dir]
		if entry != nil && !(entry.Kind == entryDir && !entry.Opaque) {
			return false
		}
	}
	return true
}

func (f *FS) statLocal(name string) (int64, time.Time, error) {
	if !isValidLocalName(name) {
		return 0, time.Time{}, fmt.Errorf("lazyfs: invalid local content name %q: %w", name, fs.ErrInvalid)
	}
	info, err := f.filesRoot.Lstat(name)
	if err != nil {
		return 0, time.Time{}, err
	}
	if !info.Mode().IsRegular() {
		return 0, time.Time{}, fmt.Errorf("lazyfs: local content %q is not a regular file: %w", name, fs.ErrInvalid)
	}
	return info.Size(), info.ModTime(), nil
}

// lookupMetaLocked returns the merged view at p without stating local content
// files on disk.
func (f *FS) lookupMetaLocked(p string) (node, bool) {
	if p == "/" {
		return node{kind: KindDir, mode: ModeDir}, true
	}
	if entry := f.entries[p]; entry != nil {
		switch entry.Kind {
		case entryWhiteout:
			return node{}, false
		case entryFile:
			return node{kind: KindFile, mode: entry.Mode, local: entry.Local}, true
		case entryRef:
			return node{kind: KindFile, mode: entry.Mode, size: entry.Size, hash: entry.Hash}, true
		case entryDir:
			return node{kind: KindDir, mode: ModeDir, opaque: entry.Opaque}, true
		case entrySymlink:
			return node{kind: KindSymlink, mode: ModeSymlink, size: int64(len(entry.Target)), target: entry.Target}, true
		}
	}
	if !f.baseVisibleLocked(p) {
		return node{}, false
	}
	entry, ok := f.base.Lookup(p)
	if !ok {
		return node{}, false
	}
	return node{kind: entry.Kind, mode: entry.Mode, size: entry.Size, hash: entry.ContentHash, target: entry.Target, base: true}, true
}

// lookupLocked returns the merged view at p, including local file size/mtime.
func (f *FS) lookupLocked(p string) (node, bool) {
	n, ok := f.lookupMetaLocked(p)
	if ok && n.local != "" {
		if size, modTime, err := f.statLocal(n.local); err == nil {
			n.size, n.modTime = size, modTime
		}
	}
	return n, ok
}

// childNamesLocked lists the names under the directory p in the merged view.
func (f *FS) childNamesLocked(p string, n node) []string {
	set := map[string]struct{}{}
	if baseEntry, ok := f.base.Lookup(p); ok && baseEntry.Kind == KindDir && !n.opaque && (n.base || f.baseVisibleLocked(joinPath(p, "x"))) {
		for _, name := range f.base.Children(p) {
			set[name] = struct{}{}
		}
	}
	for name := range f.children[p] {
		set[name] = struct{}{}
	}
	names := make([]string, 0, len(set))
	for name := range set {
		if _, ok := f.lookupMetaLocked(joinPath(p, name)); ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// resolveParentLocked checks that the parent of p exists and is a directory,
// failing as a file system would: ENOENT for a missing component, ENOTDIR for
// one that is not a directory.
func (f *FS) resolveParentLocked(op, p string) error {
	if p == "/" {
		return &fs.PathError{Op: op, Path: p, Err: fs.ErrInvalid}
	}
	parent := parentPath(p)
	var walk []string
	for dir := parent; dir != "/"; dir = parentPath(dir) {
		walk = append(walk, dir)
	}
	for i := len(walk) - 1; i >= 0; i-- {
		n, ok := f.lookupMetaLocked(walk[i])
		if !ok {
			return &fs.PathError{Op: op, Path: p, Err: fs.ErrNotExist}
		}
		if n.kind != KindDir {
			return &fs.PathError{Op: op, Path: p, Err: ErrNotDir}
		}
	}
	return nil
}

// resolveLocked looks p up as a file system would: ENOTDIR when a component
// on the way is not a directory, ENOENT when p or a component is missing.
func (f *FS) resolveLocked(op, p string) (node, error) {
	if p == "/" {
		n, _ := f.lookupMetaLocked(p)
		return n, nil
	}
	if err := f.resolveParentLocked(op, p); err != nil {
		return node{}, err
	}
	n, ok := f.lookupMetaLocked(p)
	if !ok {
		return node{}, &fs.PathError{Op: op, Path: p, Err: fs.ErrNotExist}
	}
	if n.local != "" {
		size, modTime, err := f.statLocal(n.local)
		if err != nil {
			return node{}, &fs.PathError{Op: op, Path: p, Err: err}
		}
		n.size, n.modTime = size, modTime
	}
	return n, nil
}

// localEntriesUnderLocked returns the local paths at and below p.
//
// It walks the subtree, not the whole layer: a local entry is listed under its
// directory in f.children, and a directory between p and it is either a local
// entry itself or a base directory. Removing one file of many thousands (rm
// -rf of build output) then costs that file, not every entry in the layer.
func (f *FS) localEntriesUnderLocked(p string) []string {
	var out []string
	if _, ok := f.entries[p]; ok {
		out = append(out, p)
	}
	var walk func(dir string)
	walk = func(dir string) {
		names := f.children[dir]
		for name := range names {
			q := joinPath(dir, name)
			if _, ok := f.entries[q]; ok {
				out = append(out, q)
			}
			walk(q)
		}
		for _, name := range f.base.Children(dir) {
			if _, local := names[name]; local {
				continue
			}
			q := joinPath(dir, name)
			if entry, ok := f.base.Lookup(q); ok && entry.Kind == KindDir {
				walk(q)
			}
		}
	}
	walk(p)
	sort.Strings(out)
	if checkSubtreeWalk {
		var want []string
		for q := range f.entries {
			if q == p || isUnder(q, p) {
				want = append(want, q)
			}
		}
		sort.Strings(want)
		if strings.Join(want, "\n") != strings.Join(out, "\n") {
			panic(fmt.Sprintf("lazyfs: entries under %s: walked %v, want %v", p, out, want))
		}
	}
	return out
}

// checkSubtreeWalk makes localEntriesUnderLocked compare its walk with a scan
// of every entry. Tests turn it on.
var checkSubtreeWalk = false
