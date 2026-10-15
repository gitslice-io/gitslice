package treestore

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"
)

func (s *Store) GetFile(ctx context.Context, rootTreeID, p string) (*FileEntry, error) {
	parts, err := splitPath(p)
	if err != nil {
		return nil, err
	}
	if len(parts) == 0 {
		return nil, ErrNotFound
	}
	entry, err := s.entryAt(ctx, rootTreeID, parts)
	if err != nil {
		return nil, err
	}
	if entry.Kind != "file" {
		return nil, ErrNotFound
	}
	return &FileEntry{
		Path:        "/" + strings.Join(parts, "/"),
		BlobID:      entry.BlobID,
		ContentHash: entry.ContentHash,
		Mode:        entry.Mode,
		Size:        entry.Size,
	}, nil
}

func (s *Store) GetEntry(ctx context.Context, rootTreeID, p string) (*TreeEntry, error) {
	parts, err := splitPath(p)
	if err != nil {
		return nil, err
	}
	if len(parts) == 0 {
		return &TreeEntry{Path: "/", Name: "/", Kind: "directory", TreeID: rootTreeID}, nil
	}
	entry, err := s.entryAt(ctx, rootTreeID, parts)
	if err != nil {
		return nil, err
	}
	fullPath := "/" + strings.Join(parts, "/")
	out := treeEntryFromNodeEntry(fullPath, entry)
	return &out, nil
}

func (s *Store) ListDirectory(ctx context.Context, rootTreeID, p string) ([]TreeEntry, error) {
	parts, err := splitPath(p)
	if err != nil {
		return nil, err
	}
	treeID := rootTreeID
	prefix := "/"
	if len(parts) > 0 {
		entry, err := s.entryAt(ctx, rootTreeID, parts)
		if errors.Is(err, ErrNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		fullPath := "/" + strings.Join(parts, "/")
		if entry.Kind == "file" {
			return []TreeEntry{treeEntryFromNodeEntry(fullPath, entry)}, nil
		}
		if entry.Kind != "directory" {
			return nil, nil
		}
		treeID = entry.TreeID
		prefix = fullPath
	}
	node, err := s.readSharedNode(ctx, treeID)
	if err != nil {
		return nil, err
	}
	out := make([]TreeEntry, 0, len(node.Entries))
	for _, entry := range node.Entries {
		childPath := strings.TrimRight(prefix, "/") + "/" + entry.Name
		if prefix == "/" {
			childPath = "/" + entry.Name
		}
		out = append(out, treeEntryFromNodeEntry(childPath, entry))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

const treeListConcurrency = 16

// ListTree lists everything under p, files and directories, sorted by path.
// It reads the tree a level at a time, each level's directories
// concurrently, so a cold listing costs about the tree's depth in object
// reads rather than its number of directories.
func (s *Store) ListTree(ctx context.Context, rootTreeID, p string) ([]TreeEntry, error) {
	parts, err := splitPath(p)
	if err != nil {
		return nil, err
	}
	type dir struct{ treeID, path string }
	level := []dir{{treeID: rootTreeID, path: ""}}
	if len(parts) > 0 {
		entry, err := s.entryAt(ctx, rootTreeID, parts)
		if errors.Is(err, ErrNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		fullPath := "/" + strings.Join(parts, "/")
		switch entry.Kind {
		case "file":
			return []TreeEntry{treeEntryFromNodeEntry(fullPath, entry)}, nil
		case "directory":
			level = []dir{{treeID: entry.TreeID, path: fullPath}}
		default:
			return nil, nil
		}
	}
	var mu sync.Mutex
	var out []TreeEntry
	for len(level) > 0 {
		var next []dir
		group, groupCtx := errgroup.WithContext(ctx)
		group.SetLimit(treeListConcurrency)
		for _, d := range level {
			group.Go(func() error {
				node, err := s.readSharedNode(groupCtx, d.treeID)
				if err != nil {
					return err
				}
				entries := make([]TreeEntry, 0, len(node.Entries))
				var children []dir
				for _, entry := range node.Entries {
					childPath := d.path + "/" + entry.Name
					entries = append(entries, treeEntryFromNodeEntry(childPath, entry))
					if entry.Kind == "directory" {
						children = append(children, dir{treeID: entry.TreeID, path: childPath})
					}
				}
				mu.Lock()
				out = append(out, entries...)
				next = append(next, children...)
				mu.Unlock()
				return nil
			})
		}
		if err := group.Wait(); err != nil {
			return nil, err
		}
		level = next
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func (s *Store) ListFiles(ctx context.Context, rootTreeID, prefix string) ([]FileEntry, error) {
	parts, err := splitPath(prefix)
	if err != nil {
		return nil, err
	}
	if len(parts) == 0 {
		return s.collectFiles(ctx, rootTreeID, "")
	}
	entry, err := s.entryAt(ctx, rootTreeID, parts)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	fullPath := "/" + strings.Join(parts, "/")
	switch entry.Kind {
	case "file":
		return []FileEntry{{
			Path:        fullPath,
			BlobID:      entry.BlobID,
			ContentHash: entry.ContentHash,
			Mode:        entry.Mode,
			Size:        entry.Size,
		}}, nil
	case "directory":
		return s.collectFiles(ctx, entry.TreeID, fullPath)
	default:
		return nil, nil
	}
}

func (s *Store) entryAt(ctx context.Context, treeID string, parts []string) (Entry, error) {
	node, err := s.readSharedNode(ctx, treeID)
	if err != nil {
		return Entry{}, err
	}
	entry, ok := findEntry(node.Entries, parts[0])
	if !ok {
		return Entry{}, ErrNotFound
	}
	if len(parts) == 1 {
		return entry, nil
	}
	if entry.Kind != "directory" {
		return Entry{}, ErrNotFound
	}
	return s.entryAt(ctx, entry.TreeID, parts[1:])
}

func (s *Store) collectFiles(ctx context.Context, treeID, prefix string) ([]FileEntry, error) {
	node, err := s.readSharedNode(ctx, treeID)
	if err != nil {
		return nil, err
	}
	var out []FileEntry
	for _, entry := range node.Entries {
		p := prefix + "/" + entry.Name
		if prefix == "" {
			p = "/" + entry.Name
		}
		switch entry.Kind {
		case "file":
			out = append(out, FileEntry{
				Path:        p,
				BlobID:      entry.BlobID,
				ContentHash: entry.ContentHash,
				Mode:        entry.Mode,
				Size:        entry.Size,
			})
		case "directory":
			children, err := s.collectFiles(ctx, entry.TreeID, p)
			if err != nil {
				return nil, err
			}
			out = append(out, children...)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}
