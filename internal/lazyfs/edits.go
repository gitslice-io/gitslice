package lazyfs

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
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

// Edits returns the workspace's changes against its base, sorted by path. It
// costs the size of the local layer, not of the slice: only paths under a
// local entry are compared. Local files are hashed through Content, and a
// file's hash is remembered until its size or time changes.
func (f *FS) Edits() ([]Edit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.editsLocked()
}

func (f *FS) editsLocked() ([]Edit, error) {
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

	var edits []Edit
	for _, p := range paths {
		baseEntry, inBase := f.base.Lookup(p)
		inBase = inBase && baseEntry.Kind != KindDir
		n, exists := f.lookupLocked(p)
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
	info, err := os.Stat(f.localPath(name))
	if err != nil {
		return "", err
	}
	memo, ok := f.hashes[name]
	if ok && memo.size == info.Size() && memo.modTime == info.ModTime().UnixNano() {
		return memo.hash, nil
	}
	hash, err := f.content.HashFile(f.localPath(name))
	if err != nil {
		return "", err
	}
	f.hashes[name] = hashMemo{size: info.Size(), modTime: info.ModTime().UnixNano(), hash: hash}
	return hash, nil
}

// Restore drops the local changes at and under p, so the base shows again. A
// path inside a directory that is itself deleted or replaced cannot be
// restored on its own: restore that directory.
func (f *FS) Restore(p string) error {
	p, err := cleanPath(p)
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

// MergeCase is a path both the user and the new base changed. A nil entry is
// a path that does not exist on that side.
type MergeCase struct {
	Path   string
	Base   *BaseEntry // the old base
	Theirs *BaseEntry // the new base
	Ours   *Edit      // the local change
}

// MergeResult is how a MergeCase resolves.
type MergeResult struct {
	// Delete resolves to no file.
	Delete bool
	// Content and Mode are the file otherwise.
	Content []byte
	Mode    uint32
	// Conflict marks the path as conflicted; the result still stands (it
	// may hold conflict markers) and Rebase reports the path.
	Conflict bool
}

// MergeFunc resolves a path both sides changed.
type MergeFunc func(ctx context.Context, c MergeCase) (MergeResult, error)

// Rebase moves the workspace onto newBase. Paths the user did not change take
// the new base with no work at all; changed paths the new base left alone keep
// the user's change; paths both changed go to merge, unless both ended up the
// same. It returns the paths merge marked as conflicts. The caller must keep
// newBase's manifest: Open needs it from now on.
func (f *FS) Rebase(ctx context.Context, newBase *Manifest, merge MergeFunc) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	edits, err := f.editsLocked()
	if err != nil {
		return nil, err
	}
	oldBase := f.base
	next := map[string]*localEntry{}
	usedLocal := map[string]bool{}
	var conflicts []string
	var written []string
	for _, edit := range edits {
		edit := edit
		var before, after *BaseEntry
		if entry, ok := oldBase.Lookup(edit.Path); ok && entry.Kind != KindDir {
			before = &entry
		}
		if entry, ok := newBase.Lookup(edit.Path); ok {
			after = &entry
		}
		ours := f.entryForEditLocked(edit)
		switch {
		case sameBaseEntry(before, after):
			// The new base left the path alone: keep the change.
		case editMatches(edit, after):
			// Both sides made the same change.
			ours = nil
		default:
			result, err := merge(ctx, MergeCase{Path: edit.Path, Base: before, Theirs: after, Ours: &edit})
			if err != nil {
				f.discard(written)
				return nil, err
			}
			if result.Conflict {
				conflicts = append(conflicts, edit.Path)
			}
			if result.Delete {
				ours = &localEntry{Kind: entryWhiteout}
			} else {
				name, err := f.writeLocalLocked(func(w io.Writer) error {
					_, err := w.Write(result.Content)
					return err
				})
				if err != nil {
					f.discard(written)
					return nil, err
				}
				written = append(written, name)
				ours = &localEntry{Kind: entryFile, Mode: normalizeFileMode(result.Mode), Local: name}
			}
		}
		if ours == nil {
			continue
		}
		if ours.Kind == entryWhiteout && after == nil {
			continue // deleted on both sides
		}
		if ours.Kind == entryWhiteout && after != nil && after.Kind == KindDir {
			continue // the new base has a directory there now
		}
		next[edit.Path] = ours
		if ours.Kind == entryFile {
			usedLocal[ours.Local] = true
		}
	}
	// The user's own directories that the new base does not have.
	for p, entry := range f.entries {
		if entry.Kind != entryDir || !f.isVisibleLocked(p) {
			continue
		}
		if baseEntry, ok := newBase.Lookup(p); ok && baseEntry.Kind == KindDir {
			continue
		}
		next[p] = &localEntry{Kind: entryDir}
	}
	// A base directory the user deleted stays deleted as a whole, unless the
	// new base added files to it (they show, as they would in an eager
	// workspace) or the user made new files in it.
	for p := range f.entries {
		before, ok := oldBase.Lookup(p)
		if !ok || before.Kind != KindDir || f.isVisibleLocked(p) {
			continue
		}
		after, ok := newBase.Lookup(p)
		if !ok || after.Kind != KindDir {
			continue
		}
		whole := true
		for _, file := range newBase.filesUnder(p) {
			if entry := next[file.Path]; entry == nil || entry.Kind != entryWhiteout {
				whole = false
				break
			}
		}
		for q, entry := range next {
			if isUnder(q, p) && entry.Kind != entryWhiteout {
				whole = false
				break
			}
		}
		if !whole {
			continue
		}
		for q := range next {
			if isUnder(q, p) {
				delete(next, q)
			}
		}
		next[p] = &localEntry{Kind: entryWhiteout}
	}
	// Every kept entry needs its directories.
	for p, entry := range next {
		if entry.Kind == entryWhiteout {
			continue
		}
		for dir := parentPath(p); dir != "/"; dir = parentPath(dir) {
			existing := next[dir]
			if existing != nil && existing.Kind == entryDir {
				continue
			}
			if baseEntry, ok := newBase.Lookup(dir); existing == nil && ok && baseEntry.Kind == KindDir {
				continue
			}
			next[dir] = &localEntry{Kind: entryDir}
		}
	}

	previous := f.entries
	f.base = newBase
	f.reset(next)
	if err := f.writeSnapshot(newBase.CommitID()); err != nil {
		f.base = oldBase
		f.reset(previous)
		f.discard(written)
		return nil, err
	}
	var garbage []string
	for _, entry := range previous {
		if entry.Kind == entryFile && !usedLocal[entry.Local] {
			garbage = append(garbage, entry.Local)
		}
	}
	f.deleteLocal(garbage)
	sort.Strings(conflicts)
	return conflicts, nil
}

// entryForEditLocked returns the local entry that makes an edit, reusing the
// local file that holds it.
func (f *FS) entryForEditLocked(edit Edit) *localEntry {
	if edit.Op == "delete" {
		return &localEntry{Kind: entryWhiteout}
	}
	n, _ := f.lookupLocked(edit.Path)
	switch {
	case n.kind == KindSymlink:
		return &localEntry{Kind: entrySymlink, Mode: ModeSymlink, Target: n.target}
	case n.local != "":
		return &localEntry{Kind: entryFile, Mode: n.mode, Local: n.local}
	}
	return &localEntry{Kind: entryRef, Mode: n.mode, Hash: n.hash, Size: n.size}
}

func (f *FS) isVisibleLocked(p string) bool {
	_, ok := f.lookupLocked(p)
	return ok
}

func (f *FS) discard(names []string) {
	for _, name := range names {
		_ = os.Remove(f.localPath(name))
	}
}

func sameBaseEntry(a, b *BaseEntry) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Kind == b.Kind && a.Mode == b.Mode && a.ContentHash == b.ContentHash && a.Target == b.Target
}

func editMatches(edit Edit, entry *BaseEntry) bool {
	if edit.Op == "delete" {
		return entry == nil
	}
	return entry != nil && entry.Kind != KindDir && entry.ContentHash == edit.ContentHash && entry.Mode == edit.Mode
}
