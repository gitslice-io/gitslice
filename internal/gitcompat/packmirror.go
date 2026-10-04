package gitcompat

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"gitslice.io/gitslice/internal/storage"
)

// A PackMirror keeps a slice's Git history in the object store as the pack
// files the repository is made of. A pack is immutable and named by its
// contents, so each is uploaded once, and a manifest names the ones that make
// up the current history. Restoring downloads them in parallel and points the
// branch at the head; nothing is replayed.
//
// On a lazily built projection the history packs hold only commits and trees,
// a small fraction of the data: the files themselves are already in the object
// store, and are written into the repository when a fetch needs them. That is
// what makes a mirror practical for slices of tens of gigabytes.
//
// Layout under git-mirror/<account>/<slice>/:
//
//	manifest.json                 the current packs, state and heads (written last)
//	packs/<name>.pack.<part>      the pack, in parts of packPartBytes
//	packs/<name>.idx.<part>       its index
//	state/<git head>.json.<part>  the projection state that goes with the head
//
// Objects are split into parts because the object store holds a whole object
// in memory to upload it. Memory is what a Cloud Run instance has least of (its
// disk is memory too), so an upload holds at most packUploadConcurrency parts
// of packPartBytes, 32 MiB, whatever the size of the pack.

const (
	packManifestVersion = 1
	// Parts are buffered whole to upload, but only streamed to disk to download.
	packUploadConcurrency   = 2
	packDownloadConcurrency = 4
	// Parts written before the manifest recorded their size were this big.
	legacyPartBytes = 32 << 20
)

// packPartBytes is the size of one uploaded part. It is a variable so tests can
// make small objects span several parts.
var packPartBytes int64 = 16 << 20

type packManifest struct {
	Version    int         `json:"version"`
	Account    string      `json:"account"`
	Slice      string      `json:"slice"`
	NativeHead string      `json:"native_head"`
	GitHead    string      `json:"git_head"`
	State      packObject  `json:"state"`
	Packs      []packEntry `json:"packs"`
	// Garbage is what this manifest stopped using. It is deleted by the push
	// after next, not this one: an instance that read the previous manifest may
	// still be downloading from it.
	Garbage []packObject `json:"garbage,omitempty"`
}

type packEntry struct {
	Name string     `json:"name"`
	Pack packObject `json:"pack"`
	Idx  packObject `json:"idx"`
}

// packObject is an object stored in parts: Key.0, Key.1, ...
type packObject struct {
	Key      string `json:"key"`
	Size     int64  `json:"size"`
	Parts    int    `json:"parts"`
	PartSize int64  `json:"part_size,omitempty"`
}

func (o packObject) partSize() int64 {
	if o.PartSize > 0 {
		return o.PartSize
	}
	return legacyPartBytes
}

type PackMirror struct {
	store ObjectStore
	pub   *publisher[packSnapshot]
}

type packSnapshot struct {
	account, slice, repoPath string
	nativeHead, gitHead      string
	packs                    []string
	state                    []byte // the projection state, gzipped JSON
}

// NewPackMirror mirrors every slice's projection into store.
func NewPackMirror(store ObjectStore) *PackMirror {
	m := &PackMirror{store: store}
	m.pub = newPublisher("git projection packs mirrored", func(ctx context.Context, _ string, s packSnapshot) error { return m.push(ctx, &s) })
	return m
}

func mirrorPrefix(account, slice string) string {
	return "git-mirror/" + account + "/" + slice + "/"
}

func (m *PackMirror) Publish(_ context.Context, account, slice, repoPath string, state *projectionState) {
	if state.GitHead == "" {
		return
	}
	raw, err := encodeState(state)
	if err != nil {
		slog.Warn("pack mirror snapshot failed", "repo", account+"/"+slice+".git", "error", err)
		return
	}
	m.pub.submit(account+"/"+slice, packSnapshot{
		account: account, slice: slice, repoPath: repoPath,
		nativeHead: state.NativeHeadID, gitHead: state.GitHead,
		packs: append([]string(nil), state.HistoryPacks...), state: raw,
	})
}

// Settle waits for the publish that Publish started; see Mirror.
func (m *PackMirror) Settle(ctx context.Context, account, slice string) error {
	return m.pub.wait(ctx, account+"/"+slice, mirrorSettleWait)
}

// encodeState compresses a projection state. It is a map entry per file, about
// 200 bytes each as JSON, and compresses ten to one; a snapshot waits in memory
// until it is uploaded.
func encodeState(state *projectionState) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if err := json.NewEncoder(zw).Encode(state); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func decodeState(r io.Reader) (*projectionState, error) {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	var state projectionState
	if err := json.NewDecoder(zr).Decode(&state); err != nil {
		return nil, err
	}
	return &state, nil
}

