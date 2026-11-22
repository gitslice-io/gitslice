package treestore

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"sync"

	"golang.org/x/sync/errgroup"
)

const (
	treeFlushConcurrency = 64
	treePackThreshold    = 8
)

type packWriter interface {
	PutPack(ctx context.Context, packKey string, objects map[string][]byte) error
}

func randomTreePackKey() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "packs/trees/tpk_" + hex.EncodeToString(raw[:]) + ".pack", nil
}

type bufferingObjectStore struct {
	base      ObjectStore
	mu        sync.Mutex
	writes    map[string][]byte
	writtenID map[string]string // object key -> treeID
}

func newBufferingObjectStore(base ObjectStore) *bufferingObjectStore {
	return &bufferingObjectStore{
		base:      base,
		writes:    map[string][]byte{},
		writtenID: map[string]string{},
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

func (b *bufferingObjectStore) recordTreeWrite(treeID string) {
	b.mu.Lock()
	b.writtenID[Key(treeID)] = treeID
	b.mu.Unlock()
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

// pruneToRoots discards buffered intermediate tree nodes that are no longer
// reachable from any of roots, avoiding orphan uploads during sequential edits
// (such as rename, copy, or multi-step chains).
func (b *bufferingObjectStore) pruneToRoots(roots []string, staged map[string]Node) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.writes) == 0 {
		return
	}

	reachableKeys := make(map[string]struct{}, len(b.writes))
	visitedIDs := make(map[string]struct{}, len(b.writes))

	var walk func(treeID string)
	walk = func(treeID string) {
		if treeID == "" {
			return
		}
		if _, seen := visitedIDs[treeID]; seen {
			return
		}
		key := Key(treeID)
		data, pending := b.writes[key]
		if !pending {
			return
		}
		visitedIDs[treeID] = struct{}{}
		reachableKeys[key] = struct{}{}

		if staged != nil {
			if node, ok := staged[treeID]; ok {
				for _, entry := range node.Entries {
					if entry.Kind == "directory" {
						walk(entry.TreeID)
					}
				}
				return
			}
		}
		var node Node
		if err := json.Unmarshal(data, &node); err == nil {
			for _, entry := range node.Entries {
				if entry.Kind == "directory" {
					walk(entry.TreeID)
				}
			}
		}
	}

	for _, root := range roots {
		walk(root)
	}
	for key := range b.writes {
		if _, ok := reachableKeys[key]; !ok {
			delete(b.writes, key)
			if staged != nil {
				if id, hasID := b.writtenID[key]; hasID {
					delete(staged, id)
				}
			}
			delete(b.writtenID, key)
		}
	}
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

	if len(writes) >= treePackThreshold {
		if pw, ok := b.base.(packWriter); ok {
			packKey, err := randomTreePackKey()
			if err != nil {
				return err
			}
			if err := pw.PutPack(ctx, packKey, writes); err != nil {
				return err
			}
			b.mu.Lock()
			b.writes = map[string][]byte{}
			b.writtenID = map[string]string{}
			b.mu.Unlock()
			return nil
		}
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
	b.writtenID = map[string]string{}
	b.mu.Unlock()
	return nil
}
