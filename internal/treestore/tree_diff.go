package treestore

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"
)

// DiffTree returns what changed under p from oldRootTreeID to newRootTreeID,
// as ListTree would list it: changed holds every entry of the new listing
// that the old one lacks or differs in (kind, mode, content), and removed the
// paths of the old listing the new one lacks, topmost only (a removed
// directory stands for everything under it). A path whose kind changed is in
// both. Directories whose tree id is the same on both sides are not read, so
// the cost follows the change, not the size of the tree.
//
// ok is false when p is not a directory on both sides (missing, or a file);
// the caller then compares the two listings itself.
func (s *Store) DiffTree(ctx context.Context, oldRootTreeID, newRootTreeID, p string) (changed []TreeEntry, removed []string, ok bool, err error) {
	parts, err := splitPath(p)
	if err != nil {
		return nil, nil, false, err
	}
	type pair struct{ oldID, newID, path string }
	start := pair{oldID: oldRootTreeID, newID: newRootTreeID}
	if len(parts) > 0 {
		oldEntry, oldErr := s.entryAt(ctx, oldRootTreeID, parts)
		newEntry, newErr := s.entryAt(ctx, newRootTreeID, parts)
		for _, e := range []error{oldErr, newErr} {
			if e != nil && !errors.Is(e, ErrNotFound) {
				return nil, nil, false, e
			}
		}
		if oldErr != nil || newErr != nil || oldEntry.Kind != "directory" || newEntry.Kind != "directory" {
			return nil, nil, false, nil
		}
		start = pair{oldID: oldEntry.TreeID, newID: newEntry.TreeID, path: "/" + strings.Join(parts, "/")}
	}
	var mu sync.Mutex
	level := []pair{start}
	for len(level) > 0 {
		var next []pair
		group, groupCtx := errgroup.WithContext(ctx)
		group.SetLimit(treeListConcurrency)
		for _, d := range level {
			if d.oldID == d.newID {
				continue
			}
			group.Go(func() error {
				var oldEntries []Entry
				if d.oldID != "" {
					node, err := s.readSharedNode(groupCtx, d.oldID)
					if err != nil {
						return err
					}
					oldEntries = node.Entries
				}
				node, err := s.readSharedNode(groupCtx, d.newID)
				if err != nil {
					return err
				}
				var dirChanged []TreeEntry
				var dirRemoved []string
				var children []pair
				oldByName := make(map[string]Entry, len(oldEntries))
				for _, entry := range oldEntries {
					oldByName[entry.Name] = entry
				}
				for _, entry := range node.Entries {
					childPath := d.path + "/" + entry.Name
					old, had := oldByName[entry.Name]
					delete(oldByName, entry.Name)
					switch {
					case had && old.Kind == "directory" && entry.Kind == "directory":
						if old.TreeID != entry.TreeID {
							children = append(children, pair{oldID: old.TreeID, newID: entry.TreeID, path: childPath})
						}
						continue
					case had && old.Kind == entry.Kind:
						if old.Mode == entry.Mode && old.ContentHash == entry.ContentHash && old.BlobID == entry.BlobID && old.Size == entry.Size {
							continue
						}
					case had:
						dirRemoved = append(dirRemoved, childPath)
					}
					dirChanged = append(dirChanged, treeEntryFromNodeEntry(childPath, entry))
					if entry.Kind == "directory" {
						children = append(children, pair{newID: entry.TreeID, path: childPath})
					}
				}
				for name := range oldByName {
					dirRemoved = append(dirRemoved, d.path+"/"+name)
				}
				mu.Lock()
				changed = append(changed, dirChanged...)
				removed = append(removed, dirRemoved...)
				next = append(next, children...)
				mu.Unlock()
				return nil
			})
		}
		if err := group.Wait(); err != nil {
			return nil, nil, false, err
		}
		level = next
	}
	sort.Slice(changed, func(i, j int) bool { return changed[i].Path < changed[j].Path })
	sort.Strings(removed)
	return changed, removed, true, nil
}
