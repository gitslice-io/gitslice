// Package gitslicecfg centralizes .gitslice/*.yaml config spine resolution,
// bounded tree file reading, generic memoized YAML config caching, and
// patchset touched-path expansion.
package gitslicecfg

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"sort"
	"strings"
	"sync"

	"gitslice.io/gitslice/internal/objectstore"
	"gitslice.io/gitslice/internal/paths"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// DefaultMaxFileBytes is the default byte ceiling for reading a file from a tree.
const DefaultMaxFileBytes int64 = 4 << 20

var (
	// ErrNotFound reports a missing file in a tree.
	ErrNotFound = errors.New("file not found in tree")
	// ErrFileTooLarge reports a tree file exceeding TreeReader.MaxBytes.
	ErrFileTooLarge = errors.New("tree file exceeds size limit")
)

// FileTooLargeError describes a tree file that exceeded Limit bytes.
type FileTooLargeError struct {
	Path  string
	Limit int64
}

func (e *FileTooLargeError) Error() string {
	return fmt.Sprintf("file %s exceeds %d byte limit", e.Path, e.Limit)
}

func (e *FileTooLargeError) Is(target error) bool {
	return target == ErrFileTooLarge
}

// FileEntry holds the content metadata required to read a file from an object store.
type FileEntry struct {
	ContentHash string
	Size        int64
}

// LookupFileFunc resolves storagePath at rootTreeID, returning ok=false with nil
// error when the path does not exist.
type LookupFileFunc func(ctx context.Context, rootTreeID, storagePath string) (entry FileEntry, ok bool, err error)

// ObjectReader reads blob content from an object store.
type ObjectReader interface {
	Get(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error)
}

// TreeReader reads files from a repository tree with an enforced size bound and
// optional account-scoped logical path prefixing.
type TreeReader struct {
	LookupFile  LookupFileFunc
	Objects     ObjectReader
	MaxBytes    int64
	Account     string
	NotFoundErr error
}

// AccountStoragePath resolves logicalPath under /<account> when account is
// non-empty, or returns logicalPath unchanged when account is empty.
func AccountStoragePath(account, logicalPath string) string {
	account = strings.Trim(strings.ReplaceAll(strings.TrimSpace(account), "\\", "/"), "/")
	if account == "" {
		return logicalPath
	}
	cleaned := path.Clean("/" + strings.TrimSpace(logicalPath))
	if cleaned == "/" || cleaned == "." {
		return "/" + account
	}
	return "/" + path.Join(account, strings.TrimPrefix(cleaned, "/"))
}

func (r TreeReader) notFoundError() error {
	if r.NotFoundErr != nil {
		return r.NotFoundErr
	}
	return ErrNotFound
}

func (r TreeReader) maxBytes() int64 {
	if r.MaxBytes > 0 {
		return r.MaxBytes
	}
	return DefaultMaxFileBytes
}

// ReadFile reads p from rootTreeID, enforcing MaxBytes both against directory
// metadata and while streaming the underlying blob.
func (r TreeReader) ReadFile(ctx context.Context, rootTreeID, p string) ([]byte, error) {
	if r.LookupFile == nil || r.Objects == nil {
		return nil, errors.New("tree reader is not configured")
	}
	entry, ok, err := r.LookupFile(ctx, rootTreeID, AccountStoragePath(r.Account, p))
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, r.notFoundError()
	}
	limit := r.maxBytes()
	if entry.Size > limit {
		return nil, &FileTooLargeError{Path: p, Limit: limit}
	}
	rc, err := r.Objects.Get(ctx, objectstore.BlobKey(entry.ContentHash), 0, 0)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, &FileTooLargeError{Path: p, Limit: limit}
	}
	return data, nil
}

