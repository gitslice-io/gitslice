package gitcompat

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gitslice.io/gitslice/internal/paths"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func indexPushPack(ctx context.Context, projectedRepoPath string, packfile []byte) (string, func(), error) {
	tempRoot, err := os.MkdirTemp(filepath.Dir(projectedRepoPath), ".push-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(tempRoot) }
	repoPath := filepath.Join(tempRoot, "repo.git")
	if err := runGit(ctx, "", nil, "init", "--bare", repoPath); err != nil {
		cleanup()
		return "", nil, err
	}
	infoDir := filepath.Join(repoPath, "objects", "info")
	if err := os.MkdirAll(infoDir, 0o755); err != nil {
		cleanup()
		return "", nil, err
	}
	alternates := filepath.Join(projectedRepoPath, "objects") + "\n"
	if err := os.WriteFile(filepath.Join(infoDir, "alternates"), []byte(alternates), 0o644); err != nil {
		cleanup()
		return "", nil, err
	}
	if len(packfile) > 0 {
		if _, err := gitOutputInput(ctx, repoPath, nil, packfile, "index-pack", "--stdin", "--fix-thin"); err != nil {
			cleanup()
			return "", nil, err
		}
	}
	return repoPath, cleanup, nil
}

func pushedCommitFileEdits(ctx, serviceCtx context.Context, repoDir, baseCommitID, newCommitID string, sliceRef *corev1.SliceRef, blobs BlobAPI) ([]*corev1.FileEdit, string, error) {
	title, err := gitOutput(ctx, repoDir, nil, "log", "-1", "--format=%s", newCommitID)
	if err != nil {
		return nil, "", err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Git push " + shortGitOID(newCommitID)
	}
	raw, err := gitOutputBytes(ctx, repoDir, nil, "diff-tree", "-r", "-z", "--no-commit-id", "--name-status", "--find-renames", firstNonEmpty(baseCommitID, emptyGitTree), newCommitID)
	if err != nil {
		return nil, "", err
	}
	deleted, upsertGitPaths, err := parseGitDiffNameStatus(raw)
	if err != nil {
		return nil, "", err
	}
	snapshot, err := gitFilesAtPaths(ctx, repoDir, newCommitID, upsertGitPaths)
	if err != nil {
		return nil, "", err
	}
	edits := make([]*corev1.FileEdit, 0, len(deleted)+len(upsertGitPaths))
	foundUpserts := make(map[string]struct{}, len(snapshot))
	for _, file := range snapshot {
		foundUpserts[file.GitPath] = struct{}{}
	}
	for _, gitPath := range deleted {
		globalPath, err := paths.FromWorkspacePath("/", gitPath)
		if err != nil {
			return nil, "", err
		}
		edits = append(edits, &corev1.FileEdit{Op: "delete", Path: globalPath})
	}
	for _, gitPath := range upsertGitPaths {
		file, ok := snapshot[gitPath]
		if !ok {
			if _, seen := foundUpserts[gitPath]; seen {
				continue
			}
			globalPath, err := paths.FromWorkspacePath("/", gitPath)
			if err != nil {
				return nil, "", err
			}
			edits = append(edits, &corev1.FileEdit{Op: "delete", Path: globalPath})
			continue
		}
		upload, err := blobs.UploadBlob(serviceCtx, &corev1.UploadBlobRequest{
			Data:  file.Data,
			Slice: sliceRef,
		})
		if err != nil {
			return nil, "", err
		}
		edits = append(edits, &corev1.FileEdit{
			Op:          "upsert",
			Path:        file.Path,
			BlobId:      upload.BlobId,
			ContentHash: upload.ContentHash,
			Mode:        file.Mode,
		})
	}
	sort.Slice(edits, func(i, j int) bool {
		if edits[i].Path == edits[j].Path {
			return edits[i].Op < edits[j].Op
		}
		return edits[i].Path < edits[j].Path
	})
	return edits, title, nil
}

