package cli

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gitslice.io/gitslice/internal/clientcache"
	"gitslice.io/gitslice/internal/lazyfs"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/encoding/gzip"
)

// Lazy workspaces (design/30_lazy_workspaces.md): gs mount shows a slice at a
// directory with its listing only; content arrives on first read. Each
// workspace keeps its state in a directory of its own, so it outlives its
// mount: mounting the same path again re-attaches it.

// mountRecord is a lazy workspace's mount.json.
type mountRecord struct {
	ID         string `json:"id"`
	Account    string `json:"account"`
	Slice      string `json:"slice"`
	MountPath  string `json:"mount_path"`
	ServerAddr string `json:"server_addr"`
	BaseCommit string `json:"base_commit"`
	Files      int    `json:"files"`
	CreatedAt  string `json:"created_at"`
	MountedAt  string `json:"mounted_at,omitempty"`
	// The slice's definition, for the workspace's .gs/slice.json.
	SliceID        string   `json:"slice_id,omitempty"`
	DefinitionHash string   `json:"definition_hash,omitempty"`
	IncludedPaths  []string `json:"included_paths,omitempty"`
	// ReadOnly mounts refuse writes.
	ReadOnly bool `json:"read_only,omitempty"`
}

func (rec mountRecord) ref() *corev1.SliceRef {
	return &corev1.SliceRef{Account: rec.Account, Slice: rec.Slice}
}

// mountsRoot is where lazy workspaces keep their state.
func (r Runner) mountsRoot() string {
	if dataHome := os.Getenv("XDG_DATA_HOME"); dataHome != "" && r.Home == "" {
		return filepath.Join(dataHome, "gitslice", "mounts")
	}
	return filepath.Join(r.homeDir(), ".local", "share", "gitslice", "mounts")
}

func (r Runner) mountStateDir(id string) string { return filepath.Join(r.mountsRoot(), id) }

// mountControlPath is the daemon's control socket. It lives in a short
// runtime directory: a socket path is limited to about 100 bytes.
func mountControlPath(id string) string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = filepath.Join(os.TempDir(), fmt.Sprintf("gitslice-%d", os.Getuid()))
	}
	return filepath.Join(dir, "gs-mount-"+id+".sock")
}

func newMountID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func (r Runner) readMountRecord(id string) (mountRecord, error) {
	var rec mountRecord
	err := readJSONFile(filepath.Join(r.mountStateDir(id), "mount.json"), &rec)
	return rec, err
}

// writeMountRecord replaces mount.json atomically: the daemon rewrites it
// while commands may be reading it.
func (r Runner) writeMountRecord(rec mountRecord) error {
	path := filepath.Join(r.mountStateDir(rec.ID), "mount.json")
	tmp := path + ".tmp"
	if err := writeJSONFile(tmp, rec, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// mountRecords lists every lazy workspace on this machine, by mount path.
func (r Runner) mountRecords() ([]mountRecord, error) {
	entries, err := os.ReadDir(r.mountsRoot())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []mountRecord
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		rec, err := r.readMountRecord(entry.Name())
		if err != nil {
			continue
		}
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MountPath < out[j].MountPath })
	return out, nil
}

// findMountRecord finds a workspace by id, by its mount path, or as the one
// whose mount contains path.
func (r Runner) findMountRecord(selector string) (mountRecord, error) {
	records, err := r.mountRecords()
	if err != nil {
		return mountRecord{}, err
	}
	selector = strings.TrimSpace(selector)
	abs := selector
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(r.cwd(), abs)
	}
	abs = filepath.Clean(abs)
	var best mountRecord
	for _, rec := range records {
		if rec.ID == selector {
			return rec, nil
		}
		if abs == rec.MountPath || strings.HasPrefix(abs, rec.MountPath+string(filepath.Separator)) {
			if len(rec.MountPath) > len(best.MountPath) {
				best = rec
			}
		}
	}
	if best.ID == "" {
		return mountRecord{}, userError("not_a_lazy_workspace", "no lazy workspace at "+abs, "Run gs mount list to see lazy workspaces, or gs mount <slice> <dir> to make one.")
	}
	return best, nil
}

// isMounted reports whether a file system is mounted at dir.
func isMounted(dir string) bool {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 4 && unescapeMountPath(fields[4]) == dir {
			return true
		}
	}
	return false
}

func unescapeMountPath(p string) string {
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(p)
}

// Manifests are stored as JSON lines of lazyfs.BaseEntry.

