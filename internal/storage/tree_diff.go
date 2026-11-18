package storage

import (
	"sort"
	"strings"
)

// DiffTreeListings compares two recursive listings of the same path
// (RepositoryStore.ListTreeAtTree) the way RepositoryStore.DiffTreeAtTree
// reports a change: changed holds the entries of next that prev lacks or that
// differ in kind, mode or content; removed holds the paths of prev that next
// lacks or whose kind changed, topmost only, so a removed directory stands for
// everything under it. Directories present on both sides are never changed.
func DiffTreeListings(prev, next []TreeEntry) (changed []TreeEntry, removed []string) {
	before := make(map[string]TreeEntry, len(prev))
	for _, entry := range prev {
		before[entry.Path] = entry
	}
	after := make(map[string]bool, len(next))
	for _, entry := range next {
		after[entry.Path] = true
		old, had := before[entry.Path]
		switch {
		case had && old.Kind == entry.Kind:
			if entry.Kind == "directory" || (old.Mode == entry.Mode && old.ContentHash == entry.ContentHash && old.BlobID == entry.BlobID && old.Size == entry.Size) {
				continue
			}
		case had:
			removed = append(removed, entry.Path)
		}
		changed = append(changed, entry)
	}
	var gone []string
	for _, entry := range prev {
		if !after[entry.Path] {
			gone = append(gone, entry.Path)
		}
	}
	removed = append(removed, gone...)
	sort.Strings(removed)
	// Keep the topmost: a path below another removed path goes with it.
	top := removed[:0]
	for _, p := range removed {
		if n := len(top); n > 0 && (top[n-1] == p || strings.HasPrefix(p, top[n-1]+"/")) {
			continue
		}
		top = append(top, p)
	}
	sort.Slice(changed, func(i, j int) bool { return changed[i].Path < changed[j].Path })
	if len(top) == 0 {
		top = nil
	}
	return changed, top
}
