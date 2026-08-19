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
	"sync"

	"github.com/gitslice-io/gitslice/internal/objectid"
	"golang.org/x/sync/errgroup"
)

var ErrNotFound = errors.New("not found")

type ObjectStore interface {
	Put(context.Context, string, io.Reader) error
	Get(context.Context, string, int64, int64) (io.ReadCloser, error)
}

type Store struct {
	objects ObjectStore
}

const treeFlushConcurrency = 16

type bufferingObjectStore struct {
	base   ObjectStore
	mu     sync.Mutex
	writes map[string][]byte
}

func newBufferingObjectStore(base ObjectStore) *bufferingObjectStore {
	return &bufferingObjectStore{
		base:   base,
		writes: map[string][]byte{},
	}
}

func (b *bufferingObjectStore) Put(_ context.Context, key string, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.writes[key] = data
	return nil
}

func (b *bufferingObjectStore) Get(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	b.mu.Lock()
	data, ok := b.writes[key]
	b.mu.Unlock()
	if !ok {
		return b.base.Get(ctx, key, offset, length)
	}
	start := offset
	if start < 0 {
		start = 0
	}
	if start > int64(len(data)) {
		start = int64(len(data))
	}
	end := int64(len(data))
	if length > 0 && start+length < end {
		end = start + length
	}
	return io.NopCloser(bytes.NewReader(data[int(start):int(end)])), nil
}

func (b *bufferingObjectStore) flush(ctx context.Context) error {
	b.mu.Lock()
	writes := make(map[string][]byte, len(b.writes))
	for key, data := range b.writes {
		writes[key] = data
	}
	b.mu.Unlock()
	if len(writes) == 0 {
		return nil
	}

	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(treeFlushConcurrency)
	for key, data := range writes {
		key, data := key, data
		group.Go(func() error {
			return b.base.Put(groupCtx, key, bytes.NewReader(data))
		})
	}
	if err := group.Wait(); err != nil {
		return err
	}
	b.mu.Lock()
	b.writes = map[string][]byte{}
	b.mu.Unlock()
	return nil
}

type FileEntry struct {
	Path        string
	BlobID      string
	ContentHash string
	Mode        uint32
	Size        int64
}

type TreeEntry struct {
	Path        string
	Name        string
	Kind        string
	Mode        uint32
	TreeID      string
	BlobID      string
	ContentHash string
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
	node, err := s.readNode(ctx, treeID)
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
		if edit.Op == "rename" {
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
	for _, edit := range edits {
		if len(edit.parts) == 0 {
			continue
		}
		name := edit.parts[0]
		if len(edit.parts) == 1 {
			switch edit.op {
			case "delete":
				node.Entries = removeEntry(node.Entries, name)
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
		if ok {
			if child.Kind != "directory" {
				onlyDeletes := true
				for _, edit := range edits {
					if edit.op != "delete" {
						onlyDeletes = false
						break
					}
				}
				if onlyDeletes {
					continue
				}
				return "", false, fmt.Errorf("%s is not a directory", name)
			}
			childTreeID = child.TreeID
		}
		newChildTreeID, childEmpty, err := s.applyBatch(ctx, childTreeID, edits)
		if err != nil {
			return "", false, err
		}
		if childEmpty {
			node.Entries = upsertEntry(node.Entries, Entry{
				Name:   name,
				Kind:   "directory",
				TreeID: newChildTreeID,
			})
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
	return newTreeID, len(node.Entries) == 0, nil
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
		entry.TreeID = newChildTreeID
		node.Entries = upsertEntry(node.Entries, entry)
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
	if treeID == EmptyRootID() {
		return Node{Version: "gitslice.tree.v1"}, nil
	}
	rc, err := s.objects.Get(ctx, Key(treeID), 0, 0)
	if err != nil {
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
