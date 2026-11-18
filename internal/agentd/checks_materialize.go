package agentd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"gitslice.io/gitslice/internal/clientcache"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
)

const (
	CheckMaterializePageSize        = 1000
	CheckMaterializeDirConcurrency  = 32
	CheckMaterializeReadConcurrency = 32
)

type RepoClient interface {
	ListDirectory(ctx context.Context, req *corev1.ListDirectoryRequest, opts ...grpc.CallOption) (*corev1.ListDirectoryResponse, error)
	ReadFile(ctx context.Context, req *corev1.ReadFileRequest, opts ...grpc.CallOption) (*corev1.ReadFileResponse, error)
}

type BatchReadClient interface {
	ReadFiles(ctx context.Context, req *corev1.ReadFilesRequest, opts ...grpc.CallOption) (*corev1.ReadFilesResponse, error)
}

type BatchReadTarget struct {
	Path        string
	ContentHash string
	Size        int64
}

type BatchFetcherFunc func(ctx context.Context, repo BatchReadClient, cache *clientcache.ObjectCache, rootTreeID string, targets []BatchReadTarget, workers int) error

type CheckTreeWriter struct {
	EnsureDir func(root, relDir string) (string, error)
	WriteFile func(root, rel string, mode uint32, r io.Reader) (string, int64, error)
}

func defaultCheckTreeWriter() CheckTreeWriter {
	return CheckTreeWriter{
		EnsureDir: defaultEnsureConfinedDir,
		WriteFile: defaultWriteConfinedFile,
	}
}

func MaterializeCheckTree(ctx context.Context, repo RepoClient, cache *clientcache.ObjectCache, fetchBatch BatchFetcherFunc, writer CheckTreeWriter, rootTreeID string, paths []string, destRoot string) error {
	if repo == nil {
		return fmt.Errorf("repository client is required")
	}
	if strings.TrimSpace(rootTreeID) == "" {
		return fmt.Errorf("root tree id is required")
	}
	if strings.TrimSpace(destRoot) == "" {
		return fmt.Errorf("destination root is required")
	}
	if writer.EnsureDir == nil || writer.WriteFile == nil {
		def := defaultCheckTreeWriter()
		if writer.EnsureDir == nil {
			writer.EnsureDir = def.EnsureDir
		}
		if writer.WriteFile == nil {
			writer.WriteFile = def.WriteFile
		}
	}
	prefixes, err := NormalizeCheckMaterializePrefixes(paths)
	if err != nil {
		return err
	}
	if len(prefixes) == 0 {
		prefixes = []string{"/"}
	}
	tree, ok := repo.(CheckTreeRepoClient)
	if !ok {
		tree = CheckTreeRepoClient{Inner: repo}
	}
	plan := NewCheckMaterializePlan()
	err = tree.StreamTree(ctx, rootTreeID, prefixes, plan, cache)
	switch {
	case errors.Is(err, ErrNoTreeCheckout):
		plan = NewCheckMaterializePlan()
		if err := walkCheckTree(ctx, repo, rootTreeID, prefixes, plan); err != nil {
			return err
		}
	case err != nil:
		return err
	}
	for _, dir := range plan.SortedDirs() {
		if err := ctx.Err(); err != nil {
			return err
		}
		if dir == "/" {
			if _, err := writer.EnsureDir(destRoot, "."); err != nil {
				return err
			}
			continue
		}
		if _, err := CheckTreeLocalPath(destRoot, dir); err != nil {
			return err
		}
		if _, err := writer.EnsureDir(destRoot, dir); err != nil {
			return err
		}
	}
	files := plan.SortedFiles()
	if err := prefetchCheckFiles(ctx, repo, cache, fetchBatch, rootTreeID, files); err != nil {
		return err
	}
	g, groupCtx := errgroup.WithContext(ctx)
	g.SetLimit(CheckMaterializeReadConcurrency)
	for _, entry := range files {
		entry := entry
		g.Go(func() error {
			return materializeCheckFile(groupCtx, repo, cache, writer, rootTreeID, entry, destRoot)
		})
	}
	return g.Wait()
}

func walkCheckTree(ctx context.Context, repo RepoClient, rootTreeID string, prefixes []string, plan *CheckMaterializePlan) error {
	for _, prefix := range prefixes {
		if prefix == "/" {
			if err := materializeCheckDirectory(ctx, repo, rootTreeID, "/", plan); err != nil {
				return err
			}
			continue
		}
		entry, err := lookupCheckTreeEntry(ctx, repo, rootTreeID, prefix)
		if err != nil {
			return err
		}
		switch entry.GetKind() {
		case corev1.EntryKind_ENTRY_KIND_DIRECTORY:
			entryPath, err := entryLogicalPath(entry)
			if err != nil {
				return err
			}
			if err := materializeCheckDirectory(ctx, repo, rootTreeID, entryPath, plan); err != nil {
				return err
			}
		case corev1.EntryKind_ENTRY_KIND_FILE:
			if err := plan.AddFile(entry); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported check tree entry kind for %s: %s", prefix, entryKindName(entry.GetKind()))
		}
	}
	return nil
}

