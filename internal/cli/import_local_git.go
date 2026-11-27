package cli

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/paths"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

func parseLocalImportCommitMeta(commitID, authorName, authorEmail, authoredAt, subject, fullMessage string) localImportCommitMeta {
	meta := localImportCommitMeta{
		AuthorName:  strings.TrimSpace(authorName),
		AuthorEmail: strings.TrimSpace(authorEmail),
		Subject:     strings.TrimSpace(subject),
		FullMessage: strings.TrimRight(fullMessage, " \t\r\n"),
	}
	if authored, err := time.Parse(time.RFC3339, strings.TrimSpace(authoredAt)); err == nil {
		meta.AuthoredAt = authored.UTC().Format(time.RFC3339)
	}
	if _, body, ok := strings.Cut(meta.FullMessage, "\n"); ok {
		meta.Body = strings.TrimSpace(body)
	}
	if meta.Subject == "" {
		meta.Subject = "Import git commit " + shortID(commitID)
	}
	return meta
}

func localGitSelectedCommits(ctx context.Context, repoDir, mode string, maxCommits int) ([]string, error) {
	if mode == "shallow" {
		head, err := localGitOutput(ctx, repoDir, "rev-parse", "HEAD")
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
	out, err := localGitOutput(ctx, repoDir, args...)
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

func localGitCommitMetadata(ctx context.Context, repoDir, commitID string) (localImportCommitMeta, error) {
	out, err := localGitOutput(ctx, repoDir, "log", "-1", "--format=%an%x00%ae%x00%aI%x00%s%x00%B", commitID)
	if err != nil {
		return localImportCommitMeta{}, err
	}
	parts := strings.SplitN(out, "\x00", 5)
	if len(parts) != 5 {
		return localImportCommitMeta{}, fmt.Errorf("unexpected git log output for %s", shortID(commitID))
	}
	return parseLocalImportCommitMeta(commitID, parts[0], parts[1], parts[2], parts[3], parts[4]), nil
}

func localGitSnapshotMetadata(ctx context.Context, repoDir, commitID, mountPath string) (map[string]localImportFile, []string, error) {
	raw, err := localGitOutputBytes(ctx, repoDir, "ls-tree", "-r", "-z", "--full-tree", commitID)
	if err != nil {
		return nil, nil, err
	}
	snapshot := map[string]localImportFile{}
	seen := map[string]struct{}{}
	blobIDs, err := parseLocalGitTreeRecords(raw, mountPath, snapshot, seen, nil)
	if err != nil {
		return nil, nil, err
	}
	return snapshot, blobIDs, nil
}

func streamLocalGitDeltas(
	ctx context.Context,
	repoDir string,
	pairs []localGitCommitPair,
	mountPath string,
	onCommit func(pendingLocalCommit, []string) error,
) error {
	if len(pairs) == 0 {
		return nil
	}
	cmd := exec.CommandContext(ctx, "git", "diff-tree", "--stdin", "-r", "-z", "--no-renames", "--no-abbrev", "--always",
		"--format=COMMIT%x1f%H%x1f%an%x1f%ae%x1f%aI%x1f%s%x1f%B")
	cmd.Dir = repoDir
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}

	writeErrCh := make(chan error, 1)
	go func() {
		w := bufio.NewWriterSize(stdin, 64<<10)
		for _, pair := range pairs {
			if _, err := fmt.Fprintf(w, "%s %s\n", pair.commitID, pair.prevID); err != nil {
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

	abort := func() {
		_ = cmd.Process.Kill()
		<-writeErrCh
		_ = cmd.Wait()
	}

	reader := bufio.NewReaderSize(stdout, 256<<10)
	readToken := func() (string, bool, error) {
		raw, err := reader.ReadBytes(0)
		if err != nil {
			if err == io.EOF && len(raw) == 0 {
				return "", false, nil
			}
			if err == io.EOF {
				return string(raw), true, nil
			}
			return "", false, err
		}
		return string(raw[:len(raw)-1]), true, nil
	}

	var hasCurrent bool
	var current pendingLocalCommit
	var deletedSet map[string]struct{}
	var upsertSet map[string]struct{}
	var blobSeen map[string]struct{}
	var blobIDs []string
	pairIdx := 0

	flushCurrent := func() error {
		if !hasCurrent {
			return nil
		}
		if len(deletedSet) > 0 {
			current.deleted = make([]string, 0, len(deletedSet))
			for p := range deletedSet {
				if _, upserted := upsertSet[p]; !upserted {
					current.deleted = append(current.deleted, p)
				}
			}
			sort.Strings(current.deleted)
		}
		if len(upsertSet) > 0 {
			current.upsertGitPaths = make([]string, 0, len(upsertSet))
			for p := range upsertSet {
				current.upsertGitPaths = append(current.upsertGitPaths, p)
			}
			sort.Strings(current.upsertGitPaths)
		}
		hasCurrent = false
		return onCommit(current, blobIDs)
	}

	for {
		tok, ok, err := readToken()
		if err != nil {
			abort()
			return fmt.Errorf("read git diff-tree stream: %w", err)
		}
		if !ok {
			break
		}
		trimmed := strings.TrimPrefix(tok, "\n")
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "COMMIT\x1f") {
			if err := flushCurrent(); err != nil {
				abort()
				return err
			}
			if pairIdx >= len(pairs) {
				abort()
				return fmt.Errorf("unexpected extra commit in git diff-tree stream")
			}
			fields := strings.SplitN(trimmed, "\x1f", 7)
			if len(fields) != 7 {
				abort()
				return fmt.Errorf("malformed git diff-tree commit header")
			}
			pair := pairs[pairIdx]
			pairIdx++
			meta := parseLocalImportCommitMeta(pair.commitID, fields[2], fields[3], fields[4], fields[5], fields[6])
			current = pendingLocalCommit{
				gitCommitID: pair.commitID,
				position:    pair.position,
				meta:        meta,
				snapshot:    map[string]localImportFile{},
			}
			deletedSet = map[string]struct{}{}
			upsertSet = map[string]struct{}{}
			blobSeen = map[string]struct{}{}
			blobIDs = nil
			hasCurrent = true
			continue
		}
		if !strings.HasPrefix(trimmed, ":") {
			abort()
			return fmt.Errorf("unexpected git diff-tree token %q", trimmed)
		}
		if !hasCurrent {
			abort()
			return fmt.Errorf("git diff-tree file record before commit header")
		}
		parts := strings.Fields(trimmed[1:])
		if len(parts) < 5 {
			abort()
			return fmt.Errorf("malformed git diff-tree raw header %q", trimmed)
		}
		oldMode, newMode, newSHA, statusVal := parts[0], parts[1], parts[3], parts[4]
		gitPath, pathOK, err := readToken()
		if err != nil || !pathOK {
			abort()
			return fmt.Errorf("missing path in git diff-tree stream: %v", err)
		}
		if statusVal[0] == 'D' || newMode == "000000" {
			if oldMode != "160000" {
				deletedSet[gitPath] = struct{}{}
			}
			continue
		}
		if newMode == "160000" {
			if oldMode != "000000" && oldMode != "160000" {
				deletedSet[gitPath] = struct{}{}
			}
			continue
		}
		modeValue, err := strconv.ParseUint(newMode, 8, 32)
		if err != nil {
			abort()
			return fmt.Errorf("invalid git file mode %q for %s: %w", newMode, gitPath, err)
		}
		globalPath, err := paths.FromWorkspacePath(mountPath, gitPath)
		if err != nil {
			abort()
			return err
		}
		upsertSet[gitPath] = struct{}{}
		current.snapshot[globalPath] = localImportFile{
			Path:      globalPath,
			GitPath:   gitPath,
			GitBlobID: newSHA,
			Mode:      uint32(modeValue),
		}
		if _, ok := blobSeen[newSHA]; !ok {
			blobSeen[newSHA] = struct{}{}
			blobIDs = append(blobIDs, newSHA)
		}
	}

	if err := flushCurrent(); err != nil {
		abort()
		return err
	}
	if err := <-writeErrCh; err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}
	if err := cmd.Wait(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("git diff-tree --stdin: %s", msg)
	}
	if pairIdx != len(pairs) {
		return fmt.Errorf("git diff-tree emitted %d of %d commit(s)", pairIdx, len(pairs))
	}
	return nil
}

func parseLocalGitTreeRecords(raw []byte, mountPath string, snapshot map[string]localImportFile, seen map[string]struct{}, blobIDs []string) ([]string, error) {
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
		modeValue, err := strconv.ParseUint(fields[0], 8, 32)
		if err != nil {
			return nil, err
		}
		pathText := string(gitPath)
		globalPath, err := paths.FromWorkspacePath(mountPath, pathText)
		if err != nil {
			return nil, err
		}
		gitBlobID := fields[2]
		snapshot[globalPath] = localImportFile{
			Path:      globalPath,
			GitPath:   pathText,
			GitBlobID: gitBlobID,
			Mode:      uint32(modeValue),
		}
		if _, exists := seen[gitBlobID]; !exists {
			seen[gitBlobID] = struct{}{}
			blobIDs = append(blobIDs, gitBlobID)
		}
	}
	return blobIDs, nil
}

func diffInitialLocalSnapshot(previous map[string]localImportMountedFile, snapshot map[string]localImportFile, known map[string]localImportBlobMeta) ([]*corev1.FileEdit, error) {
	seen := make(map[string]struct{}, len(previous)+len(snapshot))
	for p := range previous {
		seen[p] = struct{}{}
	}
	for p := range snapshot {
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
		after, hasAfter := snapshot[p]
		if !hasAfter {
			edits = append(edits, &corev1.FileEdit{Op: "delete", Path: p})
			continue
		}
		meta, ok := known[after.GitBlobID]
		if !ok {
			return nil, fmt.Errorf("missing uploaded blob metadata for %s (%s)", p, after.GitBlobID)
		}
		if !hadBefore || before.ContentHash != meta.ContentHash || before.Mode != after.Mode {
			edits = append(edits, &corev1.FileEdit{
				Op:          "upsert",
				Path:        p,
				BlobId:      meta.BlobID,
				ContentHash: meta.ContentHash,
				Mode:        after.Mode,
			})
		}
	}
	return edits, nil
}

func buildDeltaLocalEdits(mountPath string, deleted, upsertGitPaths []string, snapshot map[string]localImportFile, known map[string]localImportBlobMeta) ([]*corev1.FileEdit, error) {
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
		meta, ok := known[file.GitBlobID]
		if !ok {
			return nil, fmt.Errorf("missing uploaded blob metadata for %s (%s)", file.Path, file.GitBlobID)
		}
		edits = append(edits, &corev1.FileEdit{
			Op:          "upsert",
			Path:        file.Path,
			BlobId:      meta.BlobID,
			ContentHash: meta.ContentHash,
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

func localGitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := localGitOutputBytes(ctx, dir, args...)
	return string(out), err
}

func localGitOutputBytes(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return out, nil
}
