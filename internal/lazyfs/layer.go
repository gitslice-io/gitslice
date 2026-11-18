package lazyfs

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sync"
)

// Content reads and stores file content by its content hash.
type Content interface {
	// Open returns the content with the given hash. For base content this may
	// fetch it.
	Open(ctx context.Context, hash string) (io.ReadCloser, error)
	// HashFile returns the content hash of a local file, keeping a copy where
	// an upload will find it.
	HashFile(path string) (string, error)
	// HashBytes does the same for content in memory (a symlink's target).
	HashBytes(data []byte) (string, error)
}

// Errors, in addition to fs.ErrNotExist, fs.ErrExist and fs.ErrInvalid. A file
// system adapter maps them to ENOTDIR, EISDIR and ENOTEMPTY.
var (
	ErrNotDir   = errors.New("not a directory")
	ErrIsDir    = errors.New("is a directory")
	ErrNotEmpty = errors.New("directory not empty")
	// ErrBaseMismatch is returned by Open when the local layer was recorded
	// against another base commit than the manifest given.
	ErrBaseMismatch = errors.New("local layer belongs to another base commit")
)

type entryKind string

const (
	entryFile     entryKind = "file"     // content in a local file
	entryRef      entryKind = "ref"      // content known by hash, no local copy
	entryDir      entryKind = "dir"      // a directory the user made (or a renamed one)
	entrySymlink  entryKind = "symlink"  // a symlink the user made
	entryWhiteout entryKind = "whiteout" // deleted: hides the base path
)

// localEntry is one path of the local layer.
type localEntry struct {
	Kind   entryKind `json:"k"`
	Mode   uint32    `json:"m,omitempty"`
	Hash   string    `json:"h,omitempty"` // ref
	Size   int64     `json:"s,omitempty"` // ref
	Local  string    `json:"l,omitempty"` // file: name under files/
	Target string    `json:"t,omitempty"` // symlink
	// Opaque hides the base directory at the same path, so only local
	// entries show under it.
	Opaque bool `json:"o,omitempty"`
}

// record is one line of the journal. A nil entry in Set deletes the path. A
// snapshot record replaces the whole layer and names its base commit.
type record struct {
	Snapshot bool                   `json:"snapshot,omitempty"`
	Base     string                 `json:"base,omitempty"`
	Set      map[string]*localEntry `json:"set,omitempty"`
}

// Options tune an FS.
type Options struct {
	// NoSync skips fsync after each journal record. Tests use it; a file
	// system adapter syncs when the application asks to.
	NoSync bool
}

// FS is a lazy workspace: a manifest under a local layer kept in a directory.
// It is safe for concurrent use.
type FS struct {
	dir       string
	dirRoot   *os.Root
	filesRoot *os.Root
	content   Content
	opts      Options

	mu       sync.RWMutex
	hashMu   sync.Mutex
	base     *Manifest
	entries  map[string]*localEntry
	children map[string]map[string]struct{} // directory -> names with local entries
	journal  *os.File
	records  int
	hashes   map[string]hashMemo // local file name -> hash of its last seen version
	// hook lets tests stop an operation at a point, as a crash would.
	hook func(point string) error
}

type hashMemo struct {
	size    int64
	modTime int64
	hash    string
}

const (
	journalName = "index"
	filesDir    = "files"
	// compactAfter is how many records the journal may hold before Open
	// rewrites it as one snapshot.
	compactAfter = 1000
)

func verifyRealDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("lazyfs: %s is not a real directory: %w", dir, fs.ErrInvalid)
	}
	return nil
}

