package lazyfs

import (
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

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
