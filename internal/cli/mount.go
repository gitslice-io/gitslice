package cli

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"gitslice.io/gitslice/internal/clientcache"
	"gitslice.io/gitslice/internal/lazyfs"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
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
	// ConversationID links the workspace's changesets to an agent
	// conversation, as gs init --agent-conversation does.
	ConversationID string `json:"conversation_id,omitempty"`
	// Prefetch are glob patterns, relative to the mount, of files the daemon
	// fetches in the background when it mounts and after the base moves.
	Prefetch []string `json:"prefetch,omitempty"`
}

// prefetchEntries returns the base files matching the patterns that the
// cache does not have, once per content.
func prefetchEntries(manifest *lazyfs.Manifest, patterns []string, cache *clientcache.ObjectCache) []lazyfs.BaseEntry {
	var out []lazyfs.BaseEntry
	seen := map[string]bool{}
	manifest.Files(func(entry lazyfs.BaseEntry) {
		if entry.Kind != lazyfs.KindFile || seen[entry.ContentHash] {
			return
		}
		rel := strings.TrimPrefix(entry.Path, "/")
		for _, pattern := range patterns {
			if ok, _ := doublestar.Match(pattern, rel); ok {
				seen[entry.ContentHash] = true
				if !cache.Exists(entry.ContentHash) {
					out = append(out, entry)
				}
				return
			}
		}
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
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

var errInvalidMountID = errors.New("invalid mount workspace id")

// maxMountRuntimeDirLen keeps "<runtimeDir>/gs-mount-<16hex>.sock" within
// macOS's 104-byte sockaddr_un.sun_path limit (103 non-NUL bytes).
const maxMountRuntimeDirLen = 72

func isValidMountID(id string) bool {
	if len(id) != 16 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func validateMountID(id string) error {
	if !isValidMountID(id) {
		return fmt.Errorf("%w: %q", errInvalidMountID, id)
	}
	return nil
}

func (r Runner) mountStateDir(id string) string {
	if !isValidMountID(id) {
		return filepath.Join(r.mountsRoot(), "_invalid_mount_id")
	}
	return filepath.Join(r.mountsRoot(), id)
}

// mountRuntimeDir is where the mount sockets live, in a user-owned 0700
// directory short enough for Unix domain sockets (104 bytes on macOS).
func mountRuntimeDir() string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Clean(dir)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidate := filepath.Join(home, ".gitslice", "run")
		if len(candidate) <= maxMountRuntimeDirLen {
			return candidate
		}
	}
	// Fallback when HOME is unset or exceeds sun_path budget: verified via
	// strict Lstat ownership, non-symlink, and 0700 mode before use.
	tmp := os.TempDir()
	if runtime.GOOS == "darwin" {
		tmp = "/tmp"
	}
	return filepath.Join(tmp, fmt.Sprintf("gitslice-%d", os.Getuid()))
}

func verifyMountRuntimeDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("mount runtime directory %s is not a real directory", dir)
	}
	if uid, ok := fileOwnerUID(info); !ok || uid != os.Getuid() {
		return fmt.Errorf("mount runtime directory %s is not owned by current user", dir)
	}
	if info.Mode().Perm() != 0o700 {
		return fmt.Errorf("mount runtime directory %s has unsafe mode %04o (want 0700)", dir, info.Mode().Perm())
	}
	return nil
}

func ensureMountRuntimeDir(dir string) error {
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		parent := filepath.Dir(dir)
		if err := os.MkdirAll(parent, 0o700); err != nil {
			return err
		}
		if pInfo, err := os.Lstat(parent); err != nil || pInfo.Mode()&os.ModeSymlink != 0 || !pInfo.IsDir() {
			if err != nil {
				return err
			}
			return fmt.Errorf("mount runtime parent %s is not a real directory", parent)
		}
		created := false
		if err := os.Mkdir(dir, 0o700); err == nil {
			created = true
		} else if !errors.Is(err, fs.ErrExist) {
			return err
		}
		if created {
			if info, err := os.Lstat(dir); err == nil && info.Mode()&os.ModeSymlink == 0 && info.IsDir() {
				if uid, ok := fileOwnerUID(info); ok && uid == os.Getuid() {
					_ = os.Chmod(dir, 0o700)
				}
			}
		}
	}
	return verifyMountRuntimeDir(dir)
}

