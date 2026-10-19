package service

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

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

func gitFilesAtPaths(ctx context.Context, repoDir, commitID, mountPath string, gitPaths []string, known knownBlobs) (importSnapshot, error) {
	snapshot := importSnapshot{}
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
		raw, err := gitOutputBytesImport(ctx, repoDir, args...)
		if err != nil {
			return nil, err
		}
		chunk, err := gitSnapshotFromTreeRecords(ctx, repoDir, raw, mountPath, known)
		if err != nil {
			return nil, err
		}
		for p, file := range chunk {
			snapshot[p] = file
		}
	}
	return snapshot, nil
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

type gitCommitMeta struct {
	Subject     string
	Body        string
	FullMessage string
	AuthorName  string
	AuthorEmail string
	AuthoredAt  string // RFC 3339 in UTC
}

// gitCommitMetadata reads an imported commit's subject (git's %s, the first
// paragraph joined into one line), body, full message and original author.
func gitCommitMetadata(ctx context.Context, repoDir, commitID string) (gitCommitMeta, error) {
	out, err := gitOutputImport(ctx, repoDir, "log", "-1", "--format=%an%x00%ae%x00%aI%x00%s%x00%B", commitID)
	if err != nil {
		return gitCommitMeta{}, err
	}
	parts := strings.SplitN(out, "\x00", 5)
	if len(parts) != 5 {
		return gitCommitMeta{}, fmt.Errorf("unexpected git log output for %s", shortCommit(commitID))
	}
	meta := gitCommitMeta{
		AuthorName:  strings.TrimSpace(parts[0]),
		AuthorEmail: strings.TrimSpace(parts[1]),
		Subject:     strings.TrimSpace(parts[3]),
		FullMessage: strings.TrimRight(parts[4], " \t\r\n"),
	}
	if authored, err := time.Parse(time.RFC3339, strings.TrimSpace(parts[2])); err == nil {
		meta.AuthoredAt = authored.UTC().Format(time.RFC3339)
	}
	if _, body, ok := strings.Cut(meta.FullMessage, "\n"); ok {
		meta.Body = strings.TrimSpace(body)
	}
	if meta.Subject == "" {
		meta.Subject = "Import git commit " + shortCommit(commitID)
	}
	return meta, nil
}

func shortCommit(commitID string) string {
	if len(commitID) <= 12 {
		return commitID
	}
	return commitID[:12]
}

func gitOutputImport(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := gitOutputBytesImport(ctx, dir, args...)
	return string(out), err
}

func gitOutputBytesImport(ctx context.Context, dir string, args ...string) ([]byte, error) {
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

func runGitImport(ctx context.Context, dir, allowProtocol string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), "GIT_ALLOW_PROTOCOL="+allowProtocol)
	if dir != "" {
		cmd.Dir = dir
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return nil
}
