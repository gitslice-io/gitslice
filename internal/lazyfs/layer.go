package lazyfs

import (
	"bufio"
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
	"sort"
	"strings"
	"sync"
	"time"
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

func validateRecordSet(set map[string]*localEntry) error {
	for p, entry := range set {
		clean, err := cleanWritablePath(p)
		if err != nil {
			return err
		}
		if clean == "/" || clean != p {
			return fmt.Errorf("lazyfs: invalid journal path %q: %w", p, fs.ErrInvalid)
		}
		if entry != nil && entry.Kind == entryFile && !isValidLocalName(entry.Local) {
			return fmt.Errorf("lazyfs: invalid local content name %q for %s: %w", entry.Local, p, fs.ErrInvalid)
		}
	}
	return nil
}

// replay applies the journal and truncates it after its last complete record.
func (f *FS) replay() (string, int, error) {
	jInfo, err := f.dirRoot.Lstat(journalName)
	if errors.Is(err, fs.ErrNotExist) {
		return "", 0, nil
	}
	if err != nil {
		return "", 0, err
	}
	if !jInfo.Mode().IsRegular() {
		return "", 0, fmt.Errorf("lazyfs: %s/%s is not a regular file: %w", f.dir, journalName, fs.ErrInvalid)
	}
	file, err := f.dirRoot.OpenFile(journalName, os.O_RDWR, 0o600)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	var good int64
	base := ""
	records := 0
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			break // a record without its newline was cut short
		}
		var rec record
		if json.Unmarshal(line, &rec) != nil {
			break
		}
		if err := validateRecordSet(rec.Set); err != nil {
			return "", 0, err
		}
		if rec.Snapshot {
			base = rec.Base
			f.reset(rec.Set)
		} else {
			for p, entry := range rec.Set {
				f.setLocked(p, entry)
			}
		}
		records++
		good += int64(len(line))
	}
	if err := file.Truncate(good); err != nil {
		return "", 0, err
	}
	return base, records, nil
}

// commitLocked journals a batch of changes, then applies it.
func (f *FS) commitLocked(set map[string]*localEntry) error {
	if len(set) == 0 {
		return nil
	}
	line, err := json.Marshal(record{Set: set})
	if err != nil {
		return err
	}
	line = append(line, '\n')
	if err := f.at("journal-half"); err != nil {
		_, _ = f.journal.Write(line[:len(line)/2])
		return err
	}
	if _, err := f.journal.Write(line); err != nil {
		return err
	}
	if !f.opts.NoSync {
		if err := f.journal.Sync(); err != nil {
			return err
		}
	}
	if err := f.at("journal-written"); err != nil {
		return err
	}
	for p, entry := range set {
		f.setLocked(p, entry)
	}
	f.records++
	return nil
}

// writeSnapshot replaces the journal, atomically, with one record holding the
// whole layer over base.
func (f *FS) writeSnapshot(base string) error {
	line, err := json.Marshal(record{Snapshot: true, Base: base, Set: f.entries})
	if err != nil {
		return err
	}
	tmpName := journalName + ".tmp"
	_ = f.dirRoot.Remove(tmpName)
	tmpFile, err := f.dirRoot.OpenFile(tmpName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := tmpFile.Write(append(line, '\n')); err != nil {
		_ = tmpFile.Close()
		_ = f.dirRoot.Remove(tmpName)
		return err
	}
	if !f.opts.NoSync {
		if err := tmpFile.Sync(); err != nil {
			_ = tmpFile.Close()
			_ = f.dirRoot.Remove(tmpName)
			return err
		}
	}
	if err := tmpFile.Close(); err != nil {
		_ = f.dirRoot.Remove(tmpName)
		return err
	}
	if f.journal != nil {
		_ = f.journal.Close()
		f.journal = nil
	}
	if err := verifyRealDir(f.dir); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(f.dir, tmpName), filepath.Join(f.dir, journalName)); err != nil {
		return err
	}
	if !f.opts.NoSync {
		if err := syncFile(f.dir); err != nil {
			return err
		}
	}
	f.records = 1
	f.journal, err = f.dirRoot.OpenFile(journalName, os.O_WRONLY|os.O_APPEND, 0o600)
	return err
}