func listenUnixControlSocket(socket string) (net.Listener, error) {
	if err := ensureMountRuntimeDir(filepath.Dir(socket)); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(socket); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("refusing to overwrite non-socket %s", socket)
		}
		_ = os.Remove(socket)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(socket)
		return nil, err
	}
	return listener, nil
}

func verifyUnixPeerUID(conn net.Conn) error {
	sc, ok := conn.(syscall.Conn)
	if !ok {
		return fmt.Errorf("unix connection does not support peer credential check")
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return err
	}
	var peerUID int
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		peerUID, credErr = socketPeerUID(int(fd))
	}); err != nil {
		return err
	}
	if credErr != nil {
		return credErr
	}
	if peerUID != os.Getuid() {
		return fmt.Errorf("rejected unix socket peer uid %d (expected %d)", peerUID, os.Getuid())
	}
	return nil
}

// mountControlPath is a lazy workspace's control socket, served by whichever
// process has it mounted.
func mountControlPath(id string) string {
	if !isValidMountID(id) {
		return filepath.Join(mountRuntimeDir(), "gs-mount-_invalid.sock")
	}
	return filepath.Join(mountRuntimeDir(), "gs-mount-"+id+".sock")
}

// mountSupervisorSocket is the socket of the mount supervisor (gs mountd) for
// this state directory: one process serves all of a user's mounts.
func (r Runner) mountSupervisorSocket() string {
	sum := sha256.Sum256([]byte(r.mountsRoot()))
	return filepath.Join(mountRuntimeDir(), "gs-mountd-"+hex.EncodeToString(sum[:4])+".sock")
}

// mountdRequest and mountdReply are the supervisor's own protocol, besides the
// per-workspace one: it attaches workspaces and says what it serves.
type mountdRequest struct {
	Op string `json:"op"` // "attach", "ping" or "list"
	ID string `json:"id,omitempty"`
}

type mountdReply struct {
	PID        int      `json:"pid"`
	Workspaces []string `json:"workspaces,omitempty"`
	ErrorCode  string   `json:"error_code,omitempty"`
	Error      string   `json:"error,omitempty"`
	Hint       string   `json:"hint,omitempty"`
}

// callMountd sends one request to the supervisor. An unreachable supervisor
// is errMountUnreachable; an error it reports comes back as a user error.
func callMountd(socket string, req mountdRequest) (mountdReply, error) {
	var reply mountdReply
	if err := verifyMountRuntimeDir(filepath.Dir(socket)); err != nil {
		return reply, fmt.Errorf("%w: %v", errMountUnreachable, err)
	}
	conn, err := net.DialTimeout("unix", socket, 2*time.Second)
	if err != nil {
		return reply, fmt.Errorf("%w: %v", errMountUnreachable, err)
	}
	defer conn.Close()
	if err := verifyUnixPeerUID(conn); err != nil {
		return reply, fmt.Errorf("%w: %v", errMountUnreachable, err)
	}
	_ = conn.SetDeadline(time.Now().Add(time.Minute))
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return reply, err
	}
	if err := json.NewDecoder(conn).Decode(&reply); err != nil {
		return reply, err
	}
	if reply.Error != "" {
		return reply, commandError{Code: firstNonEmpty(reply.ErrorCode, "mount_failed"), Message: reply.Error, Hint: reply.Hint}
	}
	return reply, nil
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
	if err := validateMountID(id); err != nil {
		return rec, err
	}
	if err := readJSONFile(filepath.Join(r.mountStateDir(id), "mount.json"), &rec); err != nil {
		return rec, err
	}
	if rec.ID != id {
		return mountRecord{}, fmt.Errorf("%w: record id %q does not match %q", errInvalidMountID, rec.ID, id)
	}
	return rec, nil
}

