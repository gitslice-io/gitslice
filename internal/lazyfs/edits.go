package lazyfs

import (
	"errors"
	"fmt"
	"io/fs"
	"sort"
)

// Edit is one change of the workspace against its base, file by file, as a
// changeset records it. Directories are not edits: they follow from files.
type Edit struct {
	Op          string // "upsert" or "delete"
	Path        string
	ContentHash string // upsert
	Mode        uint32 // upsert
	// LocalPath is the local file holding an upsert's content, or "" when the
	// content is known by hash only (a moved or re-moded base file, a
	// symlink), which the server already has unless it is a new symlink.
	LocalPath string
}

type pendingHashEdit struct {
	path            string
	local           string
	mode            uint32
	inBase          bool
	baseContentHash string
	baseMode        uint32
}

// Edits returns the workspace's changes against its base, sorted by path. It
// costs the size of the local layer, not of the slice: only paths under a
// local entry are compared. Dirty entries are snapshotted under f.mu.RLock()
// and uncached local files are hashed outside f.mu (serialized by f.hashMu) so
// concurrent FUSE syscalls never stall behind large-file SHA-256 hashing.
func (f *FS) Edits() ([]Edit, error) {
	for {
		edits, pending, err := f.snapshotEdits()
		if err != nil || len(pending) == 0 {
			return edits, err
		}

		stale := false
		f.hashMu.Lock()
		for _, item := range pending {
			hash, current, err := f.hashPendingLocal(item)
			if err != nil {
				f.hashMu.Unlock()
				return nil, err
			}
			if !current {
				stale = true
				break
			}
			if item.inBase && item.baseContentHash == hash && item.baseMode == item.mode {
				continue
			}
			edits = append(edits, Edit{
				Op:          "upsert",
				Path:        item.path,
				Mode:        item.mode,
				ContentHash: hash,
				LocalPath:   f.localPath(item.local),
			})
		}
		f.hashMu.Unlock()
		if stale {
			continue
		}
		sort.Slice(edits, func(i, j int) bool { return edits[i].Path < edits[j].Path })
		return edits, nil
	}
}

func (f *FS) snapshotEdits() ([]Edit, []pendingHashEdit, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	paths := f.candidateEditPathsLocked()
	var edits []Edit
	var pending []pendingHashEdit
	for _, p := range paths {
		baseEntry, inBase := f.base.Lookup(p)
		inBase = inBase && baseEntry.Kind != KindDir
		n, exists := f.lookupMetaLocked(p)
		exists = exists && n.kind != KindDir
		if !exists {
			if inBase {
				edits = append(edits, Edit{Op: "delete", Path: p})
			}
			continue
		}
		if n.base {
			continue // unchanged base content
		}
		edit := Edit{Op: "upsert", Path: p, Mode: n.mode, ContentHash: n.hash}
		switch {
		case n.kind == KindSymlink:
			hash, err := f.content.HashBytes([]byte(n.target))
			if err != nil {
				return nil, nil, err
			}
			edit.ContentHash = hash
		case n.local != "":
			size, modTime, err := f.statLocal(n.local)
			if err != nil {
				return nil, nil, err
			}
			memo, ok := f.hashes[n.local]
			if !ok || memo.size != size || memo.modTime != modTime.UnixNano() {
				pending = append(pending, pendingHashEdit{
					path:            p,
					local:           n.local,
					mode:            n.mode,
					inBase:          inBase,
					baseContentHash: baseEntry.ContentHash,
					baseMode:        baseEntry.Mode,
				})
				continue
			}
			edit.ContentHash = memo.hash
			edit.LocalPath = f.localPath(n.local)
		}
		if inBase && baseEntry.ContentHash == edit.ContentHash && baseEntry.Mode == edit.Mode {
			continue // changed back, or copied up and left as it was
		}
		edits = append(edits, edit)
	}
	return edits, pending, nil
}

