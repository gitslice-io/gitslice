package treestore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gitslice-io/gitslice/internal/objectid"
)

var ErrNotFound = errors.New("not found")

type ObjectStore interface {
	Put(context.Context, string, io.Reader) error
	Get(context.Context, string, int64, int64) (io.ReadCloser, error)
}

type Store struct {
	objects ObjectStore
}

type FileEntry struct {
	Path        string
	BlobID      string
	ContentHash string
	Mode        uint32
	Size        int64
}

type FileEdit struct {
	Op      string
	Path    string
	OldPath string
	File    *FileEntry
}

type Node struct {
	Version string  `json:"version"`
	Entries []Entry `json:"entries"`
}

type Entry struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Mode        uint32 `json:"mode,omitempty"`
	TreeID      string `json:"tree_id,omitempty"`
	BlobID      string `json:"blob_id,omitempty"`
	ContentHash string `json:"content_hash,omitempty"`
	Size        int64  `json:"size,omitempty"`
}

func New(objects ObjectStore) *Store {
	return &Store{objects: objects}
}

func Key(treeID string) string {
	hash := strings.TrimPrefix(treeID, "sha256:")
	if len(hash) >= 4 {
		return filepath.ToSlash(filepath.Join("trees", "sha256", hash[:2], hash[2:4], hash+".json"))
	}
	return filepath.ToSlash(filepath.Join("trees", "sha256", hash+".json"))
}

func EmptyRootID() string {
	return objectid.EmptyTreeID()
}

func (s *Store) EnsureEmptyRoot(ctx context.Context) error {
	_, err := s.writeNode(ctx, Node{Version: "gitslice.tree.v1"})
	return err
}

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

func (s *Store) ApplyEdits(ctx context.Context, rootTreeID string, edits []FileEdit) (string, error) {
	current := rootTreeID
	for _, edit := range edits {
		var err error
		switch edit.Op {
		case "delete":
			parts, splitErr := splitPath(edit.Path)
			if splitErr != nil {
				return "", splitErr
			}
			current, _, err = s.deletePath(ctx, current, parts)
		case "rename":
			file, err := s.GetFile(ctx, current, edit.OldPath)
			if err != nil {
				return "", err
			}
			oldParts, splitErr := splitPath(edit.OldPath)
			if splitErr != nil {
				return "", splitErr
			}
			current, _, err = s.deletePath(ctx, current, oldParts)
			if err != nil {
				return "", err
			}
			file.Path = edit.Path
			newParts, splitErr := splitPath(edit.Path)
			if splitErr != nil {
				return "", splitErr
			}
			current, err = s.setFile(ctx, current, newParts, *file)
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

func (s *Store) entryAt(ctx context.Context, treeID string, parts []string) (Entry, error) {
	node, err := s.readNode(ctx, treeID)
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
	node, err := s.readNode(ctx, treeID)
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
		return treeID, len(node.Entries) == 0, nil
	}
	if len(parts) == 1 {
		node.Entries = removeEntry(node.Entries, parts[0])
		newTreeID, err := s.writeNode(ctx, node)
		return newTreeID, len(node.Entries) == 0, err
	}
	if entry.Kind != "directory" {
		return treeID, len(node.Entries) == 0, nil
	}
	newChildTreeID, childEmpty, err := s.deletePath(ctx, entry.TreeID, parts[1:])
	if err != nil {
		return "", false, err
	}
	if childEmpty {
		node.Entries = removeEntry(node.Entries, parts[0])
	} else {
		entry.TreeID = newChildTreeID
		node.Entries = upsertEntry(node.Entries, entry)
	}
	newTreeID, err := s.writeNode(ctx, node)
	return newTreeID, len(node.Entries) == 0, err
}

func (s *Store) readNode(ctx context.Context, treeID string) (Node, error) {
	if treeID == "" {
		treeID = EmptyRootID()
	}
	rc, err := s.objects.Get(ctx, Key(treeID), 0, 0)
	if err != nil {
		if treeID == EmptyRootID() {
			return Node{Version: "gitslice.tree.v1"}, nil
		}
		return Node{}, err
	}
	defer rc.Close()
	var node Node
	if err := json.NewDecoder(rc).Decode(&node); err != nil {
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
