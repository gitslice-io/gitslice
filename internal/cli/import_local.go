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
	"sync"
	"time"

	"gitslice.io/gitslice/internal/grpccomp"
	"gitslice.io/gitslice/internal/model"
	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/paths"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

const (
	defaultLocalImportBatchSize = 250
	maxLocalImportBatchSize     = 1000
	localImportMaxBatchEdits    = 20000
	localImportBlobChunkBytes   = 32 << 20
	localImportBlobChunkCount   = 4096
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

type preparedLocalBatch struct {
	commits   []*corev1.ImportGitCommitInput
	firstPos  int
	lastPos   int
	lastGitID string
}

type localGitCommitPair struct {
	position int
	prevID   string
	commitID string
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

	// Partition commits into already-completed (skipped), optional initial
	// commit (when nothing has been imported yet), and delta pairs.
	var initialPos int
	var initialGitID string
	var deltaPairs []localGitCommitPair
	var previousGitCommitID string
	skippedCount := 0
	for i, gitCommitID := range gitCommits {
		if existing, ok := completed[gitCommitID]; ok {
			response.Commits = append(response.Commits, existing)
			response.FinalCommitId = existing.GetNativeCommitId()
			previousGitCommitID = gitCommitID
			skippedCount++
			continue
		}
		if previousGitCommitID == "" {
			initialPos = i + 1
			initialGitID = gitCommitID
		} else {
			deltaPairs = append(deltaPairs, localGitCommitPair{
				position: i + 1,
				prevID:   previousGitCommitID,
				commitID: gitCommitID,
			})
		}
		previousGitCommitID = gitCommitID
	}
	if skippedCount > 0 {
		if initialGitID == "" && len(deltaPairs) == 0 {
			logf("all %d commit(s) were already imported", skippedCount)
		} else {
			logf("resumed: skipped %d already-imported commit(s)", skippedCount)
		}
	}

	startedAt := time.Now()
	importedThisRun := 0

	if initialGitID != "" || len(deltaPairs) > 0 {
		pipeCtx, cancelPipe := context.WithCancel(callCtx)
		defer cancelPipe()
		readyCh := make(chan preparedLocalBatch, 2)
		prodErrCh := make(chan error, 1)

		go func() {
			defer close(readyCh)
			known := make(map[string]localImportBlobMeta)
			var pending []pendingLocalCommit
			var pendingBlobIDs []string
			pendingBlobSeen := map[string]struct{}{}
			pendingEditsCount := 0

			emitBatch := func() error {
				if len(pending) == 0 {
					return nil
				}
				if len(pendingBlobIDs) > 0 {
					if err := uploadLocalImportBlobs(pipeCtx, repoDir, pendingBlobIDs, known, tm, retries, logf); err != nil {
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
				batch := preparedLocalBatch{
					commits:   commitInputs,
					firstPos:  pending[0].position,
					lastPos:   pending[len(pending)-1].position,
					lastGitID: pending[len(pending)-1].gitCommitID,
				}
				pending = pending[:0]
				pendingBlobIDs = pendingBlobIDs[:0]
				clear(pendingBlobSeen)
				pendingEditsCount = 0

				select {
				case readyCh <- batch:
					return nil
				case <-pipeCtx.Done():
					return pipeCtx.Err()
				}
			}

			addCommit := func(item pendingLocalCommit, commitBlobIDs []string, commitEditEstimate int) error {
				if len(pending) > 0 && (len(pending) >= batchSize || pendingEditsCount+commitEditEstimate > localImportMaxBatchEdits || len(pendingBlobIDs) >= localImportBlobChunkCount) {
					if err := emitBatch(); err != nil {
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
				if commitEditEstimate > localImportMaxBatchEdits {
					return emitBatch()
				}
				return nil
			}

			if initialGitID != "" {
				meta, err := localGitCommitMetadata(pipeCtx, repoDir, initialGitID)
				if err != nil {
					prodErrCh <- fmt.Errorf("read git commit %s metadata: %w", shortID(initialGitID), err)
					return
				}
				snapshot, commitBlobIDs, err := localGitSnapshotMetadata(pipeCtx, repoDir, initialGitID, mountPath)
				if err != nil {
					prodErrCh <- fmt.Errorf("read git commit %s tree: %w", shortID(initialGitID), err)
					return
				}
				item := pendingLocalCommit{
					gitCommitID: initialGitID,
					position:    initialPos,
					meta:        meta,
					isInitial:   true,
					snapshot:    snapshot,
				}
				if err := addCommit(item, commitBlobIDs, len(snapshot)+len(previousMounted)); err != nil {
					prodErrCh <- err
					return
				}
			}

			if len(deltaPairs) > 0 {
				if err := streamLocalGitDeltas(pipeCtx, repoDir, deltaPairs, mountPath, func(item pendingLocalCommit, commitBlobIDs []string) error {
					return addCommit(item, commitBlobIDs, len(item.deleted)+len(item.snapshot))
				}); err != nil {
					prodErrCh <- err
					return
				}
			}

			if err := emitBatch(); err != nil {
				prodErrCh <- err
				return
			}
			prodErrCh <- nil
		}()

		var consumeErr error
		for batch := range readyCh {
			var batchRes *corev1.ImportGitCommitBatchResponse
			req := &corev1.ImportGitCommitBatchRequest{
				ImportId:       beginRes.GetImportId(),
				AuthoringSlice: ref,
				MountPath:      mountPath,
				TargetRef:      model.DefaultTargetRef,
				Commits:        batch.commits,
			}
			if err := retryLocalImportCall(ctx, retries, logf, fmt.Sprintf("publish commits %d-%d", batch.firstPos, batch.lastPos), func() error {
				batchCtx, cancel := context.WithTimeout(callCtx, 5*time.Minute)
				defer cancel()
				var callErr error
				batchRes, callErr = repoClient.ImportGitCommitBatch(batchCtx, req, grpc.UseCompressor(grpccomp.ZstdName))
				return callErr
			}); err != nil {
				consumeErr = err
				cancelPipe()
				break
			}
			for _, c := range batchRes.GetCommits() {
				response.Commits = append(response.Commits, c)
				response.FinalCommitId = c.GetNativeCommitId()
			}
			importedThisRun += len(batch.commits)
			elapsed := time.Since(startedAt).Seconds()
			rate := float64(importedThisRun) / max(elapsed, 0.001)
			logf("published %d/%d %s -> %s (%d commit(s), %.1f commits/s)",
				batch.lastPos, len(gitCommits), shortID(batch.lastGitID), shortID(response.FinalCommitId), len(batch.commits), rate)
		}
		// Drain readyCh so producer goroutine can exit cleanly on cancellation.
		for range readyCh {
		}
		prodErr := <-prodErrCh
		if consumeErr != nil {
			return consumeErr
		}
		if prodErr != nil {
			return prodErr
		}
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

const (
	localImportUploadChunkBytes = 8 << 20
	localImportUploadChunkBlobs = 1000
	localImportUploadWorkers    = 3
)

func uploadLocalImportBlobs(
	ctx context.Context,
	repoDir string,
	blobIDs []string,
	known map[string]localImportBlobMeta,
	tm *TransferManager,
	retries int,
	logf func(string, ...any),
) error {
	if len(blobIDs) == 0 {
		return nil
	}
	type rawBlob struct {
		gitBlobID string
		data      []byte
	}
	type hashedBlob struct {
		gitBlobID   string
		contentHash string
		blobID      string
		size        int64
		data        []byte
	}

	g, gctx := errgroup.WithContext(ctx)
	chunkCh := make(chan []rawBlob, localImportUploadWorkers)

	var mu sync.Mutex
	uploadedTotal := 0
	lastLog := time.Now()

	for w := 0; w < localImportUploadWorkers; w++ {
		g.Go(func() error {
			for chunk := range chunkCh {
				entries := make([]hashedBlob, len(chunk))
				byHash := make(map[string][]byte, len(chunk))
				uniqueHashes := make([]string, 0, len(chunk))
				for i, item := range chunk {
					h := objectid.NewBlobHasher()
					_, _ = h.Write(item.data)
					contentHash := h.RawContentHash()
					entries[i] = hashedBlob{
						gitBlobID:   item.gitBlobID,
						contentHash: contentHash,
						blobID:      h.BlobID(),
						size:        int64(len(item.data)),
						data:        item.data,
					}
					if _, exists := byHash[contentHash]; !exists {
						byHash[contentHash] = item.data
						uniqueHashes = append(uniqueHashes, contentHash)
					}
				}
				if err := uploadLocalImportChunk(gctx, tm, uniqueHashes, byHash, retries, logf); err != nil {
					return err
				}
				mu.Lock()
				for _, entry := range entries {
					known[entry.gitBlobID] = localImportBlobMeta{
						BlobID:      entry.blobID,
						ContentHash: entry.contentHash,
						Size:        entry.size,
					}
				}
				uploadedTotal += len(entries)
				if len(blobIDs) > localImportBlobChunkCount && (time.Since(lastLog) >= 2*time.Second || uploadedTotal == len(blobIDs)) {
					logf("  uploaded %d/%d blobs", uploadedTotal, len(blobIDs))
					lastLog = time.Now()
				}
				mu.Unlock()
			}
			return nil
		})
	}

	var cur []rawBlob
	var curBytes int64
	sendChunk := func() error {
		if len(cur) == 0 {
			return nil
		}
		batch := cur
		cur = nil
		curBytes = 0
		select {
		case chunkCh <- batch:
			return nil
		case <-gctx.Done():
			return gctx.Err()
		}
	}

	beforeRead := func(size int64) error {
		if len(cur) > 0 && (curBytes+size > localImportUploadChunkBytes || len(cur) >= localImportUploadChunkBlobs) {
			return sendChunk()
		}
		return nil
	}
	onBlob := func(gitBlobID string, data []byte) error {
		cur = append(cur, rawBlob{gitBlobID: gitBlobID, data: data})
		curBytes += int64(len(data))
		return nil
	}
	readErr := streamLocalGitBlobs(gctx, repoDir, blobIDs, beforeRead, onBlob)
	if readErr == nil {
		readErr = sendChunk()
	}
	close(chunkCh)
	workerErr := g.Wait()
	if workerErr != nil {
		return workerErr
	}
	return readErr
}

func uploadLocalImportChunk(
	ctx context.Context,
	tm *TransferManager,
	uniqueHashes []string,
	byHash map[string][]byte,
	retries int,
	logf func(string, ...any),
) error {
	if len(uniqueHashes) == 0 {
		return nil
	}
	available := map[string]bool{}
	if err := retryLocalImportCall(ctx, retries, logf, "check blob status", func() error {
		statusRes, err := tm.Blob.GetBlobStatus(ctx, &corev1.GetBlobStatusRequest{
			ContentHashes: uniqueHashes,
			Slice:         tm.SliceRef,
		})
		if err != nil {
			if grpcstatus.Code(err) == codes.Unimplemented {
				return nil
			}
			return err
		}
		for _, rec := range statusRes.GetBlobs() {
			if rec.GetId() != "" && rec.GetState() == "available" {
				available[rec.GetContentHash()] = true
			}
		}
		return nil
	}); err != nil {
		return err
	}

	var small []*corev1.UploadBlobsEntry
	var large []string
	for _, h := range uniqueHashes {
		if available[h] {
			continue
		}
		data := byHash[h]
		if int64(len(data)) > blobStreamThresholdBytes {
			large = append(large, h)
			continue
		}
		small = append(small, &corev1.UploadBlobsEntry{
			ContentHash: h,
			Data:        data,
		})
	}

	if len(small) > 0 {
		req := &corev1.UploadBlobsRequest{
			Slice: tm.SliceRef,
			Blobs: small,
		}
		if err := retryLocalImportCall(ctx, retries, logf, "upload blobs", func() error {
			callCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
			defer cancel()
			_, err := tm.Blob.UploadBlobs(callCtx, req, grpc.UseCompressor(grpccomp.ZstdName))
			return err
		}); err != nil {
			return err
		}
	}

	for _, h := range large {
		data := byHash[h]
		if err := retryLocalImportCall(ctx, retries, logf, "upload large blob", func() error {
			_, err := uploadBlobReader(ctx, tm.Blob, tm.SliceRef, h, int64(len(data)), bytes.NewReader(data))
			return err
		}); err != nil {
			return err
		}
	}
	return nil
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
			meta := localImportCommitMeta{
				AuthorName:  strings.TrimSpace(fields[2]),
				AuthorEmail: strings.TrimSpace(fields[3]),
				Subject:     strings.TrimSpace(fields[5]),
				FullMessage: strings.TrimRight(fields[6], " \t\r\n"),
			}
			if authored, err := time.Parse(time.RFC3339, strings.TrimSpace(fields[4])); err == nil {
				meta.AuthoredAt = authored.UTC().Format(time.RFC3339)
			}
			if _, body, ok := strings.Cut(meta.FullMessage, "\n"); ok {
				meta.Body = strings.TrimSpace(body)
			}
			if meta.Subject == "" {
				meta.Subject = "Import git commit " + shortID(pair.commitID)
			}
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