// resolvePushBase returns the projected commit a push builds on: the nearest
// first-parent ancestor of newCommitID that belongs to the projected history.
// The pushed commits above it must form one linear chain. Any projected commit
// may be the base, not only the head, so a client that has not fetched the
// latest history can still push. When the slice has no history yet the chain
// starts at a root commit instead, and the base is "".
func resolvePushBase(ctx context.Context, repoDir string, projection *Projection, newCommitID string) (string, error) {
	raw, err := gitOutput(ctx, repoDir, nil, "rev-list", "--first-parent", "--parents", newCommitID)
	if err != nil {
		return "", err
	}
	pushed := 0
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if _, ok := projection.NativeCommitFor(fields[0]); ok {
			if pushed == 0 {
				return "", fmt.Errorf("push contains no commits beyond the projected history")
			}
			return fields[0], nil
		}
		if len(fields) > 2 {
			return "", fmt.Errorf("merge commits are not supported by Git push into changesets; rebase to a single linear commit chain")
		}
		pushed++
	}
	if projection.GitCommitID != "" {
		return "", fmt.Errorf("NEEDS_REBASE: pushed commits are not based on this slice's projected history; run git fetch origin main and rebase before pushing to refs/changes/new")
	}
	return "", nil
}

func gitFilesAtPaths(ctx context.Context, repoDir, commitID string, gitPaths []string) (map[string]pushedGitFile, error) {
	snapshot := map[string]pushedGitFile{}
	if len(gitPaths) == 0 {
		return snapshot, nil
	}
	const chunkSize = 512
	for start := 0; start < len(gitPaths); start += chunkSize {
		end := start + chunkSize
		if end > len(gitPaths) {
			end = len(gitPaths)
		}
		args := []string{"ls-tree", "-r", "-z", "--full-tree", commitID, "--"}
		for _, p := range gitPaths[start:end] {
			args = append(args, ":(literal)"+p)
		}
		raw, err := gitOutputBytes(ctx, repoDir, nil, args...)
		if err != nil {
			return nil, err
		}
		chunk, err := gitSnapshotFromTreeRecords(ctx, repoDir, raw)
		if err != nil {
			return nil, err
		}
		for p, file := range chunk {
			snapshot[p] = file
		}
	}
	return snapshot, nil
}

func gitSnapshotFromTreeRecords(ctx context.Context, repoDir string, raw []byte) (map[string]pushedGitFile, error) {
	snapshot := map[string]pushedGitFile{}
	blobIDs := make([]string, 0)
	for _, record := range bytes.Split(raw, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		meta, gitPath, ok := bytes.Cut(record, []byte{'\t'})
		if !ok {
			return nil, fmt.Errorf("malformed ls-tree record %q", string(record))
		}
		fields := strings.Fields(string(meta))
		if len(fields) < 3 {
			return nil, fmt.Errorf("malformed ls-tree record %q", string(record))
		}
		if fields[1] != "blob" {
			return nil, fmt.Errorf("unsupported Git object type %s at %s", fields[1], string(gitPath))
		}
		modeValue, err := strconv.ParseUint(fields[0], 8, 32)
		if err != nil {
			return nil, err
		}
		pathText := string(gitPath)
		globalPath, err := paths.FromWorkspacePath("/", pathText)
		if err != nil {
			return nil, err
		}
		snapshot[pathText] = pushedGitFile{
			Path:      globalPath,
			GitPath:   pathText,
			GitBlobID: fields[2],
			Mode:      uint32(modeValue),
		}
		blobIDs = append(blobIDs, fields[2])
	}
	blobs, err := gitBlobContents(ctx, repoDir, blobIDs)
	if err != nil {
		return nil, err
	}
	for gitPath, file := range snapshot {
		data, ok := blobs[file.GitBlobID]
		if !ok {
			return nil, fmt.Errorf("missing Git blob %s for %s", file.GitBlobID, file.GitPath)
		}
		file.Data = data
		snapshot[gitPath] = file
	}
	return snapshot, nil
}