func (f *FS) hashPendingLocal(item pendingHashEdit) (string, bool, error) {
	size, modTime, err := f.statLocal(item.local)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) && !f.localStillBound(item.path, item.local) {
			return "", false, nil
		}
		return "", false, err
	}
	modTimeNs := modTime.UnixNano()

	f.mu.RLock()
	memo, ok := f.hashes[item.local]
	entry := f.entries[item.path]
	stillBound := entry != nil && entry.Kind == entryFile && entry.Local == item.local
	f.mu.RUnlock()
	if !stillBound {
		return "", false, nil
	}
	if ok && memo.size == size && memo.modTime == modTimeNs {
		return memo.hash, true, nil
	}

	hash, err := f.content.HashFile(f.localPath(item.local))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) && !f.localStillBound(item.path, item.local) {
			return "", false, nil
		}
		return "", false, err
	}
	if newSize, newModTime, statErr := f.statLocal(item.local); statErr == nil && newSize == size && newModTime.UnixNano() == modTimeNs {
		f.mu.Lock()
		if cur := f.entries[item.path]; cur != nil && cur.Kind == entryFile && cur.Local == item.local {
			f.hashes[item.local] = hashMemo{size: size, modTime: modTimeNs, hash: hash}
		}
		f.mu.Unlock()
	}
	return hash, true, nil
}

func (f *FS) localStillBound(p, local string) bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	entry := f.entries[p]
	return entry != nil && entry.Kind == entryFile && entry.Local == local
}

func (f *FS) candidateEditPathsLocked() []string {
	candidates := map[string]struct{}{}
	for p, entry := range f.entries {
		candidates[p] = struct{}{}
		if entry.Kind == entryDir && !entry.Opaque {
			continue
		}
		// Whatever hides or replaces a base directory hides the files in it.
		if baseEntry, ok := f.base.Lookup(p); ok && baseEntry.Kind == KindDir {
			for _, file := range f.base.filesUnder(p) {
				candidates[file.Path] = struct{}{}
			}
		}
	}
	paths := make([]string, 0, len(candidates))
	for p := range candidates {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}

func (f *FS) editsLocked() ([]Edit, error) {
	paths := f.candidateEditPathsLocked()
	var edits []Edit
	for _, p := range paths {
		baseEntry, inBase := f.base.Lookup(p)
		inBase = inBase && baseEntry.Kind != KindDir
		n, exists := f.lookupMetaLocked(p)
		exists = exists && n.kind != KindDir
		if !exists {
			if inBase {
				edits = append(edits, Edit{Op: "delete", Path: p})
			}
			continue
		}
		if n.base {
			continue // unchanged base content
		}
		edit := Edit{Op: "upsert", Path: p, Mode: n.mode, ContentHash: n.hash}
		switch {
		case n.kind == KindSymlink:
			hash, err := f.content.HashBytes([]byte(n.target))
			if err != nil {
				return nil, err
			}
			edit.ContentHash = hash
		case n.local != "":
			hash, err := f.hashLocalLocked(n.local)
			if err != nil {
				return nil, err
			}
			edit.ContentHash = hash
			edit.LocalPath = f.localPath(n.local)
		}
		if inBase && baseEntry.ContentHash == edit.ContentHash && baseEntry.Mode == edit.Mode {
			continue // changed back, or copied up and left as it was
		}
		edits = append(edits, edit)
	}
	return edits, nil
}

func (f *FS) hashLocalLocked(name string) (string, error) {
	size, modTime, err := f.statLocal(name)
	if err != nil {
		return "", err
	}
	modTimeNs := modTime.UnixNano()
	memo, ok := f.hashes[name]
	if ok && memo.size == size && memo.modTime == modTimeNs {
		return memo.hash, nil
	}
	hash, err := f.content.HashFile(f.localPath(name))
	if err != nil {
		return "", err
	}
	f.hashes[name] = hashMemo{size: size, modTime: modTimeNs, hash: hash}
	return hash, nil
}

// Restore drops the local changes at and under p, so the base shows again. A
// path inside a directory that is itself deleted or replaced cannot be
// restored on its own: restore that directory.
func (f *FS) Restore(p string) error {
	p, err := cleanWritablePath(p)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.baseVisibleLocked(p) {
		for dir := parentPath(p); dir != "/"; dir = parentPath(dir) {
			if entry := f.entries[dir]; entry != nil && !(entry.Kind == entryDir && !entry.Opaque) {
				return &fs.PathError{Op: "restore", Path: p, Err: fmt.Errorf("%w: %s is deleted or replaced; restore it instead", fs.ErrInvalid, dir)}
			}
		}
	}
	set := map[string]*localEntry{}
	var garbage []string
	for _, q := range f.localEntriesUnderLocked(p) {
		if entry := f.entries[q]; entry.Kind == entryFile {
			garbage = append(garbage, entry.Local)
		}
		set[q] = nil
	}
	if err := f.commitLocked(set); err != nil {
		return err
	}
	f.deleteLocal(garbage)
	return nil
}

// RestoreAll drops every local change.
func (f *FS) RestoreAll() error {
	return f.Restore("/")
}
