package service

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"

	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/paths"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

type importFile struct {
	Path        string
	GitPath     string
	GitBlobID   string
	BlobID      string
	ContentHash string
	Mode        uint32
	Size        int64
	Data        []byte
}

type importSnapshot map[string]importFile

func cloneForImport(ctx context.Context, source, mode string, maxCommits int, local bool) (string, func(), error) {
	parent, err := os.MkdirTemp("", "gitslice-import-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(parent) }
	repoDir := path.Join(parent, "repo")
	args := []string{"clone", "--quiet"}
	// Remote clones are pinned to the git transport (no local optimization) and
	// the https protocol so a source that slipped past validation still cannot
	// reach file/ext/ssh. A local clone is an explicit operator opt-in, so it
	// keeps git's local-path handling and allows only the file protocol.
	allowProtocol := "https"
	if local {
		allowProtocol = "file"
	} else {
		args = append(args, "--no-local")
	}
	if mode == "shallow" {
		args = append(args, "--depth", "1")
	} else if maxCommits > 0 {
		args = append(args, "--depth", strconv.Itoa(maxCommits))
	}
	args = append(args, source, repoDir)
	if err := runGitImport(ctx, "", allowProtocol, args...); err != nil {
		cleanup()
		return "", nil, err
	}
	return repoDir, cleanup, nil
}

func selectedGitCommits(ctx context.Context, repoDir, mode string, maxCommits int) ([]string, error) {
	if mode == "shallow" {
		head, err := gitOutputImport(ctx, repoDir, "rev-parse", "HEAD")
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
	out, err := gitOutputImport(ctx, repoDir, args...)
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

func gitSnapshot(ctx context.Context, repoDir, commitID, mountPath string) (importSnapshot, error) {
	raw, err := gitOutputBytesImport(ctx, repoDir, "ls-tree", "-r", "-z", "--full-tree", commitID)
	if err != nil {
		return nil, err
	}
	return gitSnapshotFromTreeRecords(ctx, repoDir, raw, mountPath)
}

func gitSnapshotFromTreeRecords(ctx context.Context, repoDir string, raw []byte, mountPath string) (importSnapshot, error) {
	snapshot := importSnapshot{}
	blobIDs := make([]string, 0)
	seenBlobIDs := map[string]struct{}{}
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
		snapshot[globalPath] = importFile{
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
	blobs, err := gitBlobContents(ctx, repoDir, blobIDs)
	if err != nil {
		return nil, err
	}
	for path, file := range snapshot {
		data, ok := blobs[file.GitBlobID]
		if !ok {
			return nil, fmt.Errorf("missing git blob %s for %s", file.GitBlobID, file.GitPath)
		}
		file.BlobID = objectid.BlobID(data)
		file.ContentHash = objectid.RawContentHash(data)
		file.Size = int64(len(data))
		file.Data = data
		snapshot[path] = file
	}
	return snapshot, nil
}

func gitDeltaEdits(ctx context.Context, repoDir, previousCommitID, commitID, mountPath string) ([]*corev1.FileEdit, importSnapshot, error) {
	raw, err := gitOutputBytesImport(ctx, repoDir, "diff-tree", "-r", "-z", "--no-commit-id", "--name-status", "--find-renames", previousCommitID, commitID)
	if err != nil {
		return nil, nil, err
	}
	deleted, upsertGitPaths, err := parseGitDiffNameStatus(raw)
	if err != nil {
		return nil, nil, err
	}
	snapshot, err := gitFilesAtPaths(ctx, repoDir, commitID, mountPath, upsertGitPaths)
	if err != nil {
		return nil, nil, err
	}
	edits := make([]*corev1.FileEdit, 0, len(deleted)+len(snapshot))
	foundUpserts := make(map[string]struct{}, len(snapshot))
	for _, file := range snapshot {
		foundUpserts[file.GitPath] = struct{}{}
	}
	for _, gitPath := range deleted {
		globalPath, err := paths.FromWorkspacePath(mountPath, gitPath)
		if err != nil {
			return nil, nil, err
		}
		edits = append(edits, &corev1.FileEdit{Op: "delete", Path: globalPath})
	}
	for _, gitPath := range upsertGitPaths {
		if _, ok := foundUpserts[gitPath]; ok {
			continue
		}
		globalPath, err := paths.FromWorkspacePath(mountPath, gitPath)
		if err != nil {
			return nil, nil, err
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
	return edits, snapshot, nil
}

func diffImportSnapshots(previous, current importSnapshot) []*corev1.FileEdit {
	seen := map[string]struct{}{}
	for p := range previous {
		seen[p] = struct{}{}
	}
	for p := range current {
		seen[p] = struct{}{}
	}
	paths := make([]string, 0, len(seen))
	for p := range seen {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	edits := make([]*corev1.FileEdit, 0, len(paths))
	for _, p := range paths {
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
