package lazyfs

import (
	"io/fs"
	"os"
)

// Mkdir makes the directory p.
func (f *FS) Mkdir(p string) error {
	p, err := cleanWritablePath(p)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.resolveParentLocked("mkdir", p); err != nil {
		return err
	}
	if _, exists := f.lookupMetaLocked(p); exists {
		return &fs.PathError{Op: "mkdir", Path: p, Err: fs.ErrExist}
	}
	baseEntry, inBase := f.base.Lookup(p)
	// A directory made where the base has one (now deleted) must not bring
	// the base's entries back.
	return f.commitLocked(map[string]*localEntry{p: {Kind: entryDir, Opaque: inBase && baseEntry.Kind == KindDir}})
}

// Symlink makes p a symlink to target.
func (f *FS) Symlink(target, p string) error {
	p, err := cleanWritablePath(p)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.resolveParentLocked("symlink", p); err != nil {
		return err
	}
	if _, exists := f.lookupMetaLocked(p); exists {
		return &fs.PathError{Op: "symlink", Path: p, Err: fs.ErrExist}
	}
	return f.commitLocked(map[string]*localEntry{p: {Kind: entrySymlink, Mode: ModeSymlink, Target: target}})
}

// Chmod sets whether the file p is executable. Directories and symlinks have
// no mode in the native model, so it does nothing for them.
func (f *FS) Chmod(p string, mode uint32) error {
	p, err := cleanWritablePath(p)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	n, err := f.resolveLocked("chmod", p)
	if err != nil {
		return err
	}
	if n.kind != KindFile {
		return nil
	}
	mode = normalizeFileMode(mode)
	if mode == n.mode {
		return nil
	}
	if n.local != "" {
		return f.commitLocked(map[string]*localEntry{p: {Kind: entryFile, Mode: mode, Local: n.local}})
	}
	// A base file only changes its mode: a reference, no copy.
	return f.commitLocked(map[string]*localEntry{p: {Kind: entryRef, Mode: mode, Hash: n.hash, Size: n.size}})
}

// Remove deletes the file, symlink or empty directory p.
func (f *FS) Remove(p string) error {
	p, err := cleanWritablePath(p)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if p == "/" {
		return &fs.PathError{Op: "remove", Path: p, Err: fs.ErrInvalid}
	}
	if err := f.resolveParentLocked("remove", p); err != nil {
		return err
	}
	n, ok := f.lookupMetaLocked(p)
	if !ok {
		return &fs.PathError{Op: "remove", Path: p, Err: fs.ErrNotExist}
	}
	if n.kind == KindDir && len(f.childNamesLocked(p, n)) > 0 {
		return &fs.PathError{Op: "remove", Path: p, Err: ErrNotEmpty}
	}
	set, garbage := f.removalLocked(p)
	if err := f.commitLocked(set); err != nil {
		return err
	}
	f.deleteLocal(garbage)
	return nil
}

// removalLocked returns the changes that delete p and everything under it, and
// the local content files that become garbage.
func (f *FS) removalLocked(p string) (map[string]*localEntry, []string) {
	set := map[string]*localEntry{}
	var garbage []string
	for _, q := range f.localEntriesUnderLocked(p) {
		if entry := f.entries[q]; entry.Kind == entryFile {
			garbage = append(garbage, entry.Local)
		}
		set[q] = nil
	}
	if _, inBase := f.base.Lookup(p); inBase && f.baseVisibleLocked(p) {
		set[p] = &localEntry{Kind: entryWhiteout}
	}
	return set, garbage
}