func isValidLocalName(name string) bool {
	if len(name) != 32 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// Open opens the local layer stored in dir over base, creating it if dir is
// empty. It replays the journal, dropping a record a crash cut short, and
// deletes local content no entry refers to.
func Open(dir string, base *Manifest, content Content, opts Options) (retFS *FS, retErr error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := verifyRealDir(dir); err != nil {
		return nil, err
	}
	dirRoot, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	if err := dirRoot.Mkdir(filesDir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		_ = dirRoot.Close()
		return nil, err
	}
	info, err := dirRoot.Lstat(filesDir)
	if err != nil {
		_ = dirRoot.Close()
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		_ = dirRoot.Close()
		return nil, fmt.Errorf("lazyfs: %s/%s is not a real directory: %w", dir, filesDir, fs.ErrInvalid)
	}
	filesRoot, err := dirRoot.OpenRoot(filesDir)
	if err != nil {
		_ = dirRoot.Close()
		return nil, err
	}
	f := &FS{
		dir:       dir,
		dirRoot:   dirRoot,
		filesRoot: filesRoot,
		content:   content,
		opts:      opts,
		base:      base,
		hashes:    map[string]hashMemo{},
	}
	defer func() {
		if retErr != nil {
			_ = f.Close()
		}
	}()
	f.reset(nil)
	recordedBase, records, err := f.replay()
	if err != nil {
		return nil, err
	}
	if records == 0 {
		if err := f.writeSnapshot(base.CommitID()); err != nil {
			return nil, err
		}
	} else if recordedBase != base.CommitID() {
		return nil, fmt.Errorf("%w: recorded %q, given %q", ErrBaseMismatch, recordedBase, base.CommitID())
	} else if records > compactAfter {
		if err := f.writeSnapshot(base.CommitID()); err != nil {
			return nil, err
		}
	}
	if f.journal == nil {
		if f.journal, err = f.dirRoot.OpenFile(journalName, os.O_WRONLY|os.O_APPEND, 0o600); err != nil {
			return nil, err
		}
	}
	if err := f.collectGarbage(); err != nil {
		return nil, err
	}
	return f, nil
}

// RecordedBase returns the base commit the local layer in dir was recorded
// against, or "" when there is none, so a caller can load that manifest.
func RecordedBase(dir string) (string, error) {
	info, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("lazyfs: %s is not a real directory: %w", dir, fs.ErrInvalid)
	}
	dirRoot, err := os.OpenRoot(dir)
	if err != nil {
		return "", err
	}
	defer dirRoot.Close()
	jInfo, err := dirRoot.Lstat(journalName)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !jInfo.Mode().IsRegular() {
		return "", fmt.Errorf("lazyfs: %s/%s is not a regular file: %w", dir, journalName, fs.ErrInvalid)
	}
	file, err := dirRoot.Open(journalName)
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		return "", err
	}
	base := ""
	for _, line := range bytes.Split(data, []byte("\n")) {
		var rec record
		if json.Unmarshal(line, &rec) != nil {
			break
		}
		if rec.Snapshot {
			base = rec.Base
		}
	}
	return base, nil
}

// Close closes the journal and directory roots.
func (f *FS) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	var firstErr error
	if f.journal != nil {
		if f.opts.NoSync {
			_ = f.journal.Sync()
		}
		if err := f.journal.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		f.journal = nil
	}
	if f.filesRoot != nil {
		if err := f.filesRoot.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		f.filesRoot = nil
	}
	if f.dirRoot != nil {
		if err := f.dirRoot.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		f.dirRoot = nil
	}
	return firstErr
}

// Base returns the manifest the workspace is over.
func (f *FS) Base() *Manifest {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.base
}

func (f *FS) reset(entries map[string]*localEntry) {
	f.entries = map[string]*localEntry{}
	f.children = map[string]map[string]struct{}{}
	for p, entry := range entries {
		f.setLocked(p, entry)
	}
}

func (f *FS) setLocked(p string, entry *localEntry) {
	dir, name := parentPath(p), path.Base(p)
	if entry == nil {
		delete(f.entries, p)
		if names := f.children[dir]; names != nil {
			delete(names, name)
			if len(names) == 0 {
				delete(f.children, dir)
			}
		}
		return
	}
	f.entries[p] = entry
	if f.children[dir] == nil {
		f.children[dir] = map[string]struct{}{}
	}
	f.children[dir][name] = struct{}{}
}

func (f *FS) at(point string) error {
	if f.hook == nil {
		return nil
	}
	return f.hook(point)
}

func (f *FS) localPath(name string) string {
	return filepath.Join(f.dir, filesDir, name)
}

func newLocalName() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