func materializeCheckDirectory(ctx context.Context, repo RepoClient, rootTreeID, dir string, plan *CheckMaterializePlan) error {
	if plan == nil {
		return fmt.Errorf("check materialize plan is required")
	}
	dir, err := NormalizeCheckMaterializePath(dir)
	if err != nil {
		return err
	}
	g, groupCtx := errgroup.WithContext(ctx)
	g.SetLimit(CheckMaterializeDirConcurrency)
	g.Go(func() error {
		return materializeCheckDirectoryWalk(groupCtx, g, repo, rootTreeID, dir, plan)
	})
	return g.Wait()
}

func materializeCheckDirectoryWalk(ctx context.Context, g *errgroup.Group, repo RepoClient, rootTreeID, dir string, plan *CheckMaterializePlan) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := plan.AddDir(dir); err != nil {
		return err
	}
	entries, err := listCheckDirectoryAll(ctx, repo, &corev1.ListDirectoryRequest{
		RootTreeId: rootTreeID,
		Path:       logicalAbsPath(dir),
		PageSize:   CheckMaterializePageSize,
	})
	if err != nil {
		return err
	}
	var subdirs []string
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		switch entry.GetKind() {
		case corev1.EntryKind_ENTRY_KIND_DIRECTORY:
			entryPath, err := entryLogicalPath(entry)
			if err != nil {
				return err
			}
			subdirs = append(subdirs, entryPath)
		case corev1.EntryKind_ENTRY_KIND_FILE:
			if err := plan.AddFile(entry); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported check tree entry kind for %s: %s", entry.GetPath(), entryKindName(entry.GetKind()))
		}
	}
	for _, subdir := range subdirs {
		subdir := subdir
		if g.TryGo(func() error {
			return materializeCheckDirectoryWalk(ctx, g, repo, rootTreeID, subdir, plan)
		}) {
			continue
		}
		if err := materializeCheckDirectoryWalk(ctx, g, repo, rootTreeID, subdir, plan); err != nil {
			return err
		}
	}
	return nil
}

type CheckMaterializePlan struct {
	mu    sync.Mutex
	dirs  map[string]struct{}
	files map[string]*corev1.TreeEntry
}

func NewCheckMaterializePlan() *CheckMaterializePlan {
	return &CheckMaterializePlan{
		dirs:  map[string]struct{}{},
		files: map[string]*corev1.TreeEntry{},
	}
}

func (p *CheckMaterializePlan) AddDir(dir string) error {
	dir, err := NormalizeCheckMaterializePath(dir)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dirs[dir] = struct{}{}
	return nil
}

func (p *CheckMaterializePlan) AddFile(entry *corev1.TreeEntry) error {
	entryPath, err := entryLogicalPath(entry)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.files[entryPath] = entry
	p.dirs[checkMaterializeParentDir(entryPath)] = struct{}{}
	return nil
}

func (p *CheckMaterializePlan) SortedDirs() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.dirs))
	for dir := range p.dirs {
		out = append(out, dir)
	}
	sort.Slice(out, func(i, j int) bool {
		leftDepth := checkPathDepth(out[i])
		rightDepth := checkPathDepth(out[j])
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return out[i] < out[j]
	})
	return out
}

func (p *CheckMaterializePlan) SortedFiles() []*corev1.TreeEntry {
	p.mu.Lock()
	defer p.mu.Unlock()
	paths := make([]string, 0, len(p.files))
	for filePath := range p.files {
		paths = append(paths, filePath)
	}
	sort.Strings(paths)
	out := make([]*corev1.TreeEntry, 0, len(paths))
	for _, filePath := range paths {
		out = append(out, p.files[filePath])
	}
	return out
}

func prefetchCheckFiles(ctx context.Context, repo RepoClient, cache *clientcache.ObjectCache, fetchBatch BatchFetcherFunc, rootTreeID string, files []*corev1.TreeEntry) error {
	rf, ok := repo.(BatchReadClient)
	if !ok || cache == nil || fetchBatch == nil {
		return nil
	}
	if tree, isTree := repo.(CheckTreeRepoClient); isTree {
		if _, hasBatch := tree.Inner.(BatchReadClient); !hasBatch {
			return nil
		}
	}
	var targets []BatchReadTarget
	for _, entry := range files {
		hash := entry.GetContentHash()
		if hash == "" || cache.Exists(hash) {
			continue
		}
		entryPath, err := entryLogicalPath(entry)
		if err != nil {
			return err
		}
		targets = append(targets, BatchReadTarget{Path: logicalAbsPath(entryPath), ContentHash: hash, Size: entry.GetSize()})
	}
	if len(targets) == 0 {
		return nil
	}
	return fetchBatch(ctx, rf, cache, rootTreeID, targets, CheckMaterializeReadConcurrency/8)
}