func (m *PackMirror) push(ctx context.Context, s *packSnapshot) error {
	prefix := mirrorPrefix(s.account, s.slice)
	// Read, not remembered: another instance may have published since, and
	// what it uploaded is reusable.
	previous, err := m.readManifest(ctx, s.account, s.slice)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	have := map[string]packEntry{}
	if previous != nil {
		for _, e := range previous.Packs {
			have[e.Name] = e
		}
	}
	next := packManifest{Version: packManifestVersion, Account: s.account, Slice: s.slice, NativeHead: s.nativeHead, GitHead: s.gitHead}
	for _, name := range s.packs {
		if entry, ok := have[name]; ok {
			next.Packs = append(next.Packs, entry)
			continue
		}
		dir := filepath.Join(s.repoPath, "objects", "pack")
		pack, err := m.putFile(ctx, prefix+"packs/"+name+".pack", filepath.Join(dir, name+".pack"))
		if err != nil {
			return fmt.Errorf("upload %s: %w", name, err)
		}
		idx, err := m.putFile(ctx, prefix+"packs/"+name+".idx", filepath.Join(dir, name+".idx"))
		if err != nil {
			return fmt.Errorf("upload %s index: %w", name, err)
		}
		next.Packs = append(next.Packs, packEntry{Name: name, Pack: pack, Idx: idx})
	}
	state, err := m.putBytes(ctx, prefix+"state/"+s.gitHead+".json", s.state)
	if err != nil {
		return fmt.Errorf("upload state: %w", err)
	}
	next.State = state
	next.Garbage = dropped(previous, &next)
	raw, err := json.Marshal(&next)
	if err != nil {
		return err
	}
	if err := m.putManifest(ctx, prefix+"manifest.json", raw); err != nil {
		return err
	}
	if previous != nil {
		m.cleanup(ctx, previous, &next)
	}
	return nil
}

// dropped lists what previous used and next does not.
func dropped(previous *packManifest, next *packManifest) []packObject {
	if previous == nil {
		return nil
	}
	keep := map[string]bool{next.State.Key: true}
	for _, e := range next.Packs {
		keep[e.Pack.Key], keep[e.Idx.Key] = true, true
	}
	var out []packObject
	for _, e := range previous.Packs {
		for _, obj := range []packObject{e.Pack, e.Idx} {
			if !keep[obj.Key] {
				out = append(out, obj)
			}
		}
	}
	if !keep[previous.State.Key] {
		out = append(out, previous.State)
	}
	return out
}

// putManifest replaces the manifest. It goes last: until it is replaced, a
// restore sees the previous complete set of objects. It is the one key that
// changes. Object stores overwrite, but some keep the first object written
// under a key (the filesystem store is content-addressed), so what was stored is
// checked, and the old manifest deleted first if it is still there. Only on such
// a store is there an instant when a restore finds no manifest.
func (m *PackMirror) putManifest(ctx context.Context, key string, raw []byte) error {
	if err := m.store.Put(ctx, key, bytes.NewReader(raw)); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	if stored, err := m.readObject(ctx, key); err == nil && bytes.Equal(stored, raw) {
		return nil
	}
	if err := m.store.Delete(ctx, key); err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, storage.ErrNotFound) {
		return fmt.Errorf("replace manifest: %w", err)
	}
	if err := m.store.Put(ctx, key, bytes.NewReader(raw)); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	return nil
}

func (m *PackMirror) readObject(ctx context.Context, key string) ([]byte, error) {
	rc, err := m.store.Get(ctx, key, 0, 0)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, 64<<20))
}

// cleanup deletes what the manifest before this one stopped using. The
// objects the new manifest stopped using are only recorded (in its Garbage) and
// go on the next push, once nobody can still be reading the manifest that named
// them. Failures only leave garbage behind.
func (m *PackMirror) cleanup(ctx context.Context, previous, next *packManifest) {
	keep := map[string]bool{next.State.Key: true}
	for _, e := range next.Packs {
		keep[e.Pack.Key], keep[e.Idx.Key] = true, true
	}
	for _, obj := range previous.Garbage {
		if !keep[obj.Key] { // an identical pack can come back after a rebuild
			m.deleteParts(ctx, obj)
		}
	}
}

func (m *PackMirror) deleteParts(ctx context.Context, obj packObject) {
	for i := 0; i < obj.Parts; i++ {
		if err := m.store.Delete(ctx, obj.Key+"."+strconv.Itoa(i)); err != nil {
			slog.Debug("pack mirror: could not delete an old part", "key", obj.Key, "error", err)
		}
	}
}

// readManifest returns the slice's manifest, or nil when none exists.
func (m *PackMirror) readManifest(ctx context.Context, account, slice string) (*packManifest, error) {
	rc, err := m.store.Get(ctx, mirrorPrefix(account, slice)+"manifest.json", 0, 0)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer rc.Close()
	var manifest packManifest
	if err := json.NewDecoder(rc).Decode(&manifest); err != nil {
		return nil, fmt.Errorf("malformed manifest: %w", err)
	}
	return &manifest, nil
}