func parseGitDiffNameStatus(raw []byte) ([]string, []string, error) {
	if len(raw) == 0 {
		return nil, nil, nil
	}
	tokens := bytes.Split(raw, []byte{0})
	deletedSet := map[string]struct{}{}
	upsertSet := map[string]struct{}{}
	for i := 0; i < len(tokens); {
		if len(tokens[i]) == 0 {
			i++
			continue
		}
		statusValue := string(tokens[i])
		i++
		statusKind := statusValue[0]
		switch statusKind {
		case 'A', 'C', 'M', 'T':
			if i >= len(tokens) {
				return nil, nil, fmt.Errorf("malformed git diff status %q", statusValue)
			}
			pathText := string(tokens[i])
			i++
			upsertSet[pathText] = struct{}{}
		case 'D':
			if i >= len(tokens) {
				return nil, nil, fmt.Errorf("malformed git diff status %q", statusValue)
			}
			pathText := string(tokens[i])
			i++
			deletedSet[pathText] = struct{}{}
		case 'R':
			if i+1 >= len(tokens) {
				return nil, nil, fmt.Errorf("malformed git rename status %q", statusValue)
			}
			oldPath := string(tokens[i])
			newPath := string(tokens[i+1])
			i += 2
			deletedSet[oldPath] = struct{}{}
			upsertSet[newPath] = struct{}{}
		default:
			return nil, nil, fmt.Errorf("unsupported git diff status %q", statusValue)
		}
	}
	deleted := make([]string, 0, len(deletedSet))
	for p := range deletedSet {
		if _, upserted := upsertSet[p]; upserted {
			continue
		}
		deleted = append(deleted, p)
	}
	upserts := make([]string, 0, len(upsertSet))
	for p := range upsertSet {
		upserts = append(upserts, p)
	}
	sort.Strings(deleted)
	sort.Strings(upserts)
	return deleted, upserts, nil
}

func gitBlobContents(ctx context.Context, repoDir string, blobIDs []string) (map[string][]byte, error) {
	contents := make(map[string][]byte, len(blobIDs))
	if len(blobIDs) == 0 {
		return contents, nil
	}
	cmd := exec.CommandContext(ctx, "git", "cat-file", "--batch")
	cmd.Dir = repoDir
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	writeErrCh := make(chan error, 1)
	go func() {
		w := bufio.NewWriter(stdin)
		for _, blobID := range blobIDs {
			if _, err := fmt.Fprintln(w, blobID); err != nil {
				_ = stdin.Close()
				writeErrCh <- err
				return
			}
		}
		if err := w.Flush(); err != nil {
			_ = stdin.Close()
			writeErrCh <- err
			return
		}
		writeErrCh <- stdin.Close()
	}()
	reader := bufio.NewReader(stdout)
	for range blobIDs {
		header, err := reader.ReadString('\n')
		if err != nil {
			_ = cmd.Process.Kill()
			_ = <-writeErrCh
			_ = cmd.Wait()
			return nil, err
		}
		fields := strings.Fields(header)
		if len(fields) != 3 {
			_ = cmd.Process.Kill()
			_ = <-writeErrCh
			_ = cmd.Wait()
			return nil, fmt.Errorf("malformed git cat-file header %q", strings.TrimSpace(header))
		}
		if fields[1] != "blob" {
			_ = cmd.Process.Kill()
			_ = <-writeErrCh
			_ = cmd.Wait()
			return nil, fmt.Errorf("git object %s is %s, want blob", fields[0], fields[1])
		}
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || size < 0 {
			_ = cmd.Process.Kill()
			_ = <-writeErrCh
			_ = cmd.Wait()
			return nil, fmt.Errorf("invalid git blob size %q", fields[2])
		}
		data := make([]byte, size)
		if _, err := io.ReadFull(reader, data); err != nil {
			_ = cmd.Process.Kill()
			_ = <-writeErrCh
			_ = cmd.Wait()
			return nil, err
		}
		trailing, err := reader.ReadByte()
		if err != nil {
			_ = cmd.Process.Kill()
			_ = <-writeErrCh
			_ = cmd.Wait()
			return nil, err
		}
		if trailing != '\n' {
			_ = cmd.Process.Kill()
			_ = <-writeErrCh
			_ = cmd.Wait()
			return nil, fmt.Errorf("malformed git cat-file payload for %s", fields[0])
		}
		contents[fields[0]] = data
	}
	if err := <-writeErrCh; err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, err
	}
	if err := cmd.Wait(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("git cat-file --batch: %s", msg)
	}
	return contents, nil
}

func gitOutputBytes(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	out, err := gitOutputInput(ctx, dir, env, nil, args...)
	return []byte(out), err
}

func gitOutputInput(ctx context.Context, dir string, env []string, input []byte, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), env...)
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s failed: %w\n%s", strings.Join(args, " "), err, string(out))
	}
	return string(out), nil
}

func gitObjectExists(ctx context.Context, dir, rev string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "cat-file", "-e", rev)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err == nil {
		return true, nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("git cat-file -e failed: %w\n%s", err, string(out))
}
