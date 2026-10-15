package gitcompat

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

type importStats struct {
	blobs int
	bytes int
	fetch time.Duration // wall time until the last blob arrived
}

// runFastImport writes commits on top of parent with one git fast-import run.
// The blobs in need are fetched concurrently and streamed into fast-import as
// they arrive, so network reads overlap object writing and only a few blobs are
// held in memory at once. Marks are local to the stream, so arrival order does
// not affect any object id. It returns the new commit ids in order and the ids
// of the blobs it wrote.
func runFastImport(ctx context.Context, repoPath, parent string, commits []pendingCommit, need []string, fetch func(context.Context, string) ([]byte, error), known map[string]string, usernames map[string]string) ([]string, map[string]string, importStats, error) {
	var stats importStats
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	marksFile, err := os.CreateTemp(repoPath, ".marks-*")
	if err != nil {
		return nil, nil, stats, err
	}
	marksPath := marksFile.Name()
	_ = marksFile.Close()
	defer os.Remove(marksPath)

	// A small import would otherwise be written as loose objects, which are in no
	// pack, so the mirror would miss them.
	cmd := exec.CommandContext(ctx, "git", "-c", "fastimport.unpackLimit=0", "fast-import", "--quiet", "--done", "--export-marks="+marksPath)
	cmd.Dir = repoPath
	cmd.Env = append(os.Environ(), "GIT_DIR="+repoPath)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, stats, err
	}
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		return nil, nil, stats, err
	}
	fail := func(err error) ([]string, map[string]string, importStats, error) {
		cancel()
		_ = stdin.Close()
		_ = cmd.Wait()
		if output.Len() > 0 {
			err = fmt.Errorf("%w\n%s", err, output.String())
		}
		return nil, nil, stats, err
	}

	type fetched struct {
		hash string
		data []byte
		err  error
	}
	results := make(chan fetched, projectionBlobConcurrency)
	jobs := make(chan string)
	var workers sync.WaitGroup
	for i := 0; i < projectionBlobConcurrency; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for hash := range jobs {
				data, err := fetch(ctx, hash)
				select {
				case results <- fetched{hash: hash, data: data, err: err}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, hash := range need {
			select {
			case jobs <- hash:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		workers.Wait()
		close(results)
	}()

	w := bufio.NewWriterSize(stdin, 1<<20)
	fetchStart := time.Now()
	w.WriteString("feature done\n")
	blobMarks := make(map[string]int, len(need))
	mark := 0
	for result := range results {
		if result.err != nil {
			return fail(result.err)
		}
		mark++
		blobMarks[result.hash] = mark
		stats.blobs++
		stats.bytes += len(result.data)
		fmt.Fprintf(w, "blob\nmark :%d\ndata %d\n", mark, len(result.data))
		w.Write(result.data)
		if _, err := w.WriteString("\n"); err != nil {
			return fail(fmt.Errorf("git fast-import: %w", err))
		}
	}
	stats.fetch = time.Since(fetchStart)
	if len(blobMarks) != len(need) {
		return fail(fmt.Errorf("projection fetched %d of %d blobs", len(blobMarks), len(need)))
	}

	commitMarks := make([]int, len(commits))
	for i, commit := range commits {
		mark++
		commitMarks[i] = mark
		name, email := projectedIdentity(commit.native.Author, usernames)
		when := projectedTime(commit.native.CreatedAt)
		authorName, authorEmail, authored := importedAuthor(commit.imported, name, email, when)
		message := projectedCommitMessage(commit.native, commit.imported)
		fmt.Fprintf(w, "commit %s\nmark :%d\n", projectedBranch, mark)
		fmt.Fprintf(w, "author %s <%s> %d +0000\n", authorName, authorEmail, authored)
		fmt.Fprintf(w, "committer %s <%s> %d +0000\n", name, email, when)
		fmt.Fprintf(w, "data %d\n%s", len(message), message)
		if i == 0 && parent != "" {
			fmt.Fprintf(w, "from %s\n", parent)
		}
		for _, change := range commit.changes {
			if change.Delete {
				fmt.Fprintf(w, "D %s\n", fastImportPath(change.Path))
				continue
			}
			ref := known[change.ContentHash]
			if m, ok := blobMarks[change.ContentHash]; ok {
				ref = ":" + strconv.Itoa(m)
			}
			if ref == "" {
				return fail(fmt.Errorf("projection is missing content %s for %s", change.ContentHash, change.Path))
			}
			fmt.Fprintf(w, "M %s %s %s\n", change.Mode, ref, fastImportPath(change.Path))
		}
		w.WriteString("\n")
	}
	w.WriteString("done\n")
	if err := w.Flush(); err != nil {
		return fail(fmt.Errorf("git fast-import: %w", err))
	}
	if err := stdin.Close(); err != nil {
		return fail(err)
	}
	if err := cmd.Wait(); err != nil {
		return nil, nil, stats, fmt.Errorf("git fast-import failed: %w\n%s", err, output.String())
	}
	marks, err := readFastImportMarks(marksPath)
	if err != nil {
		return nil, nil, stats, err
	}
	shas := make([]string, len(commitMarks))
	for i, m := range commitMarks {
		if shas[i] = marks[m]; shas[i] == "" {
			return nil, nil, stats, fmt.Errorf("git fast-import did not report commit mark :%d", m)
		}
	}
	blobs := make(map[string]string, len(blobMarks))
	for hash, m := range blobMarks {
		blobs[hash] = marks[m]
	}
	return shas, blobs, stats, nil
}

func readFastImportMarks(path string) (map[int]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	marks := map[int]string{}
	for _, line := range strings.Split(string(data), "\n") {
		markText, sha, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok || !strings.HasPrefix(markText, ":") {
			continue
		}
		mark, err := strconv.Atoi(strings.TrimPrefix(markText, ":"))
		if err != nil {
			return nil, fmt.Errorf("malformed fast-import mark %q", line)
		}
		marks[mark] = sha
	}
	return marks, nil
}

// fastImportPath quotes a path the way git fast-import requires when it starts
// with a double quote or contains a line break or backslash.
func fastImportPath(path string) string {
	if !strings.ContainsAny(path, "\"\n\\") {
		return path
	}
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(path); i++ {
		switch c := path[i]; c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}
