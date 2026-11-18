package lazyfs

import (
	"context"
	"io"
	"io/fs"
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