// ReadConfig adapts ReadFile into ReadConfigFunc, returning (nil, false, nil)
// when configPath is absent in rootTreeID.
func (r TreeReader) ReadConfig(ctx context.Context, rootTreeID, configPath string) ([]byte, bool, error) {
	data, err := r.ReadFile(ctx, rootTreeID, configPath)
	if errors.Is(err, r.notFoundError()) || errors.Is(err, ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

// TouchesGitslice reports whether p names or lies inside a .gitslice directory.
func TouchesGitslice(p string) bool {
	normalized := strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")
	return slices.Contains(strings.Split(normalized, "/"), ".gitslice")
}

// FirstGitslicePath returns the first path in allPaths touching a .gitslice
// directory, or "" when none do.
func FirstGitslicePath(allPaths []string) string {
	for _, p := range allPaths {
		if TouchesGitslice(p) {
			return p
		}
	}
	return ""
}

func normalizeConfigRel(filename string) string {
	name := strings.ReplaceAll(strings.TrimSpace(filename), "\\", "/")
	name = strings.TrimPrefix(name, "/")
	name = strings.TrimPrefix(name, ".gitslice/")
	name = strings.TrimPrefix(name, "/")
	if name == "" || strings.Contains(name, "..") {
		return ""
	}
	return ".gitslice/" + name
}

// ConfigDir extracts the enclosing directory path (e.g. "/acme/app") from a
// config path such as "/acme/app/.gitslice/owners.yaml".
func ConfigDir(configPath, filename string) string {
	rel := normalizeConfigRel(filename)
	if rel == "" {
		return path.Dir(path.Dir(configPath))
	}
	return strings.TrimSuffix(configPath, "/"+rel)
}

// ConfigPaths returns candidate .gitslice/<filename> paths for filePath ordered
// nearest-first, bounded at /<account>/.gitslice/<filename> (never walking to
// "/.gitslice/<filename>"). When account is empty, the account segment is
// inferred from filePath.
func ConfigPaths(account, filePath, filename string) []string {
	cfgRel := normalizeConfigRel(filename)
	if cfgRel == "" {
		return nil
	}
	fp := strings.TrimSpace(strings.ReplaceAll(filePath, "\\", "/"))
	if fp == "" {
		return nil
	}
	cleaned := path.Clean("/" + strings.TrimPrefix(fp, "/"))
	if cleaned == "/" || cleaned == "." {
		return nil
	}

	acct := strings.Trim(strings.ReplaceAll(strings.TrimSpace(account), "\\", "/"), "/")
	if acct == "" {
		firstSeg, _, hasSlash := strings.Cut(strings.TrimPrefix(cleaned, "/"), "/")
		if !hasSlash || firstSeg == "" {
			return nil
		}
		acct = firstSeg
	} else if strings.Contains(acct, "/") {
		return nil
	}
	acctRoot := "/" + acct
	if !strings.HasPrefix(cleaned, acctRoot+"/") {
		return nil
	}

	var out []string
	for dir := path.Dir(cleaned); dir != "/" && dir != "."; dir = path.Dir(dir) {
		out = append(out, path.Join(dir, cfgRel))
		if dir == acctRoot {
			break
		}
	}
	return out
}

// ReadConfigFunc reads raw config bytes at (treeID, configPath), returning
// ok=false with nil error when absent.
type ReadConfigFunc func(ctx context.Context, treeID, configPath string) (data []byte, ok bool, err error)

// ParseConfigFunc parses raw YAML bytes for the folder dir (e.g. "/acme/app").
type ParseConfigFunc[T any] func(dir string, data []byte) (T, error)

type cacheKey struct {
	treeID     string
	configPath string
}

type cacheEntry[T any] struct {
	value T
	ok    bool
	err   error
}

// Resolver memoizes parsed .gitslice/<filename> structs by (treeID, configPath).
type Resolver[T any] struct {
	filename string
	read     ReadConfigFunc
	parse    ParseConfigFunc[T]

	mu    sync.Mutex
	cache map[cacheKey]cacheEntry[T]
}

// NewResolver constructs a memoized config resolver for filename.
func NewResolver[T any](filename string, read ReadConfigFunc, parse ParseConfigFunc[T]) *Resolver[T] {
	return &Resolver[T]{
		filename: filename,
		read:     read,
		parse:    parse,
		cache:    make(map[cacheKey]cacheEntry[T]),
	}
}

// Resolve returns the parsed config at (treeID, configPath), reading and
// parsing it at most once per key.
func (r *Resolver[T]) Resolve(ctx context.Context, treeID, configPath string) (T, bool, error) {
	key := cacheKey{treeID: treeID, configPath: configPath}
	r.mu.Lock()
	if cached, found := r.cache[key]; found {
		r.mu.Unlock()
		return cached.value, cached.ok, cached.err
	}
	r.mu.Unlock()

	var zero T
	if r.read == nil || r.parse == nil {
		return zero, false, errors.New("config resolver is not configured")
	}
	data, ok, err := r.read(ctx, treeID, configPath)
	var entry cacheEntry[T]
	switch {
	case err != nil:
		entry = cacheEntry[T]{err: err}
	case !ok:
		entry = cacheEntry[T]{ok: false}
	default:
		parsed, parseErr := r.parse(ConfigDir(configPath, r.filename), data)
		if parseErr != nil {
			entry = cacheEntry[T]{err: parseErr}
		} else {
			entry = cacheEntry[T]{value: parsed, ok: true}
		}
	}

	r.mu.Lock()
	if existing, found := r.cache[key]; found {
		r.mu.Unlock()
		return existing.value, existing.ok, existing.err
	}
	r.cache[key] = entry
	r.mu.Unlock()
	return entry.value, entry.ok, entry.err
}

// Nearest walks ConfigPaths(account, filePath, filename) nearest-first and
// returns the first config file that exists in treeID.
func (r *Resolver[T]) Nearest(ctx context.Context, treeID, account, filePath string) (string, T, bool, error) {
	var zero T
	for _, cfg := range ConfigPaths(account, filePath, r.filename) {
		val, ok, err := r.Resolve(ctx, treeID, cfg)
		if err != nil {
			return "", zero, false, err
		}
		if !ok {
			continue
		}
		return cfg, val, true, nil
	}
	return "", zero, false, nil
}

// RemapPrefix maps p under oldPrefix to the corresponding path under newPrefix.
func RemapPrefix(oldPrefix, newPrefix, p string) string {
	if p == oldPrefix {
		return newPrefix
	}
	rel := strings.TrimPrefix(p, oldPrefix)
	if !strings.HasPrefix(rel, "/") {
		return newPrefix
	}
	return strings.TrimSuffix(newPrefix, "/") + rel
}

// ListDirectoryFilesFunc returns all descendant file paths under dirPath at
// commitID, or nil when dirPath does not exist or is a plain file.
type ListDirectoryFilesFunc func(ctx context.Context, commitID, dirPath string) ([]string, error)

// CollectTouchedPaths returns every canonical path touched by patchset,
// including ChangedPaths, FileEdits (destination + non-copy source), and
// directory descendants under deleted, moved/renamed, or copied directories
// across patchset.BaseCommitId and commitIDs (with destination prefix remapping
// on copy/move/rename).
func CollectTouchedPaths(ctx context.Context, patchset *corev1.Patchset, commitIDs []string, listDirFiles ListDirectoryFilesFunc) ([]string, error) {
	if patchset == nil {
		return nil, nil
	}
	seen := map[string]struct{}{}
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		if canonical, err := paths.Canonical(p); err == nil {
			p = canonical
		}
		seen[p] = struct{}{}
	}
	for _, p := range patchset.GetChangedPaths() {
		add(p)
	}

	commits := uniqueNonEmptyStrings(append([]string{patchset.GetBaseCommitId()}, commitIDs...)...)
	type dirKey struct {
		commitID string
		dirPath  string
	}
	dirCache := map[dirKey][]string{}
	childrenAt := func(cid, dir string) ([]string, error) {
		if listDirFiles == nil || cid == "" || dir == "" {
			return nil, nil
		}
		if canonical, err := paths.Canonical(dir); err == nil {
			dir = canonical
		}
		k := dirKey{commitID: cid, dirPath: dir}
		if cached, ok := dirCache[k]; ok {
			return cached, nil
		}
		children, err := listDirFiles(ctx, cid, dir)
		if err != nil {
			return nil, err
		}
		dirCache[k] = children
		return children, nil
	}

	for _, edit := range patchset.GetFileEdits() {
		if edit == nil {
			continue
		}
		op := strings.ToLower(strings.TrimSpace(edit.GetOp()))
		dst := strings.TrimSpace(edit.GetPath())
		src := strings.TrimSpace(edit.GetOldPath())
		add(dst)
		if op != "copy" {
			add(src)
		}
		if listDirFiles == nil {
			continue
		}
		switch op {
		case "delete":
			for _, dir := range uniqueNonEmptyStrings(dst, src) {
				for _, cid := range commits {
					children, err := childrenAt(cid, dir)
					if err != nil {
						return nil, err
					}
					for _, child := range children {
						add(child)
					}
				}
			}
		case "rename", "move":
			if src != "" {
				for _, cid := range commits {
					children, err := childrenAt(cid, src)
					if err != nil {
						return nil, err
					}
					for _, child := range children {
						add(child)
						if dst != "" {
							add(RemapPrefix(src, dst, child))
						}
					}
				}
			}
		case "copy":
			if src != "" && dst != "" {
				for _, cid := range commits {
					children, err := childrenAt(cid, src)
					if err != nil {
						return nil, err
					}
					for _, child := range children {
						add(RemapPrefix(src, dst, child))
					}
				}
			}
		}
	}

	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

func uniqueNonEmptyStrings(values ...string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}