// writeMountRecord replaces mount.json atomically: the daemon rewrites it
// while commands may be reading it.
func (r Runner) writeMountRecord(rec mountRecord) error {
	if err := validateMountID(rec.ID); err != nil {
		return err
	}
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

// Manifests are stored as JSON lines of lazyfs.BaseEntry, one file per base
// commit: a command that reads the workspace's record and then its manifest
// finds the manifest that record names even while gs sync moves the base.

// mountManifestPath is the listing of commitID in workspace id's state.
func (r Runner) mountManifestPath(id, commitID string) string {
	key := strings.Map(func(c rune) rune {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '.' {
			return c
		}
		return '_'
	}, commitID)
	return filepath.Join(r.mountStateDir(id), "manifest-"+key)
}

// loadMountManifest reads the manifest of the workspace's base commit.
// Workspaces made before sync worked in mounts keep it in "manifest".
func (r Runner) loadMountManifest(rec mountRecord) (*lazyfs.Manifest, error) {
	m, err := readManifest(r.mountManifestPath(rec.ID, rec.BaseCommit), rec.BaseCommit)
	if errors.Is(err, fs.ErrNotExist) {
		return readManifest(filepath.Join(r.mountStateDir(rec.ID), "manifest"), rec.BaseCommit)
	}
	return m, err
}

// removeStaleManifests deletes the listings of every commit but keep.
func (r Runner) removeStaleManifests(id string, keep ...string) {
	want := map[string]bool{}
	for _, commit := range keep {
		want[filepath.Base(r.mountManifestPath(id, commit))] = true
	}
	entries, err := os.ReadDir(r.mountStateDir(id))
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if (name == "manifest" || strings.HasPrefix(name, "manifest-")) && !want[name] {
			_ = os.Remove(filepath.Join(r.mountStateDir(id), name))
		}
	}
}

func manifestFileCount(manifest *lazyfs.Manifest) int {
	files := 0
	manifest.Files(func(lazyfs.BaseEntry) { files++ })
	return files
}

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

// mountFetcher fetches content into the client cache for a mount: base files
// by path at the base commit, and content the base does not have at that path
// (a changeset's files after gs switch) by its hash.
type mountFetcher struct {
	r     Runner
	conn  *grpc.ClientConn
	slice *corev1.SliceRef
	cache *clientcache.ObjectCache

	mu       sync.Mutex
	commitID string
	// downUntil is when fetches try the server again after one gave up on an
	// unreachable server; until then they fail at once, so a program reading
	// many uncached files offline is not held up by each one.
	downUntil time.Time
	manifest  *lazyfs.Manifest
	byHash    map[string]lazyfs.BaseEntry // a base file with each content hash, built when first needed
	inflight  map[string]*fetchCall       // by content hash
	completed map[string]bool             // hashes fetched on current base, avoiding duplicate schedules across unlocked Stat
	files     int64
	bytes     int64

	// fetchFn replaces the server fetch in tests.
	fetchFn func(ctx context.Context, commitID string, entries []lazyfs.BaseEntry) (int, int64, error)
	ahead   readAhead
}

type fetchCall struct {
	done chan struct{}
	err  error
}

func newMountFetcher(r Runner, conn *grpc.ClientConn, slice *corev1.SliceRef, cache *clientcache.ObjectCache, manifest *lazyfs.Manifest) *mountFetcher {
	return &mountFetcher{
		r:         r,
		conn:      conn,
		slice:     slice,
		cache:     cache,
		commitID:  manifest.CommitID(),
		manifest:  manifest,
		inflight:  map[string]*fetchCall{},
		completed: map[string]bool{},
	}
}

// setBase points the fetcher at a new base, after gs sync moved the layers.
func (f *mountFetcher) setBase(manifest *lazyfs.Manifest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.manifest, f.commitID, f.byHash, f.completed = manifest, manifest.CommitID(), nil, map[string]bool{}
	f.ahead.reset()
}

// Fetch implements fusefs.Fetcher.
func (f *mountFetcher) Fetch(ctx context.Context, p string, attr lazyfs.Attr) (string, bool, error) {
	f.observeRead(p)
	fetched, err := f.fetchContent(ctx, p, attr.ContentHash, attr.Size)
	if err != nil {
		return "", false, err
	}
	path, err := f.cache.Path(attr.ContentHash)
	return path, fetched, err
}

