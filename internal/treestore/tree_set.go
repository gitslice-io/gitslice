package treestore

import (
	"context"
	"fmt"
)

func (s *Store) setFile(ctx context.Context, treeID string, parts []string, file FileEntry) (string, error) {
	if len(parts) == 0 {
		return "", fmt.Errorf("file path is required")
	}
	node, err := s.readNode(ctx, treeID)
	if err != nil {
		return "", err
	}
	if len(parts) == 1 {
		node.Entries = upsertEntry(node.Entries, Entry{
			Name:        parts[0],
			Kind:        "file",
			Mode:        file.Mode,
			BlobID:      file.BlobID,
			ContentHash: file.ContentHash,
			Size:        file.Size,
		})
		return s.writeNode(ctx, node)
	}
	child, ok := findEntry(node.Entries, parts[0])
	if ok && child.Kind != "directory" {
		return "", fmt.Errorf("%s is not a directory", parts[0])
	}
	childTreeID := EmptyRootID()
	if ok {
		childTreeID = child.TreeID
	}
	newChildTreeID, err := s.setFile(ctx, childTreeID, parts[1:], file)
	if err != nil {
		return "", err
	}
	node.Entries = upsertEntry(node.Entries, Entry{
		Name:   parts[0],
		Kind:   "directory",
		TreeID: newChildTreeID,
	})
	return s.writeNode(ctx, node)
}

func (s *Store) setDirectory(ctx context.Context, treeID string, parts []string) (string, error) {
	if len(parts) == 0 {
		return "", fmt.Errorf("directory path is required")
	}
	node, err := s.readNode(ctx, treeID)
	if err != nil {
		return "", err
	}
	if len(parts) == 1 {
		existing, ok := findEntry(node.Entries, parts[0])
		if ok {
			if existing.Kind != "directory" {
				return "", fmt.Errorf("%s is not a directory", parts[0])
			}
			return treeID, nil
		}
		node.Entries = upsertEntry(node.Entries, Entry{Name: parts[0], Kind: "directory", TreeID: EmptyRootID()})
		return s.writeNode(ctx, node)
	}
	child, ok := findEntry(node.Entries, parts[0])
	if ok && child.Kind != "directory" {
		return "", fmt.Errorf("%s is not a directory", parts[0])
	}
	childTreeID := EmptyRootID()
	if ok {
		childTreeID = child.TreeID
	}
	newChildTreeID, err := s.setDirectory(ctx, childTreeID, parts[1:])
	if err != nil {
		return "", err
	}
	node.Entries = upsertEntry(node.Entries, Entry{Name: parts[0], Kind: "directory", TreeID: newChildTreeID})
	return s.writeNode(ctx, node)
}

func (s *Store) setEntry(ctx context.Context, treeID string, parts []string, entry Entry) (string, error) {
	if len(parts) == 0 {
		return "", fmt.Errorf("path is required")
	}
	node, err := s.readNode(ctx, treeID)
	if err != nil {
		return "", err
	}
	if len(parts) == 1 {
		entry.Name = parts[0]
		node.Entries = upsertEntry(node.Entries, entry)
		return s.writeNode(ctx, node)
	}
	child, ok := findEntry(node.Entries, parts[0])
	if ok && child.Kind != "directory" {
		return "", fmt.Errorf("%s is not a directory", parts[0])
	}
	childTreeID := EmptyRootID()
	if ok {
		childTreeID = child.TreeID
	}
	newChildTreeID, err := s.setEntry(ctx, childTreeID, parts[1:], entry)
	if err != nil {
		return "", err
	}
	node.Entries = upsertEntry(node.Entries, Entry{Name: parts[0], Kind: "directory", TreeID: newChildTreeID})
	return s.writeNode(ctx, node)
}

// deletePath removes the entry at parts and reports whether that left the
// node empty. A directory emptied by a delete is removed from its parent, as
// Git has no empty directories, and the removal can empty the parent in turn.
// A delete that removes nothing never empties a node, so an empty directory
// made on purpose (mkdir) stays until something under it is deleted.
func (s *Store) deletePath(ctx context.Context, treeID string, parts []string) (string, bool, error) {
	if len(parts) == 0 {
		return treeID, false, nil
	}
	node, err := s.readNode(ctx, treeID)
	if err != nil {
		return "", false, err
	}
	entry, ok := findEntry(node.Entries, parts[0])
	if !ok {
		return treeID, false, nil
	}
	if len(parts) == 1 {
		node.Entries = removeEntry(node.Entries, parts[0])
		newTreeID, err := s.writeNode(ctx, node)
		return newTreeID, len(node.Entries) == 0, err
	}
	if entry.Kind != "directory" {
		return treeID, false, nil
	}
	newChildTreeID, childEmptied, err := s.deletePath(ctx, entry.TreeID, parts[1:])
	if err != nil {
		return "", false, err
	}
	if childEmptied {
		node.Entries = removeEntry(node.Entries, parts[0])
	} else {
		entry.TreeID = newChildTreeID
		node.Entries = upsertEntry(node.Entries, entry)
	}
	newTreeID, err := s.writeNode(ctx, node)
	return newTreeID, childEmptied && len(node.Entries) == 0, err
}
