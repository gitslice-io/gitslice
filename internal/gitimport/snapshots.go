package gitimport

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/paths"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// File describes one file entry read from a Git tree during import.
type File struct {
	Path        string
	GitPath     string
	GitBlobID   string
	BlobID      string
	ContentHash string
	Mode        uint32
	Size        int64
	Data        []byte
	// Reused marks a file whose blob this import has already read and
	// uploaded (an earlier commit had it, at this path or another): it has no
	// Data, and nothing needs to upload it again.
	Reused bool
}

// Snapshot maps canonical Gitslice paths to imported file entries.
type Snapshot map[string]File

// KnownBlobs remembers, for one import, the blobs it has already read and
// uploaded, by Git blob ID. A commit that moves or copies files, or leaves
// them as they are, then neither reads nor uploads them again.
type KnownBlobs map[string]File

// SelectedCommits lists the commits to import in topological oldest-first order.
func SelectedCommits(ctx context.Context, repoDir, mode string, maxCommits int) ([]string, error) {
	if mode == "shallow" {
		head, err := Output(ctx, repoDir, "rev-parse", "HEAD")
		if err != nil {
			return nil, err
		}
		return []string{strings.TrimSpace(head)}, nil
	}
	args := []string{"rev-list", "--topo-order"}
	if maxCommits > 0 {
		args = append(args, fmt.Sprintf("--max-count=%d", maxCommits))
	}
	args = append(args, "HEAD")
	out, err := Output(ctx, repoDir, args...)
	if err != nil {
		return nil, err
	}
	var commits []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			commits = append(commits, line)
		}
	}
	for i, j := 0, len(commits)-1; i < j; i, j = i+1, j-1 {
		commits[i], commits[j] = commits[j], commits[i]
	}
	return commits, nil
}

// SnapshotMetadata reads the file tree metadata at commitID without reading blob contents.
func SnapshotMetadata(ctx context.Context, repoDir, commitID, mountPath string) (Snapshot, []string, error) {
	raw, err := OutputBytes(ctx, repoDir, "ls-tree", "-r", "-z", "--full-tree", commitID)
	if err != nil {
		return nil, nil, err
	}
	snapshot := Snapshot{}
	seenBlobIDs := map[string]struct{}{}
	blobIDs, err := ParseTreeRecords(raw, mountPath, snapshot, seenBlobIDs, nil)
	if err != nil {
		return nil, nil, err
	}
	return snapshot, blobIDs, nil
}

// ParseTreeRecords parses NUL-delimited `git ls-tree -r -z` records into snapshot.
func ParseTreeRecords(raw []byte, mountPath string, snapshot Snapshot, seenBlobIDs map[string]struct{}, blobIDs []string) ([]string, error) {
	for _, record := range bytes.Split(raw, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		meta, gitPath, ok := bytes.Cut(record, []byte{'\t'})
		if !ok {
			return nil, fmt.Errorf("malformed ls-tree record %q", string(record))
		}
		fields := strings.Fields(string(meta))
		if len(fields) < 3 || fields[1] != "blob" {
			continue
		}
		gitBlobID := fields[2]
		modeValue, err := strconv.ParseUint(fields[0], 8, 32)
		if err != nil {
			return nil, err
		}
		pathText := string(gitPath)
		globalPath, err := paths.FromWorkspacePath(mountPath, pathText)
		if err != nil {
			return nil, err
		}
		snapshot[globalPath] = File{
			Path:      globalPath,
			GitPath:   pathText,
			GitBlobID: gitBlobID,
			Mode:      uint32(modeValue),
		}
		if _, ok := seenBlobIDs[gitBlobID]; !ok {
			seenBlobIDs[gitBlobID] = struct{}{}
			blobIDs = append(blobIDs, gitBlobID)
		}
	}
	return blobIDs, nil
}

// ReadSnapshot reads all files and unseen blob payloads at commitID.
func ReadSnapshot(ctx context.Context, repoDir, commitID, mountPath string, known KnownBlobs) (Snapshot, error) {
	raw, err := OutputBytes(ctx, repoDir, "ls-tree", "-r", "-z", "--full-tree", commitID)
	if err != nil {
		return nil, err
	}
	return SnapshotFromTreeRecords(ctx, repoDir, raw, mountPath, known)
}

// SnapshotFromTreeRecords reads the files ls-tree listed. A blob in known is
// not read again; the blobs it reads are added to known.
func SnapshotFromTreeRecords(ctx context.Context, repoDir string, raw []byte, mountPath string, known KnownBlobs) (Snapshot, error) {
	snapshot := Snapshot{}
	seenBlobIDs := map[string]struct{}{}
	blobIDs, err := ParseTreeRecords(raw, mountPath, snapshot, seenBlobIDs, nil)
	if err != nil {
		return nil, err
	}
	toRead := make([]string, 0, len(blobIDs))
	for _, id := range blobIDs {
		if _, ok := known[id]; !ok {
			toRead = append(toRead, id)
		}
	}
	blobs, err := BlobContents(ctx, repoDir, toRead)
	if err != nil {
		return nil, err
	}
	for p, file := range snapshot {
		if k, ok := known[file.GitBlobID]; ok {
			file.BlobID, file.ContentHash, file.Size, file.Reused = k.BlobID, k.ContentHash, k.Size, true
			snapshot[p] = file
			continue
		}
		data, ok := blobs[file.GitBlobID]
		if !ok {
			return nil, fmt.Errorf("missing git blob %s for %s", file.GitBlobID, file.GitPath)
		}
		file.BlobID = objectid.BlobID(data)
		file.ContentHash = objectid.RawContentHash(data)
		file.Size = int64(len(data))
		file.Data = data
		snapshot[p] = file
	}
	if known != nil {
		for _, file := range snapshot {
			if !file.Reused {
				known[file.GitBlobID] = File{BlobID: file.BlobID, ContentHash: file.ContentHash, Size: file.Size}
			}
		}
	}
	return snapshot, nil
}