func waitForFetchCall(ctx context.Context, call *fetchCall) error {
	select {
	case <-call.done:
		return call.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// fetchContent puts the content with hash in the cache, unless it is there.
// p is a path that shows it, if known. It reports whether it fetched.
func (f *mountFetcher) fetchContent(ctx context.Context, p, hash string, size int64) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if f.cache.Exists(hash) {
		return false, nil
	}
	f.mu.Lock()
	if call := f.inflight[hash]; call != nil {
		f.mu.Unlock()
		return false, waitForFetchCall(ctx, call)
	}
	if wait := time.Until(f.downUntil); wait > 0 {
		f.mu.Unlock()
		return false, fmt.Errorf("the server is not reachable (trying again in %s): %w", wait.Round(time.Second), errServerUnreachable)
	}
	commitID := f.commitID
	primary, siblings := f.candidatesLocked(p, hash, size)
	f.mu.Unlock()

	// Check cache existence on disk outside f.mu so os.Stat syscalls do not
	// serialize concurrent FUSE opens.
	if f.cache.Exists(hash) {
		return false, nil
	}
	filtered := make([]lazyfs.BaseEntry, 0, min(len(siblings)+1, mountBatchMaxFiles))
	filtered = append(filtered, primary)
	total := primary.Size
	for _, sibling := range siblings {
		if len(filtered) >= mountBatchMaxFiles || total+sibling.Size > mountBatchMaxBytes {
			break
		}
		if f.cache.Exists(sibling.ContentHash) {
			continue
		}
		filtered = append(filtered, sibling)
		total += sibling.Size
	}

	f.mu.Lock()
	if call := f.inflight[hash]; call != nil {
		f.mu.Unlock()
		return false, waitForFetchCall(ctx, call)
	}
	if f.completed[hash] && f.cache.Exists(hash) {
		f.mu.Unlock()
		return false, nil
	}
	if wait := time.Until(f.downUntil); wait > 0 {
		f.mu.Unlock()
		return false, fmt.Errorf("the server is not reachable (trying again in %s): %w", wait.Round(time.Second), errServerUnreachable)
	}
	call := &fetchCall{done: make(chan struct{})}
	batch := make([]lazyfs.BaseEntry, 0, len(filtered))
	for i, entry := range filtered {
		if i > 0 && (f.inflight[entry.ContentHash] != nil || f.completed[entry.ContentHash]) {
			continue
		}
		f.inflight[entry.ContentHash] = call
		batch = append(batch, entry)
	}
	f.mu.Unlock()

	go func() {
		_ = f.runBatch(commitID, batch, call)
	}()
	if err := waitForFetchCall(ctx, call); err != nil {
		return false, err
	}
	return true, nil
}

// runBatch fetches batch, which the caller has marked in flight under call,
// and settles call. One reader giving up must not fail the others waiting on
// the fetch, so it has its own deadline.
func (f *mountFetcher) runBatch(commitID string, batch []lazyfs.BaseEntry, call *fetchCall) error {
	fetchCtx, cancel := context.WithTimeout(context.Background(), mountFetchTimeout)
	defer cancel()
	// A dropped connection is retried, over a few seconds: what a retry fetches
	// again is only what the cache lacks.
	var files int
	var bytes int64
	err := retryUnavailable(fetchCtx, fetchRetryBackoff, func() error {
		n, size, err := f.fetch(fetchCtx, commitID, batch)
		files += n
		bytes += size
		return err
	})

	f.mu.Lock()
	if err == nil {
		if f.completed == nil || len(f.completed) > 65536 {
			f.completed = map[string]bool{}
		}
		for _, entry := range batch {
			f.completed[entry.ContentHash] = true
		}
	}
	for _, entry := range batch {
		delete(f.inflight, entry.ContentHash)
	}
	f.files += int64(files)
	f.bytes += bytes
	if grpcstatus.Code(err) == codes.Unavailable {
		f.downUntil = time.Now().Add(mountOutageWindow)
	}
	f.mu.Unlock()
	call.err = err
	close(call.done)
	return err
}

// fetchRetryBackoff paces the retries of a fetch whose connection dropped, and
// mountOutageWindow is how long fetches then fail at once instead of trying.
var fetchRetryBackoff = []time.Duration{250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second}

const mountOutageWindow = 5 * time.Second

var errServerUnreachable = errors.New("server unreachable")

// candidatesLocked picks the primary entry and un-inflight sibling candidates
// from in-memory manifest metadata only (without calling cache.Exists under f.mu).
func (f *mountFetcher) candidatesLocked(p, hash string, size int64) (lazyfs.BaseEntry, []lazyfs.BaseEntry) {
	if f.manifest == nil {
		return lazyfs.BaseEntry{Kind: lazyfs.KindFile, Size: size, ContentHash: hash}, nil
	}
	entry, ok := f.manifest.Lookup(p)
	if !ok || entry.Kind != lazyfs.KindFile || entry.ContentHash != hash {
		if f.byHash == nil {
			f.byHash = map[string]lazyfs.BaseEntry{}
			f.manifest.Files(func(entry lazyfs.BaseEntry) {
				if entry.Kind == lazyfs.KindFile {
					f.byHash[entry.ContentHash] = entry
				}
			})
		}
		if other, ok := f.byHash[hash]; ok {
			return other, nil
		}
		return lazyfs.BaseEntry{Kind: lazyfs.KindFile, Size: size, ContentHash: hash}, nil
	}
	if entry.Size > mountLargeFileBytes {
		return entry, nil
	}
	seen := map[string]bool{hash: true}
	dir := p[:strings.LastIndex(p, "/")]
	if dir == "" {
		dir = "/"
	}
	var siblings []lazyfs.BaseEntry
	for _, name := range f.manifest.Children(dir) {
		sibling, ok := f.manifest.Lookup(joinGlobalPath(dir, name))
		if !ok || sibling.Kind != lazyfs.KindFile || sibling.Size > mountPrefetchMaxFile || seen[sibling.ContentHash] {
			continue
		}
		if f.inflight[sibling.ContentHash] != nil || f.completed[sibling.ContentHash] {
			continue
		}
		seen[sibling.ContentHash] = true
		siblings = append(siblings, sibling)
	}
	return entry, siblings
}

func joinGlobalPath(dir, name string) string {
	if dir == "/" {
		return "/" + name
	}
	return dir + "/" + name
}

// fetch puts the entries' content in the cache, checking every hash. The
// credential is read for every fetch, so gs auth use-key takes effect without
// a remount.
func (f *mountFetcher) fetch(ctx context.Context, commitID string, entries []lazyfs.BaseEntry) (int, int64, error) {
	if f.fetchFn != nil {
		return f.fetchFn(ctx, commitID, entries)
	}
	cfg, err := f.r.readUserConfig()
	if err != nil {
		return 0, 0, err
	}
	return fetchBaseFiles(authContext(ctx, cfg), f.conn, f.slice, commitID, entries, f.cache)
}

// mountContent is the daemon's lazyfs.Content: cacheContent that fetches
// what the cache lacks, so the first write to a file nothing has read yet
// can copy its content up.
type mountContent struct {
	cacheContent
	fetcher *mountFetcher
}

func (c mountContent) Open(ctx context.Context, hash string) (io.ReadCloser, error) {
	if _, err := c.fetcher.fetchContent(ctx, "", hash, 0); err != nil {
		return nil, err
	}
	return c.cacheContent.Open(ctx, hash)
}

// fetchBaseFiles fetches base files into the cache through a TransferManager:
// small ones in ReadFiles batches, large ones, and content known by hash only
// (no path), as streams. It returns how many it fetched and their size.
func fetchBaseFiles(ctx context.Context, conn *grpc.ClientConn, slice *corev1.SliceRef, commitID string, entries []lazyfs.BaseEntry, cache *clientcache.ObjectCache) (int, int64, error) {
	targets := make([]remoteReadTarget, 0, len(entries))
	for _, entry := range entries {
		targets = append(targets, remoteReadTarget{Path: entry.Path, ContentHash: entry.ContentHash, Size: entry.Size})
	}
	tm := NewTransferManager(corev1.NewRepositoryServiceClient(conn), corev1.NewBlobServiceClient(conn), slice, cache)
	res, err := tm.FetchFilesToCache(ctx, TransferFetchRequest{CommitID: commitID, Targets: targets, Workers: 1})
	return res.FetchedFiles, res.FetchedBytes, err
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
		ConversationID: rec.ConversationID,
	}
	base := manifestSnapshot(manifest)
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
	if !isValidMountID(marker.ID) {
		return ""
	}
	return marker.ID
}

