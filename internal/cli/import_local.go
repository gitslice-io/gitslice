package cli

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
	"time"

	"gitslice.io/gitslice/internal/model"
	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/paths"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/encoding/gzip"
	grpcstatus "google.golang.org/grpc/status"
)

const (
	defaultLocalImportBatchSize = 50
	maxLocalImportBatchSize     = 200
	localImportMaxBatchEdits    = 2000
	localImportBlobChunkBytes   = 32 << 20
	localImportBlobChunkCount   = 512
	localImportMaxBlobBytes     = 100 << 20
)

type localImportBlobMeta struct {
	BlobID      string
	ContentHash string
	Size        int64
}

type localImportFile struct {
	Path      string
	GitPath   string
	GitBlobID string
	Mode      uint32
}

type localImportCommitMeta struct {
	Subject     string
	Body        string
	FullMessage string
	AuthorName  string
	AuthorEmail string
	AuthoredAt  string
}

type localImportMountedFile struct {
	ContentHash string
	Mode        uint32
}

type pendingLocalCommit struct {
	gitCommitID    string
	position       int
	meta           localImportCommitMeta
	isInitial      bool
	deleted        []string
	upsertGitPaths []string
	snapshot       map[string]localImportFile
}

func (r Runner) runLocalGitImport(
	ctx context.Context,
	opts commandOptions,
	source, mountPath, sliceRef, mode string,
	maxCommits int,
	resume bool,
	retries int,
	batchSize int,
) error {
	cfg, err := r.readUserConfig()
	if err != nil {
		return err
	}
	if mountPath == "" {
		return userError("missing_mount", "missing import mount path", "Use --mount /account/path, for example --mount /nic/gitslice.")
	}
	mountPath, err = paths.Canonical(mountPath)
	if err != nil {
		return userError("invalid_mount", "invalid import mount path", "Use --mount /account/path, for example --mount /nic/gitslice.")
	}
	if sliceRef == "" {
		account, err := importAccountFromMount(mountPath)
		if err != nil {
			return err
		}
		sliceRef = account + ":home"
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = "shallow"
	}
	if mode != "shallow" && mode != "deep" {
		return userError("invalid_mode", "import mode must be shallow or deep", "Pass --mode shallow or --mode deep.")
	}
	if maxCommits < 0 {
		return userError("invalid_max_commits", "max commits must be non-negative", "Use --max-commits 0 for no limit.")
	}
	if batchSize <= 0 {
		batchSize = defaultLocalImportBatchSize
	}
	if batchSize > maxLocalImportBatchSize {
		batchSize = maxLocalImportBatchSize
	}

	quiet := opts.Quiet || opts.JSONOutput()
	logf := func(format string, args ...any) {
		if !quiet && r.Stderr != nil {
			fmt.Fprintf(r.Stderr, format+"\n", args...)
		}
	}

	repoDir, importSource, cleanup, err := prepareLocalImportRepo(ctx, source, mode, maxCommits, logf)
	if err != nil {
		return err
	}
	defer cleanup()

	logf("listing commits...")
	gitCommits, err := localGitSelectedCommits(ctx, repoDir, mode, maxCommits)
	if err != nil {
		return fmt.Errorf("list git commits: %w", err)
	}
	if len(gitCommits) == 0 {
		return userError("empty_repo", "git repository has no commits", "Point gs import at a repository with at least one commit.")
	}
	logf("found %d commit(s)", len(gitCommits))

	conn, err := dialStream(ctx, cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	callCtx := authContext(ctx, cfg)
	ref, err := r.resolveSliceRefInput(callCtx, cfg, conn, sliceRef)
	if err != nil {
		return err
	}

	repoClient := corev1.NewRepositoryServiceClient(conn)
	blobClient := corev1.NewBlobServiceClient(conn)
	tm := NewTransferManager(nil, blobClient, ref, nil)

	var beginRes *corev1.BeginGitImportResponse
	if err := retryLocalImportCall(ctx, retries, logf, "begin import", func() error {
		var callErr error
		beginRes, callErr = repoClient.BeginGitImport(callCtx, &corev1.BeginGitImportRequest{
			Source:         importSource,
			MountPath:      mountPath,
			AuthoringSlice: ref,
			Mode:           mode,
			TargetRef:      model.DefaultTargetRef,
			TotalCommits:   int32(len(gitCommits)),
			Resume:         resume,
		})
		return callErr
	}); err != nil {
		return err
	}

	completed := make(map[string]*corev1.ImportedGitCommit, len(beginRes.GetCompletedCommits()))
	for _, c := range beginRes.GetCompletedCommits() {
		completed[c.GetGitCommitId()] = c
	}
	previousMounted := make(map[string]localImportMountedFile, len(beginRes.GetMountedFiles()))
	for _, entry := range beginRes.GetMountedFiles() {
		previousMounted[entry.GetPath()] = localImportMountedFile{
			ContentHash: entry.GetContentHash(),
			Mode:        entry.GetMode(),
		}
	}

	response := &corev1.ImportGitRepositoryResponse{
		Source:        importSource,
		MountPath:     mountPath,
		Mode:          mode,
		TargetRef:     beginRes.GetTargetRef(),
		FinalCommitId: beginRes.GetFinalCommitId(),
	}

	known := make(map[string]localImportBlobMeta)
	var pending []pendingLocalCommit
	var pendingBlobIDs []string
	pendingBlobSeen := map[string]struct{}{}
	pendingEditsCount := 0
	var previousGitCommitID string
	startedAt := time.Now()
	importedThisRun := 0
	skippedCount := 0

	flushBatch := func() error {
		if len(pending) == 0 {
			return nil
		}
		if len(pendingBlobIDs) > 0 {
			if err := uploadLocalImportBlobs(callCtx, repoDir, pendingBlobIDs, known, tm, retries, logf); err != nil {
				return err
			}
		}

		commitInputs := make([]*corev1.ImportGitCommitInput, 0, len(pending))
		for _, item := range pending {
			var edits []*corev1.FileEdit
			var err error
			if item.isInitial {
				edits, err = diffInitialLocalSnapshot(previousMounted, item.snapshot, known)
			} else {
				edits, err = buildDeltaLocalEdits(mountPath, item.deleted, item.upsertGitPaths, item.snapshot, known)
			}
			if err != nil {
				return err
			}
			commitInputs = append(commitInputs, &corev1.ImportGitCommitInput{
				GitCommitId: item.gitCommitID,
				Position:    int32(item.position),
				Message:     item.meta.Subject,
				Description: item.meta.Body,
				FullMessage: item.meta.FullMessage,
				AuthorName:  item.meta.AuthorName,
				AuthorEmail: item.meta.AuthorEmail,
				AuthoredAt:  item.meta.AuthoredAt,
				Edits:       edits,
			})
		}

		var batchRes *corev1.ImportGitCommitBatchResponse
		req := &corev1.ImportGitCommitBatchRequest{
			ImportId:       beginRes.GetImportId(),
			AuthoringSlice: ref,
			MountPath:      mountPath,
			TargetRef:      model.DefaultTargetRef,
			Commits:        commitInputs,
		}
		if err := retryLocalImportCall(ctx, retries, logf, fmt.Sprintf("publish commits %d-%d", pending[0].position, pending[len(pending)-1].position), func() error {
			var callErr error
			batchRes, callErr = repoClient.ImportGitCommitBatch(callCtx, req, grpc.UseCompressor(gzip.Name))
			return callErr
		}); err != nil {
			return err
		}
		for _, c := range batchRes.GetCommits() {
			response.Commits = append(response.Commits, c)
			response.FinalCommitId = c.GetNativeCommitId()
		}
		importedThisRun += len(pending)
		last := pending[len(pending)-1]
		elapsed := time.Since(startedAt).Seconds()
		rate := float64(importedThisRun) / max(elapsed, 0.001)
		logf("published %d/%d %s -> %s (%d commit(s), %.1f commits/s)",
			last.position, len(gitCommits), shortID(last.gitCommitID), shortID(response.FinalCommitId), len(pending), rate)

		pending = pending[:0]
		pendingBlobIDs = pendingBlobIDs[:0]
		clear(pendingBlobSeen)
		pendingEditsCount = 0
		return nil
	}

	for i, gitCommitID := range gitCommits {
		if existing, ok := completed[gitCommitID]; ok {
			response.Commits = append(response.Commits, existing)
			response.FinalCommitId = existing.GetNativeCommitId()
			previousGitCommitID = gitCommitID
			skippedCount++
			continue
		}
		if skippedCount > 0 {
			logf("resumed: skipped %d already-imported commit(s)", skippedCount)
			skippedCount = 0
		}

		meta, err := localGitCommitMetadata(ctx, repoDir, gitCommitID)
		if err != nil {
			return fmt.Errorf("read git commit %s metadata: %w", shortID(gitCommitID), err)
		}

		var item pendingLocalCommit
		item.gitCommitID = gitCommitID
		item.position = i + 1
		item.meta = meta

		var commitBlobIDs []string
		var commitEditEstimate int
		if previousGitCommitID == "" {
			item.isInitial = true
			item.snapshot, commitBlobIDs, err = localGitSnapshotMetadata(ctx, repoDir, gitCommitID, mountPath)
			if err != nil {
				return fmt.Errorf("read git commit %s tree: %w", shortID(gitCommitID), err)
			}
			commitEditEstimate = len(item.snapshot)
		} else {
			item.deleted, item.upsertGitPaths, item.snapshot, commitBlobIDs, err = localGitDeltaMetadata(ctx, repoDir, previousGitCommitID, gitCommitID, mountPath)
			if err != nil {
				return fmt.Errorf("read git commit %s diff: %w", shortID(gitCommitID), err)
			}
			commitEditEstimate = len(item.deleted) + len(item.snapshot)
		}

		if len(pending) > 0 && (len(pending) >= batchSize || pendingEditsCount+commitEditEstimate > localImportMaxBatchEdits || len(pendingBlobIDs) >= localImportBlobChunkCount) {
			if err := flushBatch(); err != nil {
				return err
			}
		}

		pending = append(pending, item)
		pendingEditsCount += commitEditEstimate
		for _, id := range commitBlobIDs {
			if _, ok := known[id]; ok {
				continue
			}
			if _, seen := pendingBlobSeen[id]; seen {
				continue
			}
			pendingBlobSeen[id] = struct{}{}
			pendingBlobIDs = append(pendingBlobIDs, id)
		}
		previousGitCommitID = gitCommitID

		if commitEditEstimate > localImportMaxBatchEdits {
			if err := flushBatch(); err != nil {
				return err
			}
		}
	}
	if skippedCount > 0 {
		logf("all %d commit(s) were already imported", skippedCount)
	}
	if err := flushBatch(); err != nil {
		return err
	}

	logf("finalizing import and draining indexes...")
	if err := retryLocalImportCall(ctx, retries, logf, "complete import", func() error {
		_, callErr := repoClient.CompleteGitImport(callCtx, &corev1.CompleteGitImportRequest{
			ImportId:       beginRes.GetImportId(),
			AuthoringSlice: ref,
			MountPath:      mountPath,
			TargetRef:      model.DefaultTargetRef,
			FinalCommitId:  response.FinalCommitId,
		})
		return callErr
	}); err != nil {
		return err
	}
	logf("import complete")

	if opts.JSONOutput() {
		return r.writeJSONOutput(opts, response)
	}
	if opts.Quiet {
		return nil
	}
	printLocalImportSummary(r.Stdout, response)
	return nil
}

func printLocalImportSummary(w io.Writer, res *corev1.ImportGitRepositoryResponse) {
	fmt.Fprintf(w, "imported %d commit(s) from %s\n", len(res.Commits), res.Source)
	fmt.Fprintf(w, "mount: %s\n", res.MountPath)
	fmt.Fprintf(w, "mode: %s\n", res.Mode)
	fmt.Fprintf(w, "final commit: %s\n", res.FinalCommitId)
	if len(res.Commits) <= 20 {
		for _, commit := range res.Commits {
			fmt.Fprintf(w, "  %s -> %s %s\n", shortID(commit.GitCommitId), commit.NativeCommitId, commit.Message)
		}
		return
	}
	for _, commit := range res.Commits[:5] {
		fmt.Fprintf(w, "  %s -> %s %s\n", shortID(commit.GitCommitId), commit.NativeCommitId, commit.Message)
	}
	fmt.Fprintf(w, "  ... (%d more commits)\n", len(res.Commits)-10)
	for _, commit := range res.Commits[len(res.Commits)-5:] {
		fmt.Fprintf(w, "  %s -> %s %s\n", shortID(commit.GitCommitId), commit.NativeCommitId, commit.Message)
	}
}

func prepareLocalImportRepo(ctx context.Context, source, mode string, maxCommits int, logf func(string, ...any)) (repoDir, importSource string, cleanup func(), err error) {
	if info, statErr := os.Stat(source); statErr == nil && info.IsDir() {
		abs, err := filepath.Abs(source)
		if err != nil {
			return "", "", nil, err
		}
		if _, err := localGitOutput(ctx, abs, "rev-parse", "--git-dir"); err != nil {
			return "", "", nil, fmt.Errorf("%s is not a git repository: %w", source, err)
		}
		return abs, abs, func() {}, nil
	}
	parent, err := os.MkdirTemp("", "gs-local-import-*")
	if err != nil {
		return "", "", nil, err
	}
	cleanup = func() { _ = os.RemoveAll(parent) }
	repoDir = filepath.Join(parent, "repo.git")
	args := []string{"clone", "--bare"}
	if mode == "shallow" {
		args = append(args, "--depth", "1")
	} else if maxCommits > 0 {
		args = append(args, "--depth", strconv.Itoa(maxCommits))
	}
	args = append(args, "--", source, repoDir)
	logf("cloning repository locally...")
	cmd := exec.CommandContext(ctx, "git", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		cleanup()
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", "", nil, fmt.Errorf("git clone %s: %s", source, msg)
	}
	return repoDir, source, cleanup, nil
}

func uploadLocalImportBlobs(
	ctx context.Context,
	repoDir string,
	blobIDs []string,
	known map[string]localImportBlobMeta,
	tm *TransferManager,
	retries int,
	logf func(string, ...any),
) error {
	type chunkEntry struct {
		gitBlobID   string
		contentHash string
		blobID      string
		size        int64
		data        []byte
	}
	var chunk []chunkEntry
	var chunkBytes int64
	uploadedTotal := 0
	lastLog := time.Now()

	flushChunk := func() error {
		if len(chunk) == 0 {
			return nil
		}
		byHash := make(map[string][]byte, len(chunk))
		edits := make([]*corev1.FileEdit, 0, len(chunk))
		for _, entry := range chunk {
			if _, exists := byHash[entry.contentHash]; !exists {
				byHash[entry.contentHash] = entry.data
				edits = append(edits, &corev1.FileEdit{
					Op:          "upsert",
					ContentHash: entry.contentHash,
				})
			}
		}
		if err := retryLocalImportCall(ctx, retries, logf, "upload blobs", func() error {
			for _, e := range edits {
				e.BlobId = ""
			}
			_, err := tm.AttachAndUploadBlobs(ctx, edits, BlobBytesSources(byHash), nil, defaultUploadConcurrency())
			return err
		}); err != nil {
			return err
		}
		for _, entry := range chunk {
			known[entry.gitBlobID] = localImportBlobMeta{
				BlobID:      entry.blobID,
				ContentHash: entry.contentHash,
				Size:        entry.size,
			}
		}
		uploadedTotal += len(chunk)
		if len(blobIDs) > localImportBlobChunkCount && (time.Since(lastLog) >= 2*time.Second || uploadedTotal == len(blobIDs)) {
			logf("  uploaded %d/%d blobs", uploadedTotal, len(blobIDs))
			lastLog = time.Now()
		}
		clear(chunk)
		chunk = chunk[:0]
		chunkBytes = 0
		return nil
	}

	beforeRead := func(size int64) error {
		if len(chunk) > 0 && (chunkBytes+size > localImportBlobChunkBytes || len(chunk) >= localImportBlobChunkCount) {
			return flushChunk()
		}
		return nil
	}
	onBlob := func(gitBlobID string, data []byte) error {
		chunk = append(chunk, chunkEntry{
			gitBlobID:   gitBlobID,
			contentHash: objectid.RawContentHash(data),
			blobID:      objectid.BlobID(data),
			size:        int64(len(data)),
			data:        data,
		})
		chunkBytes += int64(len(data))
		return nil
	}
	if err := streamLocalGitBlobs(ctx, repoDir, blobIDs, beforeRead, onBlob); err != nil {
		return err
	}
	return flushChunk()
}

func streamLocalGitBlobs(
	ctx context.Context,
	repoDir string,
	blobIDs []string,
	beforeRead func(size int64) error,
	onBlob func(blobID string, data []byte) error,
) error {
	if len(blobIDs) == 0 {
		return nil
	}
	cmd := exec.CommandContext(ctx, "git", "cat-file", "--batch")
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
		w := bufio.NewWriter(stdin)
		for _, id := range blobIDs {
			if _, err := fmt.Fprintln(w, id); err != nil {
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
	reader := bufio.NewReader(stdout)
	for range blobIDs {
		header, err := reader.ReadString('\n')
		if err != nil {
			abort()
			return err
		}
		fields := strings.Fields(header)
		if len(fields) != 3 || fields[1] != "blob" {
			abort()
			return fmt.Errorf("unexpected git cat-file header %q", strings.TrimSpace(header))
		}
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || size < 0 {
			abort()
			return fmt.Errorf("invalid git blob size %q", fields[2])
		}
		if size > localImportMaxBlobBytes {
			abort()
			return fmt.Errorf("git blob %s is %d bytes, exceeds %d byte limit", fields[0], size, localImportMaxBlobBytes)
		}
		if beforeRead != nil {
			if err := beforeRead(size); err != nil {
				abort()
				return err
			}
		}
		data := make([]byte, size)
		if _, err := io.ReadFull(reader, data); err != nil {
			abort()
			return err
		}
		trailing, err := reader.ReadByte()
		if err != nil || trailing != '\n' {
			abort()
			return fmt.Errorf("malformed git cat-file payload for %s", fields[0])
		}
		if err := onBlob(fields[0], data); err != nil {
			abort()
			return err
		}
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
		return fmt.Errorf("git cat-file --batch: %s", msg)
	}
	return nil
}

func retryLocalImportCall(ctx context.Context, retries int, logf func(string, ...any), label string, fn func() error) error {
	for attempt := 0; ; attempt++ {
		err := fn()
		if err == nil {
			return nil
		}
		if !importRetryable(err) || attempt >= retries || ctx.Err() != nil {
			return err
		}
		delay := importRetryBaseDelay << attempt
		if delay > importRetryMaxDelay || delay <= 0 {
			delay = importRetryMaxDelay
		}
		logf("%s failed (%s); retrying in %s (%d/%d)...", label, oneLineSummary(grpcstatus.Convert(err).Message()), delay, attempt+1, retries)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
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
	meta := localImportCommitMeta{
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
		meta.Subject = "Import git commit " + shortID(commitID)
	}
	return meta, nil
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

func localGitDeltaMetadata(ctx context.Context, repoDir, previousCommitID, commitID, mountPath string) ([]string, []string, map[string]localImportFile, []string, error) {
	raw, err := localGitOutputBytes(ctx, repoDir, "diff-tree", "-r", "-z", "--no-commit-id", "--name-status", "--find-renames", previousCommitID, commitID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	deleted, upsertGitPaths, err := parseLocalGitDiffNameStatus(raw)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	snapshot := map[string]localImportFile{}
	seen := map[string]struct{}{}
	var blobIDs []string
	const chunkSize = 512
	for start := 0; start < len(upsertGitPaths); start += chunkSize {
		end := min(start+chunkSize, len(upsertGitPaths))
		args := []string{"ls-tree", "-r", "-z", "--full-tree", commitID, "--"}
		for _, p := range upsertGitPaths[start:end] {
			args = append(args, ":(literal)"+p)
		}
		treeRaw, err := localGitOutputBytes(ctx, repoDir, args...)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		blobIDs, err = parseLocalGitTreeRecords(treeRaw, mountPath, snapshot, seen, blobIDs)
		if err != nil {
			return nil, nil, nil, nil, err
		}
	}
	return deleted, upsertGitPaths, snapshot, blobIDs, nil
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

func parseLocalGitDiffNameStatus(raw []byte) ([]string, []string, error) {
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
		switch statusValue[0] {
		case 'A', 'C', 'M', 'T':
			if i >= len(tokens) {
				return nil, nil, fmt.Errorf("malformed git diff status %q", statusValue)
			}
			upsertSet[string(tokens[i])] = struct{}{}
			i++
		case 'D':
			if i >= len(tokens) {
				return nil, nil, fmt.Errorf("malformed git diff status %q", statusValue)
			}
			deletedSet[string(tokens[i])] = struct{}{}
			i++
		case 'R':
			if i+1 >= len(tokens) {
				return nil, nil, fmt.Errorf("malformed git rename status %q", statusValue)
			}
			deletedSet[string(tokens[i])] = struct{}{}
			upsertSet[string(tokens[i+1])] = struct{}{}
			i += 2
		default:
			return nil, nil, fmt.Errorf("unsupported git diff status %q", statusValue)
		}
	}
	deleted := make([]string, 0, len(deletedSet))
	for p := range deletedSet {
		if _, upserted := upsertSet[p]; !upserted {
			deleted = append(deleted, p)
		}
	}
	upserts := make([]string, 0, len(upsertSet))
	for p := range upsertSet {
		upserts = append(upserts, p)
	}
	sort.Strings(deleted)
	sort.Strings(upserts)
	return deleted, upserts, nil
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
