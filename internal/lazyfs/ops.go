package lazyfs

import (
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
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
	// ModTime is a local file's modification time; zero for base content.
	ModTime time.Time
	// LocalPath is the file holding a local file's content.
	LocalPath string
}

// DirEntry is one entry of a directory listing.
type DirEntry struct {
	Name string
	Attr
}

func (n node) attr() Attr {
	return Attr{Kind: n.kind, Mode: n.mode, Size: n.size, Local: n.local != "", ContentHash: n.hash, Target: n.target, ModTime: n.modTime}
}

// Stat returns the attributes of p. It never reads content.
func (f *FS) Stat(p string) (Attr, error) {
	p, err := cleanPath(p)
	if err != nil {
		return Attr{}, err
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	n, err := f.resolveLocked("stat", p)
	if err != nil {
		return Attr{}, err
	}
	attr := n.attr()
	if n.local != "" {
		attr.LocalPath = f.localPath(n.local)
	}
	return attr, nil
}

// ReadDir lists the directory p, sorted by name. It never reads content.
func (f *FS) ReadDir(p string) ([]DirEntry, error) {
	p, err := cleanPath(p)
	if err != nil {
		return nil, err
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
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
		childPath := joinPath(p, name)
		child, _ := f.lookupMetaLocked(childPath)
		if child.local != "" {
			size, modTime, err := f.statLocal(child.local)
			if err != nil {
				return nil, &fs.PathError{Op: "readdir", Path: childPath, Err: err}
			}
			child.size, child.modTime = size, modTime
		}
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
	f.mu.RLock()
	n, err := f.resolveLocked("open", p)
	f.mu.RUnlock()
	switch {
	case err != nil:
		return nil, err
	case n.kind == KindDir:
		return nil, &fs.PathError{Op: "open", Path: p, Err: ErrIsDir}
	case n.kind != KindFile:
		return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrInvalid}
	case n.local != "":
		return f.openLocalRead(p, n.local)
	}
	return f.content.Open(ctx, n.hash)
}

func (f *FS) openLocalRead(p, name string) (io.ReadCloser, error) {
	if _, _, err := f.statLocal(name); err != nil {
		return nil, &fs.PathError{Op: "open", Path: p, Err: err}
	}
	file, err := f.filesRoot.Open(name)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrInvalid}
	}
	return file, nil
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
	p, err := cleanWritablePath(p)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.resolveParentLocked("write", p); err != nil {
		return err
	}
	existing, exists := f.lookupMetaLocked(p)
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
	name, _, err := f.writeLocalFileLocked(func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
	if err != nil {
		return err
	}
	return f.replaceLocked(p, &localEntry{Kind: entryFile, Mode: normalizeFileMode(mode), Local: name}, existing.local)
}

// CreateEmptyWritable creates or opens p as a writable local file in a single
// step, returning its attributes (including LocalPath). If p does not exist, it
// creates an empty local file with mode; if p exists and excl is set, it fails
// with fs.ErrExist; otherwise it opens p writable (truncating when truncate is
// set, or copying up base content when not).
func (f *FS) CreateEmptyWritable(ctx context.Context, p string, mode uint32, excl, truncate bool) (Attr, error) {
	p, err := cleanWritablePath(p)
	if err != nil {
		return Attr{}, err
	}
	f.mu.Lock()
	if err := f.resolveParentLocked("create", p); err != nil {
		f.mu.Unlock()
		return Attr{}, err
	}
	existing, exists := f.lookupMetaLocked(p)
	if exists {
		if excl {
			f.mu.Unlock()
			return Attr{}, &fs.PathError{Op: "create", Path: p, Err: fs.ErrExist}
		}
		if existing.kind == KindDir {
			f.mu.Unlock()
			return Attr{}, &fs.PathError{Op: "create", Path: p, Err: ErrIsDir}
		}
		if existing.kind != KindFile {
			f.mu.Unlock()
			return Attr{}, &fs.PathError{Op: "create", Path: p, Err: fs.ErrInvalid}
		}
		if existing.local != "" {
			if truncate {
				if err := f.truncateLocalLocked(existing.local); err != nil {
					f.mu.Unlock()
					return Attr{}, err
				}
			}
			size, modTime, err := f.statLocal(existing.local)
			localPath := f.localPath(existing.local)
			f.mu.Unlock()
			if err != nil {
				return Attr{}, &fs.PathError{Op: "create", Path: p, Err: err}
			}
			return Attr{
				Kind:      KindFile,
				Mode:      existing.mode,
				Size:      size,
				Local:     true,
				ModTime:   modTime,
				LocalPath: localPath,
			}, nil
		}
		if !truncate {
			f.mu.Unlock()
			if _, err := f.OpenWritable(ctx, p, false); err != nil {
				return Attr{}, err
			}
			return f.Stat(p)
		}
	}
	fileMode := normalizeFileMode(mode)
	if mode == 0 {
		fileMode = ModeFile
	}
	if exists {
		fileMode = existing.mode
	}
	name, modTime, err := f.writeLocalFileLocked(func(w io.Writer) error { return nil })
	if err != nil {
		f.mu.Unlock()
		return Attr{}, err
	}
	if err := f.replaceLocked(p, &localEntry{Kind: entryFile, Mode: fileMode, Local: name}, existing.local); err != nil {
		f.mu.Unlock()
		return Attr{}, err
	}
	localPath := f.localPath(name)
	f.mu.Unlock()
	return Attr{
		Kind:      KindFile,
		Mode:      fileMode,
		Size:      0,
		Local:     true,
		ModTime:   modTime,
		LocalPath: localPath,
	}, nil
}