// refuseInLazyWorkspace stops a command that has no meaning in a lazy
// workspace, saying what to run instead.
func (r Runner) refuseInLazyWorkspace(what, instead string) error {
	root, err := r.workspaceRoot()
	if err != nil || lazyWorkspaceID(root) == "" {
		return nil
	}
	return userError("lazy_workspace_unsupported", what+" does not apply to a lazy workspace (gs mount)", instead)
}

func multisetFileHash(p, contentHash string, mode uint32) (uint64, uint64) {
	var modeBuf [16]byte
	modeBytes := strconv.AppendUint(modeBuf[:0], uint64(mode), 8)
	h1 := fnv.New64a()
	_, _ = h1.Write([]byte(p))
	_, _ = h1.Write([]byte{0})
	_, _ = h1.Write([]byte(contentHash))
	_, _ = h1.Write([]byte{0})
	_, _ = h1.Write(modeBytes)
	s1 := h1.Sum64()

	h2 := fnv.New64()
	_, _ = h2.Write([]byte("gs-manifest-v1:"))
	_, _ = h2.Write(modeBytes)
	_, _ = h2.Write([]byte{0})
	_, _ = h2.Write([]byte(p))
	_, _ = h2.Write([]byte{0})
	_, _ = h2.Write([]byte(contentHash))
	s2 := h2.Sum64()
	return s1, s2
}