func syncFile(p string) error {
	file, err := os.Open(p)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

// collectGarbage deletes local content files no entry refers to: left by an
// operation a crash interrupted, or by one that replaced them.
func (f *FS) collectGarbage() error {
	used := map[string]bool{}
	for _, entry := range f.entries {
		if entry.Kind == entryFile {
			used[entry.Local] = true
		}
	}
	dirFile, err := f.filesRoot.Open(".")
	if err != nil {
		return err
	}
	defer dirFile.Close()
	names, err := dirFile.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, name := range names {
		if !used[name.Name()] {
			_ = f.filesRoot.Remove(name.Name())
		}
	}
	return nil
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

// node is a path in the merged view.
type node struct {
	kind    Kind
	mode    uint32
	size    int64
	modTime time.Time // local files
	hash    string    // base files and symlinks, references
	local   string    // a local file's name under files/
	target  string    // symlinks
	base    bool      // the base layer shows through
	opaque  bool
}

// baseVisibleLocked reports whether the base layer shows through at p: no
// proper ancestor has a local entry other than a plain (not opaque) directory.
func (f *FS) baseVisibleLocked(p string) bool {
	for dir := parentPath(p); dir != "/"; dir = parentPath(dir) {
		entry := f.entries[dir]
		if entry != nil && !(entry.Kind == entryDir && !entry.Opaque) {
			return false
		}
	}
	return true
}

func (f *FS) statLocal(name string) (int64, time.Time, error) {
	if !isValidLocalName(name) {
		return 0, time.Time{}, fmt.Errorf("lazyfs: invalid local content name %q: %w", name, fs.ErrInvalid)
	}
	info, err := f.filesRoot.Lstat(name)
	if err != nil {
		return 0, time.Time{}, err
	}
	if !info.Mode().IsRegular() {
		return 0, time.Time{}, fmt.Errorf("lazyfs: local content %q is not a regular file: %w", name, fs.ErrInvalid)
	}
	return info.Size(), info.ModTime(), nil
}

// lookupMetaLocked returns the merged view at p without stating local content
// files on disk.
func (f *FS) lookupMetaLocked(p string) (node, bool) {
	if p == "/" {
		return node{kind: KindDir, mode: ModeDir}, true
	}
	if entry := f.entries[p]; entry != nil {
		switch entry.Kind {
		case entryWhiteout:
			return node{}, false
		case entryFile:
			return node{kind: KindFile, mode: entry.Mode, local: entry.Local}, true
		case entryRef:
			return node{kind: KindFile, mode: entry.Mode, size: entry.Size, hash: entry.Hash}, true
		case entryDir:
			return node{kind: KindDir, mode: ModeDir, opaque: entry.Opaque}, true
		case entrySymlink:
			return node{kind: KindSymlink, mode: ModeSymlink, size: int64(len(entry.Target)), target: entry.Target}, true
		}
	}
	if !f.baseVisibleLocked(p) {
		return node{}, false
	}
	entry, ok := f.base.Lookup(p)
	if !ok {
		return node{}, false
	}
	return node{kind: entry.Kind, mode: entry.Mode, size: entry.Size, hash: entry.ContentHash, target: entry.Target, base: true}, true
}

// lookupLocked returns the merged view at p, including local file size/mtime.
func (f *FS) lookupLocked(p string) (node, bool) {
	n, ok := f.lookupMetaLocked(p)
	if ok && n.local != "" {
		if size, modTime, err := f.statLocal(n.local); err == nil {
			n.size, n.modTime = size, modTime
		}
	}
	return n, ok
}

// childNamesLocked lists the names under the directory p in the merged view.
func (f *FS) childNamesLocked(p string, n node) []string {
	set := map[string]struct{}{}
	if baseEntry, ok := f.base.Lookup(p); ok && baseEntry.Kind == KindDir && !n.opaque && (n.base || f.baseVisibleLocked(joinPath(p, "x"))) {
		for _, name := range f.base.Children(p) {
			set[name] = struct{}{}
		}
	}
	for name := range f.children[p] {
		set[name] = struct{}{}
	}
	names := make([]string, 0, len(set))
	for name := range set {
		if _, ok := f.lookupMetaLocked(joinPath(p, name)); ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// resolveParentLocked checks that the parent of p exists and is a directory,
// failing as a file system would: ENOENT for a missing component, ENOTDIR for
// one that is not a directory.
func (f *FS) resolveParentLocked(op, p string) error {
	if p == "/" {
		return &fs.PathError{Op: op, Path: p, Err: fs.ErrInvalid}
	}
	parent := parentPath(p)
	var walk []string
	for dir := parent; dir != "/"; dir = parentPath(dir) {
		walk = append(walk, dir)
	}
	for i := len(walk) - 1; i >= 0; i-- {
		n, ok := f.lookupMetaLocked(walk[i])
		if !ok {
			return &fs.PathError{Op: op, Path: p, Err: fs.ErrNotExist}
		}
		if n.kind != KindDir {
			return &fs.PathError{Op: op, Path: p, Err: ErrNotDir}
		}
	}
	return nil
}

// resolveLocked looks p up as a file system would: ENOTDIR when a component
// on the way is not a directory, ENOENT when p or a component is missing.
func (f *FS) resolveLocked(op, p string) (node, error) {
	if p == "/" {
		n, _ := f.lookupMetaLocked(p)
		return n, nil
	}
	if err := f.resolveParentLocked(op, p); err != nil {
		return node{}, err
	}
	n, ok := f.lookupMetaLocked(p)
	if !ok {
		return node{}, &fs.PathError{Op: op, Path: p, Err: fs.ErrNotExist}
	}
	if n.local != "" {
		size, modTime, err := f.statLocal(n.local)
		if err != nil {
			return node{}, &fs.PathError{Op: op, Path: p, Err: err}
		}
		n.size, n.modTime = size, modTime
	}
	return n, nil
}

// localEntriesUnderLocked returns the local paths at and below p.
//
// It walks the subtree, not the whole layer: a local entry is listed under its
// directory in f.children, and a directory between p and it is either a local
// entry itself or a base directory. Removing one file of many thousands (rm
// -rf of build output) then costs that file, not every entry in the layer.
func (f *FS) localEntriesUnderLocked(p string) []string {
	var out []string
	if _, ok := f.entries[p]; ok {
		out = append(out, p)
	}
	var walk func(dir string)
	walk = func(dir string) {
		names := f.children[dir]
		for name := range names {
			q := joinPath(dir, name)
			if _, ok := f.entries[q]; ok {
				out = append(out, q)
			}
			walk(q)
		}
		for _, name := range f.base.Children(dir) {
			if _, local := names[name]; local {
				continue
			}
			q := joinPath(dir, name)
			if entry, ok := f.base.Lookup(q); ok && entry.Kind == KindDir {
				walk(q)
			}
		}
	}
	walk(p)
	sort.Strings(out)
	if checkSubtreeWalk {
		var want []string
		for q := range f.entries {
			if q == p || isUnder(q, p) {
				want = append(want, q)
			}
		}
		sort.Strings(want)
		if strings.Join(want, "\n") != strings.Join(out, "\n") {
			panic(fmt.Sprintf("lazyfs: entries under %s: walked %v, want %v", p, out, want))
		}
	}
	return out
}

// checkSubtreeWalk makes localEntriesUnderLocked compare its walk with a scan
// of every entry. Tests turn it on.
var checkSubtreeWalk = false
