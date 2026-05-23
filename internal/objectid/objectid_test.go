package objectid

import (
	"testing"
	"time"
)

func TestTreeIDCanonicalizesEmptyEntries(t *testing.T) {
	if TreeID(nil) != TreeID([]TreeEntry{}) {
		t.Fatal("nil and empty tree entries produced different tree ids")
	}
}

func TestCommitIDCanonicalizesEmptySlices(t *testing.T) {
	createdAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	withNil := CommitObject{
		RootTreeID: EmptyTreeID(),
		Author:     "system",
		Message:    "Initial empty tree",
		CreatedAt:  createdAt,
	}
	withEmpty := CommitObject{
		ParentIDs:    []string{},
		RootTreeID:   EmptyTreeID(),
		Author:       "system",
		Message:      "Initial empty tree",
		CreatedAt:    createdAt,
		ChangedPaths: []string{},
	}
	if CommitID(withNil) != CommitID(withEmpty) {
		t.Fatal("nil and empty commit slices produced different commit ids")
	}
}
