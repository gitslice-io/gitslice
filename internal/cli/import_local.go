package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/grpccomp"
	"gitslice.io/gitslice/internal/model"
	"gitslice.io/gitslice/internal/paths"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
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
