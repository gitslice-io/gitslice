package cli

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"gitslice.io/gitslice/internal/grpccomp"
	"gitslice.io/gitslice/internal/objectid"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

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
