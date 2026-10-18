package lazyfs

import (
	"errors"
)

// ErrChanged is returned when the workspace's edits are not the ones the
// caller based an operation on: something wrote to it meanwhile.
var ErrChanged = errors.New("the workspace changed meanwhile")

// Switch makes the workspace show newBase changed by diff, as switching to a
// changeset does: a path diff maps to an entry shows that file or symlink, as
// a reference, so nothing is fetched; a path it maps to nil is deleted; every
// other path shows newBase. Local entries at the paths in keep (untracked
// files, such as build output, that a switch leaves alone) stay as they are,
// unless diff or newBase has the path; every other local change is dropped.
//
// expect is the workspace's edits as the caller saw them: if they are not the
// edits any more, Switch changes nothing and returns ErrChanged, so a write
// made meanwhile is never dropped. It returns the paths whose view may have
// changed.
func (f *FS) Switch(newBase *Manifest, diff map[string]*BaseEntry, keep []string, expect []Edit) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	current, err := f.editsLocked()
	if err != nil {
		return nil, err
	}
	if !sameEdits(current, expect) {
		return nil, ErrChanged
	}
	next := map[string]*localEntry{}
	for p, entry := range diff {
		p, err := cleanPath(p)
		if err != nil {
			return nil, err
		}
		if p == "/" {
			continue
		}
		base, inBase := newBase.Lookup(p)
		switch {
		case entry == nil:
			if inBase && base.Kind != KindDir {
				next[p] = &localEntry{Kind: entryWhiteout}
			}
		case entry.Kind == KindSymlink:
			if inBase && base.Kind == KindSymlink && base.Target == entry.Target {
				continue
			}
			next[p] = &localEntry{Kind: entrySymlink, Mode: ModeSymlink, Target: entry.Target}
		case entry.Kind == KindFile:
			mode := normalizeFileMode(entry.Mode)
			if inBase && base.Kind == KindFile && base.ContentHash == entry.ContentHash && base.Mode == mode {
				continue
			}
			next[p] = &localEntry{Kind: entryRef, Mode: mode, Hash: entry.ContentHash, Size: entry.Size}
		}
	}
	used := map[string]bool{}
	for _, p := range keep {
		entry := f.entries[p]
		if entry == nil || entry.Kind == entryDir || entry.Kind == entryWhiteout {
			continue
		}
		if _, listed := diff[p]; listed {
			continue
		}
		if _, inBase := newBase.Lookup(p); inBase {
			continue
		}
		kept := *entry
		next[p] = &kept
		if kept.Kind == entryFile {
			used[kept.Local] = true
		}
	}
	addParentDirs(next, newBase)
	return f.replaceLayerLocked(newBase, next, used)
}

// sameEdits reports whether two sorted edit lists make the same changes.
func sameEdits(a, b []Edit) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Op != b[i].Op || a[i].Path != b[i].Path || a[i].ContentHash != b[i].ContentHash || a[i].Mode != b[i].Mode {
			return false
		}
	}
	return true
}
