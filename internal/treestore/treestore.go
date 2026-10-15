package treestore

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"

	"gitslice.io/gitslice/internal/objectid"
)

var ErrNotFound = errors.New("not found")

type ObjectStore interface {
	Put(context.Context, string, io.Reader) error
	Get(context.Context, string, int64, int64) (io.ReadCloser, error)
}

type Store struct {
	objects ObjectStore
	// nodes keeps decoded nodes for readers; nil in the buffered stores
	// that build a tree.
	nodes *nodeCache
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
	return &Store{objects: objects, nodes: newNodeCache(nodeCacheEntries)}
}

// NewUncached is New without the decoded-node cache, for callers that must
// see every node read (tests that count them).
func NewUncached(objects ObjectStore) *Store {
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