// OpenWritable returns the path of a local file holding p's content, copying
// the base content up first (unless truncate is set, when the copy starts
// empty), so a file system adapter can write it in place. Writes there are
// local edits: Edits hashes the file when asked.
func (f *FS) OpenWritable(ctx context.Context, p string, truncate bool) (string, error) {
	p, err := cleanWritablePath(p)
	if err != nil {
		return "", err
	}
	f.mu.RLock()
	n, err := f.resolveLocked("open", p)
	f.mu.RUnlock()
	switch {
	case err != nil:
		return "", err
	case n.kind == KindDir:
		return "", &fs.PathError{Op: "open", Path: p, Err: ErrIsDir}
	case n.kind != KindFile:
		return "", &fs.PathError{Op: "open", Path: p, Err: fs.ErrInvalid}
	case n.local != "" && !truncate:
		return f.localPath(n.local), nil
	case n.local != "" && truncate:
		f.mu.Lock()
		current, rerr := f.resolveLocked("open", p)
		if rerr != nil {
			f.mu.Unlock()
			return "", rerr
		}
		if current.kind == KindDir {
			f.mu.Unlock()
			return "", &fs.PathError{Op: "open", Path: p, Err: ErrIsDir}
		}
		if current.kind != KindFile {
			f.mu.Unlock()
			return "", &fs.PathError{Op: "open", Path: p, Err: fs.ErrInvalid}
		}
		if current.local != "" {
			err := f.truncateLocalLocked(current.local)
			local := f.localPath(current.local)
			f.mu.Unlock()
			if err != nil {
				return "", err
			}
			return local, nil
		}
		n = current
		f.mu.Unlock()
	}
	// Stage base-file copy-up into a temporary file outside f.mu so fetching or
	// copying a large file never blocks concurrent operations on the mount.
	name, tmpName, err := f.stageLocalCopy(ctx, n.hash, truncate)
	if err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	current, ok := f.lookupMetaLocked(p)
	if !ok || current.kind != KindFile {
		_ = f.filesRoot.Remove(tmpName)
		return "", &fs.PathError{Op: "open", Path: p, Err: fs.ErrInvalid}
	}
	if current.local != "" {
		_ = f.filesRoot.Remove(tmpName)
		if _, _, err := f.statLocal(current.local); err != nil {
			return "", &fs.PathError{Op: "open", Path: p, Err: err}
		}
		if truncate {
			if err := f.truncateLocalLocked(current.local); err != nil {
				return "", err
			}
		}
		return f.localPath(current.local), nil
	}
	if current.hash != n.hash {
		_ = f.filesRoot.Remove(tmpName)
		return "", &fs.PathError{Op: "open", Path: p, Err: fs.ErrInvalid}
	}
	if err := verifyRealDir(filepath.Join(f.dir, filesDir)); err != nil {
		_ = f.filesRoot.Remove(tmpName)
		return "", err
	}
	if err := os.Rename(f.localPath(tmpName), f.localPath(name)); err != nil {
		_ = f.filesRoot.Remove(tmpName)
		return "", err
	}
	if err := f.at("content-written"); err != nil {
		return "", err
	}
	if err := f.replaceLocked(p, &localEntry{Kind: entryFile, Mode: current.mode, Local: name}, ""); err != nil {
		return "", err
	}
	return f.localPath(name), nil
}

func (f *FS) stageLocalCopy(ctx context.Context, hash string, truncate bool) (string, string, error) {
	var source io.ReadCloser
	if !truncate {
		var err error
		if source, err = f.content.Open(ctx, hash); err != nil {
			return "", "", err
		}
		defer source.Close()
	}
	name, err := newLocalName()
	if err != nil {
		return "", "", err
	}
	tmpName := name + ".tmp"
	file, err := f.filesRoot.OpenFile(tmpName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", "", err
	}
	if source != nil {
		if _, err := io.Copy(file, source); err != nil {
			_ = file.Close()
			_ = f.filesRoot.Remove(tmpName)
			return "", "", err
		}
	}
	if !f.opts.NoSync {
		if err := file.Sync(); err != nil {
			_ = file.Close()
			_ = f.filesRoot.Remove(tmpName)
			return "", "", err
		}
	}
	if err := file.Close(); err != nil {
		_ = f.filesRoot.Remove(tmpName)
		return "", "", err
	}
	return name, tmpName, nil
}

func (f *FS) truncateLocalLocked(name string) error {
	if _, _, err := f.statLocal(name); err != nil {
		return err
	}
	file, err := f.filesRoot.OpenFile(name, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	return file.Close()
}

// Truncate sets the size of the file p.
func (f *FS) Truncate(ctx context.Context, p string, size int64) error {
	local, err := f.OpenWritable(ctx, p, size == 0)
	if err != nil {
		return err
	}
	if size == 0 {
		return nil
	}
	return os.Truncate(local, size)
}

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
	name, _, err := f.writeLocalFileLocked(write)
	return name, err
}

func (f *FS) writeLocalFileLocked(write func(io.Writer) error) (string, time.Time, error) {
	name, err := newLocalName()
	if err != nil {
		return "", time.Time{}, err
	}
	file, err := f.filesRoot.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", time.Time{}, err
	}
	if err := write(file); err != nil {
		_ = file.Close()
		_ = f.filesRoot.Remove(name)
		return "", time.Time{}, err
	}
	if !f.opts.NoSync {
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return "", time.Time{}, err
		}
	}
	var modTime time.Time
	if info, err := file.Stat(); err == nil {
		modTime = info.ModTime()
	}
	if err := file.Close(); err != nil {
		return "", time.Time{}, err
	}
	if err := f.at("content-written"); err != nil {
		return "", time.Time{}, err
	}
	return name, modTime, nil
}

func (f *FS) deleteLocal(names []string) {
	if f.at("applied") != nil {
		return
	}
	for _, name := range names {
		_ = f.filesRoot.Remove(name)
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
