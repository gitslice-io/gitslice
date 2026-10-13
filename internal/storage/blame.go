package storage

import "context"

// BlameSnapshot is the blame of one version of a file: the version the file
// had right after CommitID changed it (design/27_blame.md). Owners holds, for
// each line, the commit that last changed it. A blame of a later version is
// this snapshot carried through one diff per later change, so snapshots make
// repeat blames cost a diff or two instead of a walk through the history.
type BlameSnapshot struct {
	CommitID    string
	Path        string
	ContentHash string
	// Truncated: the history walk that produced it stopped before the file's
	// first commit, so its oldest owner may be where the walk ended.
	Truncated bool
	Owners    []string
}

// BlameStore keeps blame snapshots. They are derived data: deleting them
// costs only a slower next blame.
type BlameStore interface {
	// BlameSnapshots returns the stored snapshots of the given commits, by
	// commit id and then path.
	BlameSnapshots(ctx context.Context, commitIDs []string) (map[string]map[string]*BlameSnapshot, error)
	// PutBlameSnapshot stores a snapshot; storing one that exists is a no-op.
	PutBlameSnapshot(ctx context.Context, snapshot BlameSnapshot) error
}