func writeManifest(path string, entries []lazyfs.BaseEntry) error {
	tmp := path + ".tmp"
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(file)
	enc := json.NewEncoder(w)
	for _, entry := range entries {
		if err := enc.Encode(entry); err != nil {
			file.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readManifest(path, commitID string) (*lazyfs.Manifest, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var entries []lazyfs.BaseEntry
	dec := json.NewDecoder(bufio.NewReader(file))
	for dec.More() {
		var entry lazyfs.BaseEntry
		if err := dec.Decode(&entry); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return lazyfs.NewManifest(commitID, entries)
}

// manifestEntries turns a Checkout listing into base entries.
func manifestEntries(listing []*corev1.TreeEntry) []lazyfs.BaseEntry {
	out := make([]lazyfs.BaseEntry, 0, len(listing))
	for _, entry := range listing {
		base := lazyfs.BaseEntry{Path: entry.GetPath(), Mode: entry.GetMode(), Size: entry.GetSize(), ContentHash: entry.GetContentHash(), Target: entry.GetSymlinkTarget()}
		switch entry.GetKind() {
		case corev1.EntryKind_ENTRY_KIND_DIRECTORY:
			base.Kind = lazyfs.KindDir
		case corev1.EntryKind_ENTRY_KIND_SYMLINK:
			base.Kind = lazyfs.KindSymlink
		default:
			if isSymlinkMode(entry.GetMode()) {
				base.Kind = lazyfs.KindSymlink
			} else {
				base.Kind = lazyfs.KindFile
			}
		}
		out = append(out, base)
	}
	return out
}

// Fetching. A miss fetches the file and, in the same ReadFiles call, small
// files next to it, as gs init batches (design/28_benchmarks.md): reading one
// file of a directory usually means reading more.
const (
	mountPrefetchMaxFile  = 64 << 10
	mountBatchMaxFiles    = 256
	mountBatchMaxBytes    = 4 << 20
	mountLargeFileBytes   = 4 << 20
	mountFetchTimeout     = 60 * time.Second
	mountFetchConcurrency = 4
)

// mountFetcher fetches base content into the client cache for a mount.
type mountFetcher struct {
	r        Runner
	conn     *grpc.ClientConn
	slice    *corev1.SliceRef
	commitID string
	manifest *lazyfs.Manifest
	cache    *clientcache.ObjectCache

	mu       sync.Mutex
	inflight map[string]*fetchCall // by content hash
	files    int64
	bytes    int64
}

type fetchCall struct {
	done chan struct{}
	err  error
}

// Fetch implements fusefs.Fetcher.
func (f *mountFetcher) Fetch(ctx context.Context, p string, attr lazyfs.Attr) (string, bool, error) {
	hash := attr.ContentHash
	if f.cache.Exists(hash) {
		path, err := f.cache.Path(hash)
		return path, false, err
	}
	f.mu.Lock()
	if call := f.inflight[hash]; call != nil {
		f.mu.Unlock()
		<-call.done
		if call.err != nil {
			return "", false, call.err
		}
		path, err := f.cache.Path(hash)
		return path, false, err
	}
	batch := f.planLocked(p, attr)
	call := &fetchCall{done: make(chan struct{})}
	for _, entry := range batch {
		f.inflight[entry.ContentHash] = call
	}
	f.mu.Unlock()

	// One reader giving up must not fail the others waiting on this fetch.
	fetchCtx, cancel := context.WithTimeout(context.Background(), mountFetchTimeout)
	defer cancel()
	files, bytes, err := f.fetch(fetchCtx, batch)

	f.mu.Lock()
	for _, entry := range batch {
		delete(f.inflight, entry.ContentHash)
	}
	f.files += int64(files)
	f.bytes += bytes
	f.mu.Unlock()
	call.err = err
	close(call.done)
	if err != nil {
		return "", false, err
	}
	path, err := f.cache.Path(hash)
	return path, true, err
}

// planLocked picks the file and the small files beside it that are not
// cached yet or already being fetched.
func (f *mountFetcher) planLocked(p string, attr lazyfs.Attr) []lazyfs.BaseEntry {
	batch := []lazyfs.BaseEntry{{Path: p, Kind: lazyfs.KindFile, Size: attr.Size, ContentHash: attr.ContentHash}}
	if attr.Size > mountLargeFileBytes {
		return batch
	}
	total := attr.Size
	seen := map[string]bool{attr.ContentHash: true}
	dir := p[:strings.LastIndex(p, "/")]
	if dir == "" {
		dir = "/"
	}
	for _, name := range f.manifest.Children(dir) {
		sibling, ok := f.manifest.Lookup(joinGlobalPath(dir, name))
		if !ok || sibling.Kind != lazyfs.KindFile || sibling.Size > mountPrefetchMaxFile || seen[sibling.ContentHash] {
			continue
		}
		if f.inflight[sibling.ContentHash] != nil || f.cache.Exists(sibling.ContentHash) {
			continue
		}
		if len(batch) >= mountBatchMaxFiles || total+sibling.Size > mountBatchMaxBytes {
			break
		}
		seen[sibling.ContentHash] = true
		batch = append(batch, sibling)
		total += sibling.Size
	}
	return batch
}

func joinGlobalPath(dir, name string) string {
	if dir == "/" {
		return "/" + name
	}
	return dir + "/" + name
}

// fetch puts the entries' content in the cache, checking every hash.
func (f *mountFetcher) fetch(ctx context.Context, entries []lazyfs.BaseEntry) (int, int64, error) {
	cfg, err := f.r.readUserConfig()
	if err != nil {
		return 0, 0, err
	}
	return fetchBaseFiles(authContext(ctx, cfg), f.conn, f.slice, f.commitID, entries, f.cache)
}

// fetchBaseFiles fetches base files into the cache: small ones in ReadFiles
// batches, large ones as streams. It returns how many it fetched and their
// size.
func fetchBaseFiles(ctx context.Context, conn *grpc.ClientConn, slice *corev1.SliceRef, commitID string, entries []lazyfs.BaseEntry, cache *clientcache.ObjectCache) (int, int64, error) {
	repo := corev1.NewRepositoryServiceClient(conn)
	blob := corev1.NewBlobServiceClient(conn)
	var small []lazyfs.BaseEntry
	files, total := 0, int64(0)
	for _, entry := range entries {
		if cache.Exists(entry.ContentHash) {
			continue
		}
		if entry.Size <= mountLargeFileBytes {
			small = append(small, entry)
			continue
		}
		stream, err := blob.ReadBlobStream(ctx, &corev1.ReadBlobStreamRequest{Slice: slice, ContentHash: entry.ContentHash})
		if err != nil {
			return files, total, err
		}
		cached, err := cache.PutReader(newBlobStreamReader(stream))
		if err != nil {
			return files, total, err
		}
		if cached.ContentHash != entry.ContentHash {
			return files, total, fmt.Errorf("content hash mismatch for %s: got %s, want %s", entry.Path, cached.ContentHash, entry.ContentHash)
		}
		files++
		total += entry.Size
	}
	for start := 0; start < len(small); {
		end, size := start, int64(0)
		for end < len(small) && end-start < mountBatchMaxFiles && (end == start || size+small[end].Size <= mountBatchMaxBytes) {
			size += small[end].Size
			end++
		}
		batch := small[start:end]
		paths := make([]string, len(batch))
		for i, entry := range batch {
			paths[i] = entry.Path
		}
		res, err := repo.ReadFiles(ctx, &corev1.ReadFilesRequest{CommitId: commitID, Paths: paths, Slice: slice}, grpc.UseCompressor(gzip.Name))
		if err != nil {
			return files, total, err
		}
		if len(res.GetFiles()) != len(paths) {
			return files, total, fmt.Errorf("server returned %d of %d files", len(res.GetFiles()), len(paths))
		}
		for i, entry := range batch {
			cached, err := cache.PutBytes(res.GetFiles()[i].GetData())
			if err != nil {
				return files, total, err
			}
			if cached.ContentHash != entry.ContentHash {
				return files, total, fmt.Errorf("content hash mismatch for %s: got %s, want %s", entry.Path, cached.ContentHash, entry.ContentHash)
			}
			files++
			total += entry.Size
		}
		start = end
	}
	return files, total, nil
}

// fetchConcurrently splits entries into groups fetched side by side.
func fetchConcurrently(ctx context.Context, conn *grpc.ClientConn, slice *corev1.SliceRef, commitID string, entries []lazyfs.BaseEntry, cache *clientcache.ObjectCache) (int, int64, error) {
	groups := make([][]lazyfs.BaseEntry, mountFetchConcurrency)
	for i, entry := range entries {
		groups[i%len(groups)] = append(groups[i%len(groups)], entry)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	files, total := 0, int64(0)
	var firstErr error
	for _, group := range groups {
		if len(group) == 0 {
			continue
		}
		wg.Add(1)
		go func(group []lazyfs.BaseEntry) {
			defer wg.Done()
			n, size, err := fetchBaseFiles(ctx, conn, slice, commitID, group, cache)
			mu.Lock()
			defer mu.Unlock()
			files += n
			total += size
			if err != nil && firstErr == nil {
				firstErr = err
			}
		}(group)
	}
	wg.Wait()
	return files, total, firstErr
}

// A lazy workspace's .gs lives in its state directory (served at <mount>/.gs),
// with the files gs init writes plus lazy.json, which names the workspace so
// commands know to ask its mount for edits instead of walking the tree.

type lazyMarker struct {
	ID string `json:"id"`
}

func (r Runner) mountGsDir(id string) string { return filepath.Join(r.mountStateDir(id), "gs") }

// writeLazyWorkspaceState writes the workspace files a new lazy workspace
// starts with, as gs init would: its slice, its state and its base snapshot.
func (r Runner) writeLazyWorkspaceState(rec mountRecord, manifest *lazyfs.Manifest) error {
	dir := r.mountGsDir(rec.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, "slice.json")); err == nil {
		return nil // already there: the workspace's own state wins
	}
	ws := WorkspaceConfig{
		Account:        rec.Account,
		Slice:          rec.Slice,
		SliceID:        rec.SliceID,
		DefinitionHash: rec.DefinitionHash,
		IncludedPaths:  rec.IncludedPaths,
		BaseCommitID:   rec.BaseCommit,
	}
	base := BaseSnapshot{CommitID: rec.BaseCommit, Files: map[string]BaseSnapshotFile{}}
	manifest.Files(func(entry lazyfs.BaseEntry) {
		base.Files[entry.Path] = BaseSnapshotFile{Path: entry.Path, RelPath: strings.TrimPrefix(entry.Path, "/"), ContentHash: entry.ContentHash, Mode: entry.Mode, Size: entry.Size}
	})
	for name, value := range map[string]any{
		"base_snapshot.json": base,
		"state.json":         WorkspaceState{BaseCommitID: rec.BaseCommit},
		"lazy.json":          lazyMarker{ID: rec.ID},
		"slice.json":         ws, // last: its presence means the rest is there
	} {
		if err := writeJSONFile(filepath.Join(dir, name), value, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// lazyWorkspaceID returns the id of the lazy workspace rooted at root, or "".
func lazyWorkspaceID(root string) string {
	var marker lazyMarker
	if err := readJSONFile(filepath.Join(root, ".gs", "lazy.json"), &marker); err != nil {
		return ""
	}
	return marker.ID
}

// refuseInLazyWorkspace stops a command that rewrites workspace files, which
// lazy workspaces do not support yet (design/30_lazy_workspaces.md, phase 4).
func (r Runner) refuseInLazyWorkspace(what string) error {
	root, err := r.workspaceRoot()
	if err != nil || lazyWorkspaceID(root) == "" {
		return nil
	}
	return userError("lazy_workspace_unsupported", what+" does not work in a lazy workspace (gs mount) yet",
		"Land your changes with gs create --submit, then start from a fresh mount: gs unmount "+root+"; gs mount discard "+root+"; gs mount <slice> "+root+". Or use a gs init workspace.")
}

// daemonLayerView reads a mounted lazy workspace's layers through its daemon,
// which owns them: the edits over the control socket, files through the mount.
type daemonLayerView struct {
	id        string
	manifest  *lazyfs.Manifest
	mountRoot string
}

func (v daemonLayerView) Edits() ([]lazyfs.Edit, error) {
	reply, err := callMountDaemon(v.id, mountControlRequest{Op: "edits"})
	if err != nil {
		return nil, userError("lazy_workspace_not_mounted", "cannot reach the mount of this lazy workspace: "+err.Error(), "Mount it again: gs mount <slice> "+v.mountRoot+".")
	}
	return reply.Edits, nil
}

func (v daemonLayerView) Base() *lazyfs.Manifest { return v.manifest }

func (v daemonLayerView) ReadFile(ctx context.Context, p string) ([]byte, error) {
	return os.ReadFile(filepath.Join(v.mountRoot, filepath.FromSlash(strings.TrimPrefix(p, "/"))))
}

// mountControlRequest and mountControlReply are the daemon's control
// protocol: one JSON request and one JSON reply per connection.
type mountControlRequest struct {
	Op    string   `json:"op"` // "status", "edits", "restore" or "unmount"
	Paths []string `json:"paths,omitempty"`
}

type mountControlReply struct {
	PID          int           `json:"pid"`
	MountedAt    string        `json:"mounted_at"`
	Opens        int64         `json:"opens"`
	Fetches      int64         `json:"fetches"`
	FetchedFiles int64         `json:"fetched_files"`
	FetchedBytes int64         `json:"fetched_bytes"`
	Errors       int64         `json:"errors"`
	LastError    string        `json:"last_error,omitempty"`
	Edits        []lazyfs.Edit `json:"edits,omitempty"`
	Error        string        `json:"error,omitempty"`
}

func callMountDaemon(id string, req mountControlRequest) (mountControlReply, error) {
	var reply mountControlReply
	conn, err := net.DialTimeout("unix", mountControlPath(id), 2*time.Second)
	if err != nil {
		return reply, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Minute))
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return reply, err
	}
	if err := json.NewDecoder(conn).Decode(&reply); err != nil {
		return reply, err
	}
	if reply.Error != "" {
		return reply, errors.New(reply.Error)
	}
	return reply, nil
}