func materializeCheckFile(ctx context.Context, repo RepoClient, cache *clientcache.ObjectCache, writer CheckTreeWriter, rootTreeID string, entry *corev1.TreeEntry, destRoot string) error {
	entryPath, err := entryLogicalPath(entry)
	if err != nil {
		return err
	}
	if _, err := CheckTreeTarget(destRoot, entryPath); err != nil {
		return err
	}
	data, err := materializeCheckFileBytes(ctx, repo, cache, rootTreeID, entryPath, entry.GetContentHash())
	if err != nil {
		return err
	}
	mode := entry.GetMode()
	if mode == 0 {
		mode = 0o100644
	}
	_, _, err = writer.WriteFile(destRoot, entryPath, mode, bytes.NewReader(data))
	return err
}

func materializeCheckFileBytes(ctx context.Context, repo RepoClient, cache *clientcache.ObjectCache, rootTreeID, entryPath, contentHash string) ([]byte, error) {
	if contentHash != "" && cache != nil {
		if cache.Exists(contentHash) {
			return cache.Read(contentHash)
		}
		read, err := repo.ReadFile(ctx, &corev1.ReadFileRequest{
			RootTreeId: rootTreeID,
			Path:       logicalAbsPath(entryPath),
		})
		if err != nil {
			return nil, err
		}
		cached, err := cache.PutBytes(read.GetData())
		if err != nil {
			return nil, err
		}
		if cached.ContentHash != contentHash {
			return nil, fmt.Errorf("check materialize content hash mismatch for %s: got %s, want %s", entryPath, cached.ContentHash, contentHash)
		}
		return read.GetData(), nil
	}
	read, err := repo.ReadFile(ctx, &corev1.ReadFileRequest{
		RootTreeId: rootTreeID,
		Path:       logicalAbsPath(entryPath),
	})
	if err != nil {
		return nil, err
	}
	return read.GetData(), nil
}

func defaultEnsureConfinedDir(root, relDir string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(absRoot, 0o755); err != nil {
		return "", err
	}
	normalized := strings.Trim(strings.TrimSpace(strings.ReplaceAll(relDir, "\\", "/")), "/")
	if normalized == "" || normalized == "." {
		return absRoot, nil
	}
	target, err := CheckTreeLocalPath(absRoot, normalized)
	if err != nil {
		return "", err
	}
	osRoot, err := os.OpenRoot(absRoot)
	if err != nil {
		return "", err
	}
	defer osRoot.Close()
	rel, err := filepath.Rel(absRoot, target)
	if err != nil {
		return "", err
	}
	if err := mkdirAllInRoot(osRoot, rel); err != nil {
		return "", err
	}
	return target, nil
}

func defaultWriteConfinedFile(root, rel string, mode uint32, r io.Reader) (string, int64, error) {
	target, err := CheckTreeTarget(root, rel)
	if err != nil {
		return "", 0, err
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", 0, err
	}
	osRoot, err := os.OpenRoot(absRoot)
	if err != nil {
		return "", 0, err
	}
	defer osRoot.Close()
	osRel, err := filepath.Rel(absRoot, target)
	if err != nil {
		return "", 0, err
	}
	if dir := filepath.Dir(osRel); dir != "." {
		if err := mkdirAllInRoot(osRoot, dir); err != nil {
			return "", 0, err
		}
	}
	fileMode := fs.FileMode(0o644)
	if mode&0o111 != 0 {
		fileMode = 0o755
	}
	f, err := osRoot.OpenFile(osRel, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, fileMode)
	if err != nil {
		return "", 0, err
	}
	n, copyErr := io.Copy(f, r)
	_ = f.Chmod(fileMode)
	closeErr := f.Close()
	if copyErr != nil {
		return target, n, copyErr
	}
	return target, n, closeErr
}

func mkdirAllInRoot(osRoot *os.Root, osRel string) error {
	if osRel == "" || osRel == "." {
		return nil
	}
	var cur string
	for _, part := range strings.Split(filepath.ToSlash(osRel), "/") {
		if part == "" || part == "." {
			continue
		}
		if part == ".." || part == ".gs" {
			return fmt.Errorf("refusing confined directory component %q", part)
		}
		if cur == "" {
			cur = part
		} else {
			cur = filepath.Join(cur, part)
		}
		if info, err := osRoot.Lstat(cur); err == nil {
			if info.Mode()&fs.ModeSymlink != 0 || !info.IsDir() {
				return fmt.Errorf("refusing to traverse non-directory ancestor %q", cur)
			}
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if mkErr := osRoot.Mkdir(cur, 0o755); mkErr != nil && !errors.Is(mkErr, os.ErrExist) {
			return mkErr
		}
	}
	return nil
}
