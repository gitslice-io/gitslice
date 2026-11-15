package treestore

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

func (s *Store) ApplyEdits(ctx context.Context, rootTreeID string, edits []FileEdit) (string, error) {
	buf := newBufferingObjectStore(s.objects)
	buffered := &Store{objects: buf}
	newRoot, err := s.applyEditSet(ctx, buffered, rootTreeID, edits)
	if err != nil {
		return "", err
	}
	if err := buf.flush(ctx); err != nil {
		return "", err
	}
	return newRoot, nil
}

// ApplyEditChain applies each edit set in order against a single shared
// buffer (so intermediate tree nodes are served from memory, not the object
// store) and flushes all new nodes once at the end. It returns the resulting
// root tree id after each set — roots[i] is the root after applying
// editSets[0..i]. Equivalent to folding ApplyEdits over the sets, but with
// one flush instead of len(editSets).
func (s *Store) ApplyEditChain(ctx context.Context, baseRootTreeID string, editSets [][]FileEdit) ([]string, error) {
	if len(editSets) == 0 {
		return []string{}, nil
	}
	buf := newBufferingObjectStore(s.objects)
	buffered := &Store{objects: buf}
	current := baseRootTreeID
	roots := make([]string, 0, len(editSets))
	for _, edits := range editSets {
		var err error
		current, err = s.applyEditSet(ctx, buffered, current, edits)
		if err != nil {
			return nil, err
		}
		roots = append(roots, current)
	}
	if err := buf.flush(ctx); err != nil {
		return nil, err
	}
	return roots, nil
}

func (s *Store) applyEditSet(ctx context.Context, buffered *Store, current string, edits []FileEdit) (string, error) {
	if len(edits) == 0 {
		return current, nil
	}
	if canApplyEditsInBatch(edits) {
		ops := make([]batchEdit, 0, len(edits))
		for _, edit := range edits {
			parts, err := splitPath(edit.Path)
			if err != nil {
				return "", err
			}
			if len(parts) == 0 && edit.Op != "delete" {
				return "", fmt.Errorf("file path is required")
			}
			op := batchEdit{parts: parts, op: edit.Op}
			if edit.Op != "delete" && edit.Op != "mkdir" {
				if edit.File == nil {
					return "", fmt.Errorf("file edit for %s requires file metadata", edit.Path)
				}
				op.file = *edit.File
			}
			ops = append(ops, op)
		}
		newRoot, _, err := buffered.applyBatch(ctx, current, ops)
		return newRoot, err
	}
	return buffered.applyEditsSequential(ctx, current, edits)
}

func (s *Store) applyEditsSequential(ctx context.Context, rootTreeID string, edits []FileEdit) (string, error) {
	current := rootTreeID
	for _, edit := range edits {
		var err error
		switch edit.Op {
		case "mkdir":
			parts, splitErr := splitPath(edit.Path)
			if splitErr != nil {
				return "", splitErr
			}
			current, err = s.setDirectory(ctx, current, parts)
		case "delete":
			parts, splitErr := splitPath(edit.Path)
			if splitErr != nil {
				return "", splitErr
			}
			current, _, err = s.deletePath(ctx, current, parts)
		case "rename":
			oldParts, splitErr := splitPath(edit.OldPath)
			if splitErr != nil {
				return "", splitErr
			}
			entry, err := s.entryAt(ctx, current, oldParts)
			if err != nil {
				return "", err
			}
			current, _, err = s.deletePath(ctx, current, oldParts)
			if err != nil {
				return "", err
			}
			newParts, splitErr := splitPath(edit.Path)
			if splitErr != nil {
				return "", splitErr
			}
			current, err = s.setEntry(ctx, current, newParts, entry)
		case "copy":
			// A copy points the new path at the same file or tree, so it costs
			// the same at any size: trees are content-addressed and shared.
			oldParts, splitErr := splitPath(edit.OldPath)
			if splitErr != nil {
				return "", splitErr
			}
			entry, err := s.entryAt(ctx, current, oldParts)
			if err != nil {
				return "", err
			}
			newParts, splitErr := splitPath(edit.Path)
			if splitErr != nil {
				return "", splitErr
			}
			current, err = s.setEntry(ctx, current, newParts, entry)
		default:
			if edit.File == nil {
				return "", fmt.Errorf("file edit for %s requires file metadata", edit.Path)
			}
			parts, splitErr := splitPath(edit.Path)
			if splitErr != nil {
				return "", splitErr
			}
			current, err = s.setFile(ctx, current, parts, *edit.File)
		}
		if err != nil {
			return "", err
		}
	}
	return current, nil
}

