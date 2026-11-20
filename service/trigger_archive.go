package service

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"sort"
	"strings"

	"gitslice.io/gitslice/internal/objectstore/filesystem"
	gspaths "gitslice.io/gitslice/internal/paths"
	"gitslice.io/gitslice/internal/storage"
	"gitslice.io/gitslice/internal/triggers"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	maxAutomationArchiveFiles = 20_000
	maxAutomationArchiveBytes = 256 << 20 // 256 MiB uncompressed
)

// ServeArchiveHTTP streams a tar.gz checkout archive for an active bash
// automation run, authenticated strictly by Authorization: Bearer <run_token>.
func (s *TriggerRunService) ServeArchiveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token, ok := strings.CutPrefix(strings.TrimSpace(r.Header.Get("Authorization")), "Bearer ")
	if !ok || strings.TrimSpace(token) == "" {
		http.Error(w, "missing bearer run token", http.StatusUnauthorized)
		return
	}
	call, err := s.call(r.Context(), strings.TrimSpace(token))
	if err != nil {
		writeArchiveError(w, err)
		return
	}
	if call.spec.Trigger.Action.Kind != triggers.ActionBash || call.spec.Trigger.Action.Bash == nil {
		http.Error(w, "archive is only available for bash automations", http.StatusBadRequest)
		return
	}
	if s.Repository == nil || s.Tree.Objects == nil {
		http.Error(w, "tree storage is not configured", http.StatusServiceUnavailable)
		return
	}
	bash := call.spec.Trigger.Action.Bash
	if bash.Checkout == triggers.BashCheckoutNone {
		w.Header().Set("Content-Type", "application/gzip")
		gz := gzip.NewWriter(w)
		tw := tar.NewWriter(gz)
		_ = tw.Close()
		_ = gz.Close()
		return
	}

	// Always check out a landed tree (never an unapproved draft patchset).
	commitID := call.run.DefinitionCommitID
	if (call.run.EventKind == triggers.EventPush || call.run.EventKind == triggers.EventTagCreated) && call.run.CommitID != "" {
		commitID = call.run.CommitID
	}
	// A run tried from an open changeset (--from-changeset) checks out that
	// changeset's tree, which its script also came from; only those allowed
	// to run unlanded definitions start such runs.
	rootTreeID := call.spec.DefinitionTree
	if rootTreeID == "" {
		var err error
		if rootTreeID, err = s.Repository.RootTreeForCommit(r.Context(), commitID); err != nil {
			writeArchiveError(w, grpcError(err))
			return
		}
	}

	prefixes, err := checkoutPrefixes(call.run.Account, call.run.Folder, bash)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	entries, err := s.collectArchiveEntries(r.Context(), rootTreeID, call.run.Account, prefixes)
	if err != nil {
		writeArchiveError(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/gzip")
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	accountPrefix := "/" + call.run.Account + "/"
	kept := entries[:0:0]
	for _, entry := range entries {
		rel := strings.TrimPrefix(entry.Path, accountPrefix)
		if rel == "" || rel == entry.Path || strings.HasPrefix(rel, "/") || strings.Contains(rel, "..") {
			continue
		}
		kept = append(kept, entry)
	}
	// Blobs are fetched ahead, in parallel: one by one, a cold object store
	// takes minutes for a folder of a few thousand files, longer than the
	// container waits for its checkout.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	blobs, next := s.prefetchArchiveBlobs(ctx, kept)
	for i, entry := range kept {
		rel := strings.TrimPrefix(entry.Path, accountPrefix)
		blob := <-blobs[i]
		next()
		if blob.err != nil {
			_ = tw.Close()
			_ = gz.Close()
			return
		}
		rc := io.NopCloser(bytes.NewReader(blob.data))
		mode := int64(entry.Mode & 0o777)
		if mode == 0 {
			mode = 0o644
		}
		hdr := &tar.Header{
			Name: rel,
			Mode: mode,
			Size: entry.Size,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			_ = rc.Close()
			_ = tw.Close()
			_ = gz.Close()
			return
		}
		_, copyErr := io.CopyN(tw, rc, entry.Size)
		_ = rc.Close()
		if copyErr != nil {
			_ = tw.Close()
			_ = gz.Close()
			return
		}
	}
	_ = tw.Close()
	_ = gz.Close()
}

// archiveBlob is one prefetched file of a checkout archive.
type archiveBlob struct {
	data []byte
	err  error
}

// archivePrefetch is how many blobs a checkout archive reads ahead.
const archivePrefetch = 32

// prefetchArchiveBlobs reads the entries' blobs ahead of the writer, in
// parallel: the i-th channel gets the i-th entry's content. The writer calls
// next after taking each blob, which lets one more read start, so at most
// archivePrefetch blobs are read or waiting at a time.
func (s *TriggerRunService) prefetchArchiveBlobs(ctx context.Context, entries []storage.TreeEntry) (blobs []chan archiveBlob, next func()) {
	blobs = make([]chan archiveBlob, len(entries))
	for i := range blobs {
		blobs[i] = make(chan archiveBlob, 1)
	}
	credits := make(chan struct{}, archivePrefetch)
	for i := 0; i < archivePrefetch; i++ {
		credits <- struct{}{}
	}
	go func() {
		for i, entry := range entries {
			select {
			case <-credits:
			case <-ctx.Done():
				for ; i < len(entries); i++ {
					blobs[i] <- archiveBlob{err: ctx.Err()}
				}
				return
			}
			go func(i int, entry storage.TreeEntry) {
				blobs[i] <- s.readArchiveBlob(ctx, entry)
			}(i, entry)
		}
	}()
	return blobs, func() {
		select {
		case credits <- struct{}{}:
		default:
		}
	}
}

func (s *TriggerRunService) readArchiveBlob(ctx context.Context, entry storage.TreeEntry) archiveBlob {
	rc, err := s.Tree.Objects.Get(ctx, filesystem.BlobKey(entry.ContentHash), 0, 0)
	if err != nil {
		return archiveBlob{err: err}
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, entry.Size+1))
	if err != nil {
		return archiveBlob{err: err}
	}
	if int64(len(data)) != entry.Size {
		return archiveBlob{err: fmt.Errorf("blob of %s has %d bytes, want %d", entry.Path, len(data), entry.Size)}
	}
	return archiveBlob{data: data}
}