// DeltaMetadata computes deleted/upserted paths and tree metadata between two commits.
func DeltaMetadata(ctx context.Context, repoDir, previousCommitID, commitID, mountPath string) ([]string, []string, Snapshot, []string, error) {
	raw, err := OutputBytes(ctx, repoDir, "diff-tree", "-r", "-z", "--no-commit-id", "--name-status", "--find-renames", previousCommitID, commitID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	deleted, upsertGitPaths, err := ParseDiffNameStatus(raw)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	snapshot, blobIDs, err := FilesMetadataAtPaths(ctx, repoDir, commitID, mountPath, upsertGitPaths)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return deleted, upsertGitPaths, snapshot, blobIDs, nil
}

// DeltaEditsFromSnapshot turns delta paths and their resolved snapshot into FileEdits.
func DeltaEditsFromSnapshot(mountPath string, deleted, upsertGitPaths []string, snapshot Snapshot) ([]*corev1.FileEdit, error) {
	edits := make([]*corev1.FileEdit, 0, len(deleted)+len(snapshot))
	foundUpserts := make(map[string]struct{}, len(snapshot))
	for _, file := range snapshot {
		foundUpserts[file.GitPath] = struct{}{}
	}
	for _, gitPath := range deleted {
		globalPath, err := paths.FromWorkspacePath(mountPath, gitPath)
		if err != nil {
			return nil, err
		}
		edits = append(edits, &corev1.FileEdit{Op: "delete", Path: globalPath})
	}
	for _, gitPath := range upsertGitPaths {
		if _, ok := foundUpserts[gitPath]; ok {
			continue
		}
		globalPath, err := paths.FromWorkspacePath(mountPath, gitPath)
		if err != nil {
			return nil, err
		}
		edits = append(edits, &corev1.FileEdit{Op: "delete", Path: globalPath})
	}
	for _, file := range snapshot {
		edits = append(edits, &corev1.FileEdit{
			Op:          "upsert",
			Path:        file.Path,
			BlobId:      file.BlobID,
			ContentHash: file.ContentHash,
			Mode:        file.Mode,
		})
	}
	sort.Slice(edits, func(i, j int) bool {
		if edits[i].Path == edits[j].Path {
			return edits[i].Op < edits[j].Op
		}
		return edits[i].Path < edits[j].Path
	})
	return edits, nil
}

// DeltaEdits computes the FileEdits and snapshot of upserted files between two commits.
func DeltaEdits(ctx context.Context, repoDir, previousCommitID, commitID, mountPath string, known KnownBlobs) ([]*corev1.FileEdit, Snapshot, error) {
	raw, err := OutputBytes(ctx, repoDir, "diff-tree", "-r", "-z", "--no-commit-id", "--name-status", "--find-renames", previousCommitID, commitID)
	if err != nil {
		return nil, nil, err
	}
	deleted, upsertGitPaths, err := ParseDiffNameStatus(raw)
	if err != nil {
		return nil, nil, err
	}
	snapshot, err := FilesAtPaths(ctx, repoDir, commitID, mountPath, upsertGitPaths, known)
	if err != nil {
		return nil, nil, err
	}
	edits, err := DeltaEditsFromSnapshot(mountPath, deleted, upsertGitPaths, snapshot)
	if err != nil {
		return nil, nil, err
	}
	return edits, snapshot, nil
}

// DiffSnapshots returns the FileEdits transforming previous into current.
func DiffSnapshots(previous, current Snapshot) []*corev1.FileEdit {
	seen := map[string]struct{}{}
	for p := range previous {
		seen[p] = struct{}{}
	}
	for p := range current {
		seen[p] = struct{}{}
	}
	allPaths := make([]string, 0, len(seen))
	for p := range seen {
		allPaths = append(allPaths, p)
	}
	sort.Strings(allPaths)
	edits := make([]*corev1.FileEdit, 0, len(allPaths))
	for _, p := range allPaths {
		before, hadBefore := previous[p]
		after, hasAfter := current[p]
		if !hasAfter {
			edits = append(edits, &corev1.FileEdit{Op: "delete", Path: p})
			continue
		}
		if !hadBefore || before.ContentHash != after.ContentHash || before.Mode != after.Mode {
			edits = append(edits, &corev1.FileEdit{
				Op:          "upsert",
				Path:        p,
				BlobId:      after.BlobID,
				ContentHash: after.ContentHash,
				Mode:        after.Mode,
			})
		}
	}
	return edits
}