// putFile uploads a file as parts.
func (m *PackMirror) putFile(ctx context.Context, key, path string) (packObject, error) {
	f, err := os.Open(path)
	if err != nil {
		return packObject{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return packObject{}, err
	}
	return m.putParts(ctx, key, f, info.Size())
}

func (m *PackMirror) putBytes(ctx context.Context, key string, data []byte) (packObject, error) {
	return m.putParts(ctx, key, bytes.NewReader(data), int64(len(data)))
}

func (m *PackMirror) putParts(ctx context.Context, key string, r io.ReaderAt, size int64) (packObject, error) {
	partSize := packPartBytes
	parts := int((size + partSize - 1) / partSize)
	if parts == 0 {
		parts = 1
	}
	err := forEachPart(ctx, parts, packUploadConcurrency, func(i int) error {
		offset := int64(i) * partSize
		length := min(partSize, size-offset)
		return m.store.Put(ctx, key+"."+strconv.Itoa(i), io.NewSectionReader(r, offset, max(length, 0)))
	})
	return packObject{Key: key, Size: size, Parts: parts, PartSize: partSize}, err
}

// getFile downloads an object's parts into path.
func (m *PackMirror) getFile(ctx context.Context, obj packObject, path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	err = forEachPart(ctx, obj.Parts, packDownloadConcurrency, func(i int) error {
		rc, err := m.store.Get(ctx, obj.Key+"."+strconv.Itoa(i), 0, 0)
		if err != nil {
			return err
		}
		defer rc.Close()
		_, err = io.Copy(io.NewOffsetWriter(f, int64(i)*obj.partSize()), rc)
		return err
	})
	if err != nil {
		return err
	}
	if info, err := f.Stat(); err != nil || info.Size() != obj.Size {
		return fmt.Errorf("%s: downloaded %d bytes, expected %d", obj.Key, info.Size(), obj.Size)
	}
	return nil
}

// forEachPart runs fn for 0..n-1, a few at a time, and returns the first error.
func forEachPart(ctx context.Context, n, concurrency int, fn func(int) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var once sync.Once
	var first error
	for i := 0; i < n; i++ {
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			if ctx.Err() != nil {
				return
			}
			if err := fn(i); err != nil {
				once.Do(func() { first = err; cancel() })
			}
		}()
	}
	wg.Wait()
	return first
}

// Restore builds repoPath from the mirror: it downloads the history packs and
// the state, and points the projected branch at the mirrored head.
func (m *PackMirror) Restore(ctx context.Context, account, slice, repoPath string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	started := time.Now()
	manifest, err := m.readManifest(ctx, account, slice)
	if err != nil {
		return false, err
	}
	if manifest == nil {
		return false, nil
	}
	if manifest.Version != packManifestVersion || manifest.Account != account || manifest.Slice != slice {
		return false, errors.New("mirror manifest is for another slice or version")
	}
	if err := os.MkdirAll(filepath.Dir(repoPath), 0o755); err != nil {
		return false, err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(repoPath), filepath.Base(repoPath)+".restore-")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(tmp)
	if err := runGit(ctx, "", nil, "init", "--bare", "--quiet", tmp); err != nil {
		return false, err
	}
	if err := configureProjectedRepo(ctx, tmp); err != nil {
		return false, err
	}
	packDir := filepath.Join(tmp, "objects", "pack")
	if err := os.MkdirAll(packDir, 0o755); err != nil {
		return false, err
	}
	var total int64
	for _, e := range manifest.Packs {
		// The index goes last: git treats a pack without an index as absent.
		if err := m.getFile(ctx, e.Pack, filepath.Join(packDir, e.Name+".pack")); err != nil {
			return false, err
		}
		if err := m.getFile(ctx, e.Idx, filepath.Join(packDir, e.Name+".idx")); err != nil {
			return false, err
		}
		total += e.Pack.Size + e.Idx.Size
	}
	statePath := filepath.Join(tmp, ".state-download")
	if err := m.getFile(ctx, manifest.State, statePath); err != nil {
		return false, err
	}
	stateFile, err := os.Open(statePath)
	if err != nil {
		return false, err
	}
	state, err := decodeState(stateFile)
	_ = stateFile.Close()
	_ = os.Remove(statePath)
	if err != nil {
		return false, fmt.Errorf("mirror state: %w", err)
	}
	if state.Version != projectionVersion || state.Account != account || state.Slice != slice || state.GitHead != manifest.GitHead {
		return false, errors.New("mirror state does not match its manifest")
	}
	if err := runGit(ctx, tmp, []string{"GIT_DIR=" + tmp}, "update-ref", projectedBranch, manifest.GitHead); err != nil {
		return false, err
	}
	if err := runGit(ctx, tmp, []string{"GIT_DIR=" + tmp}, "cat-file", "-e", manifest.GitHead+"^{commit}"); err != nil {
		return false, fmt.Errorf("restored packs do not hold the head: %w", err)
	}
	if err := writeProjectionState(tmp, state); err != nil {
		return false, err
	}
	if err := os.RemoveAll(repoPath); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, repoPath); err != nil {
		return false, err
	}
	slog.Info("git projection restored from mirror", "repo", account+"/"+slice+".git", "native_commits", len(state.Commits), "packs", len(manifest.Packs), "bytes", total, "total_ms", time.Since(started).Milliseconds())
	return true, nil
}