// manifestDigest computes an order-independent multiset digest of manifest's
// files, matching snapshotDigest(manifestSnapshot(manifest)).
func manifestDigest(manifest *lazyfs.Manifest) string {
	if manifest == nil {
		return ""
	}
	var count int
	var sum1, sum2, xor1 uint64
	manifest.Files(func(entry lazyfs.BaseEntry) {
		count++
		s1, s2 := multisetFileHash(entry.Path, entry.ContentHash, entry.Mode)
		sum1 += s1
		sum2 += s2
		xor1 ^= s1
	})
	return fmt.Sprintf("%d:%016x:%016x:%016x", count, sum1, sum2, xor1)
}

func snapshotDigest(base BaseSnapshot) string {
	var count int
	var sum1, sum2, xor1 uint64
	for p, file := range base.Files {
		count++
		s1, s2 := multisetFileHash(p, file.ContentHash, file.Mode)
		sum1 += s1
		sum2 += s2
		xor1 ^= s1
	}
	return fmt.Sprintf("%d:%016x:%016x:%016x", count, sum1, sum2, xor1)
}

// daemonLayerView reads a mounted lazy workspace's layers through its daemon,
// which owns them: the edits and touched base entries over the control socket,
// files through the mount. When unmounted, it falls back to reading the local
// layer and manifest on disk if present.
type daemonLayerView struct {
	r           Runner
	rec         mountRecord
	id          string
	manifest    *lazyfs.Manifest
	mountRoot   string
	offlineRead func(ctx context.Context, p string) ([]byte, error)
}

func (r Runner) newDaemonLayerView(rec mountRecord, mountRoot string, manifest *lazyfs.Manifest) *daemonLayerView {
	return &daemonLayerView{
		r:         r,
		rec:       rec,
		id:        rec.ID,
		manifest:  manifest,
		mountRoot: mountRoot,
	}
}