// Rename moves oldPath to newPath, replacing a file, or an empty directory,
// there. Moving base content records references: nothing is fetched.
func (f *FS) Rename(oldPath, newPath string) error {
	oldPath, err := cleanWritablePath(oldPath)
	if err != nil {
		return err
	}
	newPath, err = cleanWritablePath(newPath)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if oldPath == "/" || newPath == "/" {
		return &os.LinkError{Op: "rename", Old: oldPath, New: newPath, Err: fs.ErrInvalid}
	}
	// rename(2) resolves both parents before it looks for the source.
	if err := f.resolveParentLocked("rename", oldPath); err != nil {
		return err
	}
	if err := f.resolveParentLocked("rename", newPath); err != nil {
		return &os.LinkError{Op: "rename", Old: oldPath, New: newPath, Err: errorOf(err)}
	}
	src, ok := f.lookupMetaLocked(oldPath)
	if !ok {
		return &os.LinkError{Op: "rename", Old: oldPath, New: newPath, Err: fs.ErrNotExist}
	}
	if oldPath == newPath {
		return nil
	}
	// As rename(2): a directory cannot move under itself, and nothing can
	// replace one of its own ancestors.
	if isUnder(newPath, oldPath) {
		return &os.LinkError{Op: "rename", Old: oldPath, New: newPath, Err: fs.ErrInvalid}
	}
	if isUnder(oldPath, newPath) {
		return &os.LinkError{Op: "rename", Old: oldPath, New: newPath, Err: ErrNotEmpty}
	}
	dst, dstExists := f.lookupMetaLocked(newPath)
	if dstExists {
		switch {
		case dst.kind == KindDir && src.kind != KindDir:
			return &os.LinkError{Op: "rename", Old: oldPath, New: newPath, Err: ErrIsDir}
		case dst.kind != KindDir && src.kind == KindDir:
			return &os.LinkError{Op: "rename", Old: oldPath, New: newPath, Err: ErrNotDir}
		case dst.kind == KindDir && len(f.childNamesLocked(newPath, dst)) > 0:
			return &os.LinkError{Op: "rename", Old: oldPath, New: newPath, Err: ErrNotEmpty}
		}
	}

	// What is at oldPath, as entries relative to it. Local files keep their
	// content file; everything else becomes a reference or a new entry.
	moved := map[string]*localEntry{}
	kept := map[string]bool{}
	var collect func(rel, p string, n node)
	collect = func(rel, p string, n node) {
		switch n.kind {
		case KindFile:
			if n.local != "" {
				moved[rel] = &localEntry{Kind: entryFile, Mode: n.mode, Local: n.local}
				kept[n.local] = true
			} else {
				moved[rel] = &localEntry{Kind: entryRef, Mode: n.mode, Hash: n.hash, Size: n.size}
			}
		case KindSymlink:
			moved[rel] = &localEntry{Kind: entrySymlink, Mode: ModeSymlink, Target: n.target}
		case KindDir:
			moved[rel] = &localEntry{Kind: entryDir, Opaque: true}
			for _, name := range f.childNamesLocked(p, n) {
				child := joinPath(p, name)
				childNode, _ := f.lookupMetaLocked(child)
				collect(joinPath(rel, name), child, childNode)
			}
		}
	}
	collect("/", oldPath, src)

	// Clear the destination, then the source, then place the moved entries.
	set := map[string]*localEntry{}
	var garbage []string
	for _, q := range f.localEntriesUnderLocked(newPath) {
		if entry := f.entries[q]; entry.Kind == entryFile && !kept[entry.Local] {
			garbage = append(garbage, entry.Local)
		}
		set[q] = nil
	}
	sourceSet, sourceGarbage := f.removalLocked(oldPath)
	for q, entry := range sourceSet {
		set[q] = entry
	}
	for _, name := range sourceGarbage {
		if !kept[name] {
			garbage = append(garbage, name)
		}
	}
	for rel, entry := range moved {
		target := newPath
		if rel != "/" {
			target = newPath + rel
		}
		set[target] = entry
	}
	if err := f.commitLocked(set); err != nil {
		return err
	}
	f.deleteLocal(garbage)
	return nil
}

// errorOf unwraps a *fs.PathError to its error.
func errorOf(err error) error {
	if pathErr, ok := err.(*fs.PathError); ok {
		return pathErr.Err
	}
	return err
}
