package lazyfs

import (
	"context"
	"io"
	"io/fs"
	"os"
)

// Attr describes a path in the merged view.
type Attr struct {
	Kind Kind
	Mode uint32
	Size int64
	// Local is set when the content is in the local layer (an edited or new
	// file); otherwise it is base content, read by ContentHash.
	Local       bool
	ContentHash string
	Target      string // symlinks
}

// DirEntry is one entry of a directory listing.
type DirEntry struct {
	Name string
	Attr
}

func (n node) attr() Attr {
	return Attr{Kind: n.kind, Mode: n.mode, Size: n.size, Local: n.local != "", ContentHash: n.hash, Target: n.target}
}

// Stat returns the attributes of p. It never reads content.
func (f *FS) Stat(p string) (Attr, error) {
	p, err := cleanPath(p)
	if err != nil {
		return Attr{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	n, err := f.resolveLocked("stat", p)
	if err != nil {
		return Attr{}, err
	}
	return n.attr(), nil
}

// ReadDir lists the directory p, sorted by name. It never reads content.
func (f *FS) ReadDir(p string) ([]DirEntry, error) {
	p, err := cleanPath(p)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	n, err := f.resolveLocked("readdir", p)
	if err != nil {
		return nil, err
	}
	if n.kind != KindDir {
		return nil, &fs.PathError{Op: "readdir", Path: p, Err: ErrNotDir}
	}
	names := f.childNamesLocked(p, n)
	out := make([]DirEntry, 0, len(names))
	for _, name := range names {
		child, _ := f.lookupLocked(joinPath(p, name))
		out = append(out, DirEntry{Name: name, Attr: child.attr()})
	}
	return out, nil
}

// Readlink returns a symlink's target.
func (f *FS) Readlink(p string) (string, error) {
	attr, err := f.Stat(p)
	if err != nil {
		return "", err
	}
	if attr.Kind != KindSymlink {
		return "", &fs.PathError{Op: "readlink", Path: p, Err: fs.ErrInvalid}
	}
	return attr.Target, nil
}

// Open returns a reader for the file p: the local copy, or the base content
// (which Content may fetch).
func (f *FS) Open(ctx context.Context, p string) (io.ReadCloser, error) {
	p, err := cleanPath(p)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	n, err := f.resolveLocked("open", p)
	localPath := ""
	if err == nil && n.local != "" {
		localPath = f.localPath(n.local)
	}
	f.mu.Unlock()
	switch {
	case err != nil:
		return nil, err
	case n.kind == KindDir:
		return nil, &fs.PathError{Op: "open", Path: p, Err: ErrIsDir}
	case n.kind != KindFile:
		return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrInvalid}
	case localPath != "":
		return os.Open(localPath)
	}
	return f.content.Open(ctx, n.hash)
}

// ReadFile returns the content of the file p.
func (f *FS) ReadFile(ctx context.Context, p string) ([]byte, error) {
	r, err := f.Open(ctx, p)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

// WriteFile replaces the content of the file p, creating it if needed. mode 0
// keeps an existing file's mode (a new file gets ModeFile). It does not read
// the old content.
func (f *FS) WriteFile(p string, data []byte, mode uint32) error {
	p, err := cleanPath(p)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.resolveParentLocked("write", p); err != nil {
		return err
	}
	existing, exists := f.lookupLocked(p)
	if exists && existing.kind == KindDir {
		return &fs.PathError{Op: "write", Path: p, Err: ErrIsDir}
	}
	if exists && existing.kind == KindSymlink {
		return &fs.PathError{Op: "write", Path: p, Err: fs.ErrInvalid}
	}
	if mode == 0 {
		mode = ModeFile
		if exists {
			mode = existing.mode
		}
	}
	name, err := f.writeLocalLocked(func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
	if err != nil {
		return err
	}
	return f.replaceLocked(p, &localEntry{Kind: entryFile, Mode: normalizeFileMode(mode), Local: name}, existing.local)
}

// OpenWritable returns the path of a local file holding p's content, copying
// the base content up first (unless truncate is set, when the copy starts
// empty), so a file system adapter can write it in place. Writes there are
// local edits: Edits hashes the file when asked.
func (f *FS) OpenWritable(ctx context.Context, p string, truncate bool) (string, error) {
	p, err := cleanPath(p)
	if err != nil {
		return "", err
	}
	f.mu.Lock()
	n, err := f.resolveLocked("open", p)
	f.mu.Unlock()
	switch {
	case err != nil:
		return "", err
	case n.kind == KindDir:
		return "", &fs.PathError{Op: "open", Path: p, Err: ErrIsDir}
	case n.kind != KindFile:
		return "", &fs.PathError{Op: "open", Path: p, Err: fs.ErrInvalid}
	case n.local != "":
		local := f.localPath(n.local)
		if truncate {
			if err := os.Truncate(local, 0); err != nil {
				return "", err
			}
		}
		return local, nil
	}
	// Copy up outside the lock: fetching base content can take a while.
	var source io.ReadCloser
	if !truncate {
		if source, err = f.content.Open(ctx, n.hash); err != nil {
			return "", err
		}
		defer source.Close()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	name, err := f.writeLocalLocked(func(w io.Writer) error {
		if source == nil {
			return nil
		}
		_, err := io.Copy(w, source)
		return err
	})
	if err != nil {
		return "", err
	}
	current, ok := f.lookupLocked(p)
	if !ok || current.kind != KindFile || current.local != "" || current.hash != n.hash {
		// The path changed while the content was copied; let the caller retry.
		_ = os.Remove(f.localPath(name))
		return "", &fs.PathError{Op: "open", Path: p, Err: fs.ErrInvalid}
	}
	if err := f.replaceLocked(p, &localEntry{Kind: entryFile, Mode: current.mode, Local: name}, ""); err != nil {
		return "", err
	}
	return f.localPath(name), nil
}

// Truncate sets the size of the file p.
func (f *FS) Truncate(ctx context.Context, p string, size int64) error {
	local, err := f.OpenWritable(ctx, p, size == 0)
	if err != nil {
		return err
	}
	return os.Truncate(local, size)
}

// Mkdir makes the directory p.
func (f *FS) Mkdir(p string) error {
	p, err := cleanPath(p)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.resolveParentLocked("mkdir", p); err != nil {
		return err
	}
	if _, exists := f.lookupLocked(p); exists {
		return &fs.PathError{Op: "mkdir", Path: p, Err: fs.ErrExist}
	}
	baseEntry, inBase := f.base.Lookup(p)
	// A directory made where the base has one (now deleted) must not bring
	// the base's entries back.
	return f.commitLocked(map[string]*localEntry{p: {Kind: entryDir, Opaque: inBase && baseEntry.Kind == KindDir}})
}

// Symlink makes p a symlink to target.
func (f *FS) Symlink(target, p string) error {
	p, err := cleanPath(p)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.resolveParentLocked("symlink", p); err != nil {
		return err
	}
	if _, exists := f.lookupLocked(p); exists {
		return &fs.PathError{Op: "symlink", Path: p, Err: fs.ErrExist}
	}
	return f.commitLocked(map[string]*localEntry{p: {Kind: entrySymlink, Mode: ModeSymlink, Target: target}})
}

// Chmod sets whether the file p is executable. Directories and symlinks have
// no mode in the native model, so it does nothing for them.
func (f *FS) Chmod(p string, mode uint32) error {
	p, err := cleanPath(p)
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
	p, err := cleanPath(p)
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
	n, ok := f.lookupLocked(p)
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
	oldPath, err := cleanPath(oldPath)
	if err != nil {
		return err
	}
	newPath, err = cleanPath(newPath)
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
	src, ok := f.lookupLocked(oldPath)
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
	dst, dstExists := f.lookupLocked(newPath)
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
				childNode, _ := f.lookupLocked(child)
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

// replaceLocked puts entry at p and deletes the local content it replaces.
func (f *FS) replaceLocked(p string, entry *localEntry, oldLocal string) error {
	// On failure the new content file stays: the record may have reached the
	// journal. If it did not, the next Open collects it.
	if err := f.commitLocked(map[string]*localEntry{p: entry}); err != nil {
		return err
	}
	if oldLocal != "" && oldLocal != entry.Local {
		f.deleteLocal([]string{oldLocal})
	}
	return nil
}

// writeLocalLocked writes a new local content file and returns its name.
func (f *FS) writeLocalLocked(write func(io.Writer) error) (string, error) {
	name, err := newLocalName()
	if err != nil {
		return "", err
	}
	file, err := os.OpenFile(f.localPath(name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	if err := write(file); err != nil {
		file.Close()
		_ = os.Remove(f.localPath(name))
		return "", err
	}
	if !f.opts.NoSync {
		if err := file.Sync(); err != nil {
			file.Close()
			return "", err
		}
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	if err := f.at("content-written"); err != nil {
		return "", err
	}
	return name, nil
}

func (f *FS) deleteLocal(names []string) {
	if f.at("applied") != nil {
		return
	}
	for _, name := range names {
		_ = os.Remove(f.localPath(name))
		delete(f.hashes, name)
	}
}

// errorOf unwraps a *fs.PathError to its error.
func errorOf(err error) error {
	if pathErr, ok := err.(*fs.PathError); ok {
		return pathErr.Err
	}
	return err
}