func (v *daemonLayerView) EditsWithBase() ([]lazyfs.Edit, string, map[string]BaseSnapshotFile, string, bool, error) {
	reply, err := callMountDaemon(v.id, mountControlRequest{Op: "edits"})
	if errors.Is(err, errMountUnreachable) {
		edits, m, oerr := v.offlineEdits()
		if oerr == nil {
			v.manifest = m
			return edits, m.CommitID(), nil, "", false, nil
		}
		return nil, "", nil, "", false, userError("lazy_workspace_not_mounted", "cannot reach the mount of this lazy workspace: "+err.Error(), "Mount it again: gs mount <slice> "+v.mountRoot+".")
	}
	if err != nil {
		return nil, "", nil, "", false, err
	}
	if v.manifest != nil && reply.BaseCommit != "" && reply.BaseCommit != v.manifest.CommitID() {
		return nil, "", nil, "", false, userError("workspace_changed", "the lazy workspace moved to another base meanwhile", "Run the command again.")
	}
	if reply.BaseCommit != "" {
		v.rec.BaseCommit = reply.BaseCommit
	}
	return reply.Edits, reply.BaseCommit, reply.BaseFiles, reply.BaseFilesDigest, true, nil
}

func (v *daemonLayerView) offlineEdits() ([]lazyfs.Edit, *lazyfs.Manifest, error) {
	if !isValidMountID(v.id) {
		return nil, nil, errInvalidMountID
	}
	local := filepath.Join(v.r.mountStateDir(v.id), "local")
	info, err := os.Lstat(local)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		if err != nil {
			return nil, nil, err
		}
		return nil, nil, fmt.Errorf("local layer is not a real directory")
	}
	rec := v.rec
	if rec.ID == "" {
		rec, err = v.r.readMountRecord(v.id)
		if err != nil {
			return nil, nil, err
		}
	}
	if recorded, rErr := lazyfs.RecordedBase(local); rErr == nil && recorded != "" && recorded != rec.BaseCommit {
		if _, sErr := os.Stat(v.r.mountManifestPath(v.id, recorded)); sErr == nil {
			rec.BaseCommit = recorded
		}
	}
	manifest := v.manifest
	if manifest == nil || manifest.CommitID() != rec.BaseCommit {
		manifest, err = v.r.loadMountManifest(rec)
		if err != nil {
			return nil, nil, err
		}
	}
	cache, err := v.r.objectCache()
	if err != nil {
		return nil, nil, err
	}
	layer, err := lazyfs.Open(local, manifest, cacheContent{cache: cache}, lazyfs.Options{NoSync: true})
	if err != nil {
		return nil, nil, err
	}
	defer layer.Close()
	edits, err := layer.Edits()
	if err != nil {
		return nil, nil, err
	}
	editByPath := make(map[string]lazyfs.Edit, len(edits))
	for _, edit := range edits {
		editByPath[edit.Path] = edit
	}
	v.offlineRead = func(ctx context.Context, p string) ([]byte, error) {
		if edit, ok := editByPath[p]; ok {
			if edit.Op == "delete" || isSymlinkMode(edit.Mode) {
				return nil, fs.ErrNotExist
			}
			if edit.LocalPath != "" {
				return os.ReadFile(edit.LocalPath)
			}
			return cache.Read(edit.ContentHash)
		}
		entry, ok := manifest.Lookup(p)
		if !ok || entry.Kind != lazyfs.KindFile {
			return nil, fs.ErrNotExist
		}
		return cache.Read(entry.ContentHash)
	}
	return edits, manifest, nil
}

func (v *daemonLayerView) Edits() ([]lazyfs.Edit, error) {
	edits, _, _, _, _, err := v.EditsWithBase()
	return edits, err
}

func (v *daemonLayerView) Base() *lazyfs.Manifest {
	if v.manifest != nil {
		return v.manifest
	}
	rec := v.rec
	if rec.ID == "" {
		if loaded, err := v.r.readMountRecord(v.id); err == nil {
			rec = loaded
		}
	}
	if m, err := v.r.loadMountManifest(rec); err == nil {
		v.manifest = m
	}
	return v.manifest
}

