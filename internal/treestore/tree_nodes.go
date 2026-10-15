package treestore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"gitslice.io/gitslice/internal/objectid"
)

// readNode returns a node its caller may change: writers edit the entries
// of the node they read to build its replacement.
func (s *Store) readNode(ctx context.Context, treeID string) (Node, error) {
	node, err := s.readSharedNode(ctx, treeID)
	if err != nil || s.nodes == nil {
		return node, err
	}
	node.Entries = append([]Entry(nil), node.Entries...)
	return node, nil
}

// readSharedNode returns a node that may be shared with other readers, so its
// caller must not change it. A path lookup reads a node from the cache
// instead of decoding it again: decoding a directory for every file in it
// made reading many files of one directory quadratic.
func (s *Store) readSharedNode(ctx context.Context, treeID string) (Node, error) {
	if treeID == "" {
		treeID = EmptyRootID()
	}
	if s.nodes != nil {
		if node, ok := s.nodes.get(treeID); ok {
			return node, nil
		}
	}
	node, err := s.decodeNode(ctx, treeID)
	if err != nil {
		return Node{}, err
	}
	if s.nodes != nil {
		s.nodes.add(treeID, node)
	}
	return node, nil
}

func (s *Store) decodeNode(ctx context.Context, treeID string) (Node, error) {
	if treeID == EmptyRootID() {
		return Node{Version: "gitslice.tree.v1"}, nil
	}
	rc, err := s.objects.Get(ctx, Key(treeID), 0, 0)
	if err != nil {
		return Node{}, err
	}
	defer rc.Close()
	// Read to the end before decoding: the object cache keeps an object only
	// once it has been read to EOF, which a JSON decoder stops short of.
	data, err := io.ReadAll(rc)
	if err != nil {
		return Node{}, err
	}
	var node Node
	if err := json.Unmarshal(data, &node); err != nil {
		return Node{}, err
	}
	if node.Version == "" {
		node.Version = "gitslice.tree.v1"
	}
	sortEntries(node.Entries)
	return node, nil
}

func (s *Store) writeNode(ctx context.Context, node Node) (string, error) {
	node.Version = "gitslice.tree.v1"
	sortEntries(node.Entries)
	var idEntries []objectid.TreeEntry
	if len(node.Entries) > 0 {
		idEntries = make([]objectid.TreeEntry, 0, len(node.Entries))
	}
	for _, entry := range node.Entries {
		idEntries = append(idEntries, objectid.TreeEntry{
			Name:        entry.Name,
			Kind:        entry.Kind,
			Mode:        entry.Mode,
			TreeID:      entry.TreeID,
			BlobID:      entry.BlobID,
			Size:        entry.Size,
			ContentHash: entry.ContentHash,
		})
	}
	treeID := objectid.TreeID(idEntries)
	payload, err := json.Marshal(node)
	if err != nil {
		return "", err
	}
	if err := s.objects.Put(ctx, Key(treeID), bytes.NewReader(payload)); err != nil {
		return "", err
	}
	return treeID, nil
}

func splitPath(p string) ([]string, error) {
	p = strings.TrimSpace(p)
	if p == "" || p == "/" {
		return nil, nil
	}
	if !strings.HasPrefix(p, "/") {
		return nil, fmt.Errorf("path must be absolute: %s", p)
	}
	cleaned := filepath.ToSlash(filepath.Clean(p))
	if cleaned == "/" {
		return nil, nil
	}
	trimmed := strings.TrimPrefix(cleaned, "/")
	if strings.HasPrefix(trimmed, "../") || trimmed == ".." {
		return nil, fmt.Errorf("invalid path: %s", p)
	}
	return strings.Split(trimmed, "/"), nil
}

func findEntry(entries []Entry, name string) (Entry, bool) {
	for _, entry := range entries {
		if entry.Name == name {
			return entry, true
		}
	}
	return Entry{}, false
}

func upsertEntry(entries []Entry, entry Entry) []Entry {
	for i := range entries {
		if entries[i].Name == entry.Name {
			entries[i] = entry
			sortEntries(entries)
			return entries
		}
	}
	entries = append(entries, entry)
	sortEntries(entries)
	return entries
}

func removeEntry(entries []Entry, name string) []Entry {
	out := entries[:0]
	for _, entry := range entries {
		if entry.Name != name {
			out = append(out, entry)
		}
	}
	return out
}

func sortEntries(entries []Entry) {
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
}

func treeEntryFromNodeEntry(fullPath string, entry Entry) TreeEntry {
	return TreeEntry{
		Path:        fullPath,
		Name:        entry.Name,
		Kind:        entry.Kind,
		Mode:        entry.Mode,
		TreeID:      entry.TreeID,
		BlobID:      entry.BlobID,
		ContentHash: entry.ContentHash,
		Size:        entry.Size,
	}
}
