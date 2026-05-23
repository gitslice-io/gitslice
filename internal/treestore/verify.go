package treestore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gitslice-io/gitslice/internal/objectid"
)

type VerificationReport struct {
	RootTreeID string
	TreeCount  int
	FileCount  int
	MaxDepth   int
}

func (s *Store) VerifyReachable(ctx context.Context, rootTreeID string) (VerificationReport, error) {
	if rootTreeID == "" {
		rootTreeID = EmptyRootID()
	}
	report := VerificationReport{RootTreeID: rootTreeID}
	seen := map[string]bool{}
	if err := s.verifyTree(ctx, rootTreeID, "", 0, seen, &report); err != nil {
		return report, err
	}
	return report, nil
}

func (s *Store) verifyTree(ctx context.Context, treeID, prefix string, depth int, seen map[string]bool, report *VerificationReport) error {
	if seen[treeID] {
		return nil
	}
	seen[treeID] = true
	node, err := s.readNodeStrict(ctx, treeID)
	if err != nil {
		return err
	}
	if computed := treeIDForNode(node); computed != treeID {
		return fmt.Errorf("tree %s content hash mismatch: computed %s", treeID, computed)
	}
	report.TreeCount++
	if depth > report.MaxDepth {
		report.MaxDepth = depth
	}
	names := map[string]bool{}
	for _, entry := range node.Entries {
		if entry.Name == "" || strings.Contains(entry.Name, "/") || entry.Name == "." || entry.Name == ".." {
			return fmt.Errorf("tree %s has invalid entry name %q", treeID, entry.Name)
		}
		if names[entry.Name] {
			return fmt.Errorf("tree %s has duplicate entry %q", treeID, entry.Name)
		}
		names[entry.Name] = true
		switch entry.Kind {
		case "file":
			if entry.BlobID == "" || entry.ContentHash == "" {
				return fmt.Errorf("file %s/%s has incomplete blob metadata", prefix, entry.Name)
			}
			if entry.Mode == 0 {
				return fmt.Errorf("file %s/%s has empty mode", prefix, entry.Name)
			}
			report.FileCount++
		case "directory":
			if entry.TreeID == "" {
				return fmt.Errorf("directory %s/%s has empty tree id", prefix, entry.Name)
			}
			childPrefix := strings.TrimSuffix(prefix+"/"+entry.Name, "/")
			if err := s.verifyTree(ctx, entry.TreeID, childPrefix, depth+1, seen, report); err != nil {
				return err
			}
		default:
			return fmt.Errorf("tree %s has unsupported entry kind %q", treeID, entry.Kind)
		}
	}
	return nil
}

func (s *Store) readNodeStrict(ctx context.Context, treeID string) (Node, error) {
	if treeID == "" {
		return Node{}, fmt.Errorf("tree id is required")
	}
	rc, err := s.objects.Get(ctx, Key(treeID), 0, 0)
	if err != nil {
		return Node{}, fmt.Errorf("read tree %s: %w", treeID, err)
	}
	defer rc.Close()
	var node Node
	if err := json.NewDecoder(rc).Decode(&node); err != nil {
		return Node{}, fmt.Errorf("decode tree %s: %w", treeID, err)
	}
	if node.Version != "" && node.Version != "gitslice.tree.v1" {
		return Node{}, fmt.Errorf("tree %s has unsupported version %q", treeID, node.Version)
	}
	node.Version = "gitslice.tree.v1"
	sortEntries(node.Entries)
	return node, nil
}

func treeIDForNode(node Node) string {
	sortEntries(node.Entries)
	entries := make([]objectid.TreeEntry, 0, len(node.Entries))
	for _, entry := range node.Entries {
		entries = append(entries, objectid.TreeEntry{
			Name:        entry.Name,
			Kind:        entry.Kind,
			Mode:        entry.Mode,
			TreeID:      entry.TreeID,
			BlobID:      entry.BlobID,
			Size:        entry.Size,
			ContentHash: entry.ContentHash,
		})
	}
	return objectid.TreeID(entries)
}