func (v *daemonLayerView) ReadFile(ctx context.Context, p string) ([]byte, error) {
	if err := ensureNotReservedGsPath(p); err != nil {
		return nil, err
	}
	if v.offlineRead != nil {
		return v.offlineRead(ctx, p)
	}
	rel, err := cleanConfinedRelPath(strings.TrimPrefix(p, "/"))
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(v.mountRoot)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	file, err := root.Open(filepath.FromSlash(rel))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fs.ErrInvalid
	}
	return io.ReadAll(file)
}

// mountControlRequest and mountControlReply are the daemon's control
// protocol: one JSON request and one JSON reply per connection.
type mountControlRequest struct {
	// Op is "status", "edits", "restore", "rebase", "switch" or "unmount".
	Op    string   `json:"op"`
	Paths []string `json:"paths,omitempty"`
	// Commit, for rebase and switch, is the base commit to move to, whose
	// manifest gs wrote to the workspace's state first; "" keeps the base.
	Commit string `json:"commit,omitempty"`
	// Resolutions, for rebase, decide each path both the local layer and
	// the new base changed (lazyfs.NeedsMerge).
	Resolutions map[string]mountResolution `json:"resolutions,omitempty"`
	// Diff, Keep and Expect are lazyfs.FS.Switch's arguments.
	Diff   map[string]*lazyfs.BaseEntry `json:"diff,omitempty"`
	Keep   []string                     `json:"keep,omitempty"`
	Expect []lazyfs.Edit                `json:"expect,omitempty"`
}

// mountResolution resolves a path in a rebase, decided by gs for the local
// change Ours (editFingerprint); a different local change fails the rebase,
// as it was not decided on.
type mountResolution struct {
	Ours     string `json:"ours"`
	Keep     bool   `json:"keep,omitempty"`
	Theirs   bool   `json:"theirs,omitempty"`
	Delete   bool   `json:"delete,omitempty"`
	Content  []byte `json:"content,omitempty"`
	Mode     uint32 `json:"mode,omitempty"`
	Conflict bool   `json:"conflict,omitempty"`
}

func (res mountResolution) result() lazyfs.MergeResult {
	return lazyfs.MergeResult{Keep: res.Keep, Theirs: res.Theirs, Delete: res.Delete, Content: res.Content, Mode: res.Mode, Conflict: res.Conflict}
}

// editFingerprint names a local change: "delete", or its content and mode.
func editFingerprint(edit lazyfs.Edit) string {
	if edit.Op == "delete" {
		return "delete"
	}
	return fmt.Sprintf("%s:%o", edit.ContentHash, normalizedSnapshotMode(edit.Mode))
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
	// BaseCommit is the layers' base commit.
	BaseCommit string `json:"base_commit,omitempty"`
	// BaseFiles holds base entries from the daemon's in-memory manifest for
	// touched paths in Edits, and BaseFilesDigest identifies the full manifest
	// multiset so clients avoid loading manifest-<commit> when live.
	BaseFiles       map[string]BaseSnapshotFile `json:"base_files,omitempty"`
	BaseFilesDigest string                      `json:"base_files_digest,omitempty"`
	// Conflicts and Changed report a rebase or a switch: the paths the
	// rebase marked as conflicts, and how many paths the view changed at.
	Conflicts []string `json:"conflicts,omitempty"`
	Changed   int      `json:"changed,omitempty"`
	Error     string   `json:"error,omitempty"`
}

// errMountUnreachable is returned when no daemon answers a workspace's
// control socket: the workspace is not mounted, or its daemon died.
var errMountUnreachable = errors.New("no mount daemon answers")

func callMountDaemon(id string, req mountControlRequest) (mountControlReply, error) {
	var reply mountControlReply
	if err := validateMountID(id); err != nil {
		return reply, err
	}
	socket := mountControlPath(id)
	if err := verifyMountRuntimeDir(filepath.Dir(socket)); err != nil {
		return reply, fmt.Errorf("%w: %v", errMountUnreachable, err)
	}
	conn, err := net.DialTimeout("unix", socket, 2*time.Second)
	if err != nil {
		return reply, fmt.Errorf("%w: %v", errMountUnreachable, err)
	}
	defer conn.Close()
	if err := verifyUnixPeerUID(conn); err != nil {
		return reply, fmt.Errorf("%w: %v", errMountUnreachable, err)
	}
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