func checkoutPrefixes(account, folder string, bash *triggers.BashAction) ([]string, error) {
	accountRoot := "/" + strings.Trim(account, "/")
	if bash.Checkout == triggers.BashCheckoutAccount {
		return []string{accountRoot}, nil
	}
	prefixes := []string{folder}
	for _, raw := range bash.Include {
		target := raw
		if !strings.HasPrefix(target, "/") {
			target = path.Join(folder, target)
		}
		canonical, err := gspaths.CanonicalPrefix(target)
		if err != nil {
			return nil, fmt.Errorf("include path %q: %w", raw, err)
		}
		if !gspaths.Contains(accountRoot, canonical) {
			return nil, fmt.Errorf("include path %q is outside account %s", raw, account)
		}
		prefixes = append(prefixes, canonical)
	}
	return prefixes, nil
}

func (s *TriggerRunService) collectArchiveEntries(ctx context.Context, rootTreeID, account string, prefixes []string) ([]storage.TreeEntry, error) {
	seen := map[string]storage.TreeEntry{}
	var totalBytes int64
	for _, prefix := range prefixes {
		list, err := s.Repository.ListTreeAtTree(ctx, rootTreeID, prefix)
		if errors.Is(err, storage.ErrNotFound) {
			// Single-file include fallback.
			entry, getErr := s.Repository.GetEntryAtTree(ctx, rootTreeID, prefix)
			if errors.Is(getErr, storage.ErrNotFound) {
				continue
			}
			if getErr != nil {
				return nil, grpcError(getErr)
			}
			list = []storage.TreeEntry{*entry}
		} else if err != nil {
			return nil, grpcError(err)
		}
		for _, entry := range list {
			if entry.Kind == "tree" || entry.Kind == "dir" || entry.Kind == "directory" || entry.ContentHash == "" {
				continue
			}
			if _, exists := seen[entry.Path]; exists {
				continue
			}
			if len(seen) >= maxAutomationArchiveFiles {
				return nil, status.Errorf(codes.ResourceExhausted, "checkout exceeds %d files; set checkout: none or narrow folder/include", maxAutomationArchiveFiles)
			}
			if entry.Size < 0 || totalBytes+entry.Size > maxAutomationArchiveBytes {
				return nil, status.Errorf(codes.ResourceExhausted, "checkout exceeds %d MiB; set checkout: none or narrow folder/include", maxAutomationArchiveBytes>>20)
			}
			totalBytes += entry.Size
			seen[entry.Path] = entry
		}
	}
	out := make([]storage.TreeEntry, 0, len(seen))
	for _, entry := range seen {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func writeArchiveError(w http.ResponseWriter, err error) {
	st := status.Convert(err)
	code := http.StatusInternalServerError
	switch st.Code() {
	case codes.Unauthenticated:
		code = http.StatusUnauthorized
	case codes.PermissionDenied:
		code = http.StatusForbidden
	case codes.NotFound:
		code = http.StatusNotFound
	case codes.FailedPrecondition:
		code = http.StatusPreconditionFailed
	case codes.InvalidArgument:
		code = http.StatusBadRequest
	case codes.ResourceExhausted:
		code = http.StatusRequestEntityTooLarge
	case codes.Unavailable:
		code = http.StatusServiceUnavailable
	}
	http.Error(w, st.Message(), code)
}