type batchEdit struct {
	op    string
	parts []string
	file  FileEntry
}

func canApplyEditsInBatch(edits []FileEdit) bool {
	if len(edits) == 0 {
		return true
	}
	paths := make([]string, 0, len(edits))
	for _, edit := range edits {
		if edit.Op == "rename" || edit.Op == "copy" {
			return false
		}
		if edit.Op != "delete" && edit.Op != "mkdir" && edit.File == nil {
			return false
		}
		parts, err := splitPath(edit.Path)
		if err != nil || len(parts) == 0 {
			return true
		}
		paths = append(paths, strings.Join(parts, "/"))
	}
	sort.Strings(paths)
	for i := 1; i < len(paths); i++ {
		prev := paths[i-1]
		cur := paths[i]
		if cur == prev || strings.HasPrefix(cur, prev+"/") {
			return false
		}
	}
	return true
}

func (s *Store) applyBatch(ctx context.Context, treeID string, edits []batchEdit) (string, bool, error) {
	node, err := s.readNode(ctx, treeID)
	if err != nil {
		return "", false, err
	}
	childEdits := map[string][]batchEdit{}
	// deleted is set when a delete removed an entry here, or emptied a
	// directory below, which is then removed too.
	deleted := false
	for _, edit := range edits {
		if len(edit.parts) == 0 {
			continue
		}
		name := edit.parts[0]
		if len(edit.parts) == 1 {
			switch edit.op {
			case "delete":
				if _, exists := findEntry(node.Entries, name); exists {
					node.Entries = removeEntry(node.Entries, name)
					deleted = true
				}
			case "mkdir":
				existing, ok := findEntry(node.Entries, name)
				if ok && existing.Kind != "directory" {
					return "", false, fmt.Errorf("%s is not a directory", name)
				}
				if !ok {
					node.Entries = upsertEntry(node.Entries, Entry{Name: name, Kind: "directory", TreeID: EmptyRootID()})
				}
			default:
				node.Entries = upsertEntry(node.Entries, Entry{
					Name:        name,
					Kind:        "file",
					Mode:        edit.file.Mode,
					BlobID:      edit.file.BlobID,
					ContentHash: edit.file.ContentHash,
					Size:        edit.file.Size,
				})
			}
			continue
		}
		childEdits[name] = append(childEdits[name], batchEdit{
			op:    edit.op,
			parts: edit.parts[1:],
			file:  edit.file,
		})
	}
	for name, edits := range childEdits {
		child, ok := findEntry(node.Entries, name)
		childTreeID := EmptyRootID()
		onlyDeletes := true
		for _, edit := range edits {
			if edit.op != "delete" {
				onlyDeletes = false
				break
			}
		}
		if ok {
			if child.Kind != "directory" {
				if onlyDeletes {
					continue
				}
				return "", false, fmt.Errorf("%s is not a directory", name)
			}
			childTreeID = child.TreeID
		} else if onlyDeletes {
			// Nothing to delete below a directory that is not there.
			continue
		}
		newChildTreeID, childEmptied, err := s.applyBatch(ctx, childTreeID, edits)
		if err != nil {
			return "", false, err
		}
		if childEmptied {
			node.Entries = removeEntry(node.Entries, name)
			deleted = true
			continue
		}
		node.Entries = upsertEntry(node.Entries, Entry{
			Name:   name,
			Kind:   "directory",
			TreeID: newChildTreeID,
		})
	}
	newTreeID, err := s.writeNode(ctx, node)
	if err != nil {
		return "", false, err
	}
	return newTreeID, deleted && len(node.Entries) == 0, nil
}
