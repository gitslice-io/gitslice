package cli

import (
	"bytes"
	"context"
	"fmt"
	"gitslice.io/gitslice/internal/cli/clibase"
	"gitslice.io/gitslice/internal/workspace"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gitslice.io/gitslice/internal/clientcache"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

type hydrateResult struct {
	FileCount   int `json:"file_count"`
	CacheHits   int `json:"cache_hits"`
	CacheMisses int `json:"cache_misses"`
}

func (r Runner) hydrateWorkspacePaths(ctx context.Context, conn *grpc.ClientConn, ws WorkspaceConfig, commitID string, requested []string, cache *clientcache.ObjectCache) (hydrateResult, error) {
	return r.hydrateWith(ctx, conn, ws, commitID, cache, func(h *workspaceHydrator) error {
		for _, requestedPath := range requested {
			globalPath, err := workspaceInputToGlobalPath(ws, requestedPath)
			if err != nil {
				return err
			}
			if err := h.hydratePath(ctx, globalPath); err != nil {
				return err
			}
		}
		return nil
	})
}

// hydrateListing writes the files of a listing Checkout streamed: everything
// under the workspace's included paths.
func (r Runner) hydrateListing(ctx context.Context, conn *grpc.ClientConn, ws WorkspaceConfig, checkout *receivedCheckout, cache *clientcache.ObjectCache) (hydrateResult, error) {
	entries := checkout.entries
	return r.hydrateWith(ctx, conn, ws, checkout.commitID, cache, func(h *workspaceHydrator) error {
		h.fetched = checkout.fetched
		for _, included := range ws.IncludedPaths {
			prefix := strings.TrimSuffix(included, "/") + "/"
			for _, entry := range entries {
				if strings.HasPrefix(entry.GetPath(), prefix) {
					if err := h.makeDirectory(included); err != nil {
						return err
					}
					break
				}
			}
		}
		return h.addListed(entries)
	})
}

// hydrateWith writes the files add queues (and those it lists below them),
// then records them in the base snapshot and the stat cache.
func (r Runner) hydrateWith(ctx context.Context, conn *grpc.ClientConn, ws WorkspaceConfig, commitID string, cache *clientcache.ObjectCache, add func(*workspaceHydrator) error) (hydrateResult, error) {
	base, err := r.readBaseSnapshot()
	if err != nil {
		return hydrateResult{}, err
	}
	if base.CommitID == "" {
		base.CommitID = commitID
	}
	if base.Files == nil {
		base.Files = map[string]BaseSnapshotFile{}
	}
	repo := corev1.NewRepositoryServiceClient(conn)
	root, err := r.workspaceRoot()
	if err != nil {
		return hydrateResult{}, err
	}
	hydrator := workspaceHydrator{
		runner:   r,
		root:     root,
		repo:     repo,
		blob:     corev1.NewBlobServiceClient(conn),
		cache:    cache,
		ws:       ws,
		commitID: commitID,
		base:     base,
		progress: startProgress(ctx, "hydrating", "files", 0),
	}
	defer hydrator.progress.Finish()
	if err := add(&hydrator); err != nil {
		return hydrateResult{}, err
	}
	if err := hydrator.materialize(ctx); err != nil {
		return hydrateResult{}, err
	}
	// The files just written need not be hashed by the next scan.
	if len(hydrator.stats) > 0 {
		cache := workspace.ReadStatCache(root)
		for rel, entry := range hydrator.stats {
			cache.Files[rel] = entry
		}
		workspace.WriteStatCache(root, cache.Files)
	}
	if err := r.writeBaseSnapshot(hydrator.base); err != nil {
		return hydrateResult{}, err
	}
	return hydrator.result, nil
}

type workspaceHydrator struct {
	runner   Runner
	root     string
	repo     corev1.RepositoryServiceClient
	blob     corev1.BlobServiceClient
	cache    *clientcache.ObjectCache
	ws       WorkspaceConfig
	commitID string
	base     BaseSnapshot
	result   hydrateResult
	progress *clibase.ProgressReporter
	// pending are the files hydratePath found; materialize writes them.
	pending []*corev1.TreeEntry
	// stats are the written files' stat cache entries, by relative path.
	stats map[string]workspace.StatCacheEntry
	// fetched are contents just downloaded into the cache (by Checkout);
	// they count as misses, not hits (true until counted).
	fetched map[string]bool
	// mu guards base and result while materialize's batches write files.
	mu sync.Mutex
}

func (h *workspaceHydrator) hydratePath(ctx context.Context, globalPath string) error {
	// One recursive listing says what is at the path (a file lists as
	// itself) and everything under it (design/28_benchmarks.md).
	entries, recursive, err := listDirectoryPages(ctx, h.repo, &corev1.ListDirectoryRequest{CommitId: h.commitID, Path: globalPath, PageSize: 10000, Recursive: true})
	if err != nil {
		return err
	}
	if !recursive {
		return fmt.Errorf("the server listed %s one level only; upgrade the server", globalPath)
	}
	if len(entries) == 1 && entries[0].Kind == corev1.EntryKind_ENTRY_KIND_FILE && entries[0].Path == globalPath {
		h.queueFile(entries[0])
		return nil
	}
	if len(entries) == 0 {
		// An empty directory, or nothing there.
		resolved, err := h.repo.ResolvePath(ctx, &corev1.ResolvePathRequest{CommitId: h.commitID, Path: globalPath})
		if grpcstatus.Code(err) == codes.NotFound || (err == nil && resolved.GetEntry().GetKind() != corev1.EntryKind_ENTRY_KIND_DIRECTORY) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	if err := h.makeDirectory(globalPath); err != nil {
		return err
	}
	return h.addListed(entries)
}

// addListed queues a recursive listing's files and creates its directories.
func (h *workspaceHydrator) addListed(entries []*corev1.TreeEntry) error {
	for _, entry := range entries {
		switch entry.GetKind() {
		case corev1.EntryKind_ENTRY_KIND_FILE:
			h.queueFile(entry)
		case corev1.EntryKind_ENTRY_KIND_DIRECTORY:
			if err := h.makeDirectory(entry.GetPath()); err != nil {
				return err
			}
		}
	}
	return nil
}

func (h *workspaceHydrator) makeDirectory(globalPath string) error {
	if strings.TrimSpace(globalPath) == "/" {
		return nil
	}
	rel, err := workspaceRelPath(h.ws, globalPath)
	if err != nil {
		return err
	}
	_, err = ensureConfinedDir(h.root, rel)
	return err
}

// queueFile adds a file for materialize to write.
func (h *workspaceHydrator) queueFile(entry *corev1.TreeEntry) {
	h.pending = append(h.pending, entry)
}

func (h *workspaceHydrator) fileTarget(entry *corev1.TreeEntry) (string, string, uint32, error) {
	rel, err := workspaceRelPath(h.ws, entry.Path)
	if err != nil {
		return "", "", 0, err
	}
	if err := ensureNotReservedGsPath(rel); err != nil {
		return "", "", 0, err
	}
	parentRel := filepath.ToSlash(filepath.Dir(filepath.FromSlash(rel)))
	if _, err := ensureConfinedDir(h.root, parentRel); err != nil {
		return "", "", 0, err
	}
	target := filepath.Join(h.root, filepath.FromSlash(rel))
	mode := uint32(entry.Mode)
	if mode == 0 {
		mode = 0o100644
	}
	return rel, target, mode, nil
}

func (h *workspaceHydrator) writeFile(entry *corev1.TreeEntry, data []byte) error {
	rel, _, mode, err := h.fileTarget(entry)
	if err != nil {
		return err
	}
	target, _, err := writeConfinedWorkspaceFile(h.root, rel, mode, bytes.NewReader(data))
	if err != nil {
		return err
	}
	h.recordFile(entry, rel, target, mode, int64(len(data)))
	return nil
}

func (h *workspaceHydrator) writeLargeFile(ctx context.Context, entry *corev1.TreeEntry) error {
	rel, target, mode, err := h.fileTarget(entry)
	if err != nil {
		return err
	}
	size, err := h.hydrateLargeFile(ctx, entry, rel, mode)
	if err != nil {
		return err
	}
	h.recordFile(entry, rel, target, mode, size)
	return nil
}

func (h *workspaceHydrator) recordFile(entry *corev1.TreeEntry, rel, target string, mode uint32, size int64) {
	info, statErr := os.Lstat(target)
	h.mu.Lock()
	if statErr == nil {
		if stat, ok := workspace.StatCacheEntryFor(info, entry.ContentHash); ok {
			if h.stats == nil {
				h.stats = map[string]workspace.StatCacheEntry{}
			}
			h.stats[rel] = stat
		}
	}
	h.base.Files[entry.Path] = BaseSnapshotFile{
		Path:        entry.Path,
		RelPath:     rel,
		ContentHash: entry.ContentHash,
		Mode:        mode,
		Size:        size,
	}
	h.result.FileCount++
	h.mu.Unlock()
	h.progress.Add(1)
}

// writeWorkspaceFile materializes one file. A symlink whose target is a
// safe relative path inside the workspace becomes a real symlink (its content
// is the link target, as in Git). Other symlinks are written as regular files
// containing the target; the workspace scan treats that fallback as unchanged.
// A regular file never writes through an existing symlink at the same path.
func writeWorkspaceFile(target string, mode uint32, r io.Reader) (int64, error) {
	root, rel, err := workspace.InferConfiningRootForTarget(target)
	if err != nil {
		return 0, err
	}
	_, n, err := writeConfinedWorkspaceFile(root, rel, mode, r)
	return n, err
}
