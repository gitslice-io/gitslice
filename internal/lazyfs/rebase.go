package lazyfs

import (
	"context"
	"io"
	"sort"
)

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
	// Keep keeps the local change as it is.
	Keep bool
	// Theirs takes the new base, dropping the local change.
	Theirs bool
	// Delete resolves to no file.
	Delete bool
	// Content and Mode are the file otherwise.
	Content []byte
	Mode    uint32
	// Conflict marks the path as conflicted; the result still stands (it
	// may hold conflict markers) and Rebase reports the path.
	Conflict bool
}

// RebaseResult is what Rebase did.
type RebaseResult struct {
	// Conflicts are the paths merge marked as conflicts, sorted.
	Conflicts []string
	// Changed are the paths whose view may have changed, for a file system
	// adapter to tell the kernel.
	Changed []string
}

// MergeFunc resolves a path both sides changed.
type MergeFunc func(ctx context.Context, c MergeCase) (MergeResult, error)

// rebaseSides returns a path's file in the old base (nil for none, or a
// directory) and its entry in the new base (nil for none).
func rebaseSides(p string, oldBase, newBase *Manifest) (before, after *BaseEntry) {
	if entry, ok := oldBase.Lookup(p); ok && entry.Kind != KindDir {
		before = &entry
	}
	if entry, ok := newBase.Lookup(p); ok {
		after = &entry
	}
	return before, after
}

// NeedsMerge reports whether Rebase onto newBase asks its MergeFunc about an
// edit made over oldBase: the new base changed the path, and not to what the
// edit made it. A caller that resolves merges ahead of time asks the same.
func NeedsMerge(edit Edit, oldBase, newBase *Manifest) bool {
	before, after := rebaseSides(edit.Path, oldBase, newBase)
	return !sameBaseEntry(before, after) && !editMatches(edit, after)
}

// Rebase moves the workspace onto newBase. Paths the user did not change take
// the new base with no work at all; changed paths the new base left alone keep
// the user's change; paths both changed go to merge, unless both ended up the
// same. The caller must keep newBase's manifest: Open needs it from now on.
func (f *FS) Rebase(ctx context.Context, newBase *Manifest, merge MergeFunc) (RebaseResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	edits, err := f.editsLocked()
	if err != nil {
		return RebaseResult{}, err
	}
	oldBase := f.base
	next := map[string]*localEntry{}
	usedLocal := map[string]bool{}
	var conflicts []string
	var written []string
	for _, edit := range edits {
		edit := edit
		before, after := rebaseSides(edit.Path, oldBase, newBase)
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
				return RebaseResult{}, err
			}
			if result.Conflict {
				conflicts = append(conflicts, edit.Path)
			}
			switch {
			case result.Keep:
			case result.Theirs:
				ours = nil
			case result.Delete:
				ours = &localEntry{Kind: entryWhiteout}
			default:
				name, err := f.writeLocalLocked(func(w io.Writer) error {
					_, err := w.Write(result.Content)
					return err
				})
				if err != nil {
					f.discard(written)
					return RebaseResult{}, err
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
	addParentDirs(next, newBase)
	changed, err := f.replaceLayerLocked(newBase, next, usedLocal)
	if err != nil {
		f.discard(written)
		return RebaseResult{}, err
	}
	sort.Strings(conflicts)
	return RebaseResult{Conflicts: conflicts, Changed: changed}, nil
}

// addParentDirs gives every entry of next but a whiteout the directories it
// needs: the base's where they are directories there, entries elsewhere
// (replacing a whiteout, or a base file the directory takes the place of).
func addParentDirs(next map[string]*localEntry, base *Manifest) {
	for p, entry := range next {
		if entry.Kind == entryWhiteout {
			continue
		}
		for dir := parentPath(p); dir != "/"; dir = parentPath(dir) {
			existing := next[dir]
			if existing != nil && existing.Kind == entryDir {
				continue
			}
			if baseEntry, ok := base.Lookup(dir); existing == nil && ok && baseEntry.Kind == KindDir {
				continue
			}
			next[dir] = &localEntry{Kind: entryDir}
		}
	}
}

// replaceLayerLocked makes next the local layer over newBase, on disk first,
// and deletes the local files of the old layer that next does not use (used
// names them). It returns the paths whose view may have changed.
func (f *FS) replaceLayerLocked(newBase *Manifest, next map[string]*localEntry, used map[string]bool) ([]string, error) {
	oldBase, previous := f.base, f.entries
	changed := changedPaths(oldBase, previous, newBase, next)
	f.base = newBase
	f.reset(next)
	if err := f.writeSnapshot(newBase.CommitID()); err != nil {
		f.base = oldBase
		f.reset(previous)
		return nil, err
	}
	var garbage []string
	for _, entry := range previous {
		if entry.Kind == entryFile && !used[entry.Local] {
			garbage = append(garbage, entry.Local)
		}
	}
	f.deleteLocal(garbage)
	return changed, nil
}

// changedPaths lists the paths whose merged view may differ between two
// states of the layers: every path whose local entry differs, and every path
// the two bases list differently. Sorted.
func changedPaths(oldBase *Manifest, oldEntries map[string]*localEntry, newBase *Manifest, newEntries map[string]*localEntry) []string {
	set := map[string]struct{}{}
	for p, entry := range oldEntries {
		if other := newEntries[p]; other == nil || *other != *entry {
			set[p] = struct{}{}
		}
	}
	for p := range newEntries {
		if oldEntries[p] == nil {
			set[p] = struct{}{}
		}
	}
	if oldBase != newBase {
		for p, entry := range oldBase.entries {
			if other, ok := newBase.entries[p]; !ok || !sameBaseEntry(&entry, &other) {
				set[p] = struct{}{}
			}
		}
		for p := range newBase.entries {
			if _, ok := oldBase.entries[p]; !ok {
				set[p] = struct{}{}
			}
		}
	}
	delete(set, "/")
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// entryForEditLocked returns the local entry that makes an edit, reusing the
// local file that holds it.
func (f *FS) entryForEditLocked(edit Edit) *localEntry {
	if edit.Op == "delete" {
		return &localEntry{Kind: entryWhiteout}
	}
	n, _ := f.lookupMetaLocked(edit.Path)
	switch {
	case n.kind == KindSymlink:
		return &localEntry{Kind: entrySymlink, Mode: ModeSymlink, Target: n.target}
	case n.local != "":
		return &localEntry{Kind: entryFile, Mode: n.mode, Local: n.local}
	}
	return &localEntry{Kind: entryRef, Mode: n.mode, Hash: n.hash, Size: n.size}
}

func (f *FS) isVisibleLocked(p string) bool {
	_, ok := f.lookupMetaLocked(p)
	return ok
}

func (f *FS) discard(names []string) {
	for _, name := range names {
		_ = f.filesRoot.Remove(name)
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
