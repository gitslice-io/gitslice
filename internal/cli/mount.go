package cli

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"gitslice.io/gitslice/internal/clientcache"
	"gitslice.io/gitslice/internal/lazyfs"
	corev1 "gitslice.io/gitslice/proto/core/v1"
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
	if dataHome := r.Getenv("XDG_DATA_HOME"); dataHome != "" && r.Home == "" {
		return filepath.Join(dataHome, "gitslice", "mounts")
	}
	return filepath.Join(r.homeDir(), ".local", "share", "gitslice", "mounts")
}

var errInvalidMountID = errors.New("invalid mount workspace id")

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
